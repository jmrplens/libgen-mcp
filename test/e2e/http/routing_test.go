//go:build httpe2e

package httpe2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The two locations the server card answers on. Both are served and both return
// the same bytes: the current one is where the ext-server-card extension moved
// the document, and the legacy one is kept because the scanners that already
// fetch it would otherwise start getting a 404.
const (
	serverCardCurrentPath = "/server-card"
	serverCardLegacyPath  = "/.well-known/mcp/server-card.json"
)

// mcpPOSTAt is mcpPOST aimed somewhere other than the root, for the cases where
// --http-path has moved the endpoint and the path is the thing being tested.
func mcpPOSTAt(path string) request {
	r := mcpPOST(nil)
	r.path = path
	return r
}

// assertNotFound checks that a path this server does not serve answers a JSON
// 404 naming the endpoint that does exist.
//
// The status is the whole point: while the MCP handler was mounted as a
// catch-all, every unknown path came back 405 with Allow: POST, which asserted
// two things that were false — that the route exists, and that another method
// would reach it. A scanner reading that keeps probing.
//
// r.method is required rather than defaulted, so a failure message says which
// request produced it.
func assertNotFound(t *testing.T, s *server, r request, wantEndpoint string) {
	t.Helper()

	if r.method == "" {
		t.Fatalf("assertNotFound needs an explicit method for %q", r.path)
	}
	reply := s.do(t, r)
	if reply.status != http.StatusNotFound {
		t.Fatalf("%s %s = %d, want %d: an unserved path must not claim the route exists (body: %s)",
			r.method, r.path, reply.status, http.StatusNotFound, truncate(reply.body))
	}
	if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s %s: Content-Type = %q, want application/json", r.method, r.path, ct)
	}
	// The trailing newline is what keeps the body a line rather than a fragment
	// when it lands in a terminal or a log.
	if !strings.HasSuffix(reply.body, "\n") {
		t.Errorf("%s %s: body does not end in a newline: %q", r.method, r.path, reply.body)
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(reply.body), &payload); err != nil {
		t.Fatalf("%s %s: body is not JSON: %v (%s)", r.method, r.path, err, truncate(reply.body))
	}
	if payload["error"] != "not found" {
		t.Errorf("%s %s: error = %q, want %q", r.method, r.path, payload["error"], "not found")
	}
	// Naming the endpoint is the useful half: whoever asked for the wrong path
	// is told the right one instead of being left to guess it.
	if payload["mcp_endpoint"] != wantEndpoint {
		t.Errorf("%s %s: mcp_endpoint = %q, want %q", r.method, r.path, payload["mcp_endpoint"], wantEndpoint)
	}
}

// TestRouting_UnknownPathsAnswer404 covers the paths the hosted deployment
// actually sees probed.
//
// The OAuth discovery paths are not hypothetical: they are what a client
// looking for an authorization server fetches before it connects, and this
// server implements none of them. Answering 405 to those told the caller the
// endpoints exist, which is why they kept being asked for — ninety-nine times a
// day against the hosted deployment.
//
// The root's 405 is the other half of this change and is pinned where it
// already was: TestLimits_StatelessDefaultIssuesNoSession for GET / and its
// Allow: POST, TestCrossOrigin_DefaultRefusesBrowsersAndAdmitsEveryoneElse for
// the unanswered preflight. Both must keep passing — the endpoint moving from a
// catch-all to an exact match must not turn a wrong method into a wrong path.
func TestRouting_UnknownPathsAnswer404(t *testing.T) {
	s := startServer(t, nil)

	paths := []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-authorization-server",
		// The parent of the legacy card route, and a directory this server
		// does not serve: a prefix that matches a real route is exactly where
		// an accidental catch-all would show up again.
		"/.well-known/mcp",
		// What a client given the wrong base URL asks for.
		"/mcp",
		"/nope",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			assertNotFound(t, s, request{method: http.MethodGet, path: path}, "/")
		})
	}

	// A POST is the method the endpoint does take, so this is the case that
	// distinguishes "the wrong path" from "the wrong method" — it must be the
	// path that decides, not the verb.
	t.Run("POST to an unserved path", func(t *testing.T) {
		assertNotFound(t, s, mcpPOSTAt("/nope"), "/")
	})
}

// TestRouting_NotFoundIsReadableByABrowser covers why the 404 is wrapped in the
// CORS layer at all.
//
// A cross-origin response with no CORS header is not shown to the page: the
// browser reports a CORS failure and the status never surfaces. The 404 exists
// to name a mistyped path, so hiding it behind a CORS error would defeat the
// one thing it is for.
func TestRouting_NotFoundIsReadableByABrowser(t *testing.T) {
	s := startServer(t, nil, "--trusted-origins="+trustedOrigin)

	reply := s.do(t, request{method: http.MethodGet, path: "/nope", headers: map[string]string{
		"Origin": trustedOrigin, "Sec-Fetch-Site": "cross-site",
	}})
	if reply.status != http.StatusNotFound {
		t.Fatalf("cross-origin GET /nope = %d, want %d", reply.status, http.StatusNotFound)
	}
	if got := reply.header.Get("Access-Control-Allow-Origin"); got != trustedOrigin {
		t.Errorf("Allow-Origin = %q, want the origin echoed: without it the page sees a CORS error instead of the 404", got)
	}
}

// TestRouting_BothCardRoutesServeTheSameBytes pins the pair a scanner may read
// either of.
//
// Two locations for one document is a promise that they agree. They are served
// from the same slice in the handler, so the only way they could differ is a
// change that gave one of them its own copy — which is precisely the change
// this case is here to catch, since nothing else compares them.
func TestRouting_BothCardRoutesServeTheSameBytes(t *testing.T) {
	s := startServer(t, nil)

	current := s.do(t, request{method: http.MethodGet, path: serverCardCurrentPath})
	if current.status != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d", serverCardCurrentPath, current.status, http.StatusOK)
	}
	// The extension gives the document its own media type; a client comparing
	// the header literally is the reason it is not simply application/json.
	if got := current.header.Get("Content-Type"); got != "application/mcp-server-card+json" {
		t.Errorf("GET %s: Content-Type = %q, want %q", serverCardCurrentPath, got, "application/mcp-server-card+json")
	}

	legacy := s.do(t, request{method: http.MethodGet, path: serverCardLegacyPath})
	if legacy.status != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d", serverCardLegacyPath, legacy.status, http.StatusOK)
	}
	// The legacy route keeps application/json: that is what the scanners
	// already fetching it expect, and changing it would be a silent break for
	// the audience the route exists to serve.
	if ct := legacy.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET %s: Content-Type = %q, want application/json", serverCardLegacyPath, ct)
	}

	if current.body != legacy.body {
		t.Errorf("the two card routes returned different documents:\n%s = %s\n%s = %s",
			serverCardCurrentPath, truncate(current.body), serverCardLegacyPath, truncate(legacy.body))
	}
	// A pair of empty bodies would be identical too, so the comparison needs
	// something to stand on. What the document actually contains is pinned by
	// TestClient_ServerCardIsReadableWithoutASession; this only needs to know
	// that a card was served at all.
	var card map[string]any
	if err := json.Unmarshal([]byte(current.body), &card); err != nil {
		t.Fatalf("the card is not JSON: %v (%s)", err, truncate(current.body))
	}
	if _, ok := card["serverInfo"]; !ok {
		t.Errorf("the card carries no serverInfo: %s", truncate(current.body))
	}
}

// TestRouting_BasePathMovesEveryRoute covers the mount a reverse proxy that
// forwards its prefix needs.
//
// Every route moves together — the endpoint, the health probe and both card
// locations — and nothing is left behind at the root. Both halves matter: a
// server that mounted the endpoint under the prefix while still answering
// /health at the root would pass a health check that says nothing about the
// deployment it is standing in front of.
func TestRouting_BasePathMovesEveryRoute(t *testing.T) {
	const base = "/libgen"
	s := startServer(t, nil, "--http-path="+base)

	// Both spellings of the mount serve MCP: a client handed a base URL may or
	// may not keep the trailing slash, and neither is a different endpoint.
	for _, path := range []string{base, base + "/"} {
		reply := s.do(t, mcpPOSTAt(path))
		if reply.status != http.StatusOK {
			t.Errorf("POST %s = %d, want %d (body: %s)", path, reply.status, http.StatusOK, truncate(reply.body))
			continue
		}
		if !strings.Contains(reply.body, `"tools"`) {
			t.Errorf("POST %s answered %d but listed no tools: %s", path, reply.status, truncate(reply.body))
		}
	}

	// A GET there is the endpoint refusing a method, not the catch-all
	// refusing a path — which is what says the endpoint really is mounted
	// here. (The root mount's 405 and its Allow header are pinned by
	// TestLimits_StatelessDefaultIssuesNoSession.)
	for _, path := range []string{base, base + "/"} {
		if got := s.do(t, request{method: http.MethodGet, path: path}).status; got != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want %d", path, got, http.StatusMethodNotAllowed)
		}
	}

	health := s.do(t, request{method: http.MethodGet, path: base + "/health"})
	if health.status != http.StatusOK {
		t.Errorf("GET %s/health = %d, want %d", base, health.status, http.StatusOK)
	}

	for _, path := range []string{serverCardCurrentPath, serverCardLegacyPath} {
		if got := s.do(t, request{method: http.MethodGet, path: base + path}).status; got != http.StatusOK {
			t.Errorf("GET %s%s = %d, want %d", base, path, got, http.StatusOK)
		}
	}

	// Nothing stayed at the root, and the 404 names the moved endpoint rather
	// than the "/" it was compiled with.
	for _, path := range []string{"/", "/health", serverCardCurrentPath, serverCardLegacyPath, base + "/nope"} {
		assertNotFound(t, s, request{method: http.MethodGet, path: path}, base)
	}
}

// TestRouting_BasePathSpellingsAreEquivalent covers the lenient half of the
// rule, which the refusals below would otherwise let rot.
//
// A validator that rejected every spelling but one would pass every case in
// TestRouting_InvalidBasePathIsRefusedAtStartup and break the operator who
// wrote the prefix the way it appears in their proxy config. "libgen/" is the
// same mount as "/libgen".
func TestRouting_BasePathSpellingsAreEquivalent(t *testing.T) {
	s := startServer(t, nil, "--http-path=libgen/")

	if got := s.do(t, mcpPOSTAt("/libgen")).status; got != http.StatusOK {
		t.Errorf("POST /libgen = %d, want %d: --http-path=libgen/ must mount the same place as /libgen", got, http.StatusOK)
	}
	if got := s.do(t, request{method: http.MethodGet, path: "/libgen/health"}).status; got != http.StatusOK {
		t.Errorf("GET /libgen/health = %d, want %d", got, http.StatusOK)
	}
}

// TestRouting_InvalidBasePathIsRefusedAtStartup pins the loud failure.
//
// A mount the server cannot match answers 404 to everything, which looks
// exactly like a proxy fault from the outside and is the hardest kind of
// misconfiguration to find. Refusing at startup — naming the value, so the
// operator can see what their config produced — costs one deploy instead of an
// afternoon.
func TestRouting_InvalidBasePathIsRefusedAtStartup(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "a traversal segment", value: "/a/../b"},
		{name: "a query", value: "/libgen?x=1"},
		{name: "a fragment", value: "/libgen#frag"},
		// An escaped separator matches no request: the mux compares the decoded
		// path, so this mount could never be reached.
		{name: "a percent-escape", value: "/lib%2Fgen"},
		{name: "a whole URL where a path belongs", value: "http://evil.example/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runServerExpectingExit(t, "--http", "127.0.0.1:"+itoa(freePort(t)), "--http-path="+tc.value)
			if err == nil {
				t.Fatalf("the server started with --http-path=%q; it must refuse. Output:\n%s", tc.value, out)
			}
			if !strings.Contains(out, "http-path") {
				t.Errorf("output does not name the offending flag:\n%s", out)
			}
			if !strings.Contains(out, tc.value) {
				t.Errorf("output does not quote the rejected value %q:\n%s", tc.value, out)
			}
		})
	}
}
