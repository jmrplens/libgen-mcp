package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/v2/cmd/internal/mcpsurface"
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
