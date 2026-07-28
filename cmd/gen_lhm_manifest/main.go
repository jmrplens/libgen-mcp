// Command gen_lhm_manifest regenerates the tools and prompts arrays in
// lhm.plugin.json, the manifest published to the LobeHub Marketplace.
//
// LobeHub derives a listing's capability badges from the manifest's own
// tools/resources/prompts arrays — its crawler cannot introspect a server that
// ships as a Go binary or a Docker image, so a manifest without them lists the
// server as having zero tools and zero prompts no matter what the server
// actually registers. The arrays are therefore data we owe the marketplace, and
// this command derives them from a real tools/list + prompts/list round-trip
// against an in-memory server rather than from a hand-written copy that would
// drift on the next release.
//
// Every field the manifest already carries is preserved; only tools and prompts
// are rewritten.
//
// Usage:
//
//	go run ./cmd/gen_lhm_manifest/
//	go run ./cmd/gen_lhm_manifest/ --check
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/prompts"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// manifestFileName is the LobeHub manifest read by `lhm plugin publish`.
const manifestFileName = "lhm.plugin.json"

// manifest mirrors the schema accepted by the LobeHub publish endpoint
// (market.lobehub.com/s/publish-mcp/references/manifest). Every documented field
// is listed so that decoding with DisallowUnknownFields rejects a typo instead
// of silently dropping it on the next regeneration — the server strips unknown
// fields without complaint, which is exactly how a misspelled key would ship.
//
// homepage and cloudEndpoint are validated but not stored by the endpoint; they
// stay in the struct so a manifest that carries them round-trips unchanged.
type manifest struct {
	Identifier    string            `json:"identifier"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	Description   string            `json:"description,omitempty"`
	Author        string            `json:"author,omitempty"`
	AuthorURL     string            `json:"authorUrl,omitempty"`
	Category      string            `json:"category,omitempty"`
	Homepage      string            `json:"homepage,omitempty"`
	CloudEndpoint string            `json:"cloudEndpoint,omitempty"`
	Icon          string            `json:"icon,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Localizations []json.RawMessage `json:"localizations,omitempty"`
	Tools         []manifestTool    `json:"tools,omitempty"`
	Prompts       []manifestPrompt  `json:"prompts,omitempty"`
	Resources     []json.RawMessage `json:"resources,omitempty"`
}

// manifestTool is one entry of the manifest's tools array, in the standard MCP
// tool shape. The output schema is deliberately left out: it is not part of the
// shape LobeHub documents, and it more than doubles the file for something the
// listing never renders.
type manifestTool struct {
	Name        string               `json:"name"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description,omitempty"`
	InputSchema any                  `json:"inputSchema,omitempty"`
	Annotations *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

// manifestPrompt is one entry of the manifest's prompts array, in the standard
// MCP prompt shape.
type manifestPrompt struct {
	Name        string                `json:"name"`
	Title       string                `json:"title,omitempty"`
	Description string                `json:"description,omitempty"`
	Arguments   []*mcp.PromptArgument `json:"arguments,omitempty"`
}

func main() {
	checkOnly := flag.Bool("check", false, "verify the committed manifest matches the registered surface without writing it")
	flag.Parse()

	if err := run(*checkOnly); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate %s: %v\n", manifestFileName, err)
		os.Exit(1)
	}
}

// run rewrites the manifest's tools and prompts arrays from the live MCP
// surface, or, in check mode, reports whether the committed file already
// matches it.
func run(checkOnly bool) error {
	rootDir, err := findProjectRoot()
	if err != nil {
		return err
	}
	path := filepath.Join(rootDir, manifestFileName)

	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", manifestFileName, err)
	}

	generated, toolCount, promptCount, err := generate(current)
	if err != nil {
		return err
	}

	if checkOnly {
		if !bytes.Equal(current, generated) {
			return fmt.Errorf("%s is stale: run `make gen-lhm-manifest` and commit the result", manifestFileName)
		}
		fmt.Printf("%s is current (%d tools, %d prompts)\n", manifestFileName, toolCount, promptCount)
		return nil
	}

	if writeErr := os.WriteFile(path, generated, 0o600); writeErr != nil {
		return fmt.Errorf("write %s: %w", manifestFileName, writeErr)
	}
	fmt.Printf("Generated %s (%d tools, %d prompts)\n", manifestFileName, toolCount, promptCount)
	return nil
}

// generate returns the manifest bytes that current should have, with the tools
// and prompts arrays replaced by the registered surface and every other field
// left untouched.
func generate(current []byte) (out []byte, toolCount, promptCount int, err error) {
	dec := json.NewDecoder(bytes.NewReader(current))
	dec.DisallowUnknownFields()
	var m manifest
	if err = dec.Decode(&m); err != nil {
		return nil, 0, 0, fmt.Errorf("parse %s: %w", manifestFileName, err)
	}

	toolList, err := listTools()
	if err != nil {
		return nil, 0, 0, err
	}
	promptList, err := listPrompts()
	if err != nil {
		return nil, 0, 0, err
	}

	m.Tools = manifestTools(toolList)
	m.Prompts = manifestPrompts(promptList)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err = enc.Encode(m); err != nil {
		return nil, 0, 0, fmt.Errorf("encode %s: %w", manifestFileName, err)
	}
	return buf.Bytes(), len(m.Tools), len(m.Prompts), nil
}

// manifestTools converts the tools/list result into manifest entries, ordered
// the way a reader meets them: search, then get_details, download and read.
func manifestTools(list []*mcp.Tool) []manifestTool {
	out := make([]manifestTool, 0, len(list))
	for _, t := range list {
		out = append(out, manifestTool{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: t.Annotations,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return toolOrder(out[i].Name) < toolOrder(out[j].Name)
	})
	return out
}

// manifestPrompts converts the prompts/list result into manifest entries,
// ordered by name so the output does not depend on registration order.
func manifestPrompts(list []*mcp.Prompt) []manifestPrompt {
	out := make([]manifestPrompt, 0, len(list))
	for _, p := range list {
		out = append(out, manifestPrompt{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Arguments:   p.Arguments,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toolOrder ranks the tools for display, matching the order gen_llms uses so the
// two generated artifacts present the surface the same way.
func toolOrder(name string) int {
	switch name {
	case "search":
		return 0
	case "get_details":
		return 1
	case "download":
		return 2
	default:
		return 3
	}
}

// newSession creates an in-memory MCP server+client session with a page size
// high enough that the whole surface arrives in one response.
func newSession(setupServer func(*mcp.Server)) (session *mcp.ClientSession, cleanup func(), err error) {
	opts := &mcp.ServerOptions{PageSize: 2000}
	server := mcp.NewServer(&mcp.Implementation{Name: "gen-lhm-manifest", Version: "0.0.1"}, opts)
	setupServer(server)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()

	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("server connect: %w", err)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "gen-lhm-manifest-client", Version: "0.0.1"}, nil)
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

// docsConfig builds the configuration the generated manifest must describe: the
// full capability set, not whatever the ambient environment happens to enable.
// Every credential-gated source needs a placeholder here — without one, the
// download tool's schema would differ between a machine that holds the
// credential and one that does not, and the --check gate would pass or fail by
// accident. cmd/gen_llms and cmd/audit_tokens carry the same list.
func docsConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{}
	}
	cfg.Sources = nil
	if cfg.UnpaywallEmail == "" {
		cfg.UnpaywallEmail = "docs@example.com"
	}
	if cfg.CoreKey == "" {
		cfg.CoreKey = "docs-placeholder-key"
	}
	return cfg
}

// listTools returns the registered tools via a real tools/list round-trip.
// Construction is offline-safe: config.Load and mirrors.NewManager perform no
// network I/O, and the client is never asked to make a request.
func listTools() ([]*mcp.Tool, error) {
	cfg := docsConfig()
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, fmt.Errorf("create mirror manager: %w", err)
	}
	client := libgen.New(mgr, cfg)

	session, cleanup, err := newSession(func(server *mcp.Server) {
		tools.Register(server, client, cfg)
	})
	if err != nil {
		return nil, err
	}
	defer cleanup()

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return result.Tools, nil
}

// listPrompts returns the registered prompts via a real prompts/list round-trip.
func listPrompts() ([]*mcp.Prompt, error) {
	cfg := docsConfig()
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, fmt.Errorf("create mirror manager: %w", err)
	}
	client := libgen.New(mgr, cfg)

	session, cleanup, err := newSession(func(server *mcp.Server) {
		prompts.Register(server, client, cfg)
	})
	if err != nil {
		return nil, err
	}
	defer cleanup()

	result, err := session.ListPrompts(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("list prompts: %w", err)
	}
	return result.Prompts, nil
}

// findProjectRoot walks up from the working directory to the directory holding
// go.mod, so the command works from anywhere in the repository.
func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found in any parent directory")
		}
		dir = parent
	}
}
