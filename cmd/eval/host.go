//go:build eval

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// newHostSession loads the configuration from the environment, builds a real
// libgen-mcp server with its tools registered, and connects an in-memory
// MCP client to it over ctx. Tool calls on the returned session hit the real
// libgen mirrors and download sources. The cleanup closes the client and drains
// the server session. Construction is offline: config.Load and
// mirrors.NewManager do no network I/O.
func newHostSession(ctx context.Context, remote bool) (session *mcp.ClientSession, progress *progressCapture, cleanup func(), err error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load config: %w", err)
	}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create mirror manager: %w", err)
	}
	client := libgen.New(mgr, cfg)

	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-eval", Version: "0.0.1"}, nil)
	// Remote block: register the download tool in remote mode (returns a link
	// instead of writing to disk), matching a hosted HTTP deployment.
	var regOpts []tools.RegisterOption
	if remote {
		regOpts = append(regOpts, tools.WithRemoteDownloads())
	}
	tools.Register(server, client, cfg, regOpts...)

	st, ct := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("server connect: %w", err)
	}

	// Register a progress handler so scenarios can assert that download progress
	// notifications actually reach the client end to end (see progress token in
	// executeTool).
	progress = &progressCapture{}
	// Advertise the elicitation capability so scenarios that hit an on-demand
	// prompt (Unpaywall email for a DOI download, or the download-save
	// confirmation) can exercise the real elicitation surface end to end. The
	// handler answers deterministically, so it never blocks a scenario: it supplies
	// the contact email for an "email" prompt and accepts any confirmation prompt.
	// Existing scenarios are unaffected — the server only elicits when it actually
	// needs one of these (a DOI download with no configured email, or a disk-writing
	// download), and in both cases the handler's answer lets the flow proceed exactly
	// as before.
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "libgen-eval-client", Version: "0.0.1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			progress.add(r.Params)
		},
		ElicitationHandler: evalElicitationHandler,
	})
	session, err = mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		_ = serverSession.Close()
		_ = serverSession.Wait()
		return nil, nil, nil, fmt.Errorf("client connect: %w", err)
	}

	return session, progress, func() {
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

// evalElicitationHandler answers the two elicitation prompts the server can raise
// during a scenario. It branches on the single top-level field of the requested
// schema: an "email" field (the on-demand Unpaywall contact email) is answered
// with unpaywallEmail(); any other prompt is the download-save confirmation, which
// it accepts (confirm=true) so a real download flow proceeds instead of stalling.
// It never declines: the eval measures whether the model reaches the capability,
// not whether a human would approve, so a deterministic accept keeps the flow live.
func evalElicitationHandler(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	field := evalElicitFieldName(req)
	if strings.Contains(strings.ToLower(field), "email") {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{field: unpaywallEmail()}}, nil
	}
	if field == "" {
		field = "confirm"
	}
	return &mcp.ElicitResult{Action: "accept", Content: map[string]any{field: true}}, nil
}

// evalElicitFieldName returns the single top-level property name of an
// elicitation's requested schema — "email" for the Unpaywall-email prompt and
// "confirm" for the download-save prompt. Client-side the schema arrives as a
// map[string]any per the SDK's default JSON unmarshaling. It returns "" when the
// schema is not the expected {"properties": {name: ...}} shape.
func evalElicitFieldName(req *mcp.ElicitRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	schema, ok := req.Params.RequestedSchema.(map[string]any)
	if !ok {
		return ""
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return ""
	}
	for name := range props {
		return name // each server elicitation carries exactly one property.
	}
	return ""
}
