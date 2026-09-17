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
	"syscall"
	"testing"
	"time"
)

// TestDrain_HealthAnnouncesBeforeTheListenerCloses is the ordering the delay
// exists for, over the real binary and a real signal.
//
// Without it the close is what a balancer notices, one probe later, and every
// request it sent in that window failed. The assertion is therefore about
// sequence, not about the delay's length: the 503 has to be readable while the
// listener is still accepting connections.
//
// The process is not started through the harness: the harness kills with a
// context cancel, and what is under test is the graceful path a supervisor
// takes.
func TestDrain_HealthAnnouncesBeforeTheListenerCloses(t *testing.T) {
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	//nolint:gosec // the binary this package built, on a port it reserved
	cmd := exec.CommandContext(context.Background(), serverBinary(t),
		"--http", fmt.Sprintf("127.0.0.1:%d", port), "--drain-delay", "2s")
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	waitHealthy(t, &server{baseURL: base, logs: func() string { return "" }})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling: %v", err)
	}

	// Inside the delay: the listener is still up and says it is going.
	//
	// Polled rather than probed once. The signal has to reach the process and
	// the handler has to run before the flag flips, and on a loaded machine that
	// window is wider than a loopback round trip — a single probe here reads the
	// last 200 and reports a regression that is not one. The drain delay bounds
	// the wait: past it the listener is gone and this loop fails for the right
	// reason instead.
	status, body := waitDraining(t, base, 2*time.Second)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("GET /health during the drain = %d, want %d — a balancer has nothing to act on", status, http.StatusServiceUnavailable)
	}
	if body.Status != "draining" {
		t.Errorf("body status = %q, want draining", body.Status)
	}

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("exited with %v after SIGTERM, want a clean exit", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not exit within 30s of SIGTERM")
	}

	// And afterwards the listener really is gone, so the 503 was an
	// announcement rather than the whole of the shutdown.
	if s, _ := probeHealthOnce(t, base); s != 0 {
		t.Errorf("GET /health after exit answered %d; the listener outlived the process", s)
	}
}

// TestDrain_WithNoDelayTheListenerClosesAtOnce is the default, and the reason
// the delay is opt-in: a deployment with nothing in front gains nothing from
// announcing to an audience of none, and would only look like it hangs.
func TestDrain_WithNoDelayTheListenerClosesAtOnce(t *testing.T) {
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	//nolint:gosec // the binary this package built, on a port it reserved
	cmd := exec.CommandContext(context.Background(), serverBinary(t), "--http", fmt.Sprintf("127.0.0.1:%d", port))
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	waitHealthy(t, &server{baseURL: base, logs: func() string { return "" }})

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling: %v", err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("exited with %v after SIGTERM, want a clean exit", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not exit within 30s of SIGTERM")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("shutdown took %v with no drain delay configured", elapsed)
	}
}

// TestDrain_HealthCarriesTheBuildAndDigest reads the two fields a fleet is
// compared on off the real binary, since nothing else publishes them.
func TestDrain_HealthCarriesTheBuildAndDigest(t *testing.T) {
	s := startServer(t, nil)

	status, body := probeHealthOnce(t, s.baseURL)
	if status != http.StatusOK {
		t.Fatalf("GET /health = %d, want %d", status, http.StatusOK)
	}
	if body.Build == "" {
		t.Error("build is empty")
	}
	if len(body.ConfigDigest) != 12 {
		t.Errorf("config_digest = %q, want twelve characters", body.ConfigDigest)
	}
	if strings.Trim(body.ConfigDigest, "0123456789abcdef") != "" {
		t.Errorf("config_digest = %q is not hexadecimal", body.ConfigDigest)
	}

	// Two replicas configured alike agree; one configured differently does not,
	// which is the only thing the digest is for.
	same := startServer(t, nil)
	_, sameBody := probeHealthOnce(t, same.baseURL)
	if sameBody.ConfigDigest != body.ConfigDigest {
		t.Errorf("two identically configured servers report %q and %q", body.ConfigDigest, sameBody.ConfigDigest)
	}

	other := startServer(t, nil, "--http-path", "/libgen")
	_, otherBody := probeHealthAt(t, other.baseURL+"/libgen/health")
	if otherBody.ConfigDigest == body.ConfigDigest {
		t.Errorf("a server mounted elsewhere reports the same digest %q", otherBody.ConfigDigest)
	}
}

// waitDraining polls /health until it reports the drain, and returns whatever
// the last probe saw when it does not.
//
// The caller asserts on the result rather than this helper failing the test, so
// a listener that never flips and one that flipped to something unexpected are
// both reported by the assertion that describes them.
func waitDraining(t *testing.T, base string, within time.Duration) (int, healthBody) {
	t.Helper()

	deadline := time.Now().Add(within)
	for {
		status, body := probeHealthOnce(t, base)
		if status == http.StatusServiceUnavailable || time.Now().After(deadline) {
			return status, body
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// healthBody is the subset of /health this file reads.
type healthBody struct {
	Status       string `json:"status"`
	Build        string `json:"build"`
	ConfigDigest string `json:"config_digest"`
}

// probeHealthOnce reads /health at the default path. A status of 0 means the
// listener could not be reached at all.
func probeHealthOnce(t *testing.T, base string) (int, healthBody) {
	t.Helper()
	return probeHealthAt(t, base+"/health")
}

// probeHealthAt reads /health at an explicit URL.
func probeHealthAt(t *testing.T, url string) (int, healthBody) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("building the health request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, healthBody{}
	}
	defer resp.Body.Close()

	var body healthBody
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		t.Fatalf("the health body is not JSON: %v", decodeErr)
	}
	return resp.StatusCode, body
}

// TestDrain_ASecondSignalDoesNotWaitOutTheDelay is the escape hatch an operator
// needs and the code promises.
//
// signal.NotifyContext keeps intercepting after the first signal until its stop
// function is called, so with that call deferred to the end of main a second
// SIGTERM during the drain is swallowed exactly like the first — and the drain
// can be minutes. Somebody who has decided not to wait then has nothing short of
// SIGKILL, which is the outcome a graceful shutdown exists to avoid.
//
// Driven over the real binary, because what is being asserted is the process's
// signal disposition rather than a function's.
func TestDrain_ASecondSignalDoesNotWaitOutTheDelay(t *testing.T) {
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Long enough that waiting it out is unmistakable, and short enough that a
	// regression fails this test rather than hanging it.
	//nolint:gosec // the binary this package built, on a port it reserved
	cmd := exec.CommandContext(context.Background(), serverBinary(t),
		"--http", fmt.Sprintf("127.0.0.1:%d", port), "--drain-delay", "60s")
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	waitHealthy(t, &server{baseURL: base, logs: func() string { return "" }})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling: %v", err)
	}
	// The drain has begun: the listener is up and answering 503. Waiting for it
	// is what makes the second signal land during the sleep rather than before
	// the first one was handled.
	if status, _ := waitDraining(t, base, 10*time.Second); status != http.StatusServiceUnavailable {
		t.Fatalf("GET /health during the drain = %d, want %d", status, http.StatusServiceUnavailable)
	}

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling a second time: %v", err)
	}

	select {
	case <-stopped:
		// The exit status is deliberately not asserted: the second signal is
		// the default disposition, which terminates the process rather than
		// letting it return zero, and that is the point.
	case <-time.After(20 * time.Second):
		t.Fatal("the second signal was swallowed: the process sat out its drain delay with nobody able to stop it")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("the second signal took %v to end the process", elapsed)
	}
}
