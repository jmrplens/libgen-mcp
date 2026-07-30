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
//   - every enum constrains its property to a non-empty set of values, with no
//     blank or repeated one;
//   - every value an enum accepts is named in the description beside it, so the
//     prose cannot go stale against the values the schema pins;
//   - no description outgrows its budget, since a tool definition is re-sent on
//     every request (cmd/audit_tokens prices the whole surface).
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
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/cmd/internal/mcpsurface"
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
//
// The configuration comes from mcpsurface.DocsConfig rather than a second copy
// of the same placeholders. This audit used to keep its own, and it had drifted:
// it filled in a contact email for unpaywall but no API key for core, so the
// surface it graded carried core's enum value on a maintainer's machine and not
// on CI. Any rule that reads the source enum would then have held on one machine
// and not the other, which is the opposite of what a gate is for.
func listTools() ([]*mcp.Tool, error) {
	return mcpsurface.Tools(mcpsurface.DocsConfig())
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
