// Server card: the SEP-1649 document served at /.well-known/mcp/server-card.json.

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
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
}

// serverCard is the document itself, mirroring what initialize plus the list
// calls would return. Field names and shape follow the sibling
// gitlab-mcp-server so one scanner reads both.
type serverCard struct {
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
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

	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	promptsResult, err := session.ListPrompts(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list prompts: %w", err)
	}

	var card serverCard
	card.ServerInfo.Name = "libgen-mcp"
	card.ServerInfo.Version = buildversion.Current()
	// Keyless by design: no account, no API key, nothing to present. Saying so
	// explicitly is more useful to a scanner than omitting the block.
	card.Authentication.Required = false
	card.Authentication.Schemes = []string{}
	card.Tools = make([]serverCardTool, 0, len(toolsResult.Tools))
	for _, t := range toolsResult.Tools {
		card.Tools = append(card.Tools, serverCardTool{
			Name:         t.Name,
			Title:        t.Title,
			Description:  t.Description,
			InputSchema:  t.InputSchema,
			OutputSchema: t.OutputSchema,
			Annotations:  t.Annotations,
		})
	}
	card.Prompts = make([]serverCardPrompt, 0, len(promptsResult.Prompts))
	for _, p := range promptsResult.Prompts {
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
		})
	}
	card.Resources = []any{}
	card.ResourceTemplates = []any{}

	return json.Marshal(card)
}
