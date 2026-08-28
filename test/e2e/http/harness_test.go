//go:build httpe2e

// harness_test.go starts the real server binary over HTTP and drives it the way
// a client does.
//
// The existing e2e suite exercises tool behavior through an in-memory MCP
// transport, which is the right shape for that question and answers none of
// this one. Cross-origin decisions, the preflight, the no-buffering header, the
// resource-method guard and every flag that limits something live in the
// handler chain in package main — newHTTPHandler, browserCORS,
// crossOriginProtected, sseNoBuffering — which no unit test can import. A test
// that reassembled that chain would be testing its own copy rather than the
// binary this repository ships.
//
// The binary is built once and started per test with its own flags, because the
// behaviors under test are configuration-dependent by nature: the same request
// must be refused with one flag set and accepted with another.
package httpe2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The headers a client sends on a normal call. The protocol version is the
// legacy-era one on purpose: it is what a plain POST negotiates without the
// per-request _meta the 2026-07-28 era requires, so a test that is not about
// version negotiation does not have to carry that ceremony.
const (
	protocolVersion = "2025-11-25"
	acceptHeader    = "application/json, text/event-stream"
	toolsListBody   = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
)

var (
	buildOnce   sync.Once
	builtBinary string
	errBuild    error
)

// serverBinary builds cmd/server once for the whole package and returns its
// path. Building rather than importing is deliberate: the handler chain being
// tested is assembled in package main and cannot be imported, and a test that
// reassembled it would be testing its own copy.
func serverBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		// t.TempDir cannot be used here: the binary is built once for the
		// whole package under sync.Once, and the first test to arrive would
		// own a directory removed when that test ends, leaving every later
		// test pointing at a path that no longer exists.
		dir, err := os.MkdirTemp("", "libgen-mcp-httpe2e") //nolint:usetesting // see above
		if err != nil {
			errBuild = err
			return
		}
		out := filepath.Join(dir, "libgen-mcp")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/server")
		cmd.Dir = repoRoot()
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			errBuild = fmt.Errorf("building cmd/server: %w\n%s", runErr, output)
			return
		}
		builtBinary = out
	})
	if errBuild != nil {
		t.Fatalf("%v", errBuild)
	}
	return builtBinary
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// freePort reserves a port by binding and releasing it. A small race with
// another process remains, which is why startServer retries readiness rather
// than assuming the port is ours the instant we ask for it.
func freePort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if closeErr := l.Close(); closeErr != nil {
		t.Fatalf("releasing the reserved port: %v", closeErr)
	}
	return port
}

// server is a running binary under test.
type server struct {
	baseURL string
	// base is the normalized --http-path the server was started with: "" at the
	// root, "/libgen" under a prefix. Every route the server mounts moves with
	// it, the health probe included, so a harness that always looked at
	// "/health" would time out on a prefixed server that started perfectly.
	base string
	logs func() string
}

// startServer launches the binary with the given extra flags and environment,
// waits for /health, and stops it when the test ends.
func startServer(t *testing.T, env map[string]string, flags ...string) *server {
	t.Helper()
	return startServerOnPort(t, freePort(t), env, flags...)
}

// startServerOnPort is startServer with the port chosen by the caller, for
// tests that must configure something in front of it before it starts.
func startServerOnPort(t *testing.T, port int, env map[string]string, flags ...string) *server {
	t.Helper()

	bin := serverBinary(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	args := append([]string{"--http", addr}, flags...)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	// A download directory inside the test's own tree keeps a stray write from
	// landing in the runner's home, and the log level keeps the process output
	// useful when a startup failure has to explain itself.
	cmd.Env = append(os.Environ(),
		"LIBGEN_MCP_LOG_LEVEL=info",
		"LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir(),
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var out bytes.Buffer
	var mu sync.Mutex
	cmd.Stdout = &lockedWriter{mu: &mu, buf: &out}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("starting the server: %v", err)
	}

	srv := &server{
		baseURL: "http://" + addr,
		base:    basePathFromFlags(flags),
		logs: func() string {
			mu.Lock()
			defer mu.Unlock()
			return out.String()
		},
	}

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	waitHealthy(t, srv)
	return srv
}

// basePathFromFlags reports the mount point a caller asked for with
// --http-path, in the normalized form the server itself computes.
//
// It reads the flags rather than taking a parameter of its own because the
// mount reaches the harness the way every other setting does — as a flag on
// startServer. A separate argument would be one more thing to remember, and
// forgetting it would show up as a health poll timing out against a server that
// started correctly and simply mounted /health somewhere else.
//
// Both spellings are recognized, since either is a reasonable way to write it
// and only one of them would otherwise work.
func basePathFromFlags(flags []string) string {
	const name = "http-path"
	for i, f := range flags {
		trimmed := strings.TrimLeft(f, "-")
		if value, ok := strings.CutPrefix(trimmed, name+"="); ok {
			return normalizeBase(value)
		}
		if trimmed == name && i+1 < len(flags) {
			return normalizeBase(flags[i+1])
		}
	}
	return ""
}

// normalizeBase mirrors the server's own normalizeBasePath: "" for the root,
// "/prefix" with no trailing slash otherwise.
//
// The test has to reach the same answer the server did, because that answer is
// where the routes are — reproducing the rule is what lets a case pass
// "libgen", "/libgen" or "/libgen/" and still know where to look.
func normalizeBase(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

// lockedWriter serializes writes from the process's two pipes into one buffer
// the test can read while the process is still running.
type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// waitHealthy polls /health until the server answers or the deadline passes.
// A failure dumps the process output, because a server that refuses to start
// has already said why and the test should not make anyone go looking.
func waitHealthy(t *testing.T, s *server) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.baseURL+s.base+"/health", http.NoBody)
		if err != nil {
			t.Fatalf("building the health request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("server never became healthy. Output:\n%s", s.logs())
}

// healthy reports whether the server is still answering. Every robustness case
// ends with this: the bar there is not that a hostile request gets the right
// error, it is that the process survives it and keeps serving everyone else.
func (s *server) healthy(t *testing.T) bool {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.baseURL+s.base+"/health", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// request is one HTTP call to the server under test.
type request struct {
	method  string
	path    string
	body    string
	headers map[string]string
}

// response is what a test asserts against.
type response struct {
	status int
	header http.Header
	body   string
}

// do issues the request and returns the response, failing the test only when
// the call could not be made at all — a rejection is data, not an error.
func (s *server) do(t *testing.T, r request) response {
	t.Helper()

	method := r.method
	if method == "" {
		method = http.MethodPost
	}
	path := r.path
	if path == "" {
		// The MCP endpoint is the root, which is where a server started
		// without --http-path mounts it. A case that moves the mount names
		// its paths in full, because the point there is which path answers.
		path = "/"
	}
	var body io.Reader = http.NoBody
	if r.body != "" {
		body = strings.NewReader(r.body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, s.baseURL+path, body)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", acceptHeader)
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("reading the response body: %v", err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

// try issues the request from a goroutine safely, returning the response or the
// error rather than failing the test where it runs.
//
// (*server).do calls t.Fatalf, and t.Fatalf calls runtime.Goexit: from a
// worker goroutine that kills the worker without failing the test, so a caller
// waiting on a channel would block until its own timeout and report a hang that
// never happened. Anything run off the test goroutine has to use this instead.
func (s *server) try(t *testing.T, r request) (response, error) {
	t.Helper()

	method := r.method
	if method == "" {
		method = http.MethodPost
	}
	path := r.path
	if path == "" {
		path = "/"
	}
	var body io.Reader = http.NoBody
	if r.body != "" {
		body = strings.NewReader(r.body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, s.baseURL+path, body)
	if err != nil {
		return response{}, err
	}
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", acceptHeader)
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil && !errors.Is(err, io.EOF) {
		return response{}, err
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(raw)}, nil
}

// mcpPOST is the common case: a tools/list call with the headers a real client
// sends, plus whatever the test adds.
func mcpPOST(headers map[string]string) request {
	return request{method: http.MethodPost, path: "/", body: toolsListBody, headers: headers}
}

// browserPOST is the request a browser makes after a successful preflight: the
// same call carrying the two headers only a browser sets.
func browserPOST(origin string) request {
	return mcpPOST(map[string]string{"Origin": origin, "Sec-Fetch-Site": "cross-site"})
}

// preflightHeaders are the non-safelisted request headers browserPOST sends,
// lowercased the way a browser lists them.
//
// Both of them, not just the content type: MCP-Protocol-Version is not
// CORS-safelisted either, so a browser asks permission for it too. A preflight
// that named only content-type would pass while a real browser blocked the POST
// for the header it did not ask about — the exact shape of failure this module
// exists to catch, one header along.
const preflightHeaders = "content-type,mcp-protocol-version"

// preflightFor is the OPTIONS a browser sends before a cross-origin POST that
// carries non-safelisted headers.
func preflightFor(origin string) request {
	return request{
		method: http.MethodOptions,
		path:   "/",
		headers: map[string]string{
			"Origin":                         origin,
			"Access-Control-Request-Method":  http.MethodPost,
			"Access-Control-Request-Headers": preflightHeaders,
		},
	}
}

// runServerExpectingExit starts the binary and waits for it to exit, returning
// its combined output. It is for startup-validation tests, where the server is
// supposed to refuse to run.
func runServerExpectingExit(t *testing.T, args ...string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	// The binary is the one this package just built from ./cmd/server, and the
	// arguments are literals written in the test above; neither reaches here
	// from outside the test binary.
	cmd := exec.CommandContext(ctx, serverBinary(t), args...) //nolint:gosec // see above
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	// A deadline kill is not a refusal. Without this check a server that
	// happily accepted a bad flag and served for thirty seconds would be
	// killed by the context, and the non-nil error that produced would read
	// exactly like a refusal — the test passing for the opposite of the reason
	// it was written.
	if ctx.Err() != nil {
		t.Fatalf("the server was still running when the deadline killed it; it did not refuse to start. Output:\n%s", out)
	}
	return string(out), err
}

// itoa keeps strconv out of every test file that needs one port number.
func itoa(n int) string { return strconv.Itoa(n) }

// mirror is a stand-in libgen mirror the server can actually reach.
//
// Some behavior only appears once an upstream answers: a tool call that fails
// must come back as a clean MCP error rather than a panic or a hang, and the
// difference between "the mirror said no" and "the mirror never replied" is
// invisible until something plays each part. Pointing LIBGEN_MIRROR at a
// domain that does not resolve exercises neither.
type mirror struct {
	url   string
	calls func() int
}

// startMirror serves a mirror whose every response is produced by respond, so a
// test can choose what kind of misbehavior it is reproducing.
func startMirror(t *testing.T, respond http.HandlerFunc) *mirror {
	t.Helper()

	var mu sync.Mutex
	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(srv.Close)

	return &mirror{
		url: srv.URL,
		calls: func() int {
			mu.Lock()
			defer mu.Unlock()
			return calls
		},
	}
}

// mirrorEnv points the server at m and lifts the private-address guard, which
// would otherwise refuse 127.0.0.1 before the request left the process — the
// guard is right in production and exactly wrong for a fake on loopback.
func mirrorEnv(m *mirror) map[string]string {
	return map[string]string{
		"LIBGEN_MIRROR":                      m.url,
		"LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES": "true",
		"LIBGEN_MCP_SOURCES":                 "libgen",
		"LIBGEN_MCP_TIMEOUT":                 "5s",
		"LIBGEN_MCP_RETRY_ATTEMPTS":          "1",
	}
}

// rawRequest writes bytes straight onto a TCP connection, bypassing net/http's
// client-side validation.
//
// Go refuses to send a header value containing CR, LF or a NUL, and refuses to
// parse a URL with a control character in it — which means the interesting
// attacks cannot be expressed through http.Client at all. An attacker has no
// such restriction, so the request is spelled out by hand. The reply is read
// with a deadline: a server that answers nothing is as much a finding as one
// that answers wrongly.
func (s *server) raw(t *testing.T, wire string) string {
	t.Helper()

	addr := strings.TrimPrefix(s.baseURL, "http://")
	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dialing %s: %v", addr, err)
	}
	defer conn.Close()

	if deadlineErr := conn.SetDeadline(time.Now().Add(10 * time.Second)); deadlineErr != nil {
		t.Fatalf("setting the deadline: %v", deadlineErr)
	}
	if _, writeErr := conn.Write([]byte(wire)); writeErr != nil {
		// A server that closes on a malformed request line is a valid answer.
		return ""
	}

	var reply strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := conn.Read(buf)
		reply.Write(buf[:n])
		if readErr != nil || reply.Len() > 64*1024 {
			break
		}
	}
	return reply.String()
}
