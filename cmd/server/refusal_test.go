package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// probeRequest builds a POST carrying body, the way a client sends a JSON-RPC
// call the gates are about to refuse.
func probeRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body))
}

// TestRequestIDFromBodyRecoversAnIDWorthEchoing covers what correlation means:
// the value the client sent, echoed exactly as it arrived.
//
// A string and a number are both legal ids under 2026-07-28, and the raw bytes
// are returned rather than a re-encoding of them, because a client routes on the
// value it sent.
func TestRequestIDFromBodyRecoversAnIDWorthEchoing(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "a numeric id", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, want: "1"},
		{name: "a string id", body: `{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`, want: `"abc"`},
		{name: "a large numeric id", body: `{"jsonrpc":"2.0","id":9007199254740993,"method":"x"}`, want: "9007199254740993"},
		{name: "id before jsonrpc", body: `{"id":7,"jsonrpc":"2.0","method":"x"}`, want: "7"},
		{name: "whitespace around the body", body: "  \n" + `{"jsonrpc":"2.0","id":2,"method":"x"}` + "\n ", want: "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := requestIDFromBody(probeRequest(t, tc.body))
			if string(got) != tc.want {
				t.Errorf("requestIDFromBody = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRequestIDFromBodyReturnsNothingToEcho is the other half, and the one that
// decides whether the member is omitted or sent as null.
//
// Under 2026-07-28 a RequestId is a string or an integer, so null is not a legal
// value and the member is marked optional precisely so it can be left out. A
// client that recognizes this body by validating it against the published schema
// — rather than by reading error.code — fails on a null id, and the failure it
// produces is the transport downgrade the JSON-RPC shape exists to prevent.
func TestRequestIDFromBodyReturnsNothingToEcho(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// Decode stops at the end of the first JSON value, so this parses
		// happily. It is not a request, and an id lifted out of it is the id of
		// nothing.
		{name: "trailing content after the message", body: `{"jsonrpc":"2.0","id":1,"method":"x"} trailing`},
		{name: "two concatenated messages", body: `{"jsonrpc":"2.0","id":1,"method":"x"}{"jsonrpc":"2.0","id":2,"method":"y"}`},
		{name: "not JSON-RPC 2.0", body: `{"id":1,"method":"x"}`},
		{name: "the wrong JSON-RPC version", body: `{"jsonrpc":"1.0","id":1,"method":"x"}`},
		{name: "a null id", body: `{"jsonrpc":"2.0","id":null,"method":"x"}`},
		{name: "an object as the id", body: `{"jsonrpc":"2.0","id":{"a":1},"method":"x"}`},
		{name: "an array as the id", body: `{"jsonrpc":"2.0","id":[1],"method":"x"}`},
		{name: "a boolean as the id", body: `{"jsonrpc":"2.0","id":true,"method":"x"}`},
		{name: "a notification, which has no id", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{name: "not JSON at all", body: `not json`},
		{name: "an empty body", body: ``},
		{name: "a bare array", body: `[{"jsonrpc":"2.0","id":1,"method":"x"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestIDFromBody(probeRequest(t, tc.body)); got != nil {
				t.Errorf("requestIDFromBody = %q, want nothing to echo", got)
			}
		})
	}
}

// TestRequestIDFromBodyIsBounded is why the probe has a limit at all: it runs on
// requests nothing has vouched for, so an unbounded read lets any caller make
// the server buffer a body of any size.
//
// The case just under the bound is asserted beside it, so the limit cannot be
// "fixed" by refusing everything.
func TestRequestIDFromBodyIsBounded(t *testing.T) {
	idFor := func(padding int) string {
		return `{"jsonrpc":"2.0","id":1,"method":"x","params":{"pad":"` + strings.Repeat("a", padding) + `"}}`
	}

	fits := idFor(maxIDProbeBytes / 2)
	if got := requestIDFromBody(probeRequest(t, fits)); string(got) != "1" {
		t.Errorf("a %d-byte body inside the bound gave %q, want the id", len(fits), got)
	}

	tooBig := idFor(maxIDProbeBytes + 1)
	if got := requestIDFromBody(probeRequest(t, tooBig)); got != nil {
		t.Errorf("a %d-byte body was read past the %d-byte bound and gave %q", len(tooBig), maxIDProbeBytes, got)
	}
}

// TestRequestIDFromBodyIgnoresARequestWithNoBodyToRead covers the shapes where
// there is nothing to probe: a GET on the endpoint, and a nil request.
func TestRequestIDFromBodyIgnoresARequestWithNoBodyToRead(t *testing.T) {
	get := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	if got := requestIDFromBody(get); got != nil {
		t.Errorf("a GET produced an id: %q", got)
	}
	if got := requestIDFromBody(nil); got != nil {
		t.Errorf("a nil request produced an id: %q", got)
	}
}

// decodeHTTPRefusal reads a written refusal back as the client sees it. The
// stdio filter has its own decoder for the line-oriented shape it answers with.
func decodeHTTPRefusal(t *testing.T, rec *httptest.ResponseRecorder) jsonRPCError {
	t.Helper()
	var body jsonRPCError
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the refusal body is not JSON: %v (%q)", err, rec.Body.String())
	}
	return body
}

// TestRefusalWritesAJSONRPCErrorCarryingTheID is the writer's whole contract.
func TestRefusalWritesAJSONRPCErrorCarryingTheID(t *testing.T) {
	rec := httptest.NewRecorder()
	refusal{
		status:  http.StatusForbidden,
		code:    codeForbidden,
		message: "nope",
		header:  newHeader("Retry-After", "30"),
	}.write(rec, probeRequest(t, `{"jsonrpc":"2.0","id":"call-7","method":"tools/list"}`))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json: a body a client cannot recognize is the whole problem", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want the header the refusal carried", got)
	}

	body := decodeHTTPRefusal(t, rec)
	if body.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", body.JSONRPC)
	}
	if string(body.ID) != `"call-7"` {
		t.Errorf("id = %q, want the request's own id", body.ID)
	}
	if body.Error.Code != codeForbidden || body.Error.Message != "nope" {
		t.Errorf("error = %+v, want the code and message the refusal carried", body.Error)
	}
}

// TestRefusalOmitsTheIDRatherThanSendingNull pins the member's absence, which
// json.Unmarshal into a struct cannot tell apart from null.
func TestRefusalOmitsTheIDRatherThanSendingNull(t *testing.T) {
	rec := httptest.NewRecorder()
	refusal{status: http.StatusBadRequest, code: codeBadRequest, message: "nope"}.
		write(rec, probeRequest(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("the refusal body is not JSON: %v", err)
	}
	if _, present := raw["id"]; present {
		t.Errorf("the body carries an id member (%q); null is not a legal RequestId, so it must be absent", rec.Body.String())
	}
	if _, present := raw["error"]; !present {
		t.Errorf("the body carries no error member: %q", rec.Body.String())
	}
}

// TestRefusalOmitsDataWhenThereIsNone keeps the error object to what the
// specification names for each case: only the version refusal carries data.
func TestRefusalOmitsDataWhenThereIsNone(t *testing.T) {
	rec := httptest.NewRecorder()
	refusal{status: http.StatusForbidden, code: codeForbidden, message: "nope"}.
		write(rec, probeRequest(t, `{"jsonrpc":"2.0","id":1,"method":"x"}`))

	if strings.Contains(rec.Body.String(), `"data"`) {
		t.Errorf("the body carries an empty data member: %q", rec.Body.String())
	}
}

// TestNewHeaderCanonicalizesItsKeys is the bug the helper exists for: an
// http.Header built from a map literal skips canonicalization, so a key spelled
// the way its RFC spells it never answers Header.Get.
func TestNewHeaderCanonicalizesItsKeys(t *testing.T) {
	h := newHeader("WWW-Authenticate", "Bearer", "retry-after", "5")
	if got := h.Get("Www-Authenticate"); got != "Bearer" {
		t.Errorf("Get(%q) = %q, want the value that was Set", "Www-Authenticate", got)
	}
	if got := h.Get("Retry-After"); got != "5" {
		t.Errorf("Get(%q) = %q, want the value that was Set", "Retry-After", got)
	}
	if odd := newHeader("Lonely"); len(odd) != 0 {
		t.Errorf("an odd trailing name produced %v, want it ignored", odd)
	}
}
