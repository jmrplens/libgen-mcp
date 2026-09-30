//go:build httpe2e

// result_type_test.go holds the resultType of a tools/call result a receiving
// middleware makes rather than the SDK's tool dispatcher.
//
// Revision 2026-07-28 says a server implementing it MUST include resultType on
// every result. go-sdk v1.8.0 sets it on a tools/call result only inside its
// own tool dispatcher, and the rate limiter answers a refused call before that
// dispatcher runs, so until toolutil.RefusalResult labeled its own refusals
// every rate-limited tools/call at 2026-07-28 went out without the field. The
// schema's rule that a client reads an absent resultType as complete covers
// only a server implementing an earlier revision, so a client holding this
// server to 2026-07-28 was entitled to reject the refusal. See
// docs/development/upstream-bugs.md.
package httpe2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// modernToolCallBody is a tools/call under 2026-07-28, carrying the revision
// and the client's capabilities in _meta as that revision requires. The id
// looks like an md5 so the call passes validation and reaches the stand-in
// mirror, which is what makes the served call a result the dispatcher built.
const modernToolCallBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
	`"name":"get_details","arguments":{"id":"0123456789abcdef0123456789abcdef"},"_meta":{` +
	`"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientCapabilities":{},` +
	`"io.modelcontextprotocol/clientInfo":{"name":"httpe2e","version":"0"}}}}`

// legacyToolCallBody is the same call as a client of an earlier revision sends
// it, with nothing in _meta.
const legacyToolCallBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
	`"name":"get_details","arguments":{"id":"0123456789abcdef0123456789abcdef"}}}`

// startLimitedServer launches the binary on loopback with a one-token bucket
// per forwarded address, pointed at a mirror that answers every request 500.
//
// The limit needs a proxy the server believes on a loopback bind, and the
// mirror on loopback needs the private-address hatch, which startup refuses on
// a wildcard bind. So the caller is told apart by X-Real-IP from a trusted
// 127.0.0.1, and every request this file sends carries one.
func startLimitedServer(t *testing.T) *server {
	t.Helper()
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "stand-in mirror", http.StatusInternalServerError)
	})
	port := freePort(t)
	return launchServer(t, fmt.Sprintf("http://127.0.0.1:%d", port), nil, mirrorEnv(m), append([]string{
		"--http", fmt.Sprintf("127.0.0.1:%d", port),
		"--trusted-proxy-header", "X-Real-IP",
		"--trusted-proxies", "127.0.0.1/32",
	}, oneAtATime...))
}

// toolCallResult returns the result object of a tools/call response, SSE-framed
// or not, decoded as a map so an absent resultType can be told from an empty
// one.
func toolCallResult(t *testing.T, what string, reply response) map[string]any {
	t.Helper()
	if reply.status != http.StatusOK {
		t.Fatalf("%s: status = %d, want %d (body: %s)", what, reply.status, http.StatusOK, truncate(reply.body))
	}
	var envelope struct {
		Result map[string]any  `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(jsonFrame(reply.body)), &envelope); err != nil {
		t.Fatalf("%s: reply is not JSON-RPC: %v (%s)", what, err, truncate(reply.body))
	}
	if envelope.Result == nil {
		t.Fatalf("%s: no result (error %s): %s", what, envelope.Error, truncate(reply.body))
	}
	return envelope.Result
}

// firstText returns the text of a tool result's first content block, or ""
// when it has none.
func firstText(result map[string]any) string {
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}

// TestRateLimitedToolCall_ResultTypeFollowsTheRevision drives two tools/call
// through a one-token bucket at each revision and reads the refusal off the
// wire.
//
// At 2026-07-28 the served call is the control: the dispatcher labels it
// "complete", which makes the refusal's label about who built the result rather
// than about the revision, the transport or the tool. At 2025-11-25 neither
// carries the field, which is what the SDK sends a client of that revision.
func TestRateLimitedToolCall_ResultTypeFollowsTheRevision(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		headers map[string]string
		want    any
	}{
		{
			name: "2026-07-28",
			body: modernToolCallBody,
			headers: map[string]string{
				"MCP-Protocol-Version": "2026-07-28",
				"Mcp-Method":           "tools/call",
				"Mcp-Name":             "get_details",
			},
			want: "complete",
		},
		{
			name:    "2025-11-25",
			body:    legacyToolCallBody,
			headers: map[string]string{},
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := startLimitedServer(t)
			tc.headers["X-Real-IP"] = "203.0.113.7"
			call := request{method: http.MethodPost, body: tc.body, headers: tc.headers}

			served := toolCallResult(t, "the first call", s.do(t, call))
			if strings.HasPrefix(firstText(served), "rate limit exceeded") {
				t.Fatalf("the first call, which the bucket had room for, was refused: %v", served)
			}
			if got := served["resultType"]; got != tc.want {
				t.Fatalf("the served call carried resultType %v, want %v: the control is broken, so this case says nothing about the refusal", got, tc.want)
			}

			refused := toolCallResult(t, "the second call", s.do(t, call))
			if isError, _ := refused["isError"].(bool); !isError || !strings.HasPrefix(firstText(refused), "rate limit exceeded") {
				t.Fatalf("the second call was not the rate limiter's refusal, so this case is not about a middleware-made result: %v", refused)
			}
			if got := refused["resultType"]; got != tc.want {
				t.Errorf("the rate limiter's refusal carried resultType %v, want %v, the same as the call the dispatcher served", got, tc.want)
			}
		})
	}
}
