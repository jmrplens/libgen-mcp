// Command audit_tokens measures the LLM context-window footprint of libgen-mcp's
// MCP tool definitions. It builds an in-memory MCP server, lists the tools over a
// real tools/list round-trip, serializes each tool definition to JSON, and counts
// tokens with the cl100k_base tokenizer (see countTokens), falling back to a
// bytes/4 heuristic. This is the fixed context cost every request pays for having
// libgen-mcp loaded — useful for judging how "cheap" the server is to keep on.
//
// The full tool surface is measured (all download sources enabled), so the number
// is deterministic and represents the upper bound.
//
// Usage:
//
//	go run ./cmd/audit_tokens/
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/cmd/internal/mcpsurface"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "audit_tokens:", err)
		os.Exit(1)
	}
}

// toolTokenInfo is the serialized-size estimate for one MCP tool definition.
type toolTokenInfo struct {
	Name   string
	Tokens int
	Bytes  int
}

// run builds the in-memory server, lists its tools, and writes the token report
// to w. Construction is offline: config.Load and mirrors.NewManager do no network
// I/O, and no tool is called.
//
// The surface comes from mcpsurface.DocsConfig rather than a local copy of the
// same placeholders. The download tool's source enum and prose shrink when a
// credential-gated source is off, so a missing placeholder makes the reported
// figure depend on whose machine ran the audit — and keeping the placeholder set
// in one place is what stops the next credential-gated source being added to two
// of the three callers.
func run(w io.Writer) error {
	toolList, err := mcpsurface.Tools(mcpsurface.DocsConfig())
	if err != nil {
		return err
	}
	infos, totalTokens, totalBytes, err := measureTools(toolList)
	if err != nil {
		return err
	}
	writeReport(w, infos, totalTokens, totalBytes)
	return nil
}

// measureTools serializes each tool definition to JSON and counts its tokens,
// returning the per-tool breakdown and the totals.
func measureTools(toolsList []*mcp.Tool) (infos []toolTokenInfo, totalTokens, totalBytes int, err error) {
	for _, t := range toolsList {
		if t == nil {
			continue
		}
		data, marshalErr := json.Marshal(t)
		if marshalErr != nil {
			return nil, 0, 0, fmt.Errorf("marshal tool %q: %w", t.Name, marshalErr)
		}
		tk := countTokens(data)
		infos = append(infos, toolTokenInfo{Name: t.Name, Tokens: tk, Bytes: len(data)})
		totalTokens += tk
		totalBytes += len(data)
	}
	return infos, totalTokens, totalBytes, nil
}

// writeReport renders the token footprint as an aligned table plus a one-line
// summary of the total context cost of loading the server.
func writeReport(w io.Writer, infos []toolTokenInfo, totalTokens, totalBytes int) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tBYTES\tTOKENS")
	for _, in := range infos {
		fmt.Fprintf(tw, "%s\t%d\t%d\n", in.Name, in.Bytes, in.Tokens)
	}
	fmt.Fprintf(tw, "TOTAL (%d tools)\t%d\t%d\n", len(infos), totalBytes, totalTokens)
	_ = tw.Flush()
	fmt.Fprintf(w, "\nLoading libgen-mcp adds ~%d tokens of context (cl100k_base) for its %d tool definitions.\n",
		totalTokens, len(infos))
}
