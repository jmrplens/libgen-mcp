//go:build httpe2e

package httpe2e

import (
	"net/http"
	"strings"
	"testing"
)

// TestLimits_MaxRequestBodyIsEnforced sets the cap small enough to observe and
// then exceeds it.
//
// A limit that does not limit is worse than no limit: it is a number in the
// documentation that an operator relies on. The flag is checked from both
// sides, because a cap that rejects everything would pass a one-sided test.
func TestLimits_MaxRequestBodyIsEnforced(t *testing.T) {
	s := startServer(t, nil, "--max-request-body-bytes=2048")

	if got := s.do(t, mcpPOST(nil)).status; got != http.StatusOK {
		t.Errorf("a small body = %d, want %d: the cap must not reject ordinary calls", got, http.StatusOK)
	}

	// Valid JSON, over the cap: the rejection has to be about the size, not
	// about the parse.
	padding := strings.Repeat("x", 4096)
	oversized := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` + padding + `"}}`
	reply := s.do(t, request{body: oversized})
	if reply.status != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized body = %d, want %d", reply.status, http.StatusRequestEntityTooLarge)
	}
	if !s.healthy(t) {
		t.Error("the server stopped serving after refusing an oversized body")
	}
}

// TestLimits_DefaultBodyCapIsNotUnlimited pins the shipped default, which is
// the configuration nobody sets and everybody runs.
//
// The flag documents 0 as "the SDK default (4 MiB)" rather than "no limit", and
// the difference matters: an unlimited default would let one request take the
// process down. 8 MiB clears 4 MiB with room for the framing.
func TestLimits_DefaultBodyCapIsNotUnlimited(t *testing.T) {
	s := startServer(t, nil)

	padding := strings.Repeat("x", 8*1024*1024)
	huge := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` + padding + `"}}`
	reply := s.do(t, request{body: huge})
	if reply.status != http.StatusRequestEntityTooLarge {
		t.Errorf("an 8 MiB body with no flag set = %d, want %d: the default must cap something", reply.status, http.StatusRequestEntityTooLarge)
	}
	if !s.healthy(t) {
		t.Error("the server stopped serving after refusing an 8 MiB body")
	}
}

// TestLimits_NegativeBodyCapFailsStartup pins the loud failure: a cap the
// operator wrote and the server silently ignored would be the same defect as a
// limit that does not limit.
func TestLimits_NegativeBodyCapFailsStartup(t *testing.T) {
	out, err := runServerExpectingExit(t, "--http", "127.0.0.1:"+itoa(freePort(t)), "--max-request-body-bytes=-1")
	if err == nil {
		t.Fatalf("the server started with a negative cap; it must refuse. Output:\n%s", out)
	}
	if !strings.Contains(out, "max-request-body-bytes") {
		t.Errorf("output does not name the offending flag:\n%s", out)
	}
}

// TestLimits_StatelessDefaultIssuesNoSession covers the transport mode the
// 2026-07-28 protocol requires, and the SEP-2567 behaviors that come with it.
func TestLimits_StatelessDefaultIssuesNoSession(t *testing.T) {
	s := startServer(t, nil)

	reply := s.do(t, mcpPOST(nil))
	if reply.status != http.StatusOK {
		t.Fatalf("tools/list without initialize = %d, want %d", reply.status, http.StatusOK)
	}
	if id := reply.header.Get("Mcp-Session-Id"); id != "" {
		t.Errorf("Mcp-Session-Id = %q, want none in stateless mode", id)
	}
	// GET and DELETE are what a session-based client uses to open a stream and
	// end a session; neither exists here.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		got := s.do(t, request{method: method, path: "/"})
		if got.status != http.StatusMethodNotAllowed {
			t.Errorf("%s / = %d, want %d in stateless mode", method, got.status, http.StatusMethodNotAllowed)
		}
		if method == http.MethodGet && !strings.Contains(got.header.Get("Allow"), http.MethodPost) {
			t.Errorf("405 Allow = %q, want it to name POST", got.header.Get("Allow"))
		}
	}
}

// TestLimits_StatefulModeIssuesASession is the other side of the same flag: a
// mode that cannot be told apart from the default is a flag that does nothing.
func TestLimits_StatefulModeIssuesASession(t *testing.T) {
	s := startServer(t, nil, "--stateless=false")

	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"httpe2e","version":"0"}}}`
	reply := s.do(t, request{body: init})
	if reply.status != http.StatusOK {
		t.Fatalf("initialize = %d, want %d", reply.status, http.StatusOK)
	}
	session := reply.header.Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("Mcp-Session-Id is absent with --stateless=false; the flag changed nothing")
	}

	// The session is honored, not merely issued.
	follow := s.do(t, request{body: toolsListBody, headers: map[string]string{"Mcp-Session-Id": session}})
	if follow.status != http.StatusOK {
		t.Errorf("tools/list with the issued session = %d, want %d", follow.status, http.StatusOK)
	}
}

// TestLimits_JSONResponseChangesTheFraming pins the third flag: the body comes
// back as JSON rather than an SSE frame, which is what a client that cannot
// read event streams depends on.
func TestLimits_JSONResponseChangesTheFraming(t *testing.T) {
	sse := startServer(t, nil)
	reply := sse.do(t, mcpPOST(nil))
	if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("default Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(reply.body, "event:") {
		t.Errorf("default body is not SSE-framed: %q", truncate(reply.body))
	}

	jsonSrv := startServer(t, nil, "--json-response")
	jsonReply := jsonSrv.do(t, mcpPOST(nil))
	if ct := jsonReply.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("--json-response Content-Type = %q, want application/json", ct)
	}
	if strings.Contains(jsonReply.body, "event:") {
		t.Errorf("--json-response body is still SSE-framed: %q", truncate(jsonReply.body))
	}
	if !strings.HasPrefix(strings.TrimSpace(jsonReply.body), "{") {
		t.Errorf("--json-response body does not start as a JSON object: %q", truncate(jsonReply.body))
	}
}

// truncate keeps a failure message readable when the body is a whole catalog.
func truncate(s string) string {
	if len(s) <= 160 {
		return s
	}
	return s[:160] + "…"
}
