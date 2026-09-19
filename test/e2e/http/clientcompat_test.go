//go:build httpe2e

package httpe2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestClient_ProtocolVersionVariants covers what real clients actually send,
// including nothing at all.
//
// The SDK answers these before any handler under test runs, which is precisely
// why they belong here: a change to the chain in front could turn a supported
// client into a refused one without a single unit test noticing.
func TestClient_ProtocolVersionVariants(t *testing.T) {
	s := startServer(t, nil)

	cases := []struct {
		name    string
		version string
	}{
		{name: "current legacy-era revision", version: "2025-11-25"},
		{name: "an older revision", version: "2025-06-18"},
		{name: "the oldest supported", version: "2024-11-05"},
		{name: "no version header at all", version: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.version != "" {
				headers["MCP-Protocol-Version"] = tc.version
			}
			reply := s.do(t, request{body: toolsListBody, headers: headers})
			if reply.status != http.StatusOK {
				t.Errorf("status = %d, want %d (body: %s)", reply.status, http.StatusOK, truncate(reply.body))
			}
		})
	}
}

// TestClient_ModernEraNeedsPerRequestMeta pins the 2026-07-28 entry point,
// which is a different shape rather than a newer number: there is no
// initialize handshake, so every request carries its own protocol version and
// client capabilities in _meta.
//
// The negative half matters as much: asking for the modern revision without
// that _meta must be refused rather than quietly downgraded, or a client would
// believe it negotiated something it did not.
func TestClient_ModernEraNeedsPerRequestMeta(t *testing.T) {
	s := startServer(t, nil)

	withMeta := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientCapabilities":{},` +
		`"io.modelcontextprotocol/clientInfo":{"name":"httpe2e","version":"0"}}}}`
	// Mcp-Method as well as the version: the modern era names the method in a
	// header so a proxy can route without reading the body, and the SDK
	// refuses the request without it.
	ok := s.do(t, request{body: withMeta, headers: map[string]string{
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "tools/list",
	}})
	if ok.status != http.StatusOK {
		t.Errorf("modern-era call = %d, want %d (body: %s)", ok.status, http.StatusOK, truncate(ok.body))
	}

	bare := s.do(t, request{body: toolsListBody, headers: map[string]string{
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "tools/list",
	}})
	if !strings.Contains(bare.body, "_meta") {
		t.Errorf("a modern-era call without _meta was not refused for that reason: %s", truncate(bare.body))
	}
}

// TestClient_ModernEraIsRefusedStructurallyWhenStateful pins what a client is
// told when it asks for the modern revision from a --stateless=false listener,
// which cannot serve it: the 2026-07-28 era carries its session state per
// request, and a stateful server holds that state itself.
//
// The refusal is the SDK's, not this server's, and it is asserted here because
// it is a promise a client reads. It is also what the go-sdk v1.8.0 bump
// changed: v1.7.0 answered a bare `Bad Request: protocol version …` in
// text/plain, which a JSON-RPC client can only report as a transport failure.
// The structured form tells it exactly what to retry with — which versions this
// server does take, and which one it asked for — and the go-sdk client acts on
// that automatically.
//
// If this ever starts failing with no change of ours, the SDK moved: the plain
// form is still one GODEBUG away (plaintextstatefulrejection=1), and that is the
// first thing to check.
func TestClient_ModernEraIsRefusedStructurallyWhenStateful(t *testing.T) {
	s := startServer(t, nil, "--stateless=false")

	reply := s.do(t, request{body: toolsListBody, headers: map[string]string{
		"MCP-Protocol-Version": "2026-07-28",
	}})
	if reply.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", reply.status, http.StatusBadRequest, truncate(reply.body))
	}

	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *struct {
				Supported []string `json:"supported"`
				Requested string   `json:"requested"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(reply.body), &envelope); err != nil {
		t.Fatalf("the refusal is not JSON-RPC: %v\nbody: %s", err, truncate(reply.body))
	}
	if envelope.Error == nil {
		t.Fatalf("no error object in the refusal: %s", truncate(reply.body))
	}
	// -32022 is CodeUnsupportedProtocolVersion. It is written out rather than
	// imported so this asserts the number on the wire, which is what a client
	// that is not the go-sdk matches on.
	if envelope.Error.Code != -32022 {
		t.Errorf("code = %d, want -32022 (unsupported protocol version)", envelope.Error.Code)
	}
	if envelope.Error.Data == nil {
		t.Fatalf("no data payload, so a client is told what failed but not what to retry with: %s",
			truncate(reply.body))
	}
	if envelope.Error.Data.Requested != "2026-07-28" {
		t.Errorf("data.requested = %q, want the version that was asked for", envelope.Error.Data.Requested)
	}
	if len(envelope.Error.Data.Supported) == 0 {
		t.Fatal("data.supported is empty, so the refusal names nothing to fall back to")
	}
	for _, v := range envelope.Error.Data.Supported {
		if v >= "2026-07-28" {
			t.Errorf("data.supported offers %q, which is the era being refused", v)
		}
	}
}

// TestClient_UnrecognizedLegacyVersionIsRefusedAsJSONRPC is the branch the SDK
// still answers in plain text, and the one where an opaque body is worst.
//
// The specification's own backward-compatibility rule tells a client that a 400
// whose body is not a recognizable JSON-RPC error means an initialization-era
// server. It then downgrades to the withdrawn HTTP+SSE transport and issues a
// GET, which this stateless deployment answers 405 — so a typo in one header
// costs the client its transport instead of one retry.
//
// Asserted on the wire because that is the only place it is visible: the SDK
// produces the JSON-RPC form natively for revisions at or above 2026-07-28, so
// a test written against the modern era would pass without this guard existing.
func TestClient_UnrecognizedLegacyVersionIsRefusedAsJSONRPC(t *testing.T) {
	s := startServer(t, nil)

	reply := s.do(t, request{body: toolsListBody, headers: map[string]string{
		"MCP-Protocol-Version": "2024-01-01",
	}})
	if reply.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", reply.status, http.StatusBadRequest, truncate(reply.body))
	}
	if ct := reply.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json: a body a client cannot recognize is the whole failure", ct)
	}

	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *struct {
			Code int `json:"code"`
			Data *struct {
				Supported []string `json:"supported"`
				Requested string   `json:"requested"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(reply.body), &envelope); err != nil {
		t.Fatalf("the refusal is not JSON-RPC: %v\nbody: %s", err, truncate(reply.body))
	}
	if envelope.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", envelope.JSONRPC)
	}
	// toolsListBody carries id 1. Echoing it is what lets a client that routes
	// on id deliver the refusal to the call that caused it.
	if string(envelope.ID) != "1" {
		t.Errorf("id = %q, want the request's own id", envelope.ID)
	}
	if envelope.Error == nil || envelope.Error.Code != -32022 {
		t.Fatalf("error = %+v, want code -32022 (unsupported protocol version): %s", envelope.Error, truncate(reply.body))
	}
	if envelope.Error.Data == nil || len(envelope.Error.Data.Supported) == 0 {
		t.Fatalf("no supported list, so the client is told what failed but not what to retry with: %s", truncate(reply.body))
	}
	if envelope.Error.Data.Requested != "2024-01-01" {
		t.Errorf("data.requested = %q, want the version that was asked for", envelope.Error.Data.Requested)
	}
}

// TestClient_AcceptVariants covers the header clients get wrong most often.
func TestClient_AcceptVariants(t *testing.T) {
	s := startServer(t, nil)

	cases := []struct {
		name   string
		accept string
		want   int
	}{
		{name: "both types, as the spec asks", accept: "application/json, text/event-stream", want: http.StatusOK},
		{name: "reversed order", accept: "text/event-stream, application/json", want: http.StatusOK},
		{name: "with quality values", accept: "application/json;q=0.9, text/event-stream;q=1.0", want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := s.do(t, request{body: toolsListBody, headers: map[string]string{"Accept": tc.accept}})
			if reply.status != tc.want {
				t.Errorf("status = %d, want %d (body: %s)", reply.status, tc.want, truncate(reply.body))
			}
		})
	}

	// A client that sends no Accept at all: whatever the answer is, it must be
	// a decision rather than a crash, and the server must keep serving.
	reply := s.do(t, request{body: toolsListBody, headers: map[string]string{"Accept": ""}})
	t.Logf("no Accept header: status %d", reply.status)
	if !s.healthy(t) {
		t.Error("the server stopped serving after a request with no Accept header")
	}
}

// TestClient_DesktopOriginBehaviour records what happens to a non-http Origin,
// which is what an Electron-based desktop client sends.
//
// It is refused by default, and that is correct rather than unfortunate:
// nothing distinguishes "app://client" from a page claiming to be one, so the
// protection cannot safely make an exception. The escape hatch is naming it,
// like any other origin — and the test pins both halves so the default is a
// decision on record rather than an accident of the scheme check.
func TestClient_DesktopOriginBehaviour(t *testing.T) {
	const desktop = "app://libgen-desktop"

	t.Run("refused by default", func(t *testing.T) {
		s := startServer(t, nil)
		reply := s.do(t, mcpPOST(map[string]string{"Origin": desktop, "Sec-Fetch-Site": "cross-site"}))
		if reply.status != http.StatusForbidden {
			t.Errorf("desktop Origin = %d, want %d by default", reply.status, http.StatusForbidden)
		}
	})

	// And it cannot be named either, because the flag insists on an http or
	// https origin. That is a deliberate narrowing worth pinning: a desktop
	// client that wants in has to reach the server without an Origin header,
	// which every non-browser HTTP client does.
	t.Run("cannot be allowlisted", func(t *testing.T) {
		out, err := runServerExpectingExit(t, "--http", "127.0.0.1:"+itoa(freePort(t)), "--trusted-origins="+desktop)
		if err == nil {
			t.Fatalf("the server accepted --trusted-origins=%q; the flag documents http or https only. Output:\n%s", desktop, out)
		}
		// The refusal has to be about this flag. A non-nil error alone would
		// also be produced by a port collision or a bad download directory,
		// and the test would pass without exercising the validation at all.
		if !strings.Contains(out, "trusted origin") || !strings.Contains(out, desktop) {
			t.Errorf("the server refused for some other reason than the origin:\n%s", out)
		}
	})
}

// TestClient_ServerCardIsReadableWithoutASession covers the document registries
// and scanners fetch, which must work with a plain GET and no MCP handshake.
func TestClient_ServerCardIsReadableWithoutASession(t *testing.T) {
	s := startServer(t, nil)

	card := s.do(t, request{method: http.MethodGet, path: "/.well-known/mcp/server-card.json"})
	if card.status != http.StatusOK {
		t.Fatalf("card = %d, want %d", card.status, http.StatusOK)
	}
	for _, want := range []string{`"serverInfo"`, `"capabilities"`, `"tools"`, `"prompts"`} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(card.body, want) {
				t.Errorf("card does not carry %s", want)
			}
		})
	}
	// The deprecated capability must not reappear: it is advertised by the
	// SDK's default whenever ServerOptions.Capabilities is left nil, so this
	// asserts a pin that is one omission away from being lost.
	if strings.Contains(card.body, `"logging"`) {
		t.Error("the card advertises the deprecated logging capability")
	}
	if got := card.header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("card X-Content-Type-Options = %q, want nosniff", got)
	}
}

// TestClient_ResourceMethodsAreUnsupported pins the wire agreeing with the
// handshake: no resources capability is declared, so the methods answer
// method-not-found rather than an empty success that would invite a client to
// keep asking.
func TestClient_ResourceMethodsAreUnsupported(t *testing.T) {
	s := startServer(t, nil)

	for _, method := range []string{"resources/list", "resources/templates/list", "resources/read"} {
		t.Run(method, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"uri":"file:///x"}}`
			reply := s.do(t, request{body: body})
			if reply.status != http.StatusOK {
				t.Fatalf("transport status = %d, want %d: the rejection belongs in the JSON-RPC body", reply.status, http.StatusOK)
			}
			if !strings.Contains(reply.body, "-32601") {
				t.Errorf("body does not carry -32601 (method not found): %s", truncate(reply.body))
			}
		})
	}
}
