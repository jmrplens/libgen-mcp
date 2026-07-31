package main

import (
	"context"
	"encoding/json"
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

	"github.com/jmrplens/libgen-mcp/internal/cachehints"
	"github.com/jmrplens/libgen-mcp/internal/transport"
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

	err := run(context.Background(), "", transport.DefaultOptions())
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
		err = serveHTTP(canceledContext(), newTestServer(), "127.0.0.1:0", transport.DefaultOptions())
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
		err = serveHTTP(context.Background(), newTestServer(), "127.0.0.1:99999", transport.DefaultOptions())
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
		err = run(canceledContext(), "127.0.0.1:0", transport.DefaultOptions())
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
		err = run(canceledContext(), "", transport.DefaultOptions())
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
		err = run(canceledContext(), "", transport.DefaultOptions())
	})
	if !isCleanShutdown(err) {
		t.Fatalf("run(stdio, remote downloads) = %v, want a clean shutdown", err)
	}
}

// TestRunConfigLoadError covers run's config.Load failure branch: an
// unparseable duration makes Load itself (not Validate) return an error.
func TestRunConfigLoadError(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "not-a-duration")
	err := run(context.Background(), "", transport.DefaultOptions())
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
	err := run(context.Background(), "", transport.DefaultOptions())
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
	go func() { done <- serveHTTP(ctx, newTestServer(), addr, transport.DefaultOptions()) }()

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

// newSearchToolServer builds the production MCP server — middleware and all —
// carrying a single stub tool named like the real one, so a tools/list response
// has something to name.
func newSearchToolServer() *mcp.Server {
	type stubIn struct{}
	type stubOut struct{}
	srv := newMCPServer()
	mcp.AddTool(srv, &mcp.Tool{Name: "search", Description: "stub"},
		func(context.Context, *mcp.CallToolRequest, stubIn) (*mcp.CallToolResult, stubOut, error) {
			return nil, stubOut{}, nil
		})
	return srv
}

// newTransportTestServer serves the production HTTP handler for opts over an
// httptest server, exercising real transport behavior without a fixed port.
func newTransportTestServer(t *testing.T, opts transport.Options) *httptest.Server {
	t.Helper()
	srv := newSearchToolServer()
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv }, transport.StreamableHTTP(opts))
	ts := httptest.NewServer(newHTTPHandler(mcpHandler))
	t.Cleanup(ts.Close)
	return ts
}

// httpReply is the part of an HTTP response the transport tests assert on,
// captured once the body has been drained and closed.
type httpReply struct {
	status int
	header http.Header
	body   string
}

// do sends req and returns the drained reply, so callers never hold an open body.
func do(t *testing.T, req *http.Request) httpReply {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return httpReply{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

// postMCP sends a JSON-RPC body to the MCP endpoint with the Content-Type and
// Accept headers the streamable transport insists on.
func postMCP(t *testing.T, base, body string) httpReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return do(t, req)
}

// getURL performs a plain GET.
func getURL(t *testing.T, url string) httpReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return do(t, req)
}

// listToolsRequest is the JSON-RPC body used by the transport behavior tests.
const listToolsRequest = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

// TestServeHTTPStateless drives the default (stateless) transport: a bare POST
// is a complete request, no session id is handed out, and the session-oriented
// verbs are refused while /health keeps working.
func TestServeHTTPStateless(t *testing.T) {
	ts := newTransportTestServer(t, transport.DefaultOptions())

	t.Run("tools list without initialize", func(t *testing.T) {
		reply := postMCP(t, ts.URL, listToolsRequest)
		if reply.status != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", reply.status, http.StatusOK, reply.body)
		}
		if got := reply.header.Get("Mcp-Session-Id"); got != "" {
			t.Errorf("Mcp-Session-Id = %q, want none in stateless mode", got)
		}
		if !strings.Contains(reply.body, `"search"`) {
			t.Errorf("body = %q, want it to list the search tool", reply.body)
		}
	})

	t.Run("get on the mcp endpoint is refused", func(t *testing.T) {
		reply := getURL(t, ts.URL+"/")
		if reply.status != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want %d", reply.status, http.StatusMethodNotAllowed)
		}
		if got := reply.header.Get("Allow"); got != "POST" {
			t.Errorf("Allow = %q, want %q", got, "POST")
		}
	})

	t.Run("health is unaffected", func(t *testing.T) {
		reply := getURL(t, ts.URL+"/health")
		if reply.status != http.StatusOK {
			t.Errorf("status = %d, want %d", reply.status, http.StatusOK)
		}
	})
}

// TestServeHTTPJSONResponse asserts --json-response swaps the SSE framing for a
// plain JSON body a client can decode directly.
func TestServeHTTPJSONResponse(t *testing.T) {
	ts := newTransportTestServer(t, transport.Options{Stateless: true, JSONResponse: true})
	reply := postMCP(t, ts.URL, listToolsRequest)
	if reply.status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", reply.status, http.StatusOK, reply.body)
	}
	if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var decoded struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			TTLMs      int    `json:"ttlMs"`
			CacheScope string `json:"cacheScope"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(reply.body), &decoded); err != nil {
		t.Fatalf("decode %q: %v", reply.body, err)
	}
	if len(decoded.Result.Tools) != 1 || decoded.Result.Tools[0].Name != "search" {
		t.Errorf("tools = %+v, want exactly the search tool", decoded.Result.Tools)
	}
	// The cache hints ride the same response, so assert them over the wire too.
	if decoded.Result.TTLMs != cachehints.CatalogTTLMs {
		t.Errorf("ttlMs = %d, want %d", decoded.Result.TTLMs, cachehints.CatalogTTLMs)
	}
	if decoded.Result.CacheScope != "public" {
		t.Errorf("cacheScope = %q, want %q", decoded.Result.CacheScope, "public")
	}
}

// TestServeHTTPStatefulOptOut asserts -stateless=false restores the legacy
// session transport, which answers initialize with an Mcp-Session-Id.
func TestServeHTTPStatefulOptOut(t *testing.T) {
	ts := newTransportTestServer(t, transport.Options{Stateless: false})
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0.0.0"}}}`
	reply := postMCP(t, ts.URL, initReq)
	if reply.status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", reply.status, http.StatusOK, reply.body)
	}
	if reply.header.Get("Mcp-Session-Id") == "" {
		t.Error("Mcp-Session-Id is empty, want a session id in stateful mode")
	}
}

// TestMainWithExitNegativeBodyLimit covers the flag guard: a negative cap would
// disable the SDK limit entirely, so it is rejected before anything is served.
func TestMainWithExitNegativeBodyLimit(t *testing.T) {
	if code := callMainWithExit(t, "libgen-mcp", "--max-request-body-bytes", "-1"); code != 1 {
		t.Fatalf("mainWithExit(--max-request-body-bytes=-1) = %d, want 1", code)
	}
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
