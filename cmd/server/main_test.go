package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// awaitReturn runs fn in a goroutine and fails the test if it does not return
// within a short deadline, so a misbehaving server or transport can never hang
// the suite. The channel close establishes a happens-before edge, so values fn
// writes are safe to read once awaitReturn returns.
func awaitReturn(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return within deadline")
	}
}

// canceledContext returns a context that is already canceled, driving the
// graceful-shutdown paths without waiting on a real signal.
func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// newTestServer builds a minimal MCP server for exercising the serve paths.
func newTestServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
}

// stubStdinEOF replaces os.Stdin with a pipe whose write end is already closed,
// so any read returns io.EOF immediately (the stdio transport then unwinds).
func stubStdinEOF(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
	})
}

// callMainWithExit invokes mainWithExit with the given argv, restoring the
// global flag set and os.Args afterward so tests stay isolated.
func callMainWithExit(t *testing.T, args ...string) int {
	t.Helper()
	oldArgs := os.Args
	oldFlags := flag.CommandLine
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldFlags
	})
	os.Args = args
	flag.CommandLine = flag.NewFlagSet(args[0], flag.ContinueOnError)
	return mainWithExit()
}

// TestHealthEndpoint verifies HealthEndpoint.
func TestHealthEndpoint(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "mcp")
	})
	handler := newHTTPHandler(stub)

	t.Run("health", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Body.String(); got != "ok" {
			t.Errorf("body = %q, want %q", got, "ok")
		}
	})

	t.Run("delegates to mcp handler", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Body.String(); got != "mcp" {
			t.Errorf("body = %q, want %q", got, "mcp")
		}
	})
}

// TestRunValidatesConfig verifies RunValidatesConfig.
func TestRunValidatesConfig(t *testing.T) {
	// A syntactically valid but out-of-range value passes config.Load but must
	// be rejected by cfg.Validate, so run returns before attempting to serve.
	t.Setenv("LIBGEN_MCP_RATE_RPS", "999")

	err := run(context.Background(), "")
	if err == nil {
		t.Fatal("run() = nil, want validation error")
	}
	if isCleanShutdown(err) {
		t.Fatalf("run() error = %v, want a non-clean-shutdown validation error", err)
	}
}

// TestIsCleanShutdown covers IsCleanShutdown with table-driven subtests.
func TestIsCleanShutdown(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, true},
		{"eof", io.EOF, true},
		{"wrapped eof", fmt.Errorf("wrap: %w", io.EOF), true},
		{"canceled", context.Canceled, true},
		{"other", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCleanShutdown(tc.err); got != tc.want {
				t.Errorf("isCleanShutdown(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestServeHTTPGracefulShutdown starts serveHTTP with an already-canceled
// context and asserts it binds, then shuts down cleanly returning nil.
func TestServeHTTPGracefulShutdown(t *testing.T) {
	var err error
	awaitReturn(t, func() {
		err = serveHTTP(canceledContext(), newTestServer(), "127.0.0.1:0")
	})
	if err != nil {
		t.Fatalf("serveHTTP() = %v, want nil on graceful shutdown", err)
	}
}

// TestServeHTTPListenError passes a port that cannot be bound so ListenAndServe
// fails, exercising the error branch of the serve select.
func TestServeHTTPListenError(t *testing.T) {
	var err error
	awaitReturn(t, func() {
		err = serveHTTP(context.Background(), newTestServer(), "127.0.0.1:99999")
	})
	if err == nil {
		t.Fatal("serveHTTP() = nil, want a listen error for an invalid port")
	}
	if errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serveHTTP() = %v, want a real listen error", err)
	}
}

// TestRunHTTP covers run's remote branch: it registers the HTTP tools and hands
// off to serveHTTP, returning cleanly once the canceled context triggers
// shutdown.
func TestRunHTTP(t *testing.T) {
	var err error
	awaitReturn(t, func() {
		err = run(canceledContext(), "127.0.0.1:0")
	})
	if !isCleanShutdown(err) {
		t.Fatalf("run(http) = %v, want a clean shutdown", err)
	}
}

// TestRunStdio covers run's stdio branch: a canceled context makes server.Run
// return context.Canceled promptly.
func TestRunStdio(t *testing.T) {
	stubStdinEOF(t)
	var err error
	awaitReturn(t, func() {
		err = run(canceledContext(), "")
	})
	if !isCleanShutdown(err) {
		t.Fatalf("run(stdio) = %v, want a clean shutdown", err)
	}
}

// TestRunStdioRemoteDownloads covers the stdio path with LIBGEN_MCP_REMOTE_DOWNLOADS
// set: a hosted stdio deployment (e.g. behind mcp-proxy) forces remote-download
// mode even without --http, so the `cfg.RemoteDownloads` arm of the option guard runs.
func TestRunStdioRemoteDownloads(t *testing.T) {
	t.Setenv("LIBGEN_MCP_REMOTE_DOWNLOADS", "1")
	stubStdinEOF(t)
	var err error
	awaitReturn(t, func() {
		err = run(canceledContext(), "")
	})
	if !isCleanShutdown(err) {
		t.Fatalf("run(stdio, remote downloads) = %v, want a clean shutdown", err)
	}
}

// TestRunConfigLoadError covers run's config.Load failure branch: an
// unparseable duration makes Load itself (not Validate) return an error.
func TestRunConfigLoadError(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "not-a-duration")
	err := run(context.Background(), "")
	if err == nil {
		t.Fatal("run() = nil, want a config-load error")
	}
	if isCleanShutdown(err) {
		t.Fatalf("run() error = %v, want a non-clean-shutdown load error", err)
	}
}

// TestRunManagerError covers run's mirrors.NewManager failure branch: with HOME
// unset, os.UserCacheDir (used by NewManager) fails while config.Load still
// succeeds.
func TestRunManagerError(t *testing.T) {
	// A writable download dir keeps config.Load/Validate happy so failure
	// surfaces from NewManager (os.UserCacheDir) rather than the home-dir lookup.
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", t.TempDir())
	t.Setenv("HOME", "")
	err := run(context.Background(), "")
	if err == nil {
		t.Fatal("run() = nil, want a mirror-manager error")
	}
	if isCleanShutdown(err) {
		t.Fatalf("run() error = %v, want a non-clean-shutdown manager error", err)
	}
}

// TestServeHTTPServesRequests binds a real ephemeral port and drives a full
// request so the per-request server-getter closure runs, then cancels for a
// graceful shutdown.
func TestServeHTTPServesRequests(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, newTestServer(), addr) }()

	base := "http://" + addr
	waitForHealth(t, base)

	// The streamable handler only calls the per-request server-getter closure for
	// a POST that also advertises both JSON and SSE in Accept, so build the
	// request explicitly rather than using http.Post.
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if resp, gErr := http.DefaultClient.Do(req); gErr == nil {
		_ = resp.Body.Close()
	}

	cancel()
	select {
	case sErr := <-done:
		if sErr != nil {
			t.Fatalf("serveHTTP() = %v, want nil after graceful shutdown", sErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP did not return after cancel")
	}
}

// waitForHealth polls GET /health until the server accepts connections.
func waitForHealth(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if healthOK(base) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become healthy in time")
}

// healthOK reports whether GET /health currently returns 200.
func healthOK(base string) bool {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// TestMainWithExitVersion covers the --version fast path, which prints and
// returns 0 before any server is built.
func TestMainWithExitVersion(t *testing.T) {
	if code := callMainWithExit(t, "libgen-mcp", "--version"); code != 0 {
		t.Fatalf("mainWithExit(--version) = %d, want 0", code)
	}
}

// TestMainWithExitCleanShutdown covers the normal stdio path returning 0: stdin
// is at EOF, so run finishes with a clean-shutdown error.
func TestMainWithExitCleanShutdown(t *testing.T) {
	stubStdinEOF(t)
	var code int
	awaitReturn(t, func() {
		code = callMainWithExit(t, "libgen-mcp")
	})
	if code != 0 {
		t.Fatalf("mainWithExit(stdio EOF) = %d, want 0", code)
	}
}

// TestMainWithExitRunError covers the error branch: an unbindable HTTP address
// makes run fail with a non-clean error, so mainWithExit returns 1.
func TestMainWithExitRunError(t *testing.T) {
	var code int
	awaitReturn(t, func() {
		code = callMainWithExit(t, "libgen-mcp", "--http", "127.0.0.1:99999")
	})
	if code != 1 {
		t.Fatalf("mainWithExit(bad http) = %d, want 1", code)
	}
}
