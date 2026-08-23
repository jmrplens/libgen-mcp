// Server card: the SEP-1649 document served at /.well-known/mcp/server-card.json.

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverCardPath is where reputation scanners and directories look for the card.
const serverCardPath = "/.well-known/mcp/server-card.json"

// serverCardTool is one tool as the card presents it: the same fields tools/list
// returns, so a reader gets the full surface without opening a session.
type serverCardTool struct {
	Name         string               `json:"name"`
	Title        string               `json:"title,omitempty"`
	Description  string               `json:"description,omitempty"`
	InputSchema  any                  `json:"inputSchema,omitempty"`
	OutputSchema any                  `json:"outputSchema,omitempty"`
	Annotations  *mcp.ToolAnnotations `json:"annotations,omitempty"`
	// Icons carries whatever the tool declares. Empty today — this server
	// registers none — and omitted when so, which keeps the document byte for
	// byte what it was while still mirroring the surface if that changes.
	Icons []mcp.Icon `json:"icons,omitempty"`
}

// serverCardPromptArgument is one argument of a prompt as the card presents it.
type serverCardPromptArgument struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// serverCardPrompt is one prompt as the card presents it. Prompts are the reason
// this endpoint earns its place here: they are what distinguishes this server
// from the other Library Genesis MCP servers, and nothing else advertises them
// without a client first connecting.
type serverCardPrompt struct {
	Name        string                     `json:"name"`
	Title       string                     `json:"title,omitempty"`
	Description string                     `json:"description,omitempty"`
	Arguments   []serverCardPromptArgument `json:"arguments,omitempty"`
	// Icons carries whatever the prompt declares; see serverCardTool.Icons.
	Icons []mcp.Icon `json:"icons,omitempty"`
}

// serverCard is the document itself, mirroring what initialize plus the list
// calls would return. Field names and shape follow the sibling
// gitlab-mcp-server so one scanner reads both.
type serverCard struct {
	// ServerInfo is taken verbatim from the handshake (see buildServerCard)
	// rather than restated here, so the card cannot advertise less than the
	// server does: it previously hardcoded name and version and silently
	// dropped Title, Description, WebsiteURL and Icons — exactly the fields a
	// registry listing renders.
	ServerInfo     *mcp.Implementation `json:"serverInfo"`
	Authentication struct {
		Required bool     `json:"required"`
		Schemes  []string `json:"schemes"`
	} `json:"authentication"`
	Tools             []serverCardTool   `json:"tools"`
	Prompts           []serverCardPrompt `json:"prompts"`
	Resources         []any              `json:"resources"`
	ResourceTemplates []any              `json:"resourceTemplates"`
}

// buildServerCard serializes the card once, by listing the live server's own
// tools and prompts over an in-memory session.
//
// It reads the registered server rather than standing up an ephemeral copy the
// way the sibling does: that copy exists there only because its server needs
// credentials to register, and this one needs none. Listing the real server is
// both simpler and impossible to drift from what a client actually sees.
//
// Resources and resource templates are always empty: this server registers
// none. The keys are still emitted, because the card's shape is the contract a
// scanner reads and an absent key is a different statement from an empty list.
func buildServerCard(ctx context.Context, server *mcp.Server) ([]byte, error) {
	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, fmt.Errorf("server connect: %w", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "server-card-builder", Version: "0"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("client connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	var card serverCard
	card.ServerInfo = session.InitializeResult().ServerInfo
	// Keyless by design: no account, no API key, nothing to present. Saying so
	// explicitly is more useful to a scanner than omitting the block.
	card.Authentication.Required = false
	card.Authentication.Schemes = []string{}
	// The paginated iterators rather than a single ListTools/ListPrompts call:
	// one call returns one page, so a surface that outgrew the page size would
	// publish a card silently missing its tail. Four tools and four prompts fit
	// today; the card should not depend on that staying true.
	card.Tools = []serverCardTool{}
	for t, tErr := range session.Tools(ctx, nil) {
		if tErr != nil {
			return nil, fmt.Errorf("list tools: %w", tErr)
		}
		card.Tools = append(card.Tools, serverCardTool{
			Name:         t.Name,
			Title:        t.Title,
			Description:  t.Description,
			InputSchema:  t.InputSchema,
			OutputSchema: t.OutputSchema,
			Annotations:  t.Annotations,
			Icons:        t.Icons,
		})
	}
	card.Prompts = []serverCardPrompt{}
	for p, pErr := range session.Prompts(ctx, nil) {
		if pErr != nil {
			return nil, fmt.Errorf("list prompts: %w", pErr)
		}
		args := make([]serverCardPromptArgument, 0, len(p.Arguments))
		for _, a := range p.Arguments {
			args = append(args, serverCardPromptArgument{
				Name:        a.Name,
				Title:       a.Title,
				Description: a.Description,
				Required:    a.Required,
			})
		}
		card.Prompts = append(card.Prompts, serverCardPrompt{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Arguments:   args,
			Icons:       p.Icons,
		})
	}
	card.Resources = []any{}
	card.ResourceTemplates = []any{}

	return json.Marshal(card)
}
