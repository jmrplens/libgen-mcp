//go:build httpe2e

package httpe2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The values planted through the running server. Each is distinctive enough
// that finding it anywhere is unambiguous, and shaped like the thing it stands
// for so it travels the same code path the real value would.
const (
	plantedQuery    = "planted-query-arcane-hydrology-9f3a"
	plantedMD5      = "9f3a1c7d5e2b48a6c0d9f71e3b8a2c4d"
	plantedDOI      = "10.5555/planted-doi-9f3a"
	plantedAnnasKey = "planted-annas-key-not-a-credential-9f3a"
	plantedEmail    = "planted-contact-9f3a@example.org"
)

// deadProxyEnv sends every non-loopback request at a port nothing is listening
// on, so an outbound fetch fails at connect rather than reaching the internet.
//
// It is what makes the failure-path case deterministic and fast: the transport
// error a refused connection produces is exactly the shape that carries the
// whole request URL, query string included, into an error message. Loopback is
// exempt so the fixtures this harness starts stay reachable.
func deadProxyEnv(t *testing.T) map[string]string {
	t.Helper()

	dead := fmt.Sprintf("http://127.0.0.1:%d", freePort(t))
	return map[string]string{
		"HTTP_PROXY":  dead,
		"HTTPS_PROXY": dead,
		"NO_PROXY":    "127.0.0.1,localhost",
	}
}

// TestCollector_ACredentialNeverLeavesOnTheFailurePath is the case the whole
// module exists for, and it is deliberately the failure path.
//
// A working member download discloses nothing: the key is spent on a request
// that succeeded, and every sink sees a normal result. The disclosure lives in
// the failure: net/http renders a transport failure as a *url.Error whose
// Error() prints the entire request URL — and on this server the credential is
// in that URL's query string, because the two services that take one accept it
// nowhere else. That error is then written to three places at once: the
// operator's log, the model-facing failure document, and — since the telemetry
// bridge — the collector.
//
// So the assertion is made three times, against three different sinks. A test
// that checked one of them would prove nothing about the other two.
func TestCollector_ACredentialNeverLeavesOnTheFailurePath(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	env := withEnv(mirrorEnv(m), collectorEnv(c), deadProxyEnv(t), map[string]string{
		// Both credential-shaped settings, so one case covers both services
		// that take one. They are the operator's here rather than a caller's:
		// the per-call spelling arrives through elicitation, which needs a
		// bidirectional MCP client this module does not have. The value travels
		// the same query string either way, which is what is under test.
		"LIBGEN_MCP_ANNAS_KEY":         plantedAnnasKey,
		"LIBGEN_MCP_UNPAYWALL_EMAIL":   plantedEmail,
		"LIBGEN_MCP_SOURCES":           "annas,unpaywall,libgen",
		"LIBGEN_MCP_TIMEOUT":           "3s",
		"LIBGEN_MCP_RETRY_ATTEMPTS":    "1",
		"LIBGEN_MCP_RESOLVE_BUDGET":    "3s",
		"LIBGEN_MCP_TELEMETRY_SIGNALS": "traces,logs",
	})
	s := startServer(t, env)

	// An md5 download reaches Anna's member endpoint, which takes the key in
	// its query string; a DOI download reaches Unpaywall, which takes the
	// contact address the same way. Both fail at connect through the dead
	// proxy, which is the point.
	byMD5 := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"download",` +
		`"arguments":{"md5":"` + plantedMD5 + `","annas_member":true}}}`
	byDOI := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"download",` +
		`"arguments":{"doi":"` + plantedDOI + `"}}}`

	results := make([]string, 0, 2)
	for _, body := range []string{byMD5, byDOI} {
		t.Run(body, func(t *testing.T) {
			got := s.do(t, request{body: body})
			results = append(results, got.body)
		})
	}

	// Both calls have to have been exported, not merely some batch: the startup
	// one carries no tools/call at all, so waiting for it would make every
	// assertion below a statement about telemetry that predates the calls.
	c.awaitCallsExported(t, 2, 30*time.Second)

	for _, secret := range plantedForms(plantedAnnasKey, plantedEmail) {
		// 1. The collector.
		c.assertNoPayloadContains(t, secret)

		// 2. The operator's own log stream.
		if logs := s.logs(); strings.Contains(logs, secret) {
			t.Errorf("%q reached the server log; a transport failure printed the URL it was in:\n%s", secret, tail(logs))
		}

		// 3. The document the model reads back.
		for i, body := range results {
			if strings.Contains(body, secret) {
				t.Errorf("%q reached the model in the result of call %d: %s", secret, i+1, body)
			}
		}
	}
}

// plantedForms returns each value as it can appear on the wire.
//
// A credential that rides in a query string is URL-escaped on the way into the
// request, so a contact address written `name@example.org` appears as
// `name%40example.org` in the very error message this is looking for. Searching
// only for the literal spelling makes that half of the assertion pass whatever
// the server does — which it did, until the escaped form was added here.
func plantedForms(values ...string) []string {
	forms := make([]string, 0, len(values)*2)
	for _, v := range values {
		forms = append(forms, v)
		if escaped := url.QueryEscape(v); escaped != v {
			forms = append(forms, escaped)
		}
	}
	return forms
}

// TestCollector_TheSearchQueryNeverLeaves is the value this server handles that
// is most sensitive, and the one no setting exports.
//
// What somebody looked for says more about them than which address they used,
// so it is stripped by name on the export leg and recorded nowhere else. The
// query is driven through a real search against the fixture, so it travels
// every layer — the HTTP span, the MCP span, the tool handler, the log records
// — before this looks for it.
func TestCollector_TheSearchQueryNeverLeaves(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		// A catalog page with nothing in it: the search succeeds, returns no
		// results, and the query has already been through the whole server.
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><table></table></body></html>"))
	})

	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"LIBGEN_MCP_EXTRA_SOURCES": "never",
	}))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search",` +
		`"arguments":{"query":"` + plantedQuery + `"}}}`
	if got := s.do(t, request{body: body}); got.status != http.StatusOK {
		t.Fatalf("search answered %d, so nothing was driven: %s", got.status, got.body)
	}

	c.awaitCallsExported(t, 1, 30*time.Second)
	c.assertNoPayloadContains(t, plantedQuery)
}

// TestCollector_TheItemIdentifierNeverLeaves pins what the telemetry guide
// claims about md5s and DOIs.
//
// An identifier names a specific book or paper, which is the same disclosure as
// the query by another route. The machinery to export it as a keyed digest
// exists and has no caller; this is what keeps the documented state and the
// running state the same, and it fails the day somebody wires the digest in
// without saying so.
func TestCollector_TheItemIdentifierNeverLeaves(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), deadProxyEnv(t), map[string]string{
		"LIBGEN_MCP_TIMEOUT":        "3s",
		"LIBGEN_MCP_RESOLVE_BUDGET": "3s",
		"LIBGEN_MCP_RETRY_ATTEMPTS": "1",
	}))

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_details","arguments":{"md5":"` + plantedMD5 + `"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"download","arguments":{"doi":"` + plantedDOI + `"}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			s.do(t, request{body: body})
		})
	}

	c.awaitCallsExported(t, 2, 30*time.Second)
	c.assertNoPayloadContains(t, plantedMD5, plantedDOI)
}

// TestCollector_EveryIdentityPolicyKeepsThePlantedValuesIn is the same promise
// under the setting that is meant to loosen things.
//
// `full` is the policy that exports the caller's address and the client it
// connected with — and it is still not a policy that exports what was searched
// for or which credential was spent. A leak test that only ran under the
// default would say nothing about the deployment most likely to be collecting.
func TestCollector_EveryIdentityPolicyKeepsThePlantedValuesIn(t *testing.T) {
	for _, policy := range []string{"none", "pseudonymous", "full"} {
		t.Run(policy, func(t *testing.T) {
			c := startCollector(t)
			m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><body><table></table></body></html>"))
			})

			s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
				"LIBGEN_MCP_TELEMETRY_IDENTITY": policy,
				"LIBGEN_MCP_EXTRA_SOURCES":      "never",
				"LIBGEN_MCP_ANNAS_KEY":          plantedAnnasKey,
			}))

			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search",` +
				`"arguments":{"query":"` + plantedQuery + `"}}}`
			s.do(t, request{body: body})

			c.awaitCallsExported(t, 1, 30*time.Second)
			c.assertNoPayloadContains(t, plantedQuery, plantedAnnasKey)
		})
	}
}

// tail returns the last of a long log, so a failure message is readable.
func tail(s string) string {
	const keep = 4000
	if len(s) <= keep {
		return s
	}
	return "…" + s[len(s)-keep:]
}
