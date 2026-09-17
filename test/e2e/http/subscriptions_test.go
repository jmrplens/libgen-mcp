//go:build httpe2e

package httpe2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// listenNotifications is what a client asks for after reading listChanged: true
// out of the handshake.
//
// The field names are the SDK's Go-side JSON tags — toolsListChanged, not
// tools/list_changed. That distinction is the whole test: the wrong spelling
// decodes to an all-false subscription set, the server agrees to nothing, the
// handler returns at once, and the case passes against a server with the defect
// as readily as against one without it.
const listenNotifications = `"notifications":{"toolsListChanged":true,"promptsListChanged":true}`

// legacyListenBody is the request a pre-2026-07-28 client, a scanner or a
// curl-by-hand sends: no _meta, and no protocol header on the request either.
const legacyListenBody = `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{` +
	listenNotifications + `}}`

// modernListenBody is the same call under 2026-07-28, which carries the session
// state in _meta because a stateless POST has nowhere else to put it. All three
// members are required: the SDK rejects the request naming each missing one in
// turn, and a request rejected at the envelope never reaches the handler this
// test is about.
const modernListenBody = `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"_meta":{` +
	`"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientCapabilities":{},` +
	`"io.modelcontextprotocol/clientInfo":{"name":"httpe2e","version":"0"}},` +
	listenNotifications + `}}`

// listenBound is how long a subscriptions/listen POST is given to answer. The
// call is a handful of in-process map lookups, so a second would do; the defect
// makes it never answer at all, so the figure only decides how long a broken
// build takes to say so.
const listenBound = 5 * time.Second

// postListenWithin sends body and returns the reply, bounding the call so a
// parked handler fails the test instead of hanging the suite.
//
// It builds the request itself rather than going through (*server).do because
// one case turns on a header do always sets: "no MCP-Protocol-Version at all"
// cannot be expressed by overriding it, only by never adding it.
func postListenWithin(t *testing.T, s *server, body string, headers map[string]string) (int, string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), listenBound)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", acceptHeader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, derr := s.httpClient().Do(req)
	if derr != nil {
		return 0, "", derr
	}
	defer resp.Body.Close()

	// The body read is where the deadline actually bites, and reporting it is the
	// difference between a useful failure and a confusing one. A parked listen
	// still answers with headers at once — the transport opens the SSE stream and
	// writes the acknowledgement — and then holds the stream open forever, so what
	// never arrives is the result frame at the end of it.
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return resp.StatusCode, string(raw), rerr
	}
	return resp.StatusCode, string(raw), nil
}

// assertListenAnswered fails the test unless the call came back, answered the
// request that was sent, and was answered rather than refused.
func assertListenAnswered(t *testing.T, status int, body string, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("subscriptions/listen did not answer within %s: %v\n"+
			"the handler is parked: this server advertises listChanged for a notification it "+
			"never sends, so the SDK agrees to the subscription and waits on a context nothing "+
			"here will ever cancel", listenBound, err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	if !strings.Contains(body, `"id":1`) {
		t.Errorf("nothing in the reply answers the request that was sent: %s", body)
	}
	if strings.Contains(body, `"error"`) {
		t.Errorf("subscriptions/listen was refused rather than answered: %s", body)
	}
}

// TestSubscriptionsListenDoesNotPark is the wire half of the capability pin, and
// it is on the wire because the handler is correct in isolation and wrong only
// because of what the handshake says.
//
// The SDK parks a subscriptions/listen handler on <-ctx.Done() whenever the
// agreed subscriptions are non-empty, and it agrees to whatever the capabilities
// advertise. Left to itself the SDK fills in listChanged: true for any server
// that registers tools and prompts, so the POST never returns — and this server
// has no way to end it: it sends no list-changed notification, by the same
// argument the pin rests on, and configures no KeepAlive, so there is never a
// write to a dead stream that would unwind it either.
//
// All three cases park against a server with the defect, and each reaches the
// handler by a different route, which is why none of them stands in for the
// others:
//
//   - no protocol header is what a scanner, a curl-by-hand and every
//     pre-2026-07-28 client send, and it is the worst of the three. The SDK ties
//     the handler's lifetime to the POST only from 2026-07-28 onwards, so here
//     the client hanging up never reaches the parked handler and the goroutine,
//     the session and the map entry are held for the life of the process, per
//     request rather than per connection;
//   - 2026-07-28 is the case a current client reaches, where the cost is at
//     least bounded by how many connections the caller holds open;
//   - an established stateful session is the one --stateless=false reaches. A
//     bare POST there is refused as invalid during session initialization, so
//     the session has to be real for the call to arrive at all.
//
// None of them can observe the leaked goroutine from outside the process, so what
// is asserted is the observable half: the request comes back.
func TestSubscriptionsListenDoesNotPark(t *testing.T) {
	t.Run("stateless, no protocol header", func(t *testing.T) {
		s := startServer(t, nil)
		status, body, err := postListenWithin(t, s, legacyListenBody, nil)
		assertListenAnswered(t, status, body, err)
	})

	t.Run("stateless, 2026-07-28", func(t *testing.T) {
		s := startServer(t, nil)
		status, body, err := postListenWithin(t, s, modernListenBody, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			// 2026-07-28 requires the method to be named in a header as well as
			// in the body, so the transport can route without parsing.
			"Mcp-Method": "subscriptions/listen",
		})
		assertListenAnswered(t, status, body, err)
	})

	t.Run("stateful, established session", func(t *testing.T) {
		s := startServer(t, nil, "--stateless=false")
		session := establishSession(t, s)
		status, body, err := postListenWithin(t, s, legacyListenBody, map[string]string{
			"Mcp-Session-Id":       session,
			"MCP-Protocol-Version": protocolVersion,
		})
		assertListenAnswered(t, status, body, err)
	})
}

// establishSession runs the handshake against a --stateless=false server and
// returns the session id every later request must carry.
func establishSession(t *testing.T, s *server) string {
	t.Helper()

	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + protocolVersion +
		`","capabilities":{},"clientInfo":{"name":"httpe2e","version":"0"}}}`
	reply := s.do(t, request{body: init})
	if reply.status != http.StatusOK {
		t.Fatalf("initialize = %d, want 200; body: %s", reply.status, reply.body)
	}
	session := reply.header.Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("no Mcp-Session-Id with --stateless=false; the handshake did not establish a session")
	}

	s.do(t, request{
		body:    `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		headers: map[string]string{"Mcp-Session-Id": session},
	})
	return session
}

// TestHandshakeAdvertisesNoListChanged is the same promise read off the wire the
// way a client reads it: the initialize result must not offer a notification this
// server cannot send.
//
// It is the companion to the unit test in cmd/server. That one asserts what the
// server object was built with; this one asserts what a client is actually told
// over HTTP, which is what decides whether it opens the stream above at all.
func TestHandshakeAdvertisesNoListChanged(t *testing.T) {
	s := startServer(t, nil)

	res := s.do(t, request{
		body: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + protocolVersion +
			`","capabilities":{},"clientInfo":{"name":"httpe2e","version":"0"}}}`,
	})
	if res.status != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200; body: %s", res.status, res.body)
	}

	var envelope struct {
		Result struct {
			Capabilities struct {
				Tools *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
				Prompts *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"prompts"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(jsonFromSSE(res.body)), &envelope); err != nil {
		t.Fatalf("decoding the initialize result: %v\nbody: %s", err, res.body)
	}

	caps := envelope.Result.Capabilities
	if caps.Tools == nil {
		t.Fatal("no tools capability in the handshake, but this server registers four tools")
	}
	if caps.Tools.ListChanged {
		t.Error("the handshake offers tools.listChanged, which this server never sends")
	}
	if caps.Prompts == nil {
		t.Fatal("no prompts capability in the handshake, but this server registers four prompts")
	}
	if caps.Prompts.ListChanged {
		t.Error("the handshake offers prompts.listChanged, which this server never sends")
	}
}

// jsonFromSSE returns the JSON payload of body, which the streamable transport
// delivers as an SSE "data:" event unless the request asked for plain JSON.
func jsonFromSSE(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			return strings.TrimSpace(after)
		}
	}
	return body
}
