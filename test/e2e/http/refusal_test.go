//go:build httpe2e

package httpe2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// assertJSONRPCRefusal checks that a refusal on the MCP endpoint is shaped the
// way a Streamable HTTP client can read, and carries the id of the call it
// refuses.
//
// The id is the half that is easy to get wrong and invisible without this: a
// refusal that omits it, or sends null, cannot be delivered to the call that
// caused it by a client that routes on id — and null is not a legal RequestId
// under 2026-07-28, so a client validating against the published schema rejects
// the body and falls back to the transport downgrade this shape exists to
// prevent. toolsListBody carries id 1.
func assertJSONRPCRefusal(t *testing.T, what string, reply response, wantCode int) {
	t.Helper()

	if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s: Content-Type = %q, want application/json", what, ct)
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(reply.body), &envelope); err != nil {
		t.Fatalf("%s: the refusal is not JSON-RPC: %v\nbody: %s", what, err, truncate(reply.body))
	}
	if envelope.JSONRPC != "2.0" {
		t.Errorf("%s: jsonrpc = %q, want 2.0", what, envelope.JSONRPC)
	}
	if string(envelope.ID) != "1" {
		t.Errorf("%s: id = %q, want the request's own id", what, envelope.ID)
	}
	if envelope.Error == nil {
		t.Fatalf("%s: no error object: %s", what, truncate(reply.body))
	}
	if envelope.Error.Code != wantCode {
		t.Errorf("%s: code = %d, want %d", what, envelope.Error.Code, wantCode)
	}
	if envelope.Error.Message == "" {
		t.Errorf("%s: the error carries no message", what)
	}
}

// TestRefusal_EveryGateOnTheEndpointAnswersInOneShape is the point of the
// change: a client meets one body whichever layer refused it.
//
// Both of these used to be plain text — one from the standard library's
// cross-origin protection, one from this server's own Host guard — and plain
// text on this route is what the transport specification reads as an
// initialization-era server. -40300 mirrors HTTP 403; it is written out rather
// than imported, because what a non-Go client matches on is the number on the
// wire.
func TestRefusal_EveryGateOnTheEndpointAnswersInOneShape(t *testing.T) {
	t.Run("an untrusted browser origin", func(t *testing.T) {
		s := startServer(t, nil)

		reply := s.do(t, browserPOST(trustedOrigin))
		if reply.status != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", reply.status, http.StatusForbidden)
		}
		assertJSONRPCRefusal(t, "a cross-origin POST with no allowlist", reply, -40300)
	})

	t.Run("a Host this deployment does not serve", func(t *testing.T) {
		s := startServer(t, nil)

		reply := s.do(t, hostPOST(publicHost))
		if reply.status != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", reply.status, http.StatusForbidden)
		}
		assertJSONRPCRefusal(t, "a forwarded public Host with neither flag", reply, -40300)
	})
}

// TestRefusal_TheCardKeepsItsOwnAnswers is the boundary.
//
// Only the MCP endpoint speaks JSON-RPC, so only its refusals are shaped like
// one. A path this server does not serve answers the 404 that names the
// endpoint — it is talking to a scanner, not to a client with a call in flight —
// and the server card is a public document with no session behind it.
func TestRefusal_TheCardKeepsItsOwnAnswers(t *testing.T) {
	s := startServer(t, nil)

	assertNotFound(t, s, request{method: http.MethodGet, path: "/nope"}, "/")

	card := s.do(t, request{method: http.MethodGet, path: "/server-card"})
	if card.status != http.StatusOK {
		t.Errorf("GET /server-card = %d, want %d: the card is unaffected by the endpoint's gates", card.status, http.StatusOK)
	}
}
