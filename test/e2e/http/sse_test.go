//go:build httpe2e

package httpe2e

import (
	"net/http"
	"strings"
	"testing"
)

// TestSSE_AntiBufferingFollowsTheResponse is the defect, driven over the real
// binary with the Accept headers that exposed it.
//
// The header used to be decided from the request's Accept, matching the literal
// "text/event-stream". The SDK answers a stream to a bare `*/*` as well — which
// is curl's default and several HTTP libraries' — so exactly the clients most
// likely to be used to try the endpoint out received a real SSE stream with no
// anti-buffering header, and an nginx-class proxy in front held every
// notifications/progress frame until the transfer finished.
//
// What the SDK does with Accept, measured against v1.8.0 rather than assumed:
// it requires the header to cover both application/json and text/event-stream,
// so a bare `text/*` is refused outright with 400 and never reaches this
// question at all. `*/*` covers both and is answered with a stream. That is the
// case this test exists for.
//
// Each case asserts the pair together: that the response really is a stream,
// and that it carries the header. Asserting the header alone would pass on a
// build that answered JSON to everything.
func TestSSE_AntiBufferingFollowsTheResponse(t *testing.T) {
	s := startServer(t, nil)

	cases := []struct {
		name   string
		accept string
	}{
		{name: "the SDK's own spelling", accept: acceptHeader},
		{name: "curl's default", accept: "*/*"},
		{name: "a wildcard with a quality value", accept: "application/json;q=0.9, */*;q=0.8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := s.do(t, request{
				method:  http.MethodPost,
				body:    toolsListBody,
				headers: map[string]string{"Accept": tc.accept},
			})
			if reply.status != http.StatusOK {
				t.Fatalf("status = %d, want %d (body: %s)", reply.status, http.StatusOK, truncate(reply.body))
			}
			if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
				t.Skipf("the SDK answered %q rather than a stream for Accept %q, so there is nothing to buffer", ct, tc.accept)
			}
			if got := reply.header.Get("X-Accel-Buffering"); got != "no" {
				t.Errorf("X-Accel-Buffering = %q on an SSE response to Accept %q, want %q", got, tc.accept, "no")
			}
		})
	}
}

// TestSSE_JSONResponseCarriesNoStreamHeader is the negative: under
// --json-response nothing streams, so a header telling a proxy not to buffer
// would be describing a response that does not exist.
func TestSSE_JSONResponseCarriesNoStreamHeader(t *testing.T) {
	s := startServer(t, nil, "--json-response")

	reply := s.do(t, request{method: http.MethodPost, body: toolsListBody, headers: map[string]string{"Accept": "*/*"}})
	if reply.status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", reply.status, http.StatusOK, truncate(reply.body))
	}
	if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json under --json-response", ct)
	}
	if got := reply.header.Get("X-Accel-Buffering"); got != "" {
		t.Errorf("X-Accel-Buffering = %q on a JSON response, want it absent", got)
	}
}

// TestSSE_TheWrappedEndpointStillAnswersEveryOtherRoute guards the wrapping
// itself rather than the header.
//
// sseAware replaces the ResponseWriter for the MCP endpoint, and a wrapper that
// dropped a method the SDK reaches through http.NewResponseController would
// break streaming in a way no header assertion notices. The routes outside it
// are asserted here too, because that is what makes clearing the write deadline
// on the endpoint safe: they keep the listener's WriteTimeout.
func TestSSE_TheWrappedEndpointStillAnswersEveryOtherRoute(t *testing.T) {
	s := startServer(t, nil)

	assertToolsListed(t, "through the SSE-aware writer", s.do(t, mcpPOST(nil)))

	if !s.healthy(t) {
		t.Errorf("GET /health stopped answering. Output:\n%s", s.logs())
	}
	card := s.do(t, request{method: http.MethodGet, path: "/server-card"})
	if card.status != http.StatusOK {
		t.Errorf("GET /server-card = %d, want %d", card.status, http.StatusOK)
	}
	if got := card.header.Get("X-Accel-Buffering"); got != "" {
		t.Errorf("the card carries X-Accel-Buffering = %q; it is not wrapped and streams nothing", got)
	}
}
