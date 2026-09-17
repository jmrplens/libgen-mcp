//go:build httpe2e

package httpe2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runHealthcheckBinary runs the built binary's --healthcheck and returns its
// exit code and whatever it said about the decision.
func runHealthcheckBinary(t *testing.T, args ...string) (int, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	//nolint:gosec // the binary this package built, with arguments this file wrote
	cmd := exec.CommandContext(ctx, serverBinary(t), append([]string{"--healthcheck"}, args...)...)
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()

	code := 0
	var exitErr *exec.ExitError
	if err != nil {
		if !asExitError(err, &exitErr) {
			t.Fatalf("running --healthcheck: %v\n%s", err, out)
		}
		code = exitErr.ExitCode()
	}
	return code, string(out)
}

// asExitError is errors.As, spelled out so the import list of this file stays
// about the process it runs.
func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError) //nolint:errorlint // CombinedOutput returns it unwrapped
	if ok {
		*target = e
	}
	return ok
}

// TestHealthcheck_FindsARunningInstanceOnEveryListenerShape is the whole reason
// the check lives in the binary.
//
// A curl line in the image gets four of these wrong — another port, a socket, a
// mount under a prefix, TLS this process terminates — and reports unhealthy
// while the server is serving perfectly, which makes an orchestrator restart a
// container whose restart changes nothing. Each case starts a real instance and
// runs the real flag against it.
func TestHealthcheck_FindsARunningInstanceOnEveryListenerShape(t *testing.T) {
	t.Run("a plain port", func(t *testing.T) {
		startServer(t, nil)
		if code, out := runHealthcheckBinary(t); code != 0 {
			t.Errorf("exit = %d, want 0:\n%s", code, out)
		}
	})

	t.Run("a mount under a prefix", func(t *testing.T) {
		startServer(t, nil, "--http-path", "/libgen")
		code, out := runHealthcheckBinary(t)
		if code != 0 {
			t.Errorf("exit = %d, want 0:\n%s", code, out)
		}
		if !strings.Contains(out, "/libgen/health") {
			t.Errorf("the probe did not derive the mounted path:\n%s", out)
		}
	})

	t.Run("a unix socket", func(t *testing.T) {
		startUnixServer(t, nil)
		if code, out := runHealthcheckBinary(t); code != 0 {
			t.Errorf("exit = %d, want 0:\n%s", code, out)
		}
	})

	t.Run("TLS this process terminates", func(t *testing.T) {
		startTLSServer(t, nil)
		code, out := runHealthcheckBinary(t)
		if code != 0 {
			t.Errorf("exit = %d, want 0:\n%s", code, out)
		}
		if !strings.Contains(out, "https://") {
			t.Errorf("the probe did not derive an https target:\n%s", out)
		}
	})
}

// TestHealthcheck_ReportsUnhealthyWithNothingRunning is the other verdict, and
// the one an orchestrator acts on.
func TestHealthcheck_ReportsUnhealthyWithNothingRunning(t *testing.T) {
	code, out := runHealthcheckBinary(t, "http://127.0.0.1:1/health")
	if code != 1 {
		t.Errorf("exit = %d, want 1 against a port nothing is listening on:\n%s", code, out)
	}
}

// TestHealthcheck_AGivenTargetIsProbedDirectly covers the path a probe run from
// outside the container takes, where there is no process list to read.
func TestHealthcheck_AGivenTargetIsProbedDirectly(t *testing.T) {
	s := startServer(t, nil)

	code, out := runHealthcheckBinary(t, s.baseURL+"/health")
	if code != 0 {
		t.Errorf("exit = %d, want 0:\n%s", code, out)
	}

	usageCode, usageOut := runHealthcheckBinary(t, "not a target")
	if usageCode != 2 {
		t.Errorf("exit = %d, want 2 for a target that does not parse:\n%s", usageCode, usageOut)
	}
}

// TestHealthcheck_ADrainingInstanceIsNotHealthy is what makes the drain mean
// something to an orchestrator as well as to a balancer.
//
// The two verdicts have to agree: an instance that has announced it is going
// must not keep passing its container health check, or the orchestrator goes on
// treating it as a member of the set while the balancer has already stopped
// sending it work.
func TestHealthcheck_ADrainingInstanceIsNotHealthy(t *testing.T) {
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	//nolint:gosec // the binary this package built, on a port it reserved
	cmd := exec.CommandContext(context.Background(), serverBinary(t),
		"--http", fmt.Sprintf("127.0.0.1:%d", port), "--drain-delay", "3s")
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	waitHealthy(t, &server{baseURL: base, logs: func() string { return "" }})

	// Reached while serving, so the target is known good before the flip.
	if code, out := runHealthcheckBinary(t, base+"/health"); code != 0 {
		t.Fatalf("exit = %d before any shutdown, want 0:\n%s", code, out)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling: %v", err)
	}

	// Polled rather than probed once. The signal has to reach the process and
	// the handler has to run before the flag flips, and on a loaded machine that
	// window is wider than the round trip — a single probe here reads the last
	// 200 and calls it a regression. The drain delay is what bounds the wait:
	// after it the listener is gone and the probe fails for a different reason,
	// which this loop would also catch.
	var code int
	var out string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if code, out = runHealthcheckBinary(t, base+"/health"); code == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != 1 {
		t.Errorf("exit = %d while draining, want 1:\n%s", code, out)
	}

	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not exit within 30s of SIGTERM")
	}
}
