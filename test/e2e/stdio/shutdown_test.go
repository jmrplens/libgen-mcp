//go:build stdioe2e

// shutdown_test.go covers what happens when the client goes away, in the two
// ways it can: the pipe closing, and a supervisor asking the process to stop.

package stdioe2e

import (
	"testing"
	"time"
)

// shutdownGrace is how long a shutdown may take before it counts as ignored.
// Generous on purpose — what is under test is that the process leaves at all,
// not how quickly.
const shutdownGrace = 20 * time.Second

// TestShutdown_ExitStatusSaysItWasClean pins that an ordinary stop is reported
// as one.
//
// The stdio binding prescribes two shutdowns — the client closing stdin, and a
// signal — and they are the two a supervisor sees most. Announcing either as a
// failure is a report nobody can act on: systemd's Restart=on-failure restarts
// on it, a CI wrapper fails the job on it, and the npm launcher mirrors its
// child's status outward to whatever spawned it.
//
// Both cases run after a real session rather than against a freshly spawned
// process, because a server that has never served has nothing to shut down and
// would exit cleanly whatever the shutdown path did.
func TestShutdown_ExitStatusSaysItWasClean(t *testing.T) {
	tests := []struct {
		name string
		stop func(t *testing.T, s *session) (int, bool)
	}{
		{
			// The primary and portable signal in the binding: a client that
			// simply exits closes its end of the pipe and nothing else.
			name: "stdin closed",
			stop: func(t *testing.T, s *session) (int, bool) {
				t.Helper()
				return s.closeStdinAndWait(t, shutdownGrace)
			},
		},
		{
			// What a supervisor sends. The server installs a handler for it
			// through signal.NotifyContext, so this is the path that has to
			// unwind the transport rather than let the default disposition kill
			// the process.
			name: terminationSignalName,
			stop: func(t *testing.T, s *session) (int, bool) {
				t.Helper()
				return s.terminate(t, shutdownGrace)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := startSession(t, baseEnv(t, startMirror(t)))
			if got := s.call(t, initializeRequest(1)); got["error"] != nil {
				t.Fatalf("initialize failed: %v", got["error"])
			}
			if got := s.call(t, request(2, "tools/list", "")); got["error"] != nil {
				t.Fatalf("tools/list failed: %v", got["error"])
			}

			code, exited := tt.stop(t, s)
			if !exited {
				t.Fatalf("the server was still running %s after %s; it had to be killed\nstderr: %s",
					shutdownGrace, tt.name, s.stderrText())
			}
			if code != 0 {
				t.Errorf("exit status %d after %s, want 0: an ordinary shutdown reported as a failure restarts services and fails jobs\nstderr: %s",
					code, tt.name, s.stderrText())
			}
		})
	}
}
