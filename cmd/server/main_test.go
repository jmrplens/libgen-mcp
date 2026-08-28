package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/cachehints"
	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/transport"
	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
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
	handler := newHTTPHandler(stub, nil, nil, "/", false)

	// The three fields and the content type are a contract shared with the sibling
	// gitlab-mcp-server, so one external probe can read both servers and confirm
	// which build is running without entering the container. Asserting the shape
	// rather than a literal body is what keeps that contract from drifting.
	t.Run("health", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var got healthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("health body is not JSON: %v (body %q)", err, rec.Body.String())
		}
		if got.Status != "ok" {
			t.Errorf("status = %q, want ok", got.Status)
		}
		// Version must be the real number even unstamped: buildversion falls back
		// to VERSION, so a probe never sees an empty field or a placeholder.
		if got.Version != buildversion.Current() || got.Version == "" {
			t.Errorf("version = %q, want %q", got.Version, buildversion.Current())
		}
		if got.Commit != commit {
			t.Errorf("commit = %q, want %q", got.Commit, commit)
		}
		// A probe reads these to tell a restart from a long-running process, so
		// the handler must actually fill them rather than leave the zero values.
		if _, pErr := time.Parse(time.RFC3339, got.StartedAt); pErr != nil {
			t.Errorf("started_at = %q, not RFC 3339: %v", got.StartedAt, pErr)
		}
		if got.UptimeSeconds < 0 {
			t.Errorf("uptime_seconds = %d, want a non-negative count", got.UptimeSeconds)
		}
	})

	// The endpoint is the mounted path itself, not "any path that is not
	// /health": the MCP handler used to be the mux's catch-all, and this
	// subtest used to prove it by posting to /mcp. It now posts to the route
	// that actually exists, because /mcp is a path this server does not serve
	// and answers 404 (see TestNewHTTPHandlerRoutes).
	t.Run("delegates to mcp handler", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
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

	err := run(context.Background(), listenSpec{}, transport.DefaultOptions())
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
		err = serveHTTP(canceledContext(), newTestServer(), listenSpec{addr: "127.0.0.1:0"}, transport.DefaultOptions())
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
		err = serveHTTP(context.Background(), newTestServer(), listenSpec{addr: "127.0.0.1:99999"}, transport.DefaultOptions())
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
		err = run(canceledContext(), listenSpec{addr: "127.0.0.1:0"}, transport.DefaultOptions())
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
		err = run(canceledContext(), listenSpec{}, transport.DefaultOptions())
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
		err = run(canceledContext(), listenSpec{}, transport.DefaultOptions())
	})
	if !isCleanShutdown(err) {
		t.Fatalf("run(stdio, remote downloads) = %v, want a clean shutdown", err)
	}
}

// TestRunConfigLoadError covers run's config.Load failure branch: an
// unparseable duration makes Load itself (not Validate) return an error.
func TestRunConfigLoadError(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "not-a-duration")
	err := run(context.Background(), listenSpec{}, transport.DefaultOptions())
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
	err := run(context.Background(), listenSpec{}, transport.DefaultOptions())
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
	// The listener is handed to serveHTTPOn still bound. Reserving the port and
	// closing it here would leave a window for anything else on the machine —
	// including another test in this package — to take it before serveHTTP
	// rebinds.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveHTTPOn(ctx, newTestServer(), ln, transport.DefaultOptions()) }()

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
	// The budget has to clear httpShutdownTimeout: a stream the client has closed
	// but the handler has not noticed yet keeps the graceful phase running to its
	// own deadline, which a 5s wait loses to by microseconds.
	select {
	case sErr := <-done:
		if sErr != nil {
			t.Fatalf("serveHTTP() = %v, want nil after graceful shutdown", sErr)
		}
	case <-time.After(httpShutdownTimeout + 5*time.Second):
		t.Fatal("serveHTTP did not return after cancel")
	}
}

// TestServeHTTPClosesStreamsThatOutlastShutdown pins the forced-close path. A
// stateful session's GET stream stays open until the client or the server ends
// it, so the connection never returns to idle and graceful shutdown runs out its
// deadline. serveHTTP must still come back clean, having cut the stream, rather
// than reporting the deadline as a failure and leaving the listener held.
func TestServeHTTPClosesStreamsThatOutlastShutdown(t *testing.T) {
	// The listener is handed to serveHTTPOn still bound. Reserving the port and
	// closing it here would leave a window for anything else on the machine —
	// including another test in this package — to take it before serveHTTP
	// rebinds.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// Stateful mode is what exposes the long-lived GET stream; the stateless
	// default answers GET with 405 and closes every POST stream on reply.
	opts := transport.DefaultOptions()
	opts.Stateless = false
	go func() { done <- serveHTTPOn(ctx, newTestServer(), ln, opts) }()

	base := "http://" + addr
	waitForHealth(t, base)

	sessionID := initStatefulSession(t, base)
	stream := openSessionStream(t, base, sessionID)
	defer func() { _ = stream.Close() }()

	start := time.Now()
	cancel()
	select {
	case sErr := <-done:
		if sErr != nil {
			t.Fatalf("serveHTTP() = %v, want nil once the open stream is force-closed", sErr)
		}
		if elapsed := time.Since(start); elapsed < httpShutdownTimeout {
			t.Errorf("returned after %v, want at least the %v graceful phase", elapsed, httpShutdownTimeout)
		}
	case <-time.After(httpShutdownTimeout + 15*time.Second):
		t.Fatal("serveHTTP did not return after cancel with a stream still open")
	}
}

// initStatefulSession runs initialize against a stateful server and returns the
// session id it hands back.
func initStatefulSession(t *testing.T, base string) string {
	t.Helper()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0.0.0"}}}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/", body)
	if err != nil {
		t.Fatalf("build initialize request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	id := resp.Header.Get("Mcp-Session-Id")
	if id == "" {
		t.Fatal("initialize returned no Mcp-Session-Id; stateful mode is not active")
	}
	return id
}

// openSessionStream opens the session's long-lived GET stream and returns its
// still-open body, having waited for the response headers.
func openSessionStream(t *testing.T, base, sessionID string) io.ReadCloser {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/", nil)
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open session stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("stream status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	return resp.Body
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
		func(*http.Request) *mcp.Server { return srv }, transport.StreamableHTTP(opts),
	)
	ts := httptest.NewServer(newHTTPHandler(mcpHandler, nil, opts.TrustedOrigins, "/", false))
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

	// The transport spec's SHOULD. It is load-bearing here rather than
	// cosmetic: download and read emit notifications/progress on this stream
	// for the duration of a fetch, so a proxy that buffers holds every one of
	// them until the transfer ends.
	t.Run("sse response tells proxies not to buffer", func(t *testing.T) {
		reply := postMCP(t, ts.URL, listToolsRequest)
		if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("Content-Type = %q, want text/event-stream", ct)
		}
		if got := reply.header.Get("X-Accel-Buffering"); got != "no" {
			t.Errorf("X-Accel-Buffering = %q, want %q", got, "no")
		}
	})

	t.Run("health is unaffected", func(t *testing.T) {
		reply := getURL(t, ts.URL+"/health")
		if reply.status != http.StatusOK {
			t.Errorf("status = %d, want %d", reply.status, http.StatusOK)
		}
		// The no-buffering header is scoped to the MCP endpoint. A probe
		// response is a small JSON body with nothing to stream, so it must
		// not pick the header up.
		if got := reply.header.Get("X-Accel-Buffering"); got != "" {
			t.Errorf("X-Accel-Buffering = %q on /health, want none", got)
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

// TestNewHealthResponseUptimeAndStartedAt verifies the two liveness fields
// against controlled instants: started_at must be RFC 3339 in UTC, and
// uptime_seconds must be whole seconds since that instant.
//
// Both instants are parameters precisely so this can be exercised without
// waiting on a real clock or mutating package state from a parallel test.
func TestNewHealthResponseUptimeAndStartedAt(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 23, 10, 30, 0, 0, time.UTC)
	cases := []struct {
		name       string
		now        time.Time
		wantUptime int64
	}{
		{"same instant", start, 0},
		{"partial second truncates down", start.Add(1900 * time.Millisecond), 1},
		{"whole minute", start.Add(time.Minute), 60},
		{"two weeks", start.Add(14 * 24 * time.Hour), 1_209_600},
		// time.Now cannot go backwards within one process, but the clamp is what
		// keeps a negative from ever reaching a probe if a caller passes one.
		{"observation before start is clamped", start.Add(-time.Hour), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := newHealthResponse(start, tc.now)
			if got.UptimeSeconds != tc.wantUptime {
				t.Errorf("uptime_seconds = %d, want %d", got.UptimeSeconds, tc.wantUptime)
			}
			if got.StartedAt != "2026-08-23T10:30:00Z" {
				t.Errorf("started_at = %q, want the start instant in RFC 3339 UTC", got.StartedAt)
			}
		})
	}
}

// TestNewHealthResponseRendersStartedAtInUTC pins the timezone: a start instant
// observed in another zone must still be published as UTC, so two probes in
// different regions read the same string for the same process.
func TestNewHealthResponseRendersStartedAtInUTC(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("UTC+5", 5*60*60)
	start := time.Date(2026, 8, 23, 15, 30, 0, 0, zone)
	got := newHealthResponse(start, start)
	if got.StartedAt != "2026-08-23T10:30:00Z" {
		t.Errorf("started_at = %q, want the same instant normalized to UTC", got.StartedAt)
	}
}

// TestResolveCommit covers resolveCommit's three paths: a stamped release value
// always wins, an unstamped build recovers vcs.revision from the embedded build
// info, and an unavailable or revision-less build info leaves "none" in place
// rather than fabricating a value.
func TestResolveCommit(t *testing.T) {
	t.Parallel()
	withRevision := func(rev string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: rev}}}, true
		}
	}

	tests := []struct {
		name          string
		ldflagsCommit string
		readBuildInfo func() (*debug.BuildInfo, bool)
		wantCommit    string
	}{
		{
			name:          "stamped value wins over build info",
			ldflagsCommit: "abc1234",
			readBuildInfo: withRevision("def5678"),
			wantCommit:    "abc1234",
		},
		{
			name:          "unstamped recovers vcs.revision from build info",
			ldflagsCommit: "none",
			readBuildInfo: withRevision("def5678"),
			wantCommit:    "def5678",
		},
		{
			name:          "build info unavailable leaves none in place",
			ldflagsCommit: "none",
			readBuildInfo: func() (*debug.BuildInfo, bool) { return nil, false },
			wantCommit:    "none",
		},
		{
			name:          "build info present but carries no vcs.revision",
			ldflagsCommit: "none",
			readBuildInfo: func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{}, true },
			wantCommit:    "none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveCommit(tt.ldflagsCommit, tt.readBuildInfo); got != tt.wantCommit {
				t.Errorf("resolveCommit(%q) = %q, want %q", tt.ldflagsCommit, got, tt.wantCommit)
			}
		})
	}
}

// TestServerInstructionsNameEveryToolAndPrompt guards serverInstructions
// against drift: it goes straight into a connecting model's system prompt, so
// a tool or prompt renamed without updating the hand-written text would send
// the model at a name it can no longer call. The check walks the live,
// registered surface rather than a hardcoded list of names, so it catches a
// rename on either side — the registration or the instructions text.
func TestServerInstructionsNameEveryToolAndPrompt(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	server, err := newRegisteredServer(cfg, "")
	if err != nil {
		t.Fatalf("newRegisteredServer() error = %v", err)
	}

	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "instructions-test", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	instructions := session.InitializeResult().Instructions
	if instructions == "" {
		t.Fatal("handshake Instructions is empty")
	}
	for tl, tErr := range session.Tools(t.Context(), nil) {
		if tErr != nil {
			t.Fatalf("list tools: %v", tErr)
		}
		if !strings.Contains(instructions, tl.Name) {
			t.Errorf("Instructions does not mention registered tool %q", tl.Name)
		}
	}
	for p, pErr := range session.Prompts(t.Context(), nil) {
		if pErr != nil {
			t.Fatalf("list prompts: %v", pErr)
		}
		if !strings.Contains(instructions, p.Name) {
			t.Errorf("Instructions does not mention registered prompt %q", p.Name)
		}
	}
}

// TestNoResourcesIsConsistent asserts the handshake and the wire agree that
// this server has no resources: it declares no resources capability, and the
// three resource methods the SDK registers regardless answer -32601 rather
// than a successful empty listing that would imply resources exist here.
//
// The two halves are asserted together on purpose. Registering a resource
// makes the SDK infer the capability, which flips the first half and leaves a
// server advertising resources it then refuses to list — so whoever adds one
// is failed here and pointed at capguard.NoResources.
func TestNoResourcesIsConsistent(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	server, err := newRegisteredServer(cfg, "")
	if err != nil {
		t.Fatalf("newRegisteredServer() error = %v", err)
	}

	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "resources-test", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	if caps := session.InitializeResult().Capabilities; caps.Resources != nil {
		t.Fatalf("handshake declares resources capability %+v; if that is intended, "+
			"drop capguard.NoResources so the resource methods answer for real", caps.Resources)
	}

	calls := map[string]func() error{
		"resources/list": func() error {
			_, callErr := session.ListResources(t.Context(), nil)
			return callErr
		},
		"resources/templates/list": func() error {
			_, callErr := session.ListResourceTemplates(t.Context(), nil)
			return callErr
		},
		"resources/read": func() error {
			_, callErr := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "file:///nothing"})
			return callErr
		},
	}
	for method, call := range calls {
		t.Run(method, func(t *testing.T) {
			var wire *jsonrpc.Error
			callErr := call()
			if !errors.As(callErr, &wire) {
				t.Fatalf("err = %v (%T), want a *jsonrpc.Error", callErr, callErr)
			}
			if wire.Code != jsonrpc.CodeMethodNotFound {
				t.Errorf("code = %d, want %d (method not found)", wire.Code, jsonrpc.CodeMethodNotFound)
			}
		})
	}
}

// TestServeHTTPValidatesOrigin covers the 2026-07-28 transport MUST — "Servers
// MUST validate the Origin header on all incoming connections" — at the only
// layer that can enforce it, the HTTP handler.
//
// It lives apart from TestServeHTTPStateless because the three cases below are
// one contract of their own, and because folding them in pushed that function
// past the cognitive-complexity budget.
func TestServeHTTPValidatesOrigin(t *testing.T) {
	ts := newTransportTestServer(t, transport.DefaultOptions())

	// The 2026-07-28 transport MUST, at the only layer that can enforce it.
	// The three cases are the whole contract: a browser POST from elsewhere is
	// refused, the same POST with no browser headers is not (that is every
	// non-browser client), and a safe method is never touched.
	t.Run("cross-origin browser POST is refused", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL, strings.NewReader(listToolsRequest))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.invalid")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want %d for a cross-origin browser POST", resp.StatusCode, http.StatusForbidden)
		}
	})

	t.Run("non-browser POST is unaffected", func(t *testing.T) {
		reply := postMCP(t, ts.URL, listToolsRequest)
		if reply.status != http.StatusOK {
			t.Errorf("status = %d, want %d: a client sending neither Sec-Fetch-Site nor Origin must not be blocked", reply.status, http.StatusOK)
		}
	})
}

// browserPOST issues the request a browser makes after a successful preflight:
// a cross-site POST carrying an Origin.
func browserPOST(t *testing.T, base, origin string) httpReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/", strings.NewReader(listToolsRequest))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", origin)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	return do(t, req)
}

// preflight issues the OPTIONS a browser sends before a cross-origin POST that
// carries a non-safelisted Content-Type.
func preflight(t *testing.T, base, origin string) httpReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodOptions, base+"/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	return do(t, req)
}

// TestServeHTTPTrustedOrigins covers the flag end to end over a real wire.
//
// The regression that matters most is the no-Origin case in every
// configuration: every CLI and desktop client depends on it, and it is the
// easiest to break while making the browser cases work.
func TestServeHTTPTrustedOrigins(t *testing.T) {
	const trusted, untrusted = "https://claude.ai", "https://evil.example"

	t.Run("no allowlist refuses every browser origin", func(t *testing.T) {
		ts := newTransportTestServer(t, transport.DefaultOptions())
		if got := browserPOST(t, ts.URL, trusted).status; got != http.StatusForbidden {
			t.Errorf("trusted-looking origin = %d, want %d when no allowlist is configured", got, http.StatusForbidden)
		}
		if got := postMCP(t, ts.URL, listToolsRequest).status; got != http.StatusOK {
			t.Errorf("no-Origin client = %d, want %d", got, http.StatusOK)
		}
		// Unchanged from before the flag existed: nothing answers the preflight,
		// so it falls through to the MCP handler's method check.
		if got := preflight(t, ts.URL, trusted).status; got != http.StatusMethodNotAllowed {
			t.Errorf("preflight = %d, want %d without an allowlist", got, http.StatusMethodNotAllowed)
		}
	})

	t.Run("an allowlisted origin is answered and admitted", func(t *testing.T) {
		opts := transport.DefaultOptions()
		opts.TrustedOrigins = []string{trusted}
		ts := newTransportTestServer(t, opts)

		pre := preflight(t, ts.URL, trusted)
		if pre.status != http.StatusNoContent {
			t.Fatalf("preflight = %d, want %d", pre.status, http.StatusNoContent)
		}
		// Echoed, not "*": a browser rejects the wildcard on a credentialed
		// request, and Vary keeps a shared cache from serving one origin's
		// answer to another.
		if got := pre.header.Get("Access-Control-Allow-Origin"); got != trusted {
			t.Errorf("preflight Allow-Origin = %q, want the origin echoed", got)
		}
		if got := pre.header.Get("Access-Control-Allow-Headers"); got != "content-type" {
			t.Errorf("preflight Allow-Headers = %q, want the requested header echoed", got)
		}
		// Both request headers the answer is derived from: the origin it echoes
		// and the header list it echoes. A cache that missed either could
		// replay one caller's preflight for another.
		for _, want := range []string{"Origin", headerRequestHeader} {
			if !strings.Contains(pre.header.Get("Vary"), want) {
				t.Errorf("preflight Vary = %q, want it to name %s", pre.header.Get("Vary"), want)
			}
		}

		post := browserPOST(t, ts.URL, trusted)
		if post.status != http.StatusOK {
			t.Errorf("trusted POST = %d, want %d — the preflight promised it would be allowed", post.status, http.StatusOK)
		}
		if got := post.header.Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Mcp-Session-Id") {
			t.Errorf("Expose-Headers = %q, want it to name Mcp-Session-Id; a browser cannot read it otherwise", got)
		}

		if got := browserPOST(t, ts.URL, untrusted).status; got != http.StatusForbidden {
			t.Errorf("untrusted POST = %d, want %d: the allowlist must not widen to other origins", got, http.StatusForbidden)
		}
		if got := postMCP(t, ts.URL, listToolsRequest).status; got != http.StatusOK {
			t.Errorf("no-Origin client = %d, want %d", got, http.StatusOK)
		}
	})
}

// TestServeHTTPTrustedOriginsWildcard covers --trusted-origins=*, which turns
// the protection off for browsers entirely. It is a function of its own rather
// than a third subtest because the three together exceed the cognitive
// complexity budget.
func TestServeHTTPTrustedOriginsWildcard(t *testing.T) {
	const trusted, untrusted = "https://claude.ai", "https://evil.example"
	opts := transport.DefaultOptions()
	opts.TrustedOrigins = []string{transport.AnyOrigin}
	ts := newTransportTestServer(t, opts)

	for _, origin := range []string{trusted, untrusted} {
		reply := browserPOST(t, ts.URL, origin)
		if reply.status != http.StatusOK {
			t.Errorf("%s = %d, want %d under the wildcard", origin, reply.status, http.StatusOK)
		}
		if got := reply.header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("Allow-Origin = %q, want %q echoed even under the wildcard", got, origin)
		}
	}
	if got := postMCP(t, ts.URL, listToolsRequest).status; got != http.StatusOK {
		t.Errorf("no-Origin client = %d, want %d", got, http.StatusOK)
	}
}

// TestCrossOriginProtectedRejectsAnUnvalidatedOrigin covers the guard that
// should never fire: crossOriginProtected is only ever handed origins
// ParseTrustedOrigins has already accepted, so a value net/http refuses means
// the two disagree about what an origin is.
//
// It panics rather than dropping the entry, and the test exists to keep that
// choice deliberate: a silently dropped origin is a deployment that believes it
// trusts something it does not, which is the exact failure the allowlist was
// added to prevent.
func TestCrossOriginProtectedRejectsAnUnvalidatedOrigin(t *testing.T) {
	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("crossOriginProtected accepted an origin net/http rejects; a dropped origin must not pass silently")
		}
		if msg, ok := got.(string); !ok || !strings.Contains(msg, "AddTrustedOrigin") {
			t.Errorf("panic = %v, want it to name AddTrustedOrigin so the cause is readable", got)
		}
	}()
	crossOriginProtected([]string{"not-an-origin"}, http.NotFoundHandler())
}

// teapotHandler stands in for the MCP handler in the routing tests. It answers
// a status nothing else in the mux produces, so "this request reached the MCP
// endpoint" is unambiguous rather than inferred from a body.
func teapotHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
}

// wantSecurityHeaders is the exact set securityHeaders states about this server
// on every response, written once so each assertion below checks the same
// contract instead of its own copy of it.
var wantSecurityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Referrer-Policy":         "no-referrer",
	"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	"Cache-Control":           "no-store",
}

// sentHeader returns the response headers as of WriteHeader — what a client
// actually receives.
//
// rec.Header() would be the wrong thing to assert on here: it is the live map,
// and it keeps accepting writes long after the status line has gone out, so a
// middleware that set its headers on the way back would still look correct
// through it while sending a client nothing.
func sentHeader(rec *httptest.ResponseRecorder) http.Header {
	// The recorder's Result carries a NopCloser over an in-memory buffer, so
	// there is nothing here to leak by not closing.
	return rec.Result().Header
}

// assertSecurityHeaders fails the test for every header in wantSecurityHeaders
// that rec did not send exactly once with the expected value, and for a
// Strict-Transport-Security this process is in no position to promise.
//
// overridden names the headers the route under test deliberately replaces — the
// card's Cache-Control is the only one — which the caller checks itself.
func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder, overridden ...string) {
	t.Helper()
	h := sentHeader(rec)
	for name, want := range wantSecurityHeaders {
		if slices.Contains(overridden, name) {
			continue
		}
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
		// Set, never Add. The card route states nosniff itself, so an Add would
		// ship that header twice on exactly the response a scanner reads.
		if values := h.Values(name); len(values) > 1 {
			t.Errorf("%s appears %d times (%q), want exactly one", name, len(values), values)
		}
	}
	// Absent on purpose: this process usually speaks plain HTTP to a proxy or to
	// localhost, where HSTS is either a claim it cannot make or one that poisons
	// the browser's cache for that host and port.
	if got := h.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q, want none: this server does not terminate TLS", got)
	}
}

// securedRequest is one request whose response must carry the security headers,
// paired with the status written by whichever layer answers it.
type securedRequest struct {
	name       string
	method     string
	path       string
	header     http.Header
	wantStatus int
}

// TestSecurityHeadersAreSetOnEveryResponse pins the middleware's placement, not
// just its contents. It sits outermost and writes on the way in, so a layer that
// answers without ever calling next — the 403 from the cross-origin protection,
// the 204 preflight, the 404 for an unserved path — still ships the headers.
// Asserting only on a 200 would pass against a middleware that wrote them on the
// way out and lost every one of those responses.
func TestSecurityHeadersAreSetOnEveryResponse(t *testing.T) {
	const trusted = "https://claude.ai"
	handler := newHTTPHandler(teapotHandler(), nil, []string{trusted}, "/", false)

	cases := []securedRequest{
		{
			name:       "health, written by this package",
			method:     http.MethodGet,
			path:       "/health",
			wantStatus: http.StatusOK,
		},
		{
			name:       "mcp endpoint, written by the handler beneath",
			method:     http.MethodPost,
			path:       "/",
			wantStatus: http.StatusTeapot,
		},
		{
			name:       "404 for a path this server does not serve",
			method:     http.MethodGet,
			path:       "/nope",
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "204 preflight, answered by browserCORS",
			method: http.MethodOptions,
			path:   "/",
			header: http.Header{
				"Origin":            {trusted},
				headerRequestMethod: {http.MethodPost},
				headerRequestHeader: {"content-type"},
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:   "403 refusal, answered by the cross-origin protection",
			method: http.MethodPost,
			path:   "/",
			header: http.Header{
				"Origin":         {"https://evil.invalid"},
				"Sec-Fetch-Site": {"cross-site"},
			},
			wantStatus: http.StatusForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			maps.Copy(req.Header, tc.header)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			assertSecurityHeaders(t, rec)
		})
	}
}

// TestSecurityHeadersLeaveCORSAndVaryAlone pins what the middleware must not
// touch. Three handlers already own Vary and the Access-Control-* headers, and a
// second writer there is how a browser ends up rejecting a response that curl
// reports as a clean 204.
//
// Both halves are needed. A middleware that introduced a Vary of its own would
// still look correct on the routes that overwrite it, and one that clobbered a
// handler's value would still look correct on the routes that set none.
func TestSecurityHeadersLeaveCORSAndVaryAlone(t *testing.T) {
	const origin = "https://claude.ai"

	t.Run("states none of its own", func(t *testing.T) {
		bare := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		rec := serveThrough(t, securityHeaders(false, bare))
		assertSecurityHeaders(t, rec)
		for name := range sentHeader(rec) {
			if name == "Vary" || strings.HasPrefix(name, "Access-Control-") {
				t.Errorf("%s = %q, want none: the middleware owns neither", name, sentHeader(rec).Values(name))
			}
		}
	})

	t.Run("leaves the answering handler's alone", func(t *testing.T) {
		cors := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Vary", "Origin, "+headerRequestHeader)
			w.Header().Set(headerAllowOrigin, origin)
			w.WriteHeader(http.StatusNoContent)
		})
		rec := serveThrough(t, securityHeaders(false, cors))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
		}
		assertSecurityHeaders(t, rec)
		sent := sentHeader(rec)
		if got := sent.Get("Vary"); got != "Origin, "+headerRequestHeader {
			t.Errorf("Vary = %q, want the answering handler's value untouched", got)
		}
		// One Vary listing every header the answer derives from, not several:
		// Header.Get reads only the first line, so a second is a value half of
		// any reader misses.
		if values := sent.Values("Vary"); len(values) != 1 {
			t.Errorf("Vary appears %d times (%q), want exactly one", len(values), values)
		}
		if got := sent.Get(headerAllowOrigin); got != origin {
			t.Errorf("%s = %q, want %q untouched", headerAllowOrigin, got, origin)
		}
	})
}

// serveThrough drives one plain OPTIONS request through handler and returns the
// recorder, so a test asserting on headers alone does not restate the three
// lines that produce them.
func serveThrough(t *testing.T, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/", nil))
	return rec
}

// TestNormalizeBasePath covers the shapes a --http-path may be written in. The
// function is lenient about the input and strict about the output — "" for the
// root, "/prefix" with no trailing slash otherwise — so every caller
// concatenates instead of deciding again whether a slash is needed.
func TestNormalizeBasePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"root", "/", ""},
		{"bare word", "libgen", "/libgen"},
		{"leading slash", "/libgen", "/libgen"},
		{"trailing slash", "/libgen/", "/libgen"},
		{"doubled slashes both ends", "//libgen//", "/libgen"},
		{"surrounding whitespace", "  /libgen/  ", "/libgen"},
		{"nested prefix", "/a/b/", "/a/b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeBasePath(tc.in); got != tc.want {
				t.Errorf("normalizeBasePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidateBasePath covers the startup guard. A path this mux could never
// match, or one that would match something the operator did not mean, is
// refused before anything is served — and the message names the offending value,
// because the symptom otherwise is a server answering 404 to everything, which
// reads as a proxy fault.
func TestValidateBasePath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"root", "/", false},
		{"empty is the root", "", false},
		{"plain prefix", "/libgen", false},
		{"nested prefix", "/a/b", false},
		{"query", "/libgen?x=1", true},
		{"fragment", "/libgen#frag", true},
		{"traversal", "/a/../b", true},
		{"percent escape", "/lib%2Fgen", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBasePath(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateBasePath(%q) = %v, want error: %t", tc.in, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), tc.in) {
				t.Errorf("error = %q, want it to name the rejected value %q", err, tc.in)
			}
		})
	}
}

// TestEndpointPatterns pins the patterns the MCP endpoint is mounted on. At the
// root it is the exact-match wildcard and nothing else — mounting on a bare "/"
// is what made the handler a catch-all that answered every path — and under a
// prefix both spellings are accepted, since a client handed a base URL may or
// may not keep the trailing slash.
func TestEndpointPatterns(t *testing.T) {
	cases := []struct {
		name string
		base string
		want []string
	}{
		{"root", "", []string{"/{$}"}},
		{"prefix", "/libgen", []string{"/libgen", "/libgen/{$}"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := endpointPatterns(tc.base); !slices.Equal(got, tc.want) {
				t.Errorf("endpointPatterns(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

// TestNotFoundNamesTheMCPEndpoint covers the body and the status. The status is
// the point: every unknown path used to reach the MCP handler and come back 405
// with Allow: POST, asserting both that the route exists and that another method
// would work. Naming the real endpoint in the body is what makes the 404
// actionable for whoever mistyped the path.
func TestNotFoundNamesTheMCPEndpoint(t *testing.T) {
	cases := []struct {
		name         string
		base         string
		wantEndpoint string
	}{
		{"root", "", "/"},
		{"prefix", "/libgen", "/libgen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil)
			notFound(tc.base).ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
			if got := sentHeader(rec).Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			var body struct {
				Error       string `json:"error"`
				MCPEndpoint string `json:"mcp_endpoint"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
			}
			if body.Error != "not found" {
				t.Errorf("error = %q, want %q", body.Error, "not found")
			}
			if body.MCPEndpoint != tc.wantEndpoint {
				t.Errorf("mcp_endpoint = %q, want %q", body.MCPEndpoint, tc.wantEndpoint)
			}
			if !strings.HasSuffix(rec.Body.String(), "\n") {
				t.Errorf("body = %q, want a trailing newline so a terminal reading it does not run on", rec.Body.String())
			}
		})
	}
}

// routeCase is one request the mounted handler must answer in a particular way:
// the status, the Content-Type when the route states one, and the Cache-Control
// when the route replaces the middleware's no-store.
type routeCase struct {
	name       string
	method     string
	path       string
	wantStatus int
	wantType   string
	wantCache  string
}

// mountedRoutes lists what newHTTPHandler must answer for a normalized base
// prefix — "" at the root, "/libgen" under a mount. Everything outside the mount
// belongs to somebody else: the flag exists for a proxy that forwards its prefix
// rather than rewriting it away, so a request arriving without the prefix was
// not meant for this server.
func mountedRoutes(prefix string) []routeCase {
	cases := []routeCase{
		{name: "health", method: http.MethodGet, path: prefix + "/health", wantStatus: http.StatusOK, wantType: "application/json"},
		{name: "legacy card path", method: http.MethodGet, path: prefix + serverCardPath, wantStatus: http.StatusOK, wantType: "application/json", wantCache: "public, max-age=3600"},
		{name: "current card path", method: http.MethodGet, path: prefix + serverCardCurrentPath, wantStatus: http.StatusOK, wantType: serverCardMediaType, wantCache: "public, max-age=3600"},
		{name: "mcp endpoint with a trailing slash", method: http.MethodPost, path: prefix + "/", wantStatus: http.StatusTeapot},
		{name: "unknown path under the mount", method: http.MethodGet, path: prefix + "/nope", wantStatus: http.StatusNotFound, wantType: "application/json"},
		// The probe that started this: a scanner asking for OAuth discovery got
		// 405 from the catch-all, which told it the endpoint exists.
		{name: "oauth discovery probe", method: http.MethodGet, path: prefix + "/.well-known/oauth-protected-resource", wantStatus: http.StatusNotFound, wantType: "application/json"},
	}
	if prefix == "" {
		return cases
	}
	return append(cases,
		routeCase{name: "mcp endpoint without a trailing slash", method: http.MethodPost, path: prefix, wantStatus: http.StatusTeapot},
		routeCase{name: "health outside the mount", method: http.MethodGet, path: "/health", wantStatus: http.StatusNotFound, wantType: "application/json"},
		routeCase{name: "root outside the mount", method: http.MethodGet, path: "/", wantStatus: http.StatusNotFound, wantType: "application/json"},
	)
}

// assertRoute drives one routeCase against handler and checks the status, the
// Content-Type the route states, and the security headers every response of this
// server carries.
func assertRoute(t *testing.T, handler http.Handler, tc routeCase) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))

	if rec.Code != tc.wantStatus {
		t.Fatalf("%s %s = %d, want %d (body %q)", tc.method, tc.path, rec.Code, tc.wantStatus, rec.Body.String())
	}
	if got := sentHeader(rec).Get("Content-Type"); tc.wantType != "" && got != tc.wantType {
		t.Errorf("%s Content-Type = %q, want %q", tc.path, got, tc.wantType)
	}
	if tc.wantCache == "" {
		assertSecurityHeaders(t, rec)
		return
	}
	assertSecurityHeaders(t, rec, "Cache-Control")
	if got := sentHeader(rec).Get("Cache-Control"); got != tc.wantCache {
		t.Errorf("%s Cache-Control = %q, want the route's own %q", tc.path, got, tc.wantCache)
	}
}

// TestNewHTTPHandlerRoutes covers the mount as a whole for both shapes of
// --http-path. Every route this server answers lives under the base path, both
// spellings of a prefixed endpoint reach the MCP handler, and everything else —
// inside the mount or outside it — is a 404 naming the endpoint rather than a
// 405 claiming the path exists.
func TestNewHTTPHandlerRoutes(t *testing.T) {
	card, err := buildServerCard(t.Context(), newCardTestServer())
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	for _, base := range []string{"/", "/libgen"} {
		t.Run(base, func(t *testing.T) {
			handler := newHTTPHandler(teapotHandler(), card, nil, base, false)
			for _, tc := range mountedRoutes(normalizeBasePath(base)) {
				t.Run(tc.name, func(t *testing.T) {
					assertRoute(t, handler, tc)
				})
			}
		})
	}
}

// TestNewHTTPHandlerAcceptsEveryBasePathSpelling asserts the handler normalizes
// its own argument, so a mount configured as "libgen", "/libgen" or "/libgen/"
// answers on the same routes. Without it an operator's trailing slash would
// silently produce a server whose every path is a 404.
func TestNewHTTPHandlerAcceptsEveryBasePathSpelling(t *testing.T) {
	for _, base := range []string{"libgen", "/libgen", "/libgen/"} {
		t.Run(base, func(t *testing.T) {
			handler := newHTTPHandler(teapotHandler(), nil, nil, base, false)
			assertRoute(t, handler, routeCase{
				method: http.MethodGet, path: "/libgen/health",
				wantStatus: http.StatusOK, wantType: "application/json",
			})
			assertRoute(t, handler, routeCase{
				method: http.MethodPost, path: "/libgen", wantStatus: http.StatusTeapot,
			})
		})
	}
}

// TestMainWithExitRejectsAnUnmountablePath covers the startup guard from the
// flag side: a --http-path carrying a traversal is refused before anything is
// served. Deferring it to the first request would surface as a server answering
// 404 to every path, which looks like a proxy fault and is the hardest kind of
// misconfiguration to find.
func TestMainWithExitRejectsAnUnmountablePath(t *testing.T) {
	if code := callMainWithExit(t, "libgen-mcp", "--http-path", "/a/../b"); code != 1 {
		t.Fatalf("mainWithExit(--http-path=/a/../b) = %d, want 1", code)
	}
}
