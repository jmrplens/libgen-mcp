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
	handler := newHTTPHandler(stub, raw)

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

// TestServerCardRouteAbsentWhenUnbuilt pins the failure path: a card that could
// not be built must leave the route unmounted so the request falls through to
// the MCP handler, rather than the server refusing to start or answering 200
// with nothing in it.
func TestServerCardRouteAbsentWhenUnbuilt(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := newHTTPHandler(stub, nil)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, serverCardPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want the MCP handler to have seen it (%d)", rec.Code, http.StatusTeapot)
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

// TestServerCardCarriesIcons pins the icons pass-through on both primitives.
// This server registers none today, so without a fixture that declares some the
// field would sit in the card types untested and could stop being copied without
// anything noticing.
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

	// The key must be absent, not null, when nothing declares an icon — that is
	// what keeps the published document unchanged while this server has none.
	plain, err := buildServerCard(t.Context(), newCardTestServer())
	if err != nil {
		t.Fatalf("buildServerCard() error = %v", err)
	}
	if strings.Contains(string(plain), `"icons"`) {
		t.Error("a surface with no icons emitted an icons key")
	}
}
