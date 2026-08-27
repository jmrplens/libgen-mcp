//go:build httpe2e

package httpe2e

import (
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
		if !strings.Contains(card.body, want) {
			t.Errorf("card does not carry %s", want)
		}
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
