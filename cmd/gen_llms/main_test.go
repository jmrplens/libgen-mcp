// main_test.go contains focused, offline unit tests for the pure llms.txt and
// llms-full.txt generation helpers: rune-safe truncation, sentence/paragraph
// extraction, JSON Schema type labeling, and the two structural validators.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTruncateRunes_RespectsRuneBoundaries verifies truncation counts runes (not
// bytes), leaves short strings untouched, and appends an ellipsis only when it
// actually cuts.
func TestTruncateRunes_RespectsRuneBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{name: "short unchanged", in: "hello", maxRunes: 10, want: "hello"},
		{name: "exact length", in: "hello", maxRunes: 5, want: "hello"},
		{name: "truncated ascii", in: "hello world", maxRunes: 5, want: "hello..."},
		{name: "multibyte", in: "áéíóú world", maxRunes: 5, want: "áéíóú..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateRunes(tt.in, tt.maxRunes); got != tt.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tt.in, tt.maxRunes, got, tt.want)
			}
		})
	}
}

// TestFirstSentence_SplitsAndSkipsAbbreviations verifies the first-sentence
// extractor stops at the first real sentence boundary, ignores abbreviations,
// and cuts at newlines.
func TestFirstSentence_SplitsAndSkipsAbbreviations(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple", in: "First. Second.", want: "First."},
		{name: "no boundary", in: "No trailing period here", want: "No trailing period here"},
		{name: "abbreviation", in: "Use e.g. this one. Then stop.", want: "Use e.g. this one."},
		{name: "newline cut", in: "Line one\nLine two", want: "Line one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstSentence(tt.in); got != tt.want {
				t.Fatalf("firstSentence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestFirstParagraph_CutsAtBlankLine verifies the first-paragraph extractor stops
// at the first blank-line break and trims surrounding whitespace.
func TestFirstParagraph_CutsAtBlankLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "single paragraph", in: "Just one paragraph.", want: "Just one paragraph."},
		{name: "two paragraphs", in: "First para.\n\nSecond para.", want: "First para."},
		{name: "leading whitespace", in: "  padded.\n\nnext", want: "padded."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstParagraph(tt.in); got != tt.want {
				t.Fatalf("firstParagraph(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSchemaTypeLabel_CommonShapes verifies schemaTypeLabel summarizes nullable,
// scalar, array, nested-array, object, and untyped JSON Schema shapes into the
// human-readable phrases used in llms-full.txt parameter references.
func TestSchemaTypeLabel_CommonShapes(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{
			name:   "plain string",
			schema: map[string]any{"type": "string"},
			want:   "string",
		},
		{
			name:   "nullable string",
			schema: map[string]any{"type": []any{"null", "string"}},
			want:   "string",
		},
		{
			name: "string array",
			schema: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			want: "array of strings",
		},
		{
			name: "nested integer array",
			schema: map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "integer"},
				},
			},
			want: "array of arrays of integers",
		},
		{
			name:   "untyped any",
			schema: map[string]any{},
			want:   "any",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := schemaTypeLabel(tt.schema); got != tt.want {
				t.Fatalf("schemaTypeLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestValidateLLMSTxt_Cases verifies the llms.txt structural validator accepts a
// well-formed document and rejects the common malformed shapes: missing summary,
// non-link content under an H2 file-list section, and an empty markdown link
// label.
func TestValidateLLMSTxt_Cases(t *testing.T) {
	valid := strings.Join([]string{
		"# libgen-mcp",
		"",
		"> Short project summary.",
		"",
		"Prose and a fenced code block are allowed before the H2 sections.",
		"",
		"```json",
		"{ \"mcpServers\": { \"libgen\": { \"command\": \"docker\" } } }",
		"```",
		"",
		"## Documentation",
		"",
		"- [Guide](docs/getting-started.md): Short guide",
		"- [Reference](docs/tools.md)",
		"",
		"## Optional",
		"",
		"- [Full reference](llms-full.txt): Expanded context",
		"",
	}, "\n")
	if err := validateLLMSTxt(valid); err != nil {
		t.Fatalf("validateLLMSTxt(valid) error: %v", err)
	}

	invalidCases := map[string]string{
		"missing summary": strings.Join([]string{
			"# libgen-mcp",
			"",
			"No blockquote here.",
			"",
			"## Documentation",
			"",
			"- [Guide](docs/getting-started.md): Short guide",
			"",
		}, "\n"),
		"non-link section content": strings.Join([]string{
			"# libgen-mcp",
			"",
			"> Summary.",
			"",
			"## Documentation",
			"",
			"Plain prose is not a file-list entry.",
			"",
		}, "\n"),
		"empty link label": strings.Join([]string{
			"# libgen-mcp",
			"",
			"> Summary.",
			"",
			"## Documentation",
			"",
			"- [](docs/getting-started.md)",
			"",
		}, "\n"),
	}
	for name, content := range invalidCases {
		t.Run(name, func(t *testing.T) {
			if err := validateLLMSTxt(content); err == nil {
				t.Fatalf("validateLLMSTxt(%s) error = nil, want error", name)
			}
		})
	}
}

// TestValidateLLMSFullTxt_RequiresToolsSection verifies llms-full.txt validation
// requires an H1 title and a "## Tools" section, rejecting a document that omits
// the section.
func TestValidateLLMSFullTxt_RequiresToolsSection(t *testing.T) {
	valid := strings.Join([]string{
		"# libgen-mcp — Full Reference",
		"",
		"> Version 0.1.0 | 3 tools",
		"",
		"## Tools",
		"",
		"### search",
		"",
	}, "\n")
	if err := validateLLMSFullTxt(valid); err != nil {
		t.Fatalf("validateLLMSFullTxt(valid) error: %v", err)
	}

	if err := validateLLMSFullTxt("# libgen-mcp — Full Reference\n\nNo tools section.\n"); err == nil {
		t.Fatal("validateLLMSFullTxt(no Tools) error = nil, want error")
	}
	if err := validateLLMSFullTxt("No H1 title\n\n## Tools\n"); err == nil {
		t.Fatal("validateLLMSFullTxt(no H1) error = nil, want error")
	}
}

// TestWriteGeneratedFile_RejectsUnexpectedNames verifies generated llms files can
// only target the two supported top-level artifact names, blocking path escapes
// and arbitrary filenames.
func TestWriteGeneratedFile_RejectsUnexpectedNames(t *testing.T) {
	for _, name := range []string{"README.md", "../llms.txt", "docs/llms.txt"} {
		t.Run(name, func(t *testing.T) {
			if err := writeGeneratedFile(name, "content", true); err == nil {
				t.Fatal("writeGeneratedFile() error = nil, want error")
			}
		})
	}
}

// TestWriteGeneratedFile_CheckModeAcceptsCRLF verifies check mode treats CRLF and
// LF generated files as equivalent so cross-platform line endings do not cause
// false drift.
func TestWriteGeneratedFile_CheckModeAcceptsCRLF(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, llmsFileName), []byte("# Example\r\n\r\n"), 0o600); err != nil {
		t.Fatalf("write llms.txt: %v", err)
	}
	t.Chdir(dir)

	if err := writeGeneratedFile(llmsFileName, "# Example\n\n", true); err != nil {
		t.Fatalf("writeGeneratedFile() error = %v", err)
	}
}

// TestReadVersion_ReadsFromRoot verifies readVersion reads and trims VERSION from
// the supplied root, and falls back to "unknown" when the file is absent.
func TestReadVersion_ReadsFromRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("1.2.3\n"), 0o600); err != nil {
		t.Fatalf("write VERSION: %v", err)
	}
	if got := readVersion(dir); got != "1.2.3" {
		t.Fatalf("readVersion() = %q, want 1.2.3", got)
	}
	if got := readVersion(filepath.Join(dir, "does-not-exist")); got != "unknown" {
		t.Fatalf("readVersion(missing) = %q, want unknown", got)
	}
}
