package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/toolutil"
	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
)

// newCardTestServer registers one tool and one prompt, which is all the card
// builder reads — enough to prove it walks both lists and copies their fields.
func newCardTestServer() *mcp.Server {
	type stubIn struct{}
	type stubOut struct{}
	srv := newMCPServer()
	mcp.AddTool(srv, &mcp.Tool{Name: "search", Title: "Search", Description: "stub tool"},
		func(context.Context, *mcp.CallToolRequest, stubIn) (*mcp.CallToolResult, stubOut, error) {
			return nil, stubOut{}, nil
		})
	srv.AddPrompt(&mcp.Prompt{
		Name:        "acquire_book",
		Title:       "Acquire Book",
		Description: "stub prompt",
		Arguments: []*mcp.PromptArgument{
			{Name: "title", Title: "Title", Description: "what to look for", Required: true},
		},
	}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{}, nil
	})
	return srv
}

// buildTestCard builds and parses the card for the stub surface, failing the
// test if either step does. Each assertion below gets its own top-level test so
// a failure names the part of the contract that broke.
func buildTestCard(t *testing.T) serverCard {
	t.Helper()
	raw, err := buildServerCard(t.Context(), newCardTestServer())
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	var card serverCard
	if uErr := json.Unmarshal(raw, &card); uErr != nil {
		t.Fatalf("card is not valid JSON: %v", uErr)
	}
	return card
}

// handshakeCapabilities returns what a client actually negotiates with the stub
// surface, read over its own in-memory session. The capabilities assertion is
// then against the handshake itself rather than against a second hand-written
// expectation that would have to be edited every time the surface grows.
func handshakeCapabilities(t *testing.T) *mcp.ServerCapabilities {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := newCardTestServer().Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "capabilities-probe", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	return session.InitializeResult().Capabilities
}

// TestServerCardIdentity checks the card names this server and the build serving it.
func TestServerCardIdentity(t *testing.T) {
	card := buildTestCard(t)
	if card.ServerInfo.Name != "libgen-mcp" {
		t.Errorf("serverInfo.name = %q, want libgen-mcp", card.ServerInfo.Name)
	}
	if card.ServerInfo.Version != buildversion.Current() || card.ServerInfo.Version == "" {
		t.Errorf("serverInfo.version = %q, want %q", card.ServerInfo.Version, buildversion.Current())
	}
}

// TestServerCardIdentityCarriesDisplayMetadata pins ServerInfo to the live
// handshake rather than a restated {name, version}: it previously hardcoded
// just those two fields and silently dropped Title, Description and
// WebsiteURL — exactly what a registry listing renders.
func TestServerCardIdentityCarriesDisplayMetadata(t *testing.T) {
	card := buildTestCard(t)
	if card.ServerInfo.Title != implementationTitle {
		t.Errorf("serverInfo.title = %q, want %q", card.ServerInfo.Title, implementationTitle)
	}
	if card.ServerInfo.Description != implementationDescription {
		t.Errorf("serverInfo.description = %q, want %q", card.ServerInfo.Description, implementationDescription)
	}
	if card.ServerInfo.WebsiteURL != implementationWebsiteURL {
		t.Errorf("serverInfo.websiteUrl = %q, want %q", card.ServerInfo.WebsiteURL, implementationWebsiteURL)
	}
	if len(card.ServerInfo.Icons) != len(toolutil.IconBrand) {
		t.Errorf("serverInfo.icons = %+v, want the %d-entry brand icon", card.ServerInfo.Icons, len(toolutil.IconBrand))
	}
}

// TestServerCardDeclaresNoAuthentication pins the keyless guarantee. It is a
// product promise, so the card states it rather than leaving the block out and
// letting a reader assume.
func TestServerCardDeclaresNoAuthentication(t *testing.T) {
	card := buildTestCard(t)
	if card.Authentication.Required {
		t.Error("authentication.required = true, want false: this server takes no credentials")
	}
	if card.Authentication.Schemes == nil {
		t.Error("authentication.schemes is null, want an empty list")
	}
}

// TestServerCardCarriesHandshakeCapabilities pins the capabilities block to the
// live handshake rather than to a restatement of it. It is the one part of the
// card a reader cannot reconstruct from the tools and prompts listings: what
// the server negotiates is a separate statement from what it registers, and
// without this key a directory would have to grep English prose for it.
func TestServerCardCarriesHandshakeCapabilities(t *testing.T) {
	card := buildTestCard(t)
	if card.Capabilities == nil {
		t.Fatal("capabilities is absent; a reader learns nothing about what this server negotiates")
	}
	want, err := json.Marshal(handshakeCapabilities(t))
	if err != nil {
		t.Fatalf("marshal handshake capabilities: %v", err)
	}
	got, err := json.Marshal(card.Capabilities)
	if err != nil {
		t.Fatalf("marshal card capabilities: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("capabilities = %s, want the handshake's %s", got, want)
	}
	if card.Capabilities.Tools == nil || card.Capabilities.Prompts == nil {
		t.Errorf("capabilities = %s, want the tools and prompts this surface registers", got)
	}
}

// TestServerCardOmitsDeprecatedLoggingCapability guards the Capabilities pin in
// newMCPServer from the card's side. Left nil, ServerOptions fills that field
// with the SDK's default of {"logging":{}} — a capability deprecated in
// revision 2026-07-28 (SEP-2577) that this server neither implements nor wants
// — and the card would then publish it to every scanner that reads it.
//
// The assertion is on the raw JSON rather than the decoded struct because the
// promise is about the key a scanner sees, and a typed nil pointer decodes the
// same way whether the key was absent or explicitly null.
func TestServerCardOmitsDeprecatedLoggingCapability(t *testing.T) {
	raw, err := buildServerCard(t.Context(), newCardTestServer())
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	var doc map[string]json.RawMessage
	if uErr := json.Unmarshal(raw, &doc); uErr != nil {
		t.Fatalf("card is not valid JSON: %v", uErr)
	}
	rawCaps, ok := doc["capabilities"]
	if !ok {
		t.Fatal("card carries no capabilities key")
	}
	var caps map[string]json.RawMessage
	if uErr := json.Unmarshal(rawCaps, &caps); uErr != nil {
		t.Fatalf("capabilities is not an object: %v", uErr)
	}
	if _, present := caps["logging"]; present {
		t.Errorf("capabilities = %s, want no logging key: SEP-2577 deprecated it and this server logs to stderr", rawCaps)
	}
	for _, key := range []string{"tools", "prompts"} {
		if _, present := caps[key]; !present {
			t.Errorf("capabilities = %s, want the %s key this surface serves", rawCaps, key)
		}
	}
}

// TestServerCardMirrorsTools checks the tool listing carries the metadata a
// reader needs to understand the surface without connecting.
func TestServerCardMirrorsTools(t *testing.T) {
	card := buildTestCard(t)
	if len(card.Tools) != 1 || card.Tools[0].Name != "search" {
		t.Fatalf("tools = %+v, want exactly the registered search tool", card.Tools)
	}
	got := card.Tools[0]
	if got.Title != "Search" || got.Description != "stub tool" {
		t.Errorf("tool metadata not carried over: %+v", got)
	}
	if got.InputSchema == nil {
		t.Error("tool inputSchema is absent; a reader cannot tell how to call it")
	}
}

// TestServerCardMirrorsPrompts carries the weight of this endpoint. Prompts are
// the reason this server publishes a card at all: nothing else exposes them to a
// reader who never opens a session, and the arguments are half their value.
func TestServerCardMirrorsPrompts(t *testing.T) {
	card := buildTestCard(t)
	if len(card.Prompts) != 1 || card.Prompts[0].Name != "acquire_book" {
		t.Fatalf("prompts = %+v, want exactly the registered prompt", card.Prompts)
	}
	got := card.Prompts[0]
	if got.Title != "Acquire Book" || got.Description != "stub prompt" {
		t.Errorf("prompt metadata not carried over: %+v", got)
	}
	if len(got.Arguments) != 1 {
		t.Fatalf("prompt arguments = %+v, want the one registered", got.Arguments)
	}
	if arg := got.Arguments[0]; arg.Name != "title" || !arg.Required || arg.Description == "" {
		t.Errorf("prompt argument not carried over: %+v", arg)
	}
}

// TestServerCardEmitsEmptyResourceLists checks both keys are present and empty:
// an absent key says something different to a scanner than one holding nothing,
// and this server registers neither resources nor templates.
func TestServerCardEmitsEmptyResourceLists(t *testing.T) {
	card := buildTestCard(t)
	if card.Resources == nil || card.ResourceTemplates == nil {
		t.Fatalf("must be present, got %v / %v", card.Resources, card.ResourceTemplates)
	}
	if len(card.Resources) != 0 || len(card.ResourceTemplates) != 0 {
		t.Errorf("this server registers neither, got %v / %v", card.Resources, card.ResourceTemplates)
	}
}

// TestServerCardRouteServesTheDocument covers the wiring: the path, the content
// type and the cache header a scanner reads.
func TestServerCardRouteServesTheDocument(t *testing.T) {
	raw, err := buildServerCard(t.Context(), newCardTestServer())
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := newHTTPHandler(stub, raw, nil)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, serverCardPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=") {
		t.Errorf("Cache-Control = %q, want a max-age a scanner can honor", cc)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Error("served body is not valid JSON")
	}
}

// TestServerCardRouteAllowsCrossOriginReads covers the CORS wiring. The card's
// audience is browser-based registries and scanners, and a browser drops a
// cross-origin response that carries no Access-Control-Allow-Origin however
// public the document is — so a card without it is readable by curl and by
// nothing that would actually list this server. The preflight is asserted too,
// because a scanner that sets any header of its own sends OPTIONS first.
func TestServerCardRouteAllowsCrossOriginReads(t *testing.T) {
	raw, err := buildServerCard(t.Context(), newCardTestServer())
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := newHTTPHandler(stub, raw, nil)

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, serverCardPath, nil))
	if origin := getRec.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("GET Access-Control-Allow-Origin = %q, want *", origin)
	}

	preflightRec := httptest.NewRecorder()
	preflight := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, serverCardPath, nil)
	// The realistic preflight: a browser only sends one when the fetch carries
	// a header of its own, and it names that header here. Without the echo
	// below the browser refuses the request it was asking permission for.
	preflight.Header.Set("Origin", "https://example.invalid")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	preflight.Header.Set("Access-Control-Request-Headers", "x-scanner-id")
	handler.ServeHTTP(preflightRec, preflight)
	if preflightRec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want %d", preflightRec.Code, http.StatusNoContent)
	}
	if origin := preflightRec.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("OPTIONS Access-Control-Allow-Origin = %q, want *", origin)
	}
	if methods := preflightRec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, http.MethodGet) {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to name GET", methods)
	}
	if allowed := preflightRec.Header().Get("Access-Control-Allow-Headers"); allowed != "x-scanner-id" {
		t.Errorf("Access-Control-Allow-Headers = %q, want the requested %q echoed back", allowed, "x-scanner-id")
	}
	// The answer is derived from the requested header list, so it varies by it.
	if vary := preflightRec.Header().Get("Vary"); !strings.Contains(vary, "Access-Control-Request-Headers") {
		t.Errorf("Vary = %q, want it to name Access-Control-Request-Headers", vary)
	}
}

// TestServerCardRouteAbsentWhenUnbuilt pins the failure path: a card that could
// not be built must leave the route unmounted so the request falls through to
// the MCP handler, rather than the server refusing to start or answering 200
// with nothing in it. The CORS preflight is mounted with the card and must
// disappear with it too: an OPTIONS route left behind would answer 204 for a
// document that is not being served.
func TestServerCardRouteAbsentWhenUnbuilt(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := newHTTPHandler(stub, nil, nil)

	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		req := httptest.NewRequestWithContext(t.Context(), method, serverCardPath, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusTeapot {
			t.Errorf("%s status = %d, want the MCP handler to have seen it (%d)", method, rec.Code, http.StatusTeapot)
		}
	}
}

// TestServerCardListsBeyondOnePage guards the reason the builder uses the SDK's
// paginated iterators rather than a single ListTools call: one call returns one
// page, so a surface that outgrew the page size would publish a card silently
// missing its tail.
//
// The server is built with a deliberately tiny PageSize. The SDK's default is
// 1000, so a test that merely registered a lot of tools would pass against the
// single-call code too and prove nothing; forcing several pages is what makes
// this able to fail.
func TestServerCardListsBeyondOnePage(t *testing.T) {
	type stubIn struct{}
	type stubOut struct{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp", Version: "test"},
		&mcp.ServerOptions{PageSize: 2})
	const want = 7
	for i := range want {
		mcp.AddTool(srv, &mcp.Tool{Name: fmt.Sprintf("tool_%d", i), Description: "stub"},
			func(context.Context, *mcp.CallToolRequest, stubIn) (*mcp.CallToolResult, stubOut, error) {
				return nil, stubOut{}, nil
			})
	}

	raw, err := buildServerCard(t.Context(), srv)
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	var card serverCard
	if uErr := json.Unmarshal(raw, &card); uErr != nil {
		t.Fatalf("card is not valid JSON: %v", uErr)
	}
	if len(card.Tools) != want {
		t.Errorf("card lists %d tools across %d-item pages, want all %d",
			len(card.Tools), 2, want)
	}
}

// TestServerCardCarriesIcons pins the icons pass-through on both primitives,
// using fixture icons distinct from the real toolutil ones so this test does
// not accidentally pass just because the real tools happen to carry icons now.
func TestServerCardCarriesIcons(t *testing.T) {
	type stubIn struct{}
	type stubOut struct{}
	toolIcon := mcp.Icon{Source: "https://example.invalid/tool.svg", MIMEType: "image/svg+xml", Sizes: []string{"any"}}
	promptIcon := mcp.Icon{Source: "https://example.invalid/prompt.png", MIMEType: "image/png"}

	srv := newMCPServer()
	mcp.AddTool(srv, &mcp.Tool{Name: "search", Description: "stub", Icons: []mcp.Icon{toolIcon}},
		func(context.Context, *mcp.CallToolRequest, stubIn) (*mcp.CallToolResult, stubOut, error) {
			return nil, stubOut{}, nil
		})
	srv.AddPrompt(&mcp.Prompt{Name: "acquire_book", Description: "stub", Icons: []mcp.Icon{promptIcon}},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})

	raw, err := buildServerCard(t.Context(), srv)
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	var card serverCard
	if uErr := json.Unmarshal(raw, &card); uErr != nil {
		t.Fatalf("card is not valid JSON: %v", uErr)
	}

	if len(card.Tools) != 1 || len(card.Tools[0].Icons) != 1 {
		t.Fatalf("tool icons = %+v, want the one declared", card.Tools)
	}
	if got := card.Tools[0].Icons[0]; got.Source != toolIcon.Source || got.MIMEType != toolIcon.MIMEType {
		t.Errorf("tool icon = %+v, want %+v", got, toolIcon)
	}
	if len(card.Prompts) != 1 || len(card.Prompts[0].Icons) != 1 {
		t.Fatalf("prompt icons = %+v, want the one declared", card.Prompts)
	}
	if got := card.Prompts[0].Icons[0]; got.Source != promptIcon.Source {
		t.Errorf("prompt icon = %+v, want %+v", got, promptIcon)
	}

	// A tool or prompt that declares no icon of its own must carry an absent
	// key, not a null one. The Implementation itself is a different story:
	// newMCPServer always sets IconBrand, so serverInfo carries icons
	// regardless (see TestServerCardIdentityCarriesDisplayMetadata).
	plainRaw, err := buildServerCard(t.Context(), newCardTestServer())
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	var plain serverCard
	if uErr := json.Unmarshal(plainRaw, &plain); uErr != nil {
		t.Fatalf("card is not valid JSON: %v", uErr)
	}
	if len(plain.Tools) != 1 || plain.Tools[0].Icons != nil {
		t.Errorf("tool icons = %+v, want none declared", plain.Tools)
	}
	if len(plain.Prompts) != 1 || plain.Prompts[0].Icons != nil {
		t.Errorf("prompt icons = %+v, want none declared", plain.Prompts)
	}
}
