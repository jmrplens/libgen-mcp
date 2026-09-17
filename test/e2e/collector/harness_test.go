//go:build collectore2e

// harness_test.go starts the real binary against the real collector.
//
// It is a smaller sibling of test/e2e/http's harness and deliberately not a copy
// of it: what this module needs is one server, pointed at a container, with a
// fixture upstream so a tool call has something to reach. Two lessons are
// carried over from the sibling project's duplicate rather than rediscovered —
// the race flag reaches the server build, and liveness is decided by asking
// whether the process is alive rather than whether a port answers.
package collectore2e

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildOnce keeps one `go build` for the whole package.
var (
	buildOnce  sync.Once
	binaryPath string
	errBuild   error
)

// serverBinary builds cmd/server once and returns the path.
//
// -race is passed through when the suite itself is running under the detector.
// Without that, a module that builds its own binary quietly tests an
// uninstrumented one: the flag on the `go test` command line applies to the test
// process, and the server is a separate program.
func serverBinary(t *testing.T) string {
	t.Helper()

	buildOnce.Do(func() {
		// t.TempDir cannot be used here: the binary is built once for the whole
		// package under sync.Once, and the first test to arrive would own a
		// directory removed when that test ends, leaving every later test
		// pointing at a path that no longer exists.
		dir, err := os.MkdirTemp("", "libgen-mcp-collectore2e") //nolint:usetesting // see above
		if err != nil {
			errBuild = err
			return
		}
		binaryPath = filepath.Join(dir, "libgen-mcp")
		args := []string{"build"}
		if raceEnabled {
			args = append(args, "-race")
		}
		args = append(args, "-o", binaryPath, "../../../cmd/server")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, "go", args...).CombinedOutput()
		if err != nil {
			errBuild = fmt.Errorf("building cmd/server: %w\n%s", err, out)
		}
	})
	if errBuild != nil {
		t.Fatalf("%v", errBuild)
	}
	return binaryPath
}

// server is a running libgen-mcp, with its output kept for assertions.
type server struct {
	baseURL string
	logs    func() string
}

// startServer runs the binary against the collector and a fixture mirror.
//
// The environment is the minimum that makes a tool call reach something: a
// mirror on loopback, the private-address guard lifted for it, and the export
// schedule shortened so a case does not wait ten seconds for a batch. Every
// OTEL_ duration is an integer number of milliseconds, per the specification —
// "200ms" parses as nothing and keeps the default.
func startServer(t *testing.T, c *collector, env map[string]string) *server {
	t.Helper()

	m := startMirror(t)
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	base := map[string]string{
		"LIBGEN_MCP_LOG_LEVEL":               "info",
		"LIBGEN_MCP_DOWNLOAD_DIR":            t.TempDir(),
		"LIBGEN_MIRROR":                      m,
		"LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES": "true",
		"LIBGEN_MCP_SOURCES":                 "libgen",
		"LIBGEN_MCP_EXTRA_SOURCES":           "never",
		"LIBGEN_MCP_TIMEOUT":                 "5s",
		"LIBGEN_MCP_TELEMETRY":               "1",
		"OTEL_EXPORTER_OTLP_ENDPOINT":        c.endpoint,
		"OTEL_EXPORTER_OTLP_TIMEOUT":         "5000",
		"OTEL_BSP_SCHEDULE_DELAY":            "200",
		"OTEL_BLRP_SCHEDULE_DELAY":           "200",
		"OTEL_METRIC_EXPORT_INTERVAL":        "500",
		"OTEL_SERVICE_NAME":                  "libgen-mcp-collectore2e",
	}
	maps.Copy(base, env)

	ctx, cancel := context.WithCancel(context.Background())
	//#nosec G204 -- the binary this package built, on a port it reserved
	cmd := exec.CommandContext(ctx, serverBinary(t), "--http", addr)
	cmd.Env = append(os.Environ(), raceEnviron()...)
	for k, v := range base {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var mu sync.Mutex
	var out bytes.Buffer
	cmd.Stdout = &lockedWriter{mu: &mu, buf: &out}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("starting the server: %v", err)
	}
	s := &server{
		baseURL: "http://" + addr,
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

	s.waitHealthy(t, cmd)
	return s
}

// waitHealthy polls /health, and gives up early when the process is gone.
//
// Asking the process rather than only the port is the difference between "not
// ready yet" and "refused to start": a server that exited on a configuration
// error would otherwise be waited on for the whole deadline and then reported as
// slow, which sends a reader looking in the wrong place.
func (s *server) waitHealthy(t *testing.T, cmd *exec.Cmd) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil {
			t.Fatalf("the server exited before serving:\n%s", s.logs())
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.baseURL+"/health", nil)
		if err != nil {
			t.Fatalf("building the health probe: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the server never became healthy:\n%s", s.logs())
}

// call sends one JSON-RPC body and returns the reply.
func (s *server) call(t *testing.T, body string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.baseURL+"/", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building the call: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("calling the server: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var reply bytes.Buffer
	if _, copyErr := reply.ReadFrom(resp.Body); copyErr != nil {
		t.Fatalf("reading the reply: %v", copyErr)
	}
	return reply.String()
}

// startMirror serves an empty catalog page, so a search succeeds without the
// network and without a fixture corpus this module has no use for.
func startMirror(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><table></table></body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// lockedWriter serializes the two pipes into one buffer.
type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

// Write appends under the lock.
func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
