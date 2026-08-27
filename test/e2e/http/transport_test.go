//go:build httpe2e

package httpe2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestTransport_HealthPayload pins what a container or load balancer reads.
//
// The endpoint is not only a status code: a probe that reports 200 while saying
// nothing about which build answered is a probe that cannot tell two
// deployments apart, which is exactly the question asked when one of them is
// misbehaving.
func TestTransport_HealthPayload(t *testing.T) {
	s := startServer(t, nil)

	reply := s.do(t, request{method: http.MethodGet, path: "/health"})
	if reply.status != http.StatusOK {
		t.Fatalf("status = %d, want %d", reply.status, http.StatusOK)
	}
	if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(reply.body), &payload); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, truncate(reply.body))
	}
	for _, key := range []string{"status", "version", "commit", "started_at", "uptime_seconds"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("payload has no %q: %s", key, truncate(reply.body))
		}
	}
	if payload["status"] != "ok" {
		t.Errorf("status = %v, want \"ok\"", payload["status"])
	}

	// The no-buffering header is scoped to the MCP endpoint: a probe answers a
	// small JSON body with nothing to stream, and a header that leaked onto
	// every response would be telling proxies something untrue about it.
	if got := reply.header.Get("X-Accel-Buffering"); got != "" {
		t.Errorf("X-Accel-Buffering = %q on /health, want none", got)
	}
}

// TestTransport_CardPreflightIsAnswered covers the route mounted beside the
// card and exercised nowhere else over a real socket.
//
// A plain fetch of the card never preflights, so this branch exists for the
// caller that adds a header of its own — a scanner stamping a request id. Its
// request is refused unless the preflight names that header back.
func TestTransport_CardPreflightIsAnswered(t *testing.T) {
	s := startServer(t, nil)

	reply := s.do(t, request{
		method: http.MethodOptions,
		path:   "/.well-known/mcp/server-card.json",
		headers: map[string]string{
			"Origin":                         untrustedOrigin,
			"Access-Control-Request-Method":  http.MethodGet,
			"Access-Control-Request-Headers": "x-scanner-id",
		},
	})
	if reply.status != http.StatusNoContent {
		t.Fatalf("card preflight = %d, want %d", reply.status, http.StatusNoContent)
	}
	// The card is public, so its answer is the same for every caller — unlike
	// the MCP endpoint, whose trust is named.
	if got := reply.header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
	if got := reply.header.Get("Access-Control-Allow-Headers"); got != "x-scanner-id" {
		t.Errorf("Allow-Headers = %q, want the requested header echoed", got)
	}
	if !strings.Contains(reply.header.Get("Vary"), "Access-Control-Request-Headers") {
		t.Errorf("Vary = %q, want it to name Access-Control-Request-Headers", reply.header.Get("Vary"))
	}
	if reply.header.Get("Access-Control-Max-Age") == "" {
		t.Error("no Access-Control-Max-Age: a scanner would preflight on every fetch")
	}
}

// TestTransport_ConcurrentClientsAreServed checks the property every other case
// assumes: one client's request does not serialize behind another's.
//
// It is worth asserting because the SSE path holds a response open, and a
// handler chain that accidentally shared state would show up here and nowhere
// else in this module.
func TestTransport_ConcurrentClientsAreServed(t *testing.T) {
	s := startServer(t, nil)

	const clients = 25
	var wg sync.WaitGroup
	results := make(chan int, clients)
	for range clients {
		wg.Go(func() {
			results <- s.do(t, mcpPOST(nil)).status
		})
	}
	wg.Wait()
	close(results)

	for status := range results {
		if status != http.StatusOK {
			t.Errorf("a concurrent client got %d, want %d", status, http.StatusOK)
		}
	}
	if !s.healthy(t) {
		t.Error("the server stopped serving after concurrent load")
	}
}

// TestTransport_GracefulShutdownOnSIGTERM covers the path a container runtime
// takes on every deploy, and which nothing else in this module reaches.
//
// The requirement is that the signal is handled rather than defaulted: the
// process drains and exits on its own, within the shutdown budget, instead of
// being killed. A server that ignored SIGTERM would be killed by the runtime
// after its grace period with requests still in flight.
func TestTransport_GracefulShutdownOnSIGTERM(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// context.Background rather than the test's: this case is about the process
	// exiting on its own when signaled, so a context that canceled it on
	// cleanup would be answering a different question. The explicit Kill below
	// is what guarantees nothing outlives the test.
	cmd := exec.CommandContext(context.Background(), serverBinary(t), "--http", addr) //nolint:gosec // the binary this package built, with literal args
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
	})

	waitHealthy(t, &server{baseURL: "http://" + addr, logs: func() string { return "" }})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling: %v", err)
	}

	// The shutdown budget is 5s; twice that is a generous ceiling that still
	// fails a server which ignored the signal outright.
	select {
	case err := <-stopped:
		if err != nil {
			t.Logf("exited with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the server did not exit within 15s of SIGTERM; the signal is not being handled")
	}

	// And the port is released, which is what lets a redeploy bind it again.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/health", http.NoBody)
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return // refused: the listener is gone, which is the point
		}
		_ = resp.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("the address still answers after shutdown; the listener was not released")
}

// TestTransport_VersionFlagExitsWithoutServing pins the one flag that must not
// start a server: a container whose command is wrong should say so and stop,
// not bind a port and sit there looking healthy.
func TestTransport_VersionFlagExitsWithoutServing(t *testing.T) {
	out, err := runServerExpectingExit(t, "--version")
	if err != nil {
		t.Fatalf("--version exited with %v. Output:\n%s", err, out)
	}
	if !strings.Contains(out, "libgen-mcp") {
		t.Errorf("output does not name the binary:\n%s", out)
	}
	if !strings.Contains(out, "commit") {
		t.Errorf("output does not carry the commit:\n%s", out)
	}
}
