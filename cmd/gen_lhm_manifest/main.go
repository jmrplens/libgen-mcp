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
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/cmd/internal/mcpsurface"
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
	rootDir, err := mcpsurface.ProjectRoot()
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

	cfg := mcpsurface.DocsConfig()
	toolList, err := mcpsurface.Tools(cfg)
	if err != nil {
		return nil, 0, 0, err
	}
	promptList, err := mcpsurface.Prompts(cfg)
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
	return unescapeHTMLEntities(buf.Bytes()), len(m.Tools), len(m.Prompts), nil
}

// htmlEntityUnescaper rewrites the three sequences Go's json package escapes for
// HTML safety back into the characters they stand for.
var htmlEntityUnescaper = strings.NewReplacer(`\u0026`, "&", `\u003c`, "<", `\u003e`, ">")

// unescapeHTMLEntities makes the generated bytes agree with what jq writes.
//
// SetEscapeHTML(false) on the outer encoder is not enough: the SDK types nested
// in this manifest — a tool's Annotations, its InputSchema — marshal themselves
// with the default encoder, so an "&" inside a tool title arrives already
// escaped and the outer setting cannot undo it. That only became visible in
// v1.6.0, when a title first contained one.
//
// It matters because the release workflow stamps the version into this file with
// jq (scripts/update-server-json-sha.sh), and jq writes those characters
// literally. Byte-comparing the two would then report the file as stale forever
// after every release, which is exactly what it did. Rewriting them here keeps
// the generator and the stamper producing the same bytes.
func unescapeHTMLEntities(b []byte) []byte {
	return []byte(htmlEntityUnescaper.Replace(string(b)))
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
