// pprof.go serves Go's profiling handlers on a loopback listener of their own,
// for anybody asking where a running server's memory and processor time go.
//
// This process has more places for both to go than most: it streams
// multi-megabyte files, fans a search out across a dozen providers
// concurrently, holds a temp cache that defaults to 512 MiB, and extracts PDF
// text in process. None of that is observable from outside today.
//
// A listener of its own rather than a path on the MCP one, for two reasons. The
// MCP listener is reachable from wherever the deployment is, and a heap profile
// is a copy of the process's memory: the catalogs, whatever a handler held when
// the profile was taken, and any per-call secret in flight — an Anna's member
// key, an Unpaywall address, a presigned download URL. And the MCP listener
// carries the cross-origin policy and the middleware chain, none of which a
// profile request should have to pass or be counted by. So the flag names an
// address, that address has to be loopback, and the handlers are mounted on a
// mux nothing else registers on. It is not on /health either: that endpoint
// answers without a credential on purpose, and what it answers is chosen to give
// nothing away.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// pprofReadHeaderTimeout bounds a request's headers. There is deliberately no
// write timeout: a CPU profile blocks for as many seconds as it was asked for,
// and cutting it short would hand back a truncated profile.
const pprofReadHeaderTimeout = 5 * time.Second

// pprofServe runs the accept loop. A variable so a test can make the loop fail,
// which no network condition does on demand.
var pprofServe = func(srv *http.Server, ln net.Listener) error { return srv.Serve(ln) }

// pprofListenAddr is the address the profiling handlers are served on, empty
// when nothing was asked for. The --pprof-addr flag writes this variable before
// it is read (see env_flags.go).
func pprofListenAddr() string { return config.TrimmedGetenv("PPROF_ADDR") }

// validatePprofAddr refuses every address that is not loopback.
//
// "localhost" is accepted by name because it is what a person types; every other
// name is refused rather than resolved, since a name can resolve to any
// interface and the check would then depend on the resolver rather than on the
// flag. An empty host is refused too: ":6060" binds every interface, which is
// the one thing this listener must never do.
func validatePprofAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--pprof-addr %q: want host:port on a loopback address such as 127.0.0.1:6060: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("--pprof-addr %q binds every interface; a profile listener reachable from the network hands out copies of the process's memory, so it must name a loopback address such as 127.0.0.1:6060", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("--pprof-addr %q: %q is a name, not an address; only localhost or a loopback IP is accepted, because a name can resolve to any interface", addr, host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("--pprof-addr %q: %s is not a loopback address; a profile listener reachable from the network hands out copies of the process's memory", addr, host)
	}
	return nil
}

// pprofListener is a running profile listener. A nil one is a listener that was
// never asked for, and stopping it is a no-op, so the caller can defer stop
// unconditionally.
type pprofListener struct {
	addr string
	srv  *http.Server
	done chan struct{}
}

// startPprofListener binds addr and serves the profiling handlers on it until
// stop is called. An empty addr starts nothing and returns nil.
//
// Started before the catalog is registered and before either transport, so a
// profile of startup itself can be taken — which is the half of this process's
// time nothing else can reach, since the server is not answering yet.
func startPprofListener(ctx context.Context, addr string) (*pprofListener, error) {
	if addr == "" {
		return nil, nil //nolint:nilnil // nothing asked for is nothing running, and the nil listener stops safely
	}
	if err := validatePprofAddr(addr); err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("--pprof-addr %q: %w", addr, err)
	}

	// Registered explicitly on a mux of this file's own.
	//
	// That is not what keeps them off the network, and it is worth being exact
	// about which half does what: importing net/http/pprof registers the same
	// handlers on http.DefaultServeMux in its own init, and nothing at a call
	// site can undo that — the side effect IS the import. What contains them is
	// that this process never serves DefaultServeMux: the MCP listener is built
	// by newHTTPHandler on a mux of its own, whose catch-all answers 404, which
	// TestTheMCPListenerDoesNotServeProfiles pins. Mounting here rather than
	// serving DefaultServeMux is what keeps that true of this listener too, so
	// the two muxes cannot drift into sharing anything.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	l := &pprofListener{
		addr: ln.Addr().String(),
		srv:  &http.Server{Handler: mux, ReadHeaderTimeout: pprofReadHeaderTimeout},
		done: make(chan struct{}),
	}
	slog.InfoContext(ctx, "pprof listener started", "component", "pprof", "addr", l.addr)
	go func() {
		defer close(l.done)
		if serveErr := pprofServe(l.srv, ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "pprof listener stopped", "component", "pprof", "error", serveErr)
		}
	}()
	return l, nil
}

// stop closes the listener and every connection on it, and waits for the accept
// loop to return. Close rather than Shutdown: this runs when the process is
// exiting, and a profile still being collected has nobody left to read it.
func (l *pprofListener) stop() {
	if l == nil {
		return
	}
	_ = l.srv.Close()
	<-l.done
}
