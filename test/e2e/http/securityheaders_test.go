//go:build httpe2e

package httpe2e

import (
	"net/http"
	"strings"
	"testing"
)

// constantSecurityHeaders is what the outermost middleware states about this
// server on every response, whatever produced it.
//
// Cache-Control is deliberately not here. It is the one header of the five an
// inner layer legitimately replaces — the SDK stamps its own on a streamed MCP
// response, the card overrides it with a lifetime — so each case says what it
// expects instead of sharing one answer that would be wrong somewhere.
var constantSecurityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Referrer-Policy":         "no-referrer",
	"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
}

// assertConstantSecurityHeaders checks the four headers that are the same on
// every response, that each appears exactly once, and that HSTS is absent.
//
// The count is not pedantry. The middleware Sets rather than Adds precisely so
// that a route writing its own copy — serverCardGET repeats nosniff — replaces
// it instead of appending a second value, and a duplicated directive is the kind
// of thing a scanner flags and a browser reads unpredictably. Nothing but the
// count can tell the two apart, because Header.Get returns the first either way.
func assertConstantSecurityHeaders(t *testing.T, what string, h http.Header) {
	t.Helper()

	for name, want := range constantSecurityHeaders {
		got := h.Values(name)
		switch {
		case len(got) == 0:
			t.Errorf("%s: %s is absent, want %q", what, name, want)
		case len(got) > 1:
			t.Errorf("%s: %s appears %d times (%q), want exactly one — an Add where the middleware means Set", what, name, len(got), got)
		case got[0] != want:
			t.Errorf("%s: %s = %q, want %q", what, name, got[0], want)
		}
	}
	// This process usually speaks plain HTTP to a proxy or to localhost, where
	// HSTS is either a claim it cannot make or one that poisons the browser's
	// cache for that host and port — including for whatever else the developer
	// runs there next.
	if got := h.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("%s: Strict-Transport-Security = %q, want none: this server does not terminate TLS", what, got)
	}
}

// assertNotCacheable checks a Cache-Control that forbids storing the response,
// without pinning which spelling of that the layer that wrote it chose.
//
// The middleware says no-store; the SDK's streamable handler answers no-cache,
// no-transform on the endpoint and wins, being inner. Both are correct answers
// to the question that matters — nothing here may be replayed from a shared
// cache — and asserting one exact string would fail the day the SDK adjusts the
// other.
func assertNotCacheable(t *testing.T, what string, h http.Header) {
	t.Helper()

	got := h.Get("Cache-Control")
	if got == "" {
		t.Errorf("%s: no Cache-Control at all; a shared cache is free to keep this", what)
		return
	}
	if !strings.Contains(got, "no-store") && !strings.Contains(got, "no-cache") {
		t.Errorf("%s: Cache-Control = %q, want it to forbid caching", what, got)
	}
}

// TestSecurity_HeadersOnAnMCPResponse covers the response every client sees on
// every call, in both framings.
//
// The two framings are separate cases because they are separate code paths in
// the SDK, and the middleware sits outside both: a header set on the way in
// survives whichever one answers, and a chain that only worked for one of them
// would ship half-protected.
func TestSecurity_HeadersOnAnMCPResponse(t *testing.T) {
	cases := []struct {
		name string
		flag []string
	}{
		{name: "SSE, the default framing"},
		{name: "JSON framing", flag: []string{"--json-response"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := startServer(t, nil, tc.flag...)

			reply := s.do(t, mcpPOST(nil))
			if reply.status != http.StatusOK {
				t.Fatalf("tools/list = %d, want %d (body: %s)", reply.status, http.StatusOK, truncate(reply.body))
			}
			assertConstantSecurityHeaders(t, "an MCP POST", reply.header)
			// A tool result is produced for one caller at one moment, so it is
			// never a shared-cache entry — whichever layer got the last word.
			assertNotCacheable(t, "an MCP POST", reply.header)
		})
	}
}

// TestSecurity_HeadersOnTheAnswersTheChainWritesItself covers the responses no
// inner handler ever gets to touch.
//
// These are the ones a middleware placed on the way out would miss: the
// cross-origin refusal, the preflight and the 404 all answer without calling
// through, so headers written after the fact would never reach them. That is
// the whole reason securityHeaders sits outermost and writes on the way in, and
// it is invisible until something asks each of those layers directly.
//
// The statuses here are preconditions, not the subject: the 405's Allow: POST
// is pinned by TestLimits_StatelessDefaultIssuesNoSession and the 403 by
// TestCrossOrigin_DefaultRefusesBrowsersAndAdmitsEveryoneElse, and this case
// asks only what headers ride along with them.
func TestSecurity_HeadersOnTheAnswersTheChainWritesItself(t *testing.T) {
	s := startServer(t, nil, "--trusted-origins="+trustedOrigin)

	cases := []struct {
		name   string
		req    request
		status int
	}{
		{
			name:   "the 404 for an unknown path",
			req:    request{method: http.MethodGet, path: "/nope"},
			status: http.StatusNotFound,
		},
		{
			name:   "the 204 preflight",
			req:    preflightFor(trustedOrigin),
			status: http.StatusNoContent,
		},
		{
			name:   "the 403 for a refused browser origin",
			req:    browserPOST(untrustedOrigin),
			status: http.StatusForbidden,
		},
		{
			name:   "the SDK's 405 for a method the endpoint does not take",
			req:    request{method: http.MethodGet, path: "/"},
			status: http.StatusMethodNotAllowed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := s.do(t, tc.req)
			if reply.status != tc.status {
				t.Fatalf("status = %d, want %d (body: %s)", reply.status, tc.status, truncate(reply.body))
			}
			assertConstantSecurityHeaders(t, tc.name, reply.header)
			// Nothing on this list carries a lifetime of its own, so the
			// middleware's own value is what must survive.
			if got := reply.header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s: Cache-Control = %q, want %q", tc.name, got, "no-store")
			}
		})
	}
}

// TestSecurity_CardKeepsItsOwnCacheControl pins the one deliberate exception,
// from the outside.
//
// The card is the only response here a cache should keep: it changes with a
// release and is fetched by scanners that would otherwise re-request it every
// time. That works only because the middleware writes on the way in and the
// route Sets over the top — an outer handler that wrote its headers after the
// route would silently turn the card back into no-store, and the only visible
// symptom would be traffic.
func TestSecurity_CardKeepsItsOwnCacheControl(t *testing.T) {
	s := startServer(t, nil)

	for _, path := range []string{serverCardCurrentPath, serverCardLegacyPath} {
		t.Run(path, func(t *testing.T) {
			reply := s.do(t, request{method: http.MethodGet, path: path})
			if reply.status != http.StatusOK {
				t.Fatalf("GET %s = %d, want %d", path, reply.status, http.StatusOK)
			}
			// Including nosniff exactly once: this route sets it itself, so it
			// is where a Set-versus-Add mistake in the middleware would show.
			assertConstantSecurityHeaders(t, "GET "+path, reply.header)
			if got := reply.header.Get("Cache-Control"); got != "public, max-age=3600" {
				t.Errorf("GET %s: Cache-Control = %q, want the card's own lifetime %q", path, got, "public, max-age=3600")
			}
		})
	}
}

// TestSecurity_NoHSTSAnywhere states the absence as its own case, because an
// absent header is the kind of thing that gets added by a well-meaning change
// and noticed by nobody until a developer's browser refuses to load anything
// else on localhost over plain HTTP.
//
// assertConstantSecurityHeaders checks it on every response above; this case
// exists so the reason is written down where it is the point rather than a line
// inside a helper.
func TestSecurity_NoHSTSAnywhere(t *testing.T) {
	s := startServer(t, nil)

	paths := []string{"/", "/health", serverCardCurrentPath, serverCardLegacyPath, "/nope"}
	for _, path := range paths {
		reply := s.do(t, request{method: http.MethodGet, path: path})
		if got := reply.header.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("GET %s (%d): Strict-Transport-Security = %q, want none", path, reply.status, got)
		}
	}

	// And the POST, which is the only request that reaches a handler rather
	// than a guard.
	if got := s.do(t, mcpPOST(nil)).header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("an MCP POST: Strict-Transport-Security = %q, want none", got)
	}
}
