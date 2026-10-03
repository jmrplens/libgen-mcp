//go:build httpe2e

package httpe2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shutdownTestDrain is longer than the five seconds --shutdown used to wait
// before killing, so a regression shows as a kill rather than as a slow test.
const shutdownTestDrain = 6 * time.Second

// privateBinary copies the built binary under a name no other process on the
// machine has.
//
// --shutdown ends every instance of its own binary name, so running the shared
// libgen-mcp build would end every other server of that name on the machine:
// another test's, or a developer's own. The name stays under the fifteen
// characters Linux keeps of a process name, so the lookup reads it whole.
func privateBinary(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(serverBinary(t))
	if err != nil {
		t.Fatalf("reading the built binary: %v", err)
	}
	dst := filepath.Join(t.TempDir(), fmt.Sprintf("lgsd%06d", os.Getpid()%1000000))
	if writeErr := os.WriteFile(dst, data, 0o700); writeErr != nil { //nolint:gosec // G306: an executable, so it needs its execute bit
		t.Fatalf("copying the built binary: %v", writeErr)
	}
	return dst
}

// startPrivateServer starts bin on a free loopback port with the extra flags
// and environment, waits for it to answer, and returns a channel that carries
// how the process ended.
func startPrivateServer(t *testing.T, bin string, flags, env []string) <-chan error {
	t.Helper()
	port := freePort(t)
	//nolint:gosec // G204: a copy of the binary this package built, on a port it reserved
	peer := exec.CommandContext(context.Background(), bin,
		append([]string{"--http", fmt.Sprintf("127.0.0.1:%d", port)}, flags...)...)
	peer.Env = append(append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir()), env...)
	if err := peer.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- peer.Wait() }()
	t.Cleanup(func() { _ = peer.Process.Kill() })
	waitHealthy(t, &server{baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), logs: func() string { return "" }})
	return stopped
}

// TestShutdown_WaitsOutTheDrainOfThePeerItEnds is the systemd case: a server
// with a drain delay is asked to go by --shutdown, and must be let go, not
// killed mid-drain. A kill is what a supervisor reads as a failure, and the
// restart that follows undoes the shutdown.
//
// The drain is given once on the command line and once through the
// environment, which is where a unit's Environment= puts it.
func TestShutdown_WaitsOutTheDrainOfThePeerItEnds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		env   []string
	}{
		{name: "a typed drain", flags: []string{"--drain-delay", shutdownTestDrain.String()}},
		{name: "a drain from the environment", env: []string{"LIBGEN_MCP_DRAIN_DELAY=" + shutdownTestDrain.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.env) > 0 {
				requireProcEnviron(t)
			}
			bin := privateBinary(t)
			stopped := startPrivateServer(t, bin, tc.flags, tc.env)

			start := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, bin, "--shutdown").CombinedOutput()
			if err != nil {
				t.Fatalf("--shutdown: %v\n%s", err, out)
			}

			select {
			case waitErr := <-stopped:
				if waitErr != nil {
					t.Fatalf("the server ended with %v, want a clean exit after its drain. --shutdown said:\n%s", waitErr, out)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("the server outlived --shutdown by 30s. --shutdown said:\n%s", out)
			}
			if strings.Contains(string(out), "force-killed") || !strings.Contains(string(out), "waiting up to") {
				t.Errorf("--shutdown did not wait for the drain:\n%s", out)
			}
			if elapsed := time.Since(start); elapsed < shutdownTestDrain {
				t.Errorf("the server exited %s after --shutdown, before its %s drain", elapsed, shutdownTestDrain)
			}
		})
	}
}
