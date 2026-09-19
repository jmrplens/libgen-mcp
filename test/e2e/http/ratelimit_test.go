//go:build httpe2e

package httpe2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The refusal the server writes when a caller's bucket is empty. -42900 mirrors
// HTTP 429; it is written out rather than imported, because what a client
// matches on is the number on the wire.
const rateLimitedCode = -42900

// oneAtATime is a limit tight enough to reach with two requests and slow enough
// that it never refills during a test: one token every sixteen minutes.
var oneAtATime = []string{"--rate-limit-rps", "0.001", "--rate-limit-burst", "1"}

// startWildcardServer launches the binary on a wildcard bind, which is the
// hosted deployment's shape and both documented recipes'.
//
// It matters here rather than being a detail: the startup rule turns the limit
// off on a listener whose every peer is this machine unless a proxy is named,
// and startServer binds 127.0.0.1. A case about the limit being ON with no proxy
// flags has to be on the listener where that is allowed.
func startWildcardServer(t *testing.T, env map[string]string, flags ...string) *server {
	t.Helper()

	port := freePort(t)
	return launchServer(t, fmt.Sprintf("http://127.0.0.1:%d", port), nil, env,
		append([]string{"--http", fmt.Sprintf(":%d", port)}, flags...))
}

// forwardedFrom is a tools/list announcing a client address, as a proxy would.
func forwardedFrom(header, address string) request {
	return request{method: http.MethodPost, body: toolsListBody, headers: map[string]string{header: address}}
}

// rpcErrorCode returns the JSON-RPC error code in a reply, or 0 when the reply
// carries a result instead.
//
// The refusal travels inside a 200: it is a JSON-RPC error, not an HTTP one, so
// a test that looked at the status would see a served request.
func rpcErrorCode(t *testing.T, what string, reply response) int {
	t.Helper()

	if reply.status != http.StatusOK {
		t.Fatalf("%s: status = %d, want %d (body: %s)", what, reply.status, http.StatusOK, truncate(reply.body))
	}
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(jsonFrame(reply.body)), &envelope); err != nil {
		t.Fatalf("%s: reply is not JSON-RPC: %v (%s)", what, err, truncate(reply.body))
	}
	if envelope.Error == nil {
		return 0
	}
	return envelope.Error.Code
}

// TestRateLimit_TwoForwardedAddressesGetABucketEach is the property the whole
// step exists for, driven over the real binary.
//
// The two clients differ in nothing but the address the proxy forwards, so if
// they share a bucket it is because the server cannot tell them apart — which is
// exactly the failure the flags are there to prevent.
func TestRateLimit_TwoForwardedAddressesGetABucketEach(t *testing.T) {
	s := startWildcardServer(t, nil, append([]string{
		"--trusted-proxy-header", "X-Real-IP",
		"--trusted-proxies", "127.0.0.1/32",
	}, oneAtATime...)...)

	first := s.do(t, forwardedFrom("X-Real-IP", "203.0.113.7"))
	if code := rpcErrorCode(t, "the first caller's first request", first); code != 0 {
		t.Fatalf("the very first request was refused with code %d", code)
	}

	// The same caller again: its own bucket is empty.
	again := s.do(t, forwardedFrom("X-Real-IP", "203.0.113.7"))
	if code := rpcErrorCode(t, "the first caller's second request", again); code != rateLimitedCode {
		t.Errorf("code = %d, want %d: one caller drained their bucket and was served anyway", code, rateLimitedCode)
	}

	// A different caller, arriving on the same connection from the same peer:
	// a bucket of their own, still full.
	other := s.do(t, forwardedFrom("X-Real-IP", "198.51.100.23"))
	if code := rpcErrorCode(t, "the second caller's first request", other); code != 0 {
		t.Errorf("code = %d, want a served request: the second caller paid for the first one's traffic", code)
	}
}

// TestRateLimit_WithoutTheProxyFlagsEveryCallerSharesOneBucket is the same three
// requests against a server that was told to believe nobody.
//
// It is the state the flags exist to leave behind, and asserting it is what
// makes the test above mean something: without it, a build that ignored the
// header entirely would pass by refusing nobody.
func TestRateLimit_WithoutTheProxyFlagsEveryCallerSharesOneBucket(t *testing.T) {
	s := startWildcardServer(t, nil, oneAtATime...)

	if code := rpcErrorCode(t, "the first request", s.do(t, forwardedFrom("X-Real-IP", "203.0.113.7"))); code != 0 {
		t.Fatalf("the very first request was refused with code %d", code)
	}
	second := s.do(t, forwardedFrom("X-Real-IP", "198.51.100.23"))
	if code := rpcErrorCode(t, "a different forwarded address", second); code != rateLimitedCode {
		t.Errorf("code = %d, want %d: the header was believed from a peer nobody vouched for", code, rateLimitedCode)
	}
}

// TestRateLimit_TheWarningNamesTheFlagsThatFixIt covers the case no startup rule
// can decide: a wildcard bind reached from an address no public client could
// have, with nothing vouching for a proxy.
//
// One line, once. The condition holds for every request such a deployment
// serves, so anything per-request would be a flood — and a flood is how a
// warning stops being read.
func TestRateLimit_TheWarningNamesTheFlagsThatFixIt(t *testing.T) {
	s := startWildcardServer(t, nil, oneAtATime...)

	// Two requests, so a per-request line would show up as two.
	for range 2 {
		s.do(t, mcpPOST(nil))
	}

	logs := s.logs()
	if got := strings.Count(logs, "callers cannot be told apart"); got != 1 {
		t.Errorf("the warning appears %d times after two requests, want exactly 1:\n%s", got, logs)
	}
	for _, want := range []string{"--trusted-proxy-header", "--trusted-proxies"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(logs, want) {
				t.Errorf("the warning does not name %s:\n%s", want, logs)
			}
		})
	}
}

// TestRateLimit_NoWarningWhenTheProxyIsNamed is the negative: a correctly
// configured deployment must not be told it is misconfigured, or the line is
// noise everywhere and evidence nowhere.
func TestRateLimit_NoWarningWhenTheProxyIsNamed(t *testing.T) {
	s := startServer(t, nil,
		"--trusted-proxy-header", "X-Real-IP",
		"--trusted-proxies", "127.0.0.1/32",
	)

	s.do(t, forwardedFrom("X-Real-IP", "203.0.113.7"))

	if logs := s.logs(); strings.Contains(logs, "callers cannot be told apart") {
		t.Errorf("a deployment that named its proxy was warned anyway:\n%s", logs)
	}
}

// TestRateLimit_OffOnASocketUnlessItsPeersAreTrusted is the documented same-host
// recommendation, in both states.
//
// A socket peer is a path, so without the "unix" entry every caller is one key
// and a per-caller limit is one budget for the whole deployment. The startup
// line is the only place that difference is visible.
func TestRateLimit_OffOnASocketUnlessItsPeersAreTrusted(t *testing.T) {
	t.Run("off without the unix entry", func(t *testing.T) {
		s := startUnixServer(t, nil)
		assertToolsListed(t, "over a socket with no limit", s.do(t, mcpPOST(nil)))
		if logs := s.logs(); !strings.Contains(logs, "inbound rate limit: off") {
			t.Errorf("the startup line does not say the limit is off:\n%s", logs)
		}
	})

	t.Run("on with it", func(t *testing.T) {
		s := startUnixServer(t, nil,
			"--trusted-proxy-header", "X-Real-IP",
			"--trusted-proxies", unixPeerEntryLiteral,
		)
		assertToolsListed(t, "over a socket whose peers are trusted", s.do(t, mcpPOST(nil)))
		if logs := s.logs(); !strings.Contains(logs, "per charged address") {
			t.Errorf("the startup line does not say the limit is on:\n%s", logs)
		}
	})
}

// unixPeerEntryLiteral is the --trusted-proxies spelling for a socket's peers,
// written out because this package cannot import cmd/server.
const unixPeerEntryLiteral = "unix"

// TestRateLimit_StartupRefusals covers the listeners where an explicit
// --rate-limit-rps cannot mean what the operator thinks.
//
// Off by default is honest for somebody who never asked for a limit. Silently
// off for somebody who did is a deployment that believes it is bounded and is
// not, so startup refuses and names the two flags that would fix it.
func TestRateLimit_StartupRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "a loopback bind", args: []string{"--http", "127.0.0.1:0", "--rate-limit-rps", "10"}},
		{name: "a unix socket", args: []string{"--http", socketPath(t), "--rate-limit-rps", "10"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runServerExpectingExit(t, tc.args...)
			if err == nil {
				t.Fatalf("the server started anyway. Output:\n%s", out)
			}
			for _, want := range []string{"--rate-limit-rps", "--trusted-proxies"} {
				if !strings.Contains(out, want) {
					t.Errorf("the refusal does not name %s:\n%s", want, out)
				}
			}
		})
	}
}

// TestRateLimit_ALoopbackBindWithATrustedProxyKeepsTheLimit is the other half of
// that rule: once a proxy is named, the address means something again and the
// explicit flag is honored rather than refused.
func TestRateLimit_ALoopbackBindWithATrustedProxyKeepsTheLimit(t *testing.T) {
	port := freePort(t)
	s := launchServer(t, fmt.Sprintf("http://127.0.0.1:%d", port), nil, nil, append([]string{
		"--http", fmt.Sprintf("127.0.0.1:%d", port),
		"--trusted-proxy-header", "X-Real-IP",
		"--trusted-proxies", "127.0.0.1/32",
	}, oneAtATime...))

	if code := rpcErrorCode(t, "the first request", s.do(t, forwardedFrom("X-Real-IP", "203.0.113.7"))); code != 0 {
		t.Fatalf("the very first request was refused with code %d", code)
	}
	second := s.do(t, forwardedFrom("X-Real-IP", "203.0.113.7"))
	if code := rpcErrorCode(t, "the second request", second); code != rateLimitedCode {
		t.Errorf("code = %d, want %d: the limit was not applied on a loopback bind that names its proxy", code, rateLimitedCode)
	}
}
