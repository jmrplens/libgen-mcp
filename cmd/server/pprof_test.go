// pprof_test.go covers the profile listener, whose whole security property is
// one predicate: it binds loopback or it does not bind at all.

package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestValidatePprofAddrAcceptsOnlyLoopback is the predicate, seen from both
// sides.
//
// A heap profile is a copy of this process's memory — the catalogs, whatever a
// handler held when it was taken, and any per-call secret in flight — so a
// listener anything but this machine can reach hands all of it to whoever asks,
// with no credential anywhere on the path. Every refusal names why, because the
// operator who typed the address is about to be told no.
func TestValidatePprofAddrAcceptsOnlyLoopback(t *testing.T) {
	tests := []struct {
		name string
		addr string
		// wantErr is a fragment the refusal must carry, or "" for accepted.
		wantErr string
	}{
		{name: "loopback v4", addr: "127.0.0.1:6060"},
		{name: "loopback anywhere in 127/8", addr: "127.9.9.9:6060"},
		{name: "loopback v6", addr: "[::1]:6060"},
		// Accepted by name because it is what a person types. Every OTHER name
		// is refused rather than resolved: a name can resolve to any interface,
		// and resolving would make the check depend on the resolver instead of
		// on the flag.
		{name: "localhost by name", addr: "localhost:6060"},

		// The one that matters most: ":6060" binds every interface, and reads
		// like a local address to anybody used to writing it for a dev server.
		{name: "a wildcard bind", addr: ":6060", wantErr: "binds every interface"},
		{name: "an explicit wildcard", addr: "0.0.0.0:6060", wantErr: "not a loopback address"},
		{name: "an IPv6 wildcard", addr: "[::]:6060", wantErr: "not a loopback address"},
		{name: "a LAN address", addr: "192.168.1.50:6060", wantErr: "not a loopback address"},
		{name: "a public address", addr: "203.0.113.7:6060", wantErr: "not a loopback address"},
		{name: "any other name", addr: "profiler.internal:6060", wantErr: "is a name, not an address"},
		{name: "no port at all", addr: "127.0.0.1", wantErr: "want host:port"},
		{name: "not an address", addr: "nonsense", wantErr: "want host:port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePprofAddr(tt.addr)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validatePprofAddr(%q) = %v, want it accepted", tt.addr, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validatePprofAddr(%q) = nil, want it refused", tt.addr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("refusal = %q, want it to say %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.addr) {
				t.Errorf("refusal = %q, want it to name the address that was given", err)
			}
		})
	}
}

// TestPprofListenerServesTheHandlers is the other half: the refusals above are
// only worth having if the accepted case actually works.
//
// Port 0 rather than 6060, so the case cannot collide with anything already
// running on the machine — including a previous run of itself.
func TestPprofListenerServesTheHandlers(t *testing.T) {
	l, err := startPprofListener(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofListener: %v", err)
	}
	t.Cleanup(l.stop)

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/symbol"} {
		t.Run(path, func(t *testing.T) {
			body, status := getPprof(t, l.addr, path)
			if status != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, status)
			}
			if len(body) == 0 {
				t.Errorf("GET %s returned an empty body", path)
			}
		})
	}

	// Nothing else is on this mux. A profile listener that also answered the
	// health route, or anything else this server serves, would be a second way
	// to reach the surface the address check exists to keep separate.
	if _, status := getPprof(t, l.addr, "/health"); status != http.StatusNotFound {
		t.Errorf("GET /health on the profile listener = %d, want 404", status)
	}
}

// TestTheMCPListenerDoesNotServeProfiles is the containment that actually
// protects this, asserted where it can be.
//
// Importing net/http/pprof registers the handlers on http.DefaultServeMux in its
// own init, and no amount of care at the call site undoes that — the side effect
// is the import. This test was first written to assert DefaultServeMux was
// clean; it failed, correctly, and the comment it was checking was wrong.
//
// What keeps profiles off the network is therefore not that they are unregistered
// somewhere, but that nothing in this process ever SERVES DefaultServeMux: the
// MCP listener is built by newHTTPHandler, on a mux of its own, whose catch-all
// answers 404. That is the claim worth pinning, because it is the one a later
// change could break by handing http.DefaultServeMux to a server.
func TestTheMCPListenerDoesNotServeProfiles(t *testing.T) {
	handler := newHTTPHandler(http.NotFoundHandler(), nil, nil, "/", false, testHealth())
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/cmdline"} {
		t.Run(path, func(t *testing.T) {
			if _, status := getPprof(t, strings.TrimPrefix(srv.URL, "http://"), path); status != http.StatusNotFound {
				t.Errorf("the MCP listener answers %s with %d; profiles must be reachable only on their own loopback listener", path, status)
			}
		})
	}
}

// TestPprofListenerDoesNothingWhenNoAddressIsGiven covers the default, which is
// every deployment that never asks for a profile.
//
// The nil listener has to be safe to stop, because the caller defers that
// unconditionally rather than testing for it.
func TestPprofListenerDoesNothingWhenNoAddressIsGiven(t *testing.T) {
	l, err := startPprofListener(t.Context(), "")
	if err != nil {
		t.Fatalf("startPprofListener(\"\") = %v, want no error", err)
	}
	if l != nil {
		t.Errorf("startPprofListener(\"\") returned a listener on %q", l.addr)
	}
	l.stop() // must not panic
}

// TestPprofListenerRefusesBeforeItBinds pins the order.
//
// The address is checked before anything is bound, so a refused address never
// becomes a socket even briefly. Checking after the bind would leave a listener
// on a reachable interface for as long as it took to notice.
func TestPprofListenerRefusesBeforeItBinds(t *testing.T) {
	l, err := startPprofListener(t.Context(), "0.0.0.0:0")
	if err == nil {
		l.stop()
		t.Fatal("a wildcard address was bound")
	}
	if l != nil {
		t.Error("a refused address still produced a listener")
	}
	if !strings.Contains(err.Error(), "not a loopback address") {
		t.Errorf("err = %v, want it to say the address is not loopback", err)
	}
}

// TestPprofListenerReportsAServeFailure covers the accept loop's own error path,
// which no network condition produces on demand.
func TestPprofListenerReportsAServeFailure(t *testing.T) {
	previous := pprofServe
	t.Cleanup(func() { pprofServe = previous })
	failed := make(chan struct{})
	pprofServe = func(*http.Server, net.Listener) error {
		close(failed)
		return errors.New("accept: boom")
	}

	l, err := startPprofListener(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofListener: %v", err)
	}
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("the accept loop never ran")
	}
	// stop() waits for the goroutine, so a serve error that left it running
	// would hang here rather than pass.
	l.stop()
}

// getPprof issues a context-carrying GET against the profile listener.
func getPprof(t *testing.T, addr, path string) (body []byte, status int) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext(%q): %v", path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return read, resp.StatusCode
}
