//go:build httpe2e

package httpe2e

import (
	"net/http"
	"strings"
	"testing"
)

const (
	trustedOrigin   = "https://claude.ai"
	untrustedOrigin = "https://evil.example"
)

// TestCrossOrigin_DefaultRefusesBrowsersAndAdmitsEveryoneElse pins the shipped
// default, which is the one configuration nobody sets deliberately.
//
// The no-Origin case is the important half. Every CLI, desktop and SDK client
// sends no Origin at all, so it must pass in every configuration — and it is
// the easiest thing to break while making the browser cases work, because
// nothing about it changes when the allowlist does.
func TestCrossOrigin_DefaultRefusesBrowsersAndAdmitsEveryoneElse(t *testing.T) {
	s := startServer(t, nil)

	if got := s.do(t, mcpPOST(nil)).status; got != http.StatusOK {
		t.Errorf("no-Origin client = %d, want %d", got, http.StatusOK)
	}
	if got := s.do(t, browserPOST(trustedOrigin)).status; got != http.StatusForbidden {
		t.Errorf("browser POST = %d, want %d with no allowlist configured", got, http.StatusForbidden)
	}
	// Nothing answers the preflight without an allowlist, so it falls through
	// to the MCP handler's own method check. That is the state the deployment
	// was in when a proxy answering OPTIONS made the endpoint look open.
	if got := s.do(t, preflightFor(trustedOrigin)).status; got != http.StatusMethodNotAllowed {
		t.Errorf("preflight = %d, want %d with no allowlist", got, http.StatusMethodNotAllowed)
	}
}

// TestCrossOrigin_AllowlistedOriginIsAnsweredAndAdmitted covers the pair that
// has to agree: the preflight promises the request will be allowed, and the
// request that follows is allowed.
//
// Splitting them is how the bug shipped — a proxy answered the preflight, the
// server refused the POST, and each half looked correct on its own.
func TestCrossOrigin_AllowlistedOriginIsAnsweredAndAdmitted(t *testing.T) {
	s := startServer(t, nil, "--trusted-origins="+trustedOrigin)

	pre := s.do(t, preflightFor(trustedOrigin))
	if pre.status != http.StatusNoContent {
		t.Fatalf("preflight = %d, want %d", pre.status, http.StatusNoContent)
	}
	// Echoed, never "*": a browser rejects the wildcard on a credentialed
	// request, so answering "*" would refuse the very client the allowlist
	// exists for.
	if got := pre.header.Get("Access-Control-Allow-Origin"); got != trustedOrigin {
		t.Errorf("preflight Allow-Origin = %q, want the origin echoed", got)
	}
	if got := pre.header.Get("Access-Control-Allow-Headers"); got != "content-type" {
		t.Errorf("preflight Allow-Headers = %q, want the requested header echoed", got)
	}
	// The answer is derived from both request headers it echoes, so a shared
	// cache must key on both.
	for _, want := range []string{"Origin", "Access-Control-Request-Headers"} {
		if !strings.Contains(pre.header.Get("Vary"), want) {
			t.Errorf("preflight Vary = %q, want it to name %s", pre.header.Get("Vary"), want)
		}
	}

	post := s.do(t, browserPOST(trustedOrigin))
	if post.status != http.StatusOK {
		t.Errorf("trusted POST = %d, want %d — the preflight promised it would be allowed", post.status, http.StatusOK)
	}
	// Not CORS-safelisted, so a browser cannot read it unless it is exposed.
	if got := post.header.Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Mcp-Session-Id") {
		t.Errorf("Expose-Headers = %q, want it to name Mcp-Session-Id", got)
	}

	if got := s.do(t, browserPOST(untrustedOrigin)).status; got != http.StatusForbidden {
		t.Errorf("untrusted POST = %d, want %d: naming one origin must not widen to others", got, http.StatusForbidden)
	}
	if got := s.do(t, mcpPOST(nil)).status; got != http.StatusOK {
		t.Errorf("no-Origin client = %d, want %d", got, http.StatusOK)
	}
}

// TestCrossOrigin_WildcardAcceptsAnyOriginAndStillEchoes covers the escape
// hatch. The echo matters even here: "*" is not interchangeable with the
// origin, because a browser refuses the wildcard on a credentialed request.
func TestCrossOrigin_WildcardAcceptsAnyOriginAndStillEchoes(t *testing.T) {
	s := startServer(t, nil, "--trusted-origins=*")

	for _, origin := range []string{trustedOrigin, untrustedOrigin} {
		reply := s.do(t, browserPOST(origin))
		if reply.status != http.StatusOK {
			t.Errorf("%s = %d, want %d under the wildcard", origin, reply.status, http.StatusOK)
		}
		if got := reply.header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("Allow-Origin = %q, want %q echoed even under the wildcard", got, origin)
		}
	}
	if got := s.do(t, mcpPOST(nil)).status; got != http.StatusOK {
		t.Errorf("no-Origin client = %d, want %d", got, http.StatusOK)
	}
}

// TestCrossOrigin_SafeMethodsStayReachable pins what the protection must never
// touch. The card and the health probe are what registries, scanners and load
// balancers read, and they are GETs — always allowed, cross-origin or not.
func TestCrossOrigin_SafeMethodsStayReachable(t *testing.T) {
	s := startServer(t, nil)

	for _, path := range []string{"/health", "/.well-known/mcp/server-card.json"} {
		reply := s.do(t, request{method: http.MethodGet, path: path, headers: map[string]string{
			"Origin": untrustedOrigin, "Sec-Fetch-Site": "cross-site",
		}})
		if reply.status != http.StatusOK {
			t.Errorf("GET %s from an untrusted origin = %d, want %d", path, reply.status, http.StatusOK)
		}
	}

	// The card carries its own permissive CORS, deliberately unlike the MCP
	// endpoint: it is a public document with no per-origin answer to fish out.
	card := s.do(t, request{method: http.MethodGet, path: "/.well-known/mcp/server-card.json"})
	if got := card.header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("card Allow-Origin = %q, want *", got)
	}
}

// TestCrossOrigin_MalformedFlagFailsStartup pins the loud failure. An origin
// that is dropped silently is a deployment that believes it trusts something it
// does not, and whose browser clients are refused with nothing to look at.
func TestCrossOrigin_MalformedFlagFailsStartup(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "no scheme", value: "claude.ai"},
		{name: "trailing slash", value: "https://claude.ai/"},
		{name: "a path", value: "https://claude.ai/mcp"},
		// url.Parse cannot report either of these faithfully, so both would
		// otherwise be stored verbatim and never match an Origin header.
		{name: "trailing question mark", value: "https://claude.ai?"},
		{name: "trailing hash", value: "https://claude.ai#"},
		{name: "wildcard inside a list", value: "https://claude.ai,*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runServerExpectingExit(t, "--http", "127.0.0.1:"+itoa(freePort(t)), "--trusted-origins="+tc.value)
			if err == nil {
				t.Fatalf("the server started with --trusted-origins=%q; it must refuse. Output:\n%s", tc.value, out)
			}
			if !strings.Contains(out, "trusted origin") {
				t.Errorf("output does not name the offending flag:\n%s", out)
			}
		})
	}
}
