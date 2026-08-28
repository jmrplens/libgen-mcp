// listen.go binds the HTTP-mode listener, which is not always a TCP port.
//
// A deployment behind a reverse proxy on the same machine has two ways to make
// the hop between them unreadable. It can encrypt it — which is what
// --tls-cert/--tls-key are for, and what a proxy on a different machine needs.
// Or it can remove it: a unix socket has no network segment to read in the first
// place, no bridge, no docker-proxy hop, and no certificate to issue or rotate.
// The socket is the cheaper answer where it applies, so --http accepts a
// filesystem path as well as host:port.

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// defaultSocketMode is the permission mode a unix socket is created with when
// --http-socket-mode is not given: readable and writable by the owner and the
// group, and by nobody else. A proxy reaches it by sharing the group, which is
// the arrangement that does not also hand the socket to every local account.
const defaultSocketMode os.FileMode = 0o660

// staleSocketDialTimeout bounds the probe that tells a socket left behind by a
// crashed process apart from one a live process is serving. It is a connect() to
// a local path: it either completes immediately or the path is dead.
const staleSocketDialTimeout = 200 * time.Millisecond

// loadTLSKeyPair reads a certificate and its key from disk. It is a variable so
// a test can drive the failure branch without a filesystem.
var loadTLSKeyPair = tls.LoadX509KeyPair //nolint:gochecknoglobals // test seam, mirroring the pattern used elsewhere in this package

// listenSpec is everything needed to bind the listener, kept out of
// transport.Options on purpose: that struct maps flags onto the SDK's
// streamable-HTTP options, and none of these ever reach the SDK. They are
// consumed before the MCP handler is built at all.
type listenSpec struct {
	// addr is the --http value: host:port, or a filesystem path for a unix
	// socket.
	addr string
	// socketMode is the permission mode for a unix socket, already parsed.
	socketMode os.FileMode
	// tlsCert and tlsKey are the PEM files this process terminates TLS with, or
	// both empty for plain HTTP.
	tlsCert, tlsKey string
}

// servesTLS reports whether this process terminates TLS itself, rather than
// leaving it to a proxy in front.
func (s listenSpec) servesTLS() bool { return s.tlsCert != "" }

// isUnixSocketAddr reports whether a listen address names a filesystem path
// rather than a TCP address.
//
// A TCP listen address is host:port, and a host is a name or an IP — neither
// contains a path separator. Anything with one is a path, which also covers the
// relative "./mcp.sock" form. A bare "mcp.sock" is deliberately NOT a socket: it
// is indistinguishable from a hostname, and guessing wrong there would silently
// bind something other than what the operator meant.
func isUnixSocketAddr(addr string) bool {
	return strings.Contains(addr, "/") || strings.Contains(addr, string(os.PathSeparator))
}

// parseSocketMode resolves --http-socket-mode into permission bits.
//
// The value is read as octal whether or not it carries a leading 0, because
// "0660" and "660" mean the same thing to everyone who has ever used chmod, and
// reading the second as decimal would silently produce 0o1224. That is also why
// this is a string flag rather than flag.Uint, which parses with a base of 0 and
// would take "660" as decimal without complaint.
func parseSocketMode(value string) (os.FileMode, error) {
	if strings.TrimSpace(value) == "" {
		return defaultSocketMode, nil
	}
	mode, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(value), "0o"), 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid --http-socket-mode %q: expected an octal mode such as 0660", value)
	}
	if mode == 0 || mode > 0o777 {
		return 0, fmt.Errorf("invalid --http-socket-mode %q: expected a permission mode between 0001 and 0777", value)
	}
	return os.FileMode(mode), nil
}

// validateTLSFiles checks the certificate pair before the server starts.
//
// Both or neither: a cert without a key is a deployment that believes it is
// serving TLS and is not. The pair is loaded eagerly rather than left to
// http.Server, so an unreadable file or a mismatched key is a named startup
// error instead of a handshake that fails on the first real request — by which
// time the operator has stopped watching.
func validateTLSFiles(certFile, keyFile string) error {
	switch {
	case certFile == "" && keyFile == "":
		return nil
	case certFile == "":
		return errors.New("--tls-key was given without --tls-cert; supply both or neither")
	case keyFile == "":
		return errors.New("--tls-cert was given without --tls-key; supply both or neither")
	}
	if _, err := loadTLSKeyPair(certFile, keyFile); err != nil {
		return fmt.Errorf("loading the TLS certificate and key: %w", err)
	}
	return nil
}

// tlsConfigFor builds the server TLS configuration for a validated pair.
//
// NextProtos is not decoration. http.Server negotiates HTTP/2 for a TLS listener
// only when the config advertises h2 — ServeTLS adds it for you, and
// tls.NewListener does not — so omitting it would silently drop every client to
// HTTP/1.1. MinVersion is stated rather than inherited so the floor is visible
// at the place it is decided.
func tlsConfigFor(certFile, keyFile string) (*tls.Config, error) {
	cert, err := loadTLSKeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading the TLS certificate and key: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}

// listenHTTP binds the address the server serves on — a unix socket when the
// address is a path, a TCP port otherwise — and wraps it in TLS when this
// process terminates it.
//
// It all happens here rather than in serveHTTPOn because that function hands the
// listener straight to http.Server, whose Serve closes it on return; an early
// error return added between the two would leak the listener and, for a unix
// socket, leave the file on disk.
func listenHTTP(ctx context.Context, spec listenSpec) (net.Listener, error) {
	var (
		ln  net.Listener
		err error
	)
	if isUnixSocketAddr(spec.addr) {
		ln, err = listenUnix(ctx, spec.addr, spec.socketMode)
	} else {
		var lc net.ListenConfig
		ln, err = lc.Listen(ctx, "tcp", spec.addr)
	}
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", spec.addr, err)
	}
	if !spec.servesTLS() {
		return ln, nil
	}
	cfg, cfgErr := tlsConfigFor(spec.tlsCert, spec.tlsKey)
	if cfgErr != nil {
		_ = ln.Close()
		return nil, cfgErr
	}
	return tls.NewListener(ln, cfg), nil
}

// listenUnix binds a unix socket, clearing a stale one first and giving it the
// permission mode the deployment asked for.
//
// The mode is applied twice, and both halves are load-bearing. The kernel
// creates the socket inode with 0777 &^ umask, which on an ordinary process
// lands at 0755 — world-connectable — so a chmod after bind leaves a window in
// which any local account can connect. Narrowing the umask around the bind
// closes that window; the chmod that follows is what makes the mode exact, since
// a umask can only clear bits and never set them.
func listenUnix(ctx context.Context, path string, socketMode os.FileMode) (net.Listener, error) {
	if err := clearStaleSocket(ctx, path); err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" {
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("its directory is not usable: %w", err)
		}
	}
	if socketMode == 0 {
		socketMode = defaultSocketMode
	}
	var (
		ln  net.Listener
		err error
	)
	withSocketUmask(socketMode, func() {
		var lc net.ListenConfig
		ln, err = lc.Listen(ctx, "unix", path)
	})
	if err != nil {
		return nil, err
	}
	// A socket bound but left at the umask's mercy is the failure this whole
	// path exists to avoid: either the proxy cannot reach it, or everyone can.
	// Failing loudly beats serving on permissions nobody checked.
	if chmodErr := chmodSocket(path, socketMode); chmodErr != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("setting mode %#o on the socket: %w", socketMode, chmodErr)
	}
	return ln, nil
}

// clearStaleSocket removes a socket file left behind by a process that did not
// shut down cleanly, and refuses every other case.
//
// The distinction that matters is between a dead socket and a live one. bind
// fails on an existing path either way, so an unconditional remove would let a
// second instance silently steal a socket the first is still serving; a
// successful connect proves someone is there. A path that exists and is not a
// socket is never removed — the operator pointed at something, and replacing it
// is not this program's call.
func clearStaleSocket(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("%q exists and is not a socket; refusing to replace it", path)
	}
	conn, dialErr := (&net.Dialer{Timeout: staleSocketDialTimeout}).DialContext(ctx, "unix", path)
	if dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("%q is already served by another process", path)
	}
	if removeErr := os.Remove(path); removeErr != nil {
		return fmt.Errorf("removing the stale socket: %w", removeErr)
	}
	slog.Warn("removed a stale unix socket left by an earlier run", "path", path)
	return nil
}

// describeListener renders a listener for the startup log.
//
// It exists because ln.Addr().String() alone is misleading in exactly the two
// modes this file adds: a unix listener prints a bare path with nothing saying
// it is a socket, and tls.NewListener delegates Addr to the listener it wraps,
// so a TLS endpoint prints an unchanged host:port with no sign that it is https.
func describeListener(ln net.Listener, servesTLS bool) string {
	addr := ln.Addr()
	scheme := "http"
	if servesTLS {
		scheme = "https"
	}
	if addr.Network() == "unix" {
		return fmt.Sprintf("unix socket %s (%s)", addr.String(), scheme)
	}
	return fmt.Sprintf("%s://%s", scheme, addr.String())
}
