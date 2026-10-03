package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmrplens/libgen-mcp/v2/internal/transport"
)

// initializeBody opens a stateful session on a legacy revision.
const initializeBody = `{"jsonrpc":"2.0","id":3,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0.0.0"}}}`

// statefulOptions is the legacy transport that keeps sessions, closing none
// that go idle, so a session's slot is held until the test ends it.
func statefulOptions() transport.Options {
	opts := transport.DefaultOptions()
	opts.Stateless = false
	return opts
}

// onSession returns the headers of a request on an open session.
func onSession(id string) map[string]string {
	return map[string]string{"Mcp-Session-Id": id, protocolVersionHeader: "2025-06-18"}
}

// deleteSession ends a session the way a client does.
func deleteSession(t *testing.T, base, id string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/", nil)
	if err != nil {
		t.Fatalf("build DELETE: %v", err)
	}
	req.Header.Set("Mcp-Session-Id", id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// TestStatefulSessionsFor_IsHalfTheHeldCeiling pins the share, and its floor.
func TestStatefulSessionsFor_IsHalfTheHeldCeiling(t *testing.T) {
	for _, tc := range []struct {
		name string
		held int64
		want int64
	}{
		{name: "a hard limit of 1024", held: 75, want: 37},
		{name: "an odd ceiling rounds down", held: 5, want: 2},
		{name: "one held call still keeps one session", held: 1, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := statefulSessionsFor(tc.held); got != tc.want {
				t.Errorf("statefulSessionsFor(%d) = %d, want %d", tc.held, got, tc.want)
			}
		})
	}
}

// TestStatefulSessions_AreBoundedAcrossTheProcess is the other unbounded
// thing: with --stateless=false every initialize kept a session, and each
// session could hold its stream open, until the streams held every descriptor.
func TestStatefulSessions_AreBoundedAcrossTheProcess(t *testing.T) {
	ceilings := testCeilings(4, 1)
	base := serveWithCeilings(t, newSearchToolServer(), statefulOptions(), ceilings)

	first := initStatefulSession(t, base)
	if ceilings.sessions.open.Load() != 1 || ceilings.held.open.Load() != 1 {
		t.Fatalf("one session holds %d session and %d held slots, want 1 and 1",
			ceilings.sessions.open.Load(), ceilings.held.open.Load())
	}

	t.Run("the next initialize is refused in the gate", func(t *testing.T) {
		r, err := sendMCP(shortContext(t), base, initializeBody, nil)
		if err != nil {
			t.Fatalf("second initialize: %v", err)
		}
		assertBusyRefusal(t, r, `3`)
		if id := r.header.Get("Mcp-Session-Id"); id != "" {
			t.Errorf("the refused initialize was handed session %q", id)
		}
	})

	t.Run("the open session's stream takes no slot of its own", func(t *testing.T) {
		stream := openSessionStream(t, base, first)
		defer func() { _ = stream.Close() }()
		if got := ceilings.held.open.Load(); got != 1 {
			t.Errorf("held slots with the stream open = %d, want the 1 reserved when the session opened", got)
		}
	})

	t.Run("a request on the open session is still served", func(t *testing.T) {
		r, err := sendMCP(shortContext(t), base, `{"jsonrpc":"2.0","id":4,"method":"ping"}`, onSession(first))
		if err != nil || r.status != http.StatusOK {
			t.Fatalf("ping on the open session: %v %d %s", err, r.status, r.body)
		}
	})

	deleteSession(t, base, first)
	waitUntil(t, func() bool { return ceilings.sessions.open.Load() == 0 && ceilings.held.open.Load() == 0 })
	initStatefulSession(t, base)
}

// TestStatefulSessions_ASessionIsRefusedWhenItsStreamHasNoSlot pins the
// reservation: a session slot is free, but the held slot its stream would take
// is not, so the session is refused rather than opened without one.
func TestStatefulSessions_ASessionIsRefusedWhenItsStreamHasNoSlot(t *testing.T) {
	ceilings := testCeilings(1, 2)
	base := serveWithCeilings(t, newSearchToolServer(), statefulOptions(), ceilings)

	initStatefulSession(t, base)
	r, err := sendMCP(shortContext(t), base, initializeBody, nil)
	if err != nil {
		t.Fatalf("second initialize: %v", err)
	}
	assertBusyRefusal(t, r, `3`)
	if got := ceilings.sessions.open.Load(); got != 1 {
		t.Errorf("session slots = %d after the refusal, want the 1 the open session holds", got)
	}
}

// TestStatefulSessions_APOSTThatKeepsNoSessionGivesItsSlotBack covers the POST
// that carries no session id and is not an initialize: the SDK keeps no
// session for it, and the slot the gate took comes back as the POST ends.
func TestStatefulSessions_APOSTThatKeepsNoSessionGivesItsSlotBack(t *testing.T) {
	ceilings := testCeilings(2, 1)
	base := serveWithCeilings(t, newSearchToolServer(), statefulOptions(), ceilings)

	if _, err := sendMCP(shortContext(t), base, listToolsRequest, legacyHeaders()); err != nil {
		t.Fatalf("tools/list with no session: %v", err)
	}
	waitUntil(t, func() bool { return ceilings.sessions.open.Load() == 0 && ceilings.held.open.Load() == 0 })
	initStatefulSession(t, base)
}

// TestProcessGate_CountsNothingOnTheStatelessTransport keeps the session
// ceiling where sessions exist: a stateless POST with no session id opens none
// that outlives it.
func TestProcessGate_CountsNothingOnTheStatelessTransport(t *testing.T) {
	ceilings := testCeilings(1, 0)
	var reached bool
	gate := processGate(ceilings, true, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(listToolsRequest))
	gate.ServeHTTP(httptest.NewRecorder(), req)
	if !reached {
		t.Error("a stateless POST was refused by a session ceiling of zero")
	}
}
