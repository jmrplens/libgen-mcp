// Command audit_tokens measures the LLM context-window footprint of libgen-mcp's
// MCP tools and prompts. It builds an in-memory MCP server, lists both catalogs
// over real tools/list and prompts/list round-trips, serializes each definition
// to JSON, and counts tokens with the cl100k_base tokenizer (see countTokens),
// falling back to a bytes/4 heuristic. This is the fixed context cost every
// request pays for having libgen-mcp loaded — useful for judging how "cheap" the
// server is to keep on.
//
// The full tool surface is measured (all download sources enabled), so the number
// is deterministic and represents the upper bound.
//
// Usage:
//
//	go run ./cmd/audit_tokens/
package main

import (
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

// entryTokenInfo is the serialized-size estimate for one MCP tool or prompt
// definition.
type entryTokenInfo struct {
	Name   string
	Tokens int
	Bytes  int
}

// run builds the in-memory server, lists its tools and prompts, and writes the
// token report to w. Construction is offline: config.Load and mirrors.NewManager
// do no network I/O, and no tool is called.
//
// The surface comes from mcpsurface.DocsConfig rather than a local copy of the
// same placeholders. The download tool's source enum and prose shrink when a
// credential-gated source is off, so a missing placeholder makes the reported
// figure depend on whose machine ran the audit — and keeping the placeholder set
// in one place is what stops the next credential-gated source being added to two
// of the three callers.
func run(w io.Writer) error {
	cfg := mcpsurface.DocsConfig()

	toolList, err := mcpsurface.Tools(cfg)
	if err != nil {
		return err
	}
	toolInfos, toolTokens, toolBytes, err := measureTools(toolList)
	if err != nil {
		return err
	}

	promptList, err := mcpsurface.Prompts(cfg)
	if err != nil {
		return err
	}
	promptInfos, promptTokens, promptBytes, err := measurePrompts(promptList)
	if err != nil {
		return err
	}

	writeReport(w, toolInfos, toolTokens, toolBytes, promptInfos, promptTokens, promptBytes)
	return nil
}

// measureTools serializes each tool definition to JSON and counts its tokens,
// returning the per-tool breakdown and the totals. Serialization goes through
// marshalModelFacing (modelfacing.go) so a presentation-only field like Icons
// never counts toward the LLM context-window figure this command reports.
func measureTools(toolsList []*mcp.Tool) (infos []entryTokenInfo, totalTokens, totalBytes int, err error) {
	for _, t := range toolsList {
		if t == nil {
			continue
		}
		data, marshalErr := marshalModelFacing(t)
		if marshalErr != nil {
			return nil, 0, 0, fmt.Errorf("marshal tool %q: %w", t.Name, marshalErr)
		}
		tk := countTokens(data)
		infos = append(infos, entryTokenInfo{Name: t.Name, Tokens: tk, Bytes: len(data)})
		totalTokens += tk
		totalBytes += len(data)
	}
	return infos, totalTokens, totalBytes, nil
}

// measurePrompts serializes each prompt definition to JSON and counts its
// tokens, the same way measureTools does for tools.
func measurePrompts(promptList []*mcp.Prompt) (infos []entryTokenInfo, totalTokens, totalBytes int, err error) {
	for _, p := range promptList {
		if p == nil {
			continue
		}
		data, marshalErr := marshalModelFacing(p)
		if marshalErr != nil {
			return nil, 0, 0, fmt.Errorf("marshal prompt %q: %w", p.Name, marshalErr)
		}
		tk := countTokens(data)
		infos = append(infos, entryTokenInfo{Name: p.Name, Tokens: tk, Bytes: len(data)})
		totalTokens += tk
		totalBytes += len(data)
	}
	return infos, totalTokens, totalBytes, nil
}

// writeReport renders the tool and prompt token footprints as aligned tables
// plus a one-line summary of the total context cost of loading the server.
func writeReport(w io.Writer,
	toolInfos []entryTokenInfo, toolTokens, toolBytes int,
	promptInfos []entryTokenInfo, promptTokens, promptBytes int,
) {
	writeTable(w, "TOOL", "tools", toolInfos, toolTokens, toolBytes)
	fmt.Fprintln(w)
	writeTable(w, "PROMPT", "prompts", promptInfos, promptTokens, promptBytes)
	fmt.Fprintf(w, "\nLoading libgen-mcp adds ~%d tokens of context (cl100k_base): ~%d for its %d tool definitions and ~%d for its %d prompt definitions.\n",
		toolTokens+promptTokens, toolTokens, len(toolInfos), promptTokens, len(promptInfos))
}

// writeTable renders one aligned BYTES/TOKENS table for infos, headed by
// header (e.g. "TOOL") and totaled as "TOTAL (N <noun>)" (e.g. "tools").
func writeTable(w io.Writer, header, noun string, infos []entryTokenInfo, totalTokens, totalBytes int) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\tBYTES\tTOKENS\n", header)
	for _, in := range infos {
		fmt.Fprintf(tw, "%s\t%d\t%d\n", in.Name, in.Bytes, in.Tokens)
	}
	fmt.Fprintf(tw, "TOTAL (%d %s)\t%d\t%d\n", len(infos), noun, totalBytes, totalTokens)
	_ = tw.Flush()
}
