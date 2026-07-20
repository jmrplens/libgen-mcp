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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/tools"
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
func run(w io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{}
	}
	// Measure the full tool surface regardless of the ambient environment: the
	// download tool's source enum shrinks when sources are disabled (e.g. unpaywall
	// without an email), so enable everything for a deterministic upper-bound count.
	cfg.Sources = nil
	if cfg.UnpaywallEmail == "" {
		cfg.UnpaywallEmail = "audit@example.com"
	}

	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return fmt.Errorf("create mirror manager: %w", err)
	}
	client := libgen.New(mgr, cfg)

	server := mcp.NewServer(&mcp.Implementation{Name: "audit-tokens", Version: "0.0.1"}, nil)
	tools.Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return fmt.Errorf("server connect: %w", err)
	}
	defer func() { _ = serverSession.Wait() }()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "audit-tokens-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		return fmt.Errorf("client connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}

	infos, totalTokens, totalBytes, err := measureTools(result.Tools)
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
