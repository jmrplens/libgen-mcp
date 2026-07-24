// Command audit_surface_quality audits the quality of libgen-mcp's MCP tool
// surface: the shape LLM clients actually see over a tools/list round-trip.
//
// It builds an in-memory MCP server with the full tool surface enabled,
// lists the tools, and enforces the repo's surface conventions:
//
//   - every tool has a Title, non-nil Annotations, and a description of at
//     least [minDescLen] characters;
//   - every tool's InputSchema is a JSON Schema object;
//   - every named input and output field carries a non-empty jsonschema
//     description (so the model is never handed an unlabeled parameter);
//   - every enum constrains its property to a non-empty set of values.
//
// The audit exits non-zero when it finds violations, so it works as a CI gate
// (see the audit-surface-quality Make target). Pass -json to emit the findings
// as a structured document instead of the Markdown report.
//
// Usage:
//
//	go run ./cmd/audit_surface_quality/            # Markdown report, gate mode
//	go run ./cmd/audit_surface_quality/ -json      # JSON findings
//	go run ./cmd/audit_surface_quality/ -no-fail   # report only, always exit 0
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

func main() {
	outputJSON := flag.Bool("json", false, "emit JSON instead of the Markdown report")
	noFail := flag.Bool("no-fail", false, "always exit 0, even when violations are found (report only)")
	flag.Parse()

	toolList, err := listTools()
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit_surface_quality:", err)
		os.Exit(1)
	}

	violations := auditTools(toolList)
	if writeErr := writeReport(os.Stdout, toolList, violations, *outputJSON); writeErr != nil {
		fmt.Fprintln(os.Stderr, "audit_surface_quality:", writeErr)
		os.Exit(1)
	}

	if len(violations) > 0 && !*noFail {
		os.Exit(1)
	}
}

// listTools builds the in-memory MCP server with the full tool surface enabled
// and returns the tools as a client sees them over a tools/list round-trip.
//
// Construction is offline: config.Load and mirrors.NewManager do no network
// I/O, and no tool is ever called. The full surface is forced (all download
// sources enabled) so the audit is deterministic regardless of the ambient
// environment.
func listTools() ([]*mcp.Tool, error) {
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{}
	}
	// Enable every source so the download tool's source enum and prose reach
	// their full shape; a disabled source would shrink the surface we audit.
	cfg.Sources = nil
	if cfg.UnpaywallEmail == "" {
		cfg.UnpaywallEmail = "audit@example.com"
	}

	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, fmt.Errorf("create mirror manager: %w", err)
	}
	client := libgen.New(mgr, cfg)

	server := mcp.NewServer(&mcp.Implementation{Name: "audit-surface-quality", Version: "0.0.1"}, nil)
	tools.Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, fmt.Errorf("server connect: %w", err)
	}
	defer func() { _ = serverSession.Wait() }()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "audit-surface-quality-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("client connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return result.Tools, nil
}

// writeReport renders the audit findings to w, either as JSON (json=true) or as
// the human-readable Markdown report. It returns an error only when JSON
// encoding fails.
func writeReport(w io.Writer, toolList []*mcp.Tool, violations []violation, json bool) error {
	if json {
		return writeJSONReport(w, toolList, violations)
	}
	writeMarkdownReport(w, toolList, violations)
	return nil
}
