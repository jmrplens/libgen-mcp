//go:build stdioe2e

// readiness_test.go is about what a client meets in the first second of a
// session, which is the window a stdio client has no way to see into.
//
// It spawns a process and writes to its pipe immediately; it cannot observe
// that the process is not reading yet. If the server spends that time building
// something before it reads, the client waits, gives up, and writes its
// handshake again — and the SDK correctly refuses the duplicate, which kills
// the connection. The retry meant to recover it is what breaks it.
//
// That is a reported failure in the sibling project, not a hypothesis: a server
// that took about 1.8 seconds to register its catalog met a client that gave up
// at 1.7. This server registers everything inside newRegisteredServer before
// Run is called, so it is correct by construction — and construction is exactly
// what a test outside the process can pin.

package stdioe2e

import (
	"testing"
	"time"
)

// clientGiveUp is how long a client waits for its handshake before deciding the
// server is not listening and sending it again.
//
// Measured rather than chosen: it is what GitHub Copilot CLI waited in the
// incident this bound comes from, against a server that answered at about 1.8
// seconds. Using the same number keeps the assertion tied to a client that
// really behaves this way instead of to a round figure.
const clientGiveUp = 1700 * time.Millisecond

// TestInitialize_IsAnsweredBeforeAClientWouldGiveUp is the latency assertion.
//
// A duration is a weak claim on its own — it passes on a fast machine for the
// wrong reason — so it is stated as a backstop against a specific regression
// rather than as a performance target: anything that moves work in front of the
// transport being read, a probe, a catalog fetch, a directory walk, puts this
// over the bound the way the incident did.
//
// The clock starts before the process is spawned, because the client's does:
// what the window covers is Go package initialization, config loading, the
// download-directory writability probe and registration, not just the reply.
func TestInitialize_IsAnsweredBeforeAClientWouldGiveUp(t *testing.T) {
	m := startMirror(t)
	env := baseEnv(t, m)

	start := time.Now()
	s := startSession(t, env)
	got := s.call(t, initializeRequest(1))
	elapsed := time.Since(start)

	if got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}
	t.Logf("spawn to handshake: %s", elapsed.Round(time.Millisecond))
	if elapsed > clientGiveUp {
		t.Errorf("the handshake took %s, longer than the %s a real client waits before retrying; a retried handshake is refused as a duplicate and kills the connection\nstderr: %s",
			elapsed.Round(time.Millisecond), clientGiveUp, s.stderrText())
	}
}

// TestCatalogIssuedDuringStartup_ComesBackWhole pins the construction the bound
// above rests on.
//
// Both requests are written into the pipe before the server has read either, so
// the answer cannot be "it was ready by the time we asked" — it was asked
// before anything could be ready. What comes back must be the finished catalog,
// not a partial one: every tool and prompt is registered inside
// newRegisteredServer, before Run connects the transport, so there is no window
// in which a list can be answered half-built.
//
// It holds today by construction, and asserting it is what pins the
// construction. The same fact is what the ListChanged=false capability rests
// on, so the two guard each other: a catalog that could change after
// registration would make that advertisement a lie.
func TestCatalogIssuedDuringStartup_ComesBackWhole(t *testing.T) {
	s := startSession(t, baseEnv(t, startMirror(t)))

	// Written back to back, before any answer is read.
	s.send(t, initializeRequest(1))
	s.send(t, request(2, "tools/list", ""))
	s.send(t, request(3, "prompts/list", ""))

	// Collected by id rather than read in order. The SDK answers requests
	// concurrently, so three written together can come back in any order, and a
	// reader that discarded everything until it saw the id it wanted would throw
	// away another subtest's answer and then wait for it forever.
	responses := collectResponses(t, s, 1, 2, 3)

	for _, tc := range []struct {
		name string
		id   int
		key  string
		want int
	}{
		{name: "tools", id: 2, key: "tools", want: registeredTools},
		{name: "prompts", id: 3, key: "prompts", want: registeredPrompts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := responses[tc.id]
			if got["error"] != nil {
				t.Fatalf("%s/list failed: %v", tc.name, got["error"])
			}
			result, ok := got["result"].(map[string]any)
			if !ok {
				t.Fatalf("%s/list returned no result: %v", tc.name, got)
			}
			listed, ok := result[tc.key].([]any)
			if !ok {
				t.Fatalf("%s/list returned no %s array: %v", tc.name, tc.key, result)
			}
			if len(listed) != tc.want {
				t.Errorf("%s/list issued during startup returned %d of %d; the catalog was answered half-built",
					tc.name, len(listed), tc.want)
			}
		})
	}
}

// collectResponses reads until every requested id has been answered, keyed by
// id. Anything that is not one of them — a notification, an answer to a request
// this test did not make — fails rather than being skipped: nothing here sends
// anything else, so an extra message is a finding of its own.
func collectResponses(t *testing.T, s *session, ids ...int) map[int]map[string]any {
	t.Helper()
	got := make(map[int]map[string]any, len(ids))
	for len(got) < len(ids) {
		msg := s.readMessage(t, 30*time.Second)
		matched := false
		for _, id := range ids {
			if sameID(msg["id"], id) {
				got[id] = msg
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("read a message for none of the requests that were sent: %v\nstderr: %s", msg, s.stderrText())
		}
	}
	return got
}
