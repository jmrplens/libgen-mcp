//go:build eval

package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// newHostSession loads the configuration from the environment, builds a real
// libgen-mcp server with its three tools registered, and connects an in-memory
// MCP client to it over ctx. Tool calls on the returned session hit the real
// libgen mirrors and download sources. The cleanup closes the client and drains
// the server session. Construction is offline: config.Load and
// mirrors.NewManager do no network I/O.
func newHostSession(ctx context.Context) (session *mcp.ClientSession, cleanup func(), err error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create mirror manager: %w", err)
	}
	client := libgen.New(mgr, cfg)

	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-eval", Version: "0.0.1"}, nil)
	tools.Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("server connect: %w", err)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "libgen-eval-client", Version: "0.0.1"}, nil)
	session, err = mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		_ = serverSession.Close()
		_ = serverSession.Wait()
		return nil, nil, fmt.Errorf("client connect: %w", err)
	}

	return session, func() {
		_ = session.Close()
		_ = serverSession.Wait()
	}, nil
}

// toolDefs lists the server's tools over a real MCP tools/list round-trip and
// converts them to Messages API tool definitions. A nil input schema falls back
// to an empty object schema.
func toolDefs(ctx context.Context, session *mcp.ClientSession) ([]toolDef, error) {
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	defs := make([]toolDef, 0, len(result.Tools))
	for _, tool := range result.Tools {
		if tool == nil {
			continue
		}
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		defs = append(defs, toolDef{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	return defs, nil
}
