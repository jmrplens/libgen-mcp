//go:build httpe2e

package httpe2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The bar in this file is not that a hostile request gets the right error. It
// is that the process survives it and keeps serving everyone else, so every
// case ends by asking /health.

// TestRobust_MalformedBodies covers bodies that are wrong in ways a parser can
// be talked into mishandling.
func TestRobust_MalformedBodies(t *testing.T) {
	s := startServer(t, nil)

	cases := []struct {
		name string
		body string
	}{
		{name: "not JSON at all", body: "this is not json"},
		{name: "truncated JSON", body: `{"jsonrpc":"2.0","id":1,`},
		{name: "an empty object", body: `{}`},
		{name: "a JSON array where an object belongs", body: `[1,2,3]`},
		{name: "a null body", body: `null`},
		{name: "deeply nested", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":` + strings.Repeat(`{"a":`, 500) + `1` + strings.Repeat(`}`, 500) + `}`},
		{name: "a null byte inside a string", body: "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/\x00list\"}"},
		{name: "invalid UTF-8", body: "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"\xff\xfe\"}"},
		{name: "a huge id", body: `{"jsonrpc":"2.0","id":` + strings.Repeat("9", 400) + `,"method":"tools/list"}`},
		{name: "duplicate keys", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := s.do(t, request{body: tc.body})
			t.Logf("status %d", reply.status)
			if !s.healthy(t) {
				t.Fatal("the server stopped serving")
			}
		})
	}
}

// TestRobust_HostileHeaderValues covers values a client can legally send and a
// handler might pass somewhere it should not.
func TestRobust_HostileHeaderValues(t *testing.T) {
	s := startServer(t, nil)

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{name: "a very long Origin", headers: map[string]string{"Origin": "https://" + strings.Repeat("a", 8000) + ".example"}},
		{name: "an Origin with a fragment", headers: map[string]string{"Origin": "https://evil.example#x"}},
		{name: "several Origins in one value", headers: map[string]string{"Origin": "https://a.example https://b.example"}},
		{name: "an Origin claiming to be the wildcard", headers: map[string]string{"Origin": "*"}},
		{name: "a very long protocol version", headers: map[string]string{"MCP-Protocol-Version": strings.Repeat("9", 4000)}},
		{name: "a session id that is a path", headers: map[string]string{"Mcp-Session-Id": "../../etc/passwd"}},
		{name: "a UTF-8 session id", headers: map[string]string{"Mcp-Session-Id": "sesión-ñ"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := s.do(t, mcpPOST(tc.headers))
			t.Logf("status %d", reply.status)
			if !s.healthy(t) {
				t.Fatal("the server stopped serving")
			}
		})
	}
}

// TestRobust_RawSocketAttacks spells the requests out by hand, because Go's own
// client refuses to send most of them: a header value with CR, LF or NUL, and a
// URL with a control character, are rejected client-side. An attacker has no
// such restriction, so the only way to ask the question is to write the bytes.
func TestRobust_RawSocketAttacks(t *testing.T) {
	s := startServer(t, nil)
	host := strings.TrimPrefix(s.baseURL, "http://")

	cases := []struct {
		name string
		wire string
	}{
		{
			name: "CRLF injection in a header value",
			wire: "POST / HTTP/1.1\r\nHost: " + host + "\r\nOrigin: https://a.example\r\nX-Evil: a\r\nInjected: yes\r\nContent-Length: 0\r\n\r\n",
		},
		{
			name: "smuggling: two Content-Length headers",
			wire: "POST / HTTP/1.1\r\nHost: " + host + "\r\nContent-Length: 6\r\nContent-Length: 5\r\n\r\nabcdef",
		},
		{
			name: "smuggling: Content-Length with Transfer-Encoding",
			wire: "POST / HTTP/1.1\r\nHost: " + host + "\r\nContent-Length: 6\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
		},
		{
			name: "a bare LF between headers",
			wire: "POST / HTTP/1.1\r\nHost: " + host + "\nContent-Length: 0\r\n\r\n",
		},
		{
			name: "an absurd Content-Length",
			wire: "POST / HTTP/1.1\r\nHost: " + host + "\r\nContent-Length: 999999999999\r\n\r\n",
		},
		{
			name: "a control character in the path",
			wire: "POST /\x01\x02 HTTP/1.1\r\nHost: " + host + "\r\nContent-Length: 0\r\n\r\n",
		},
		{
			name: "no Host header at all",
			wire: "POST / HTTP/1.1\r\nContent-Length: 0\r\n\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := s.raw(t, tc.wire)
			first, _, _ := strings.Cut(reply, "\r\n")
			t.Logf("reply: %q", first)
			if strings.Contains(reply, "Injected: yes") {
				t.Error("the injected header was reflected: a value crossed into the response")
			}
			if !s.healthy(t) {
				t.Fatal("the server stopped serving")
			}
		})
	}
}

// TestRobust_UnusualMethodsAndPaths covers what a scanner sends. None of these
// should reach a handler that assumes otherwise, and none should stop the
// process.
func TestRobust_UnusualMethodsAndPaths(t *testing.T) {
	s := startServer(t, nil)

	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodHead, http.MethodTrace, "BREW"} {
		t.Run("method "+method, func(t *testing.T) {
			reply := s.do(t, request{method: method, path: "/"})
			t.Logf("status %d", reply.status)
			if !s.healthy(t) {
				t.Fatal("the server stopped serving")
			}
		})
	}

	for _, path := range []string{
		"/../../etc/passwd",
		"/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/.well-known/mcp/../../../etc/passwd",
		"/health/../../",
		"/" + strings.Repeat("a", 4000),
	} {
		t.Run("path "+truncate(path), func(t *testing.T) {
			reply := s.do(t, request{method: http.MethodGet, path: path})
			if strings.Contains(reply.body, "root:") {
				t.Error("the response carries what looks like /etc/passwd")
			}
			t.Logf("status %d", reply.status)
			if !s.healthy(t) {
				t.Fatal("the server stopped serving")
			}
		})
	}
}

// TestRobust_HeaderFlood sends more headers than any client would, which is the
// cheapest way to exhaust a server that keeps them all.
func TestRobust_HeaderFlood(t *testing.T) {
	s := startServer(t, nil)
	host := strings.TrimPrefix(s.baseURL, "http://")

	var wire strings.Builder
	wire.WriteString("POST / HTTP/1.1\r\nHost: " + host + "\r\n")
	for i := range 2000 {
		fmt.Fprintf(&wire, "X-Flood-%d: %s\r\n", i, strings.Repeat("v", 100))
	}
	wire.WriteString("Content-Length: 0\r\n\r\n")

	reply := s.raw(t, wire.String())
	first, _, _ := strings.Cut(reply, "\r\n")
	t.Logf("reply: %q", first)
	if !s.healthy(t) {
		t.Fatal("the server stopped serving after a header flood")
	}
}

// TestRobust_MisbehavingMirrorLeavesTheServerServing is the case this server
// has and the sibling does not: every tool call reaches an upstream mirror, so
// a mirror that is slow, broken or hostile is a live input to the process.
//
// The requirement is not a particular error message. It is that a bad upstream
// produces a bounded, clean answer and the server keeps serving — no panic, no
// goroutine wedged on a read that never returns, no request that outlives its
// own timeout.
func TestRobust_MisbehavingMirrorLeavesTheServerServing(t *testing.T) {
	searchBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"anything"}}}`

	cases := []struct {
		name    string
		respond http.HandlerFunc
	}{
		{
			name: "500 from the mirror",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream is unwell", http.StatusInternalServerError)
			},
		},
		{
			name: "HTML where a result list belongs",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><body>not what you asked for</body></html>"))
			},
		},
		{
			name: "a body that never ends",
			respond: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				for {
					select {
					case <-r.Context().Done():
						return
					default:
					}
					if _, err := w.Write([]byte("<div>padding</div>")); err != nil {
						return
					}
					if ok {
						flusher.Flush()
					}
					time.Sleep(10 * time.Millisecond)
				}
			},
		},
		{
			name: "a mirror that accepts and never answers",
			respond: func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			},
		},
		{
			name: "a redirect loop",
			respond: func(w http.ResponseWriter, r *http.Request) {
				// A deliberately hostile stand-in: redirecting to its own path is
				// the loop under test, and the "taint" is a request this test made.
				http.Redirect(w, r, r.URL.Path, http.StatusFound) //nolint:gosec // see above
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := startMirror(t, tc.respond)
			s := startServer(t, mirrorEnv(m))

			// s.try rather than s.do: do calls t.Fatalf, which calls
			// runtime.Goexit, which would kill this worker without failing the
			// test — and the select below would then wait out its full timeout
			// and report a hang that never happened.
			type outcome struct {
				reply response
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				reply, err := s.try(t, request{body: searchBody})
				done <- outcome{reply: reply, err: err}
			}()

			select {
			case got := <-done:
				if got.err != nil {
					// A transport error is an acceptable answer here — the
					// requirement is boundedness, not a particular status.
					t.Logf("call returned an error after %d mirror calls: %v", m.calls(), got.err)
					break
				}
				t.Logf("status %d after %d mirror calls", got.reply.status, m.calls())
			case <-time.After(90 * time.Second):
				t.Fatal("the tool call never returned; a bad mirror must not hold a request open indefinitely")
			}

			if !s.healthy(t) {
				t.Fatal("the server stopped serving after a misbehaving mirror")
			}
			// And it keeps serving other clients, not merely /health.
			if got := s.do(t, mcpPOST(nil)).status; got != http.StatusOK {
				t.Errorf("tools/list after the bad mirror = %d, want %d", got, http.StatusOK)
			}
		})
	}
}
