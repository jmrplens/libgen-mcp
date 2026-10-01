//go:build httpe2e

package httpe2e

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// heldDescriptorLimit is the descriptor limit these cases start the binary
// under. The ceilings follow from it: (256 - 256/8 - 64) / 10 = 16 held calls,
// ten descriptors being the widest fan-out one call has, and half that, 8,
// stateful sessions. Small enough to fill from one test, large enough that the
// idle process, the refused connections and /health fit in the spare eighth.
const (
	heldDescriptorLimit = 256
	heldCeiling         = 16
	sessionCeiling      = 8
)

// heldSearchBody is a search that reaches the catalog and nothing beyond it,
// so it waits on the stand-in mirror and on the outbound bucket and on nothing
// a test does not control.
const heldSearchBody = `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"search",` +
	`"arguments":{"query":"held","extra_sources":"never"}}}`

// busyText is what every refusal of a process ceiling says.
const busyText = "This server is busy. Retry later."

// startServerUnderDescriptorLimit starts the binary under a lowered descriptor
// limit, which a shell sets for that process alone before it execs.
func startServerUnderDescriptorLimit(t *testing.T, env map[string]string, flags ...string) *server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the descriptor limit is a unix property")
	}
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	wrapper := []string{"/bin/sh", "-c", fmt.Sprintf(`ulimit -n %d && exec "$0" "$@"`, heldDescriptorLimit)}
	return launchServerVia(t, wrapper, "http://"+addr, nil, env, append([]string{"--http", addr}, flags...))
}

// hangingMirror is a stand-in catalog that answers nothing until the test ends,
// so every search that reaches it, and every one queued behind the outbound
// bucket, stays held.
func hangingMirror(t *testing.T) map[string]string {
	t.Helper()
	release := make(chan struct{})
	m := startMirror(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	// Registered after startMirror, so it runs before that fixture's Close,
	// which would otherwise wait out a handler blocked on purpose.
	t.Cleanup(func() { close(release) })
	env := mirrorEnv(m)
	env["LIBGEN_MCP_TIMEOUT"] = "120s"
	return env
}

// TestHeld_TheProcessRefusesPastItsCeilingAndKeepsAnswering offers the binary
// more held searches than its descriptor limit can carry, from one caller.
//
// Before the ceiling every one was held, each with a connection open, until
// the process could accept none at all and /health went unanswered with the
// rest. Now the ceiling holds exactly its figure, refuses the rest at once,
// closes their connections, and /health answers throughout.
func TestHeld_TheProcessRefusesPastItsCeilingAndKeepsAnswering(t *testing.T) {
	s := startServerUnderDescriptorLimit(t, hangingMirror(t))

	if !strings.Contains(s.logs(), fmt.Sprintf(`"held_calls_per_process":%d`, heldCeiling)) {
		t.Fatalf("the startup line does not announce %d held calls. Output:\n%s", heldCeiling, s.logs())
	}

	const offered = heldCeiling + 40
	replies := make(chan response, offered)
	// sequential: every call is offered at once and held open together
	for range offered {
		go func() {
			reply, err := s.try(t, request{body: heldSearchBody})
			if err != nil {
				return
			}
			replies <- reply
		}()
	}

	refused := 0
	deadline := time.After(60 * time.Second)
	for refused < offered-heldCeiling {
		select {
		case r := <-replies:
			if !strings.Contains(r.body, busyText) {
				t.Fatalf("a call past the ceiling was answered with something other than the refusal: %d %s", r.status, truncate(r.body))
			}
			if !r.closed {
				t.Errorf("a refused call's connection was kept open: %v", r.header)
			}
			refused++
		case <-deadline:
			t.Fatalf("only %d of %d calls past the ceiling were refused. Output:\n%s", refused, offered-heldCeiling, s.logs())
		}
	}

	if !s.healthy(t) {
		t.Error("/health did not answer with every held slot taken")
	}

	modern := s.do(t, request{body: heldSearchBody, headers: map[string]string{
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "search",
	}})
	if modern.status != http.StatusServiceUnavailable || modern.header.Get("Retry-After") != "30" ||
		!strings.Contains(modern.body, busyText) || !modern.closed {
		t.Errorf("a 2026-07-28 call at a full ceiling = %d, Retry-After %q, %s; want 503, 30 and the refusal",
			modern.status, modern.header.Get("Retry-After"), truncate(modern.body))
	}

	// Nothing more comes back: the held calls are still held, not refused late.
	select {
	case r := <-replies:
		t.Errorf("a held call came back while the mirror still hangs: %d %s", r.status, truncate(r.body))
	case <-time.After(time.Second):
	}
	if !strings.Contains(s.logs(), "too many calls held across the process") {
		t.Errorf("the refusals were not logged for the operator. Output:\n%s", s.logs())
	}
}

// TestHeld_StatefulSessionsStopAtTheirCeiling opens sessions on a stateful
// listener until the ceiling refuses one, and checks what stays served.
func TestHeld_StatefulSessionsStopAtTheirCeiling(t *testing.T) {
	s := startServerUnderDescriptorLimit(t, nil, "--stateless=false")

	if !strings.Contains(s.logs(), fmt.Sprintf(`"stateful_sessions_per_process":%d`, sessionCeiling)) {
		t.Fatalf("the startup line does not announce %d sessions. Output:\n%s", sessionCeiling, s.logs())
	}

	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"` + protocolVersion + `","capabilities":{},"clientInfo":{"name":"httpe2e","version":"0"}}}`
	sessions := make([]string, 0, sessionCeiling)
	// sequential: every session stays open while the next one is opened
	for range sessionCeiling {
		r := s.do(t, request{body: initialize})
		id := r.header.Get("Mcp-Session-Id")
		if r.status != http.StatusOK || id == "" {
			t.Fatalf("session %d of %d was not opened: %d %s", len(sessions)+1, sessionCeiling, r.status, truncate(r.body))
		}
		sessions = append(sessions, id)
	}

	past := s.do(t, request{body: initialize})
	if past.status != http.StatusServiceUnavailable || past.header.Get("Mcp-Session-Id") != "" {
		t.Fatalf("the session past the ceiling = %d with session %q, want 503 and none",
			past.status, past.header.Get("Mcp-Session-Id"))
	}
	if !s.healthy(t) {
		t.Error("/health did not answer with every session slot taken")
	}

	ping := s.do(t, request{body: `{"jsonrpc":"2.0","id":2,"method":"ping"}`, headers: map[string]string{
		"Mcp-Session-Id": sessions[0],
	}})
	if ping.status != http.StatusOK {
		t.Errorf("a request on an open session was not served at the ceiling: %d %s", ping.status, truncate(ping.body))
	}

	s.do(t, request{method: http.MethodDelete, headers: map[string]string{"Mcp-Session-Id": sessions[0]}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := s.do(t, request{body: initialize})
		if r.status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a deleted session's slot never came back: %d %s", r.status, truncate(r.body))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
