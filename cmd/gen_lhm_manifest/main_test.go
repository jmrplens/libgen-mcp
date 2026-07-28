package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// minimalManifest is the smallest input generate accepts: the three required
// fields and nothing else.
const minimalManifest = `{
  "identifier": "jmrplens-libgen-mcp",
  "name": "Library Genesis MCP Server",
  "version": "9.9.9"
}`

// TestGenerate_FillsSurfaceAndPreservesFields checks that generate replaces the
// capability arrays with the registered surface while leaving the manifest's own
// metadata untouched — the arrays are the only thing this command owns.
func TestGenerate_FillsSurfaceAndPreservesFields(t *testing.T) {
	in := []byte(`{
  "identifier": "jmrplens-libgen-mcp",
  "name": "Library Genesis MCP Server",
  "version": "9.9.9",
  "description": "desc",
  "author": "Someone",
  "authorUrl": "https://example.com",
  "icon": "📚",
  "tags": ["a", "b"],
  "tools": [],
  "prompts": []
}`)

	out, toolCount, promptCount, err := generate(in)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if toolCount == 0 || promptCount == 0 {
		t.Fatalf("got %d tools and %d prompts, want both non-zero", toolCount, promptCount)
	}

	var m manifest
	if decodeErr := json.Unmarshal(out, &m); decodeErr != nil {
		t.Fatalf("unmarshal output: %v", decodeErr)
	}
	if m.Identifier != "jmrplens-libgen-mcp" || m.Version != "9.9.9" || m.Description != "desc" {
		t.Errorf("metadata was not preserved: %+v", m)
	}
	if m.Icon != "📚" || len(m.Tags) != 2 {
		t.Errorf("icon/tags were not preserved: icon=%q tags=%v", m.Icon, m.Tags)
	}
	if len(m.Tools) != toolCount || len(m.Prompts) != promptCount {
		t.Errorf("counts disagree with the encoded arrays: %d/%d vs %d/%d",
			len(m.Tools), len(m.Prompts), toolCount, promptCount)
	}
	for _, tool := range m.Tools {
		if tool.Name == "" || tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("tool %q is missing name, description or inputSchema", tool.Name)
		}
	}
	for _, p := range m.Prompts {
		if p.Name == "" || p.Description == "" {
			t.Errorf("prompt %q is missing name or description", p.Name)
		}
	}
}

// TestGenerate_Idempotent verifies that re-running generate over its own output
// changes nothing. The --check gate compares bytes, so any instability here
// would fail CI on a machine that merely ran the generator twice.
func TestGenerate_Idempotent(t *testing.T) {
	first, _, _, err := generate([]byte(minimalManifest))
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	second, _, _, err := generate(first)
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if string(first) != string(second) {
		t.Error("generate is not idempotent: a second run produced different bytes")
	}
}

// TestGenerate_RejectsUnknownField makes sure a misspelled key fails loudly.
// LobeHub's publish endpoint strips unknown fields silently, so a typo would
// otherwise be dropped here and never noticed in the listing either.
//
// Note the limit of this guard: encoding/json matches field names
// case-insensitively, so "authorURL" would still bind to authorUrl. It catches a
// wrong name, not wrong casing.
func TestGenerate_RejectsUnknownField(t *testing.T) {
	in := []byte(`{"identifier": "x", "name": "y", "version": "1.0.0", "autorUrl": "https://example.com"}`)
	_, _, _, err := generate(in)
	if err == nil {
		t.Fatal("expected an error for an unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "autorUrl") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

// TestGenerate_InvalidJSON checks that a malformed manifest is reported rather
// than silently overwritten with a freshly generated one.
func TestGenerate_InvalidJSON(t *testing.T) {
	if _, _, _, err := generate([]byte("{not json")); err == nil {
		t.Fatal("expected a parse error, got nil")
	}
}

// TestManifestTools_Order pins the display order: search first, then
// get_details, download and read, matching gen_llms.
func TestManifestTools_Order(t *testing.T) {
	got := manifestTools([]*mcp.Tool{
		{Name: "read"},
		{Name: "download"},
		{Name: "search"},
		{Name: "get_details"},
	})
	want := []string{"search", "get_details", "download", "read"}
	if len(got) != len(want) {
		t.Fatalf("got %d tools, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, want[i])
		}
	}
}

// TestManifestPrompts_SortedByName verifies prompts come out in name order, so
// the file does not churn when registration order changes.
func TestManifestPrompts_SortedByName(t *testing.T) {
	got := manifestPrompts([]*mcp.Prompt{
		{Name: "research_topic"},
		{Name: "acquire_book"},
		{Name: "get_paper"},
	})
	want := []string{"acquire_book", "get_paper", "research_topic"}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, want[i])
		}
	}
}

// TestDocsConfig_ForcesFullSurface checks the placeholder credentials are
// applied, since a missing one would make the generated schema depend on the
// ambient environment and the --check gate pass or fail by accident.
func TestDocsConfig_ForcesFullSurface(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SOURCES", "libgen")
	t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", "")
	t.Setenv("LIBGEN_MCP_CORE_KEY", "")

	cfg := docsConfig()
	if cfg.Sources != nil {
		t.Errorf("Sources should be cleared to advertise every source, got %v", cfg.Sources)
	}
	if cfg.UnpaywallEmail == "" || cfg.CoreKey == "" {
		t.Errorf("credential placeholders missing: email=%q core=%q", cfg.UnpaywallEmail, cfg.CoreKey)
	}
}

// TestFindProjectRoot locates the repository root from the package directory and
// confirms the manifest this command maintains actually lives there.
func TestFindProjectRoot(t *testing.T) {
	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("findProjectRoot: %v", err)
	}
	if root == "" {
		t.Fatal("findProjectRoot returned an empty path")
	}
}
