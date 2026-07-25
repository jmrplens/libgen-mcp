// main_test.go contains focused, offline unit tests for the pure llms.txt and
// llms-full.txt generation helpers: rune-safe truncation, sentence/paragraph
// extraction, JSON Schema type labeling, and the two structural validators.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
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
		"> Version 1.0.0 | 3 tools",
		"",
		"## Tools",
		"",
		"### search",
		"",
		"## Configuration",
		"",
		"## Download sources",
		"",
		"## Transports",
		"",
		"## Install (headless)",
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

// newProjectRoot creates a temp dir containing go.mod (and a VERSION file) and
// chdirs into it, so findProjectRoot/readVersion resolve to the temp tree.
func newProjectRoot(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(version+"\n"), 0o600); err != nil {
			t.Fatalf("write VERSION: %v", err)
		}
	}
	t.Chdir(dir)
	return dir
}

// TestListTools_ReturnsOrderedRealTools verifies the in-memory MCP session
// (newSession) lists the real registered tools and toolOrder sorts them into the
// natural search/get_details/download/read workflow order.
func TestListTools_ReturnsOrderedRealTools(t *testing.T) {
	toolList, err := listTools()
	if err != nil {
		t.Fatalf("listTools() error: %v", err)
	}
	if len(toolList) != 4 {
		t.Fatalf("listTools() returned %d tools, want 4", len(toolList))
	}
	want := []string{"search", "get_details", "download", "read"}
	for i, name := range want {
		if toolList[i].Name != name {
			t.Fatalf("tool[%d] = %q, want %q", i, toolList[i].Name, name)
		}
	}
}

// TestToolOrder_Ordinals covers every branch of toolOrder including the default.
func TestToolOrder_Ordinals(t *testing.T) {
	cases := map[string]int{
		"search":      0,
		"get_details": 1,
		"download":    2,
		"anything":    3,
	}
	for name, want := range cases {
		if got := toolOrder(name); got != want {
			t.Fatalf("toolOrder(%q) = %d, want %d", name, got, want)
		}
	}
}

// TestRun_GeneratesAndValidates runs the full generation into a temp project
// root, asserts both files are written with real content, then re-runs in
// check-only mode to confirm the freshly written files validate.
func TestRun_GeneratesAndValidates(t *testing.T) {
	dir := newProjectRoot(t, "9.9.9")

	if err := run(false); err != nil {
		t.Fatalf("run(false) error: %v", err)
	}

	llms, err := os.ReadFile(filepath.Join(dir, llmsFileName))
	if err != nil {
		t.Fatalf("read llms.txt: %v", err)
	}
	if !strings.Contains(string(llms), "# libgen-mcp") || !strings.Contains(string(llms), "v9.9.9") {
		t.Fatalf("llms.txt missing expected content:\n%s", llms)
	}

	full, err := os.ReadFile(filepath.Join(dir, llmsFullFileName))
	if err != nil {
		t.Fatalf("read llms-full.txt: %v", err)
	}
	for _, want := range []string{"## Tools", "### search", "### get_details", "### download", "### read", "## Configuration"} {
		if !strings.Contains(string(full), want) {
			t.Fatalf("llms-full.txt missing %q", want)
		}
	}

	// The freshly generated files must pass check-only validation.
	if checkErr := run(true); checkErr != nil {
		t.Fatalf("run(true) after generate error: %v", checkErr)
	}
}

// TestRun_FindRootError verifies run surfaces the findProjectRoot failure when no
// go.mod exists anywhere up the tree.
func TestRun_FindRootError(t *testing.T) {
	t.Chdir(t.TempDir()) // temp dir has no go.mod up to the filesystem root
	if err := run(false); err == nil {
		t.Fatal("run(false) with no project root: error = nil, want error")
	}
}

// TestRun_CheckDrift verifies check-only mode reports drift when an existing
// generated file does not match freshly generated content.
func TestRun_CheckDrift(t *testing.T) {
	dir := newProjectRoot(t, "1.0.0")
	if err := os.WriteFile(filepath.Join(dir, llmsFileName), []byte("stale\n"), 0o600); err != nil {
		t.Fatalf("seed stale llms.txt: %v", err)
	}
	if err := run(true); err == nil {
		t.Fatal("run(true) with stale llms.txt: error = nil, want drift error")
	}
}

// TestNewSession_SetupError covers the branch where the server setup callback
// returns an error before any transport is connected.
func TestNewSession_SetupError(t *testing.T) {
	_, _, err := newSession(func(*mcp.Server) error {
		return errors.New("setup failed")
	})
	if err == nil {
		t.Fatal("newSession with failing setup: error = nil, want error")
	}
}

// TestRun_FullWriteError covers the run branch where llms.txt writes but
// llms-full.txt fails because a directory occupies its target name.
func TestRun_FullWriteError(t *testing.T) {
	dir := newProjectRoot(t, "1.0.0")
	if err := os.Mkdir(filepath.Join(dir, llmsFullFileName), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := run(false); err == nil {
		t.Fatal("run(false) with llms-full.txt dir: error = nil, want error")
	}
}

// TestWriteLLMSTxt_And_Full exercises the two top-level writers directly against
// real tools and asserts the generated files land in the project root.
func TestWriteLLMSTxt_And_Full(t *testing.T) {
	dir := newProjectRoot(t, "2.3.4")
	toolList, err := listTools()
	if err != nil {
		t.Fatalf("listTools() error: %v", err)
	}

	if writeErr := writeLLMSTxt("2.3.4", toolList, false); writeErr != nil {
		t.Fatalf("writeLLMSTxt error: %v", writeErr)
	}
	if writeErr := writeLLMSFullTxt("2.3.4", toolList, false); writeErr != nil {
		t.Fatalf("writeLLMSFullTxt error: %v", writeErr)
	}
	for _, name := range []string{llmsFileName, llmsFullFileName} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Fatalf("expected %s written: %v", name, statErr)
		}
	}
}

// TestWriteLLMSFullTool_RendersSections verifies a single tool renders its
// heading, title, description, parameters and annotations block.
func TestWriteLLMSFullTool_RendersSections(t *testing.T) {
	toolList, err := listTools()
	if err != nil {
		t.Fatalf("listTools() error: %v", err)
	}
	var b strings.Builder
	for _, tool := range toolList {
		writeLLMSFullTool(&b, tool)
	}
	out := b.String()
	for _, want := range []string{"### search", "**Parameters:**", "Annotations:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("writeLLMSFullTool output missing %q:\n%s", want, out)
		}
	}
}

// TestStaticFullSections exercises the four static llms-full.txt section writers.
func TestStaticFullSections(t *testing.T) {
	cases := []struct {
		name  string
		write func(*strings.Builder)
		want  string
	}{
		{"configuration", writeLLMSFullConfiguration, "## Configuration"},
		{"download sources", writeLLMSFullDownloadSources, "## Download sources"},
		{"transports", writeLLMSFullTransports, "## Transports"},
		{"install", writeLLMSFullInstall, "## Install (headless)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			tc.write(&b)
			if !strings.Contains(b.String(), tc.want) {
				t.Fatalf("%s output missing %q", tc.name, tc.want)
			}
		})
	}
}

// TestDownloadSourcesNamesEveryKnownSource verifies the download-source section
// documents every recognized source name. The section is prose, so it silently
// went stale when sources were added; deriving the check from config.KnownSources
// makes a future addition fail here instead of shipping a reference that
// contradicts the server.
func TestDownloadSourcesNamesEveryKnownSource(t *testing.T) {
	var b strings.Builder
	writeLLMSFullDownloadSources(&b)
	got := b.String()
	for _, name := range config.KnownSources {
		if !strings.Contains(got, "`"+name+"`") && !strings.Contains(got, name+",") && !strings.Contains(got, ","+name) {
			t.Errorf("download-source section never mentions the %q source", name)
		}
	}
}

// TestWriteMcpServersJSON_Forms covers the command-only and command+args forms.
func TestWriteMcpServersJSON_Forms(t *testing.T) {
	var noArgs strings.Builder
	writeMcpServersJSON(&noArgs, "/usr/local/bin/libgen-mcp", nil)
	if got := noArgs.String(); !strings.Contains(got, `"command": "/usr/local/bin/libgen-mcp"`) || strings.Contains(got, `"args"`) {
		t.Fatalf("command-only form unexpected:\n%s", got)
	}

	var withArgs strings.Builder
	writeMcpServersJSON(&withArgs, "docker", []string{"run", "-i", "--rm", "ghcr.io/jmrplens/libgen-mcp:latest"})
	got := withArgs.String()
	if !strings.Contains(got, `"args": ["run", "-i", "--rm", "ghcr.io/jmrplens/libgen-mcp:latest"]`) {
		t.Fatalf("args form unexpected:\n%s", got)
	}
}

// TestWriteLLMSLink_Format checks the markdown link rendering.
func TestWriteLLMSLink_Format(t *testing.T) {
	var b strings.Builder
	writeLLMSLink(&b, "Guide", "https://example.com/guide", "A guide")
	if got := b.String(); got != "- [Guide](https://example.com/guide): A guide\n" {
		t.Fatalf("writeLLMSLink = %q", got)
	}
}

// TestCompactToolDescription covers the three branches: short paragraph passes
// through, an over-long paragraph collapses to its first sentence, and a single
// over-long sentence is hard-truncated.
func TestCompactToolDescription(t *testing.T) {
	short := "A short description."
	if got := compactToolDescription(short); got != short {
		t.Fatalf("short: got %q", got)
	}

	longSentence := strings.Repeat("word ", 200) // one sentence, > maxFullDescRunes runes
	firstShort := "Concise lead sentence. " + longSentence
	got := compactToolDescription(firstShort)
	if got != "Concise lead sentence." {
		t.Fatalf("first-sentence fallback: got %q", got)
	}

	truncated := compactToolDescription(longSentence)
	if !strings.HasSuffix(truncated, "...") {
		t.Fatalf("hard-truncate branch: got %q", truncated)
	}
}

// TestWriteAnnotations covers nil, explicit hint pointers, and default nil hints.
func TestWriteAnnotations(t *testing.T) {
	var nilB strings.Builder
	writeAnnotations(&nilB, nil)
	if nilB.String() != "" {
		t.Fatalf("nil annotations wrote %q", nilB.String())
	}

	destructive := true
	openWorld := false
	var explicit strings.Builder
	writeAnnotations(&explicit, &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: &destructive,
		IdempotentHint:  true,
		OpenWorldHint:   &openWorld,
	})
	if got := explicit.String(); !strings.Contains(got, "readOnly=true") || !strings.Contains(got, "destructive=true") || !strings.Contains(got, "openWorld=false") {
		t.Fatalf("explicit annotations = %q", got)
	}

	var defaults strings.Builder
	writeAnnotations(&defaults, &mcp.ToolAnnotations{})
	if got := defaults.String(); !strings.Contains(got, "destructive=false") || !strings.Contains(got, "openWorld=true") {
		t.Fatalf("default annotations = %q", got)
	}
}

// TestWriteInputSchema covers the non-map, empty-properties, and populated
// branches (including a non-map property that is skipped).
func TestWriteInputSchema(t *testing.T) {
	var notMap strings.Builder
	writeInputSchema(&notMap, "not a schema")
	if notMap.String() != "" {
		t.Fatalf("non-map schema wrote %q", notMap.String())
	}

	var empty strings.Builder
	writeInputSchema(&empty, map[string]any{"type": "object"})
	if empty.String() != "" {
		t.Fatalf("empty-properties schema wrote %q", empty.String())
	}

	var populated strings.Builder
	writeInputSchema(&populated, map[string]any{
		"properties": map[string]any{
			"query":  map[string]any{"type": "string", "description": "search query"},
			"limit":  map[string]any{"type": "integer"},
			"skipme": "not-a-map",
		},
		"required": []any{"query"},
	})
	got := populated.String()
	if !strings.Contains(got, "**Parameters:**") ||
		!strings.Contains(got, "- `query` (string) (required): search query") ||
		!strings.Contains(got, "- `limit` (integer)\n") {
		t.Fatalf("populated schema = %q", got)
	}
	if strings.Contains(got, "skipme") {
		t.Fatalf("non-map property should be skipped: %q", got)
	}
}

// TestSchemaRequiredSet covers absent, malformed, and valid "required" arrays.
func TestSchemaRequiredSet(t *testing.T) {
	if got := schemaRequiredSet(map[string]any{}); len(got) != 0 {
		t.Fatalf("absent required = %v", got)
	}
	if got := schemaRequiredSet(map[string]any{"required": "oops"}); len(got) != 0 {
		t.Fatalf("malformed required = %v", got)
	}
	got := schemaRequiredSet(map[string]any{"required": []any{"a", 42, "b"}})
	if !got["a"] || !got["b"] || len(got) != 2 {
		t.Fatalf("valid required = %v", got)
	}
}

// TestWriteSchemaProperty covers the with-description, no-description, and the
// ",required" suffix-trimming branches.
func TestWriteSchemaProperty(t *testing.T) {
	var withDesc strings.Builder
	writeSchemaProperty(&withDesc, "q", map[string]any{"type": "string", "description": "text,required"}, true)
	if got := withDesc.String(); got != "- `q` (string) (required): text\n" {
		t.Fatalf("with desc = %q", got)
	}

	var noDesc strings.Builder
	writeSchemaProperty(&noDesc, "n", map[string]any{"type": "integer"}, false)
	if got := noDesc.String(); got != "- `n` (integer)\n" {
		t.Fatalf("no desc = %q", got)
	}
}

// TestSchemaTypeLabel_RemainingShapes covers the array-of-untyped, object-by-
// properties, and multi-type "or" branches not exercised elsewhere.
func TestSchemaTypeLabel_RemainingShapes(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{"array untyped items", map[string]any{"type": "array", "items": map[string]any{}}, "array"},
		{"array by items only", map[string]any{"items": map[string]any{"type": "string"}}, "array"},
		{"object by properties", map[string]any{"properties": map[string]any{}}, "object"},
		{"multi-type or", map[string]any{"type": []any{"string", "integer"}}, "string or integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := schemaTypeLabel(tc.schema); got != tc.want {
				t.Fatalf("schemaTypeLabel(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestPluralSchemaType covers every branch of the pluralizer.
func TestPluralSchemaType(t *testing.T) {
	cases := map[string]string{
		"array of strings":  "arrays of strings",
		"integer":           "integers",
		"number":            "numbers",
		"string":            "strings",
		"boolean":           "booleans",
		"object":            "objects",
		"string or integer": "values",
		"widget":            "widgets",
	}
	for in, want := range cases {
		if got := pluralSchemaType(in); got != want {
			t.Fatalf("pluralSchemaType(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateLLMSTxt_RemainingBranches covers the H1 branches and the
// trailing section-with-no-links branch not covered by the existing suite.
func TestValidateLLMSTxt_RemainingBranches(t *testing.T) {
	cases := map[string]string{
		"empty first line":  "\n> Summary.\n",
		"first line not H1": "Not a title\n\n> Summary.\n",
		"trailing empty section": strings.Join([]string{
			"# libgen-mcp",
			"",
			"> Summary.",
			"",
			"## Documentation",
			"",
		}, "\n"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateLLMSTxt(content); err == nil {
				t.Fatalf("validateLLMSTxt(%s) = nil, want error", name)
			}
		})
	}
}

// TestValidateHeading_RemainingBranches covers the H3-reject, empty-title, and
// the mid-document section-with-no-links branches.
func TestValidateHeading_RemainingBranches(t *testing.T) {
	cases := map[string]string{
		"h3 rejected": strings.Join([]string{
			"# libgen-mcp", "", "> Summary.", "", "### Too deep", "",
		}, "\n"),
		"empty h2 title": strings.Join([]string{
			"# libgen-mcp", "", "> Summary.", "", "## ", "",
		}, "\n"),
		"section without links before next section": strings.Join([]string{
			"# libgen-mcp",
			"",
			"> Summary.",
			"",
			"## Documentation",
			"",
			"## Optional",
			"",
			"- [X](y): z",
			"",
		}, "\n"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateLLMSTxt(content); err == nil {
				t.Fatalf("validateLLMSTxt(%s) = nil, want error", name)
			}
		})
	}
}

// TestValidateLLMSFileListItem_Branches covers each malformed-entry branch.
func TestValidateLLMSFileListItem_Branches(t *testing.T) {
	cases := map[string]string{
		"not a link":          "* plain bullet",
		"missing label":       "- [](x)",
		"missing bracket":     "- [label]",
		"unterminated target": "- [label](no-close",
		"empty target":        "- [label]( )",
		"bad notes":           "- [label](x) trailing prose",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateLLMSFileListItem(line); err == nil {
				t.Fatalf("validateLLMSFileListItem(%q) = nil, want error", line)
			}
		})
	}

	if err := validateLLMSFileListItem("- [label](x): notes"); err != nil {
		t.Fatalf("valid entry error: %v", err)
	}
	if err := validateLLMSFileListItem("- [label](x)"); err != nil {
		t.Fatalf("valid bare entry error: %v", err)
	}
}

// TestWriteGeneratedFile_RemainingBranches covers the write success path, the
// write-error path (a directory occupying the target name), the findProjectRoot
// error, and the check-only read-error (missing file) branch.
func TestWriteGeneratedFile_RemainingBranches(t *testing.T) {
	t.Run("write success", func(t *testing.T) {
		dir := newProjectRoot(t, "")
		if err := writeGeneratedFile(llmsFileName, "hello\n", false); err != nil {
			t.Fatalf("write success error: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, llmsFileName))
		if err != nil || string(data) != "hello\n" {
			t.Fatalf("written content = %q, err %v", data, err)
		}
	})

	t.Run("write error dir occupied", func(t *testing.T) {
		dir := newProjectRoot(t, "")
		if err := os.Mkdir(filepath.Join(dir, llmsFullFileName), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := writeGeneratedFile(llmsFullFileName, "content", false); err == nil {
			t.Fatal("write over directory: error = nil, want error")
		}
	})

	t.Run("find root error", func(t *testing.T) {
		t.Chdir(t.TempDir()) // no go.mod
		if err := writeGeneratedFile(llmsFileName, "x", true); err == nil {
			t.Fatal("no project root: error = nil, want error")
		}
	})

	t.Run("check missing file", func(t *testing.T) {
		newProjectRoot(t, "")
		if err := writeGeneratedFile(llmsFileName, "x", true); err == nil {
			t.Fatal("check-only missing file: error = nil, want read error")
		}
	})
}

// TestFindProjectRoot_NotFound covers the walk-to-root failure branch.
func TestFindProjectRoot_NotFound(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := findProjectRoot(); err == nil {
		t.Fatal("findProjectRoot in tree without go.mod: error = nil, want error")
	}
}

// TestReadVersion_ReadFileError covers the branch where the root opens but the
// VERSION file is absent, returning "unknown".
func TestReadVersion_ReadFileError(t *testing.T) {
	dir := t.TempDir() // exists, but has no VERSION file
	if got := readVersion(dir); got != "unknown" {
		t.Fatalf("readVersion(no VERSION) = %q, want unknown", got)
	}
}

// configGoPath resolves internal/config/config.go relative to this test file, so
// the lookup survives the t.Chdir calls other tests in this package make.
func configGoPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate config.go")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "internal", "config", "config.go")
}

// envVarRe matches a LIBGEN_MIRROR or LIBGEN_MCP_* variable name as it appears in
// a Go string literal.
var envVarRe = regexp.MustCompile(`LIBGEN_(?:MCP_[A-Z0-9_]+|MIRROR)`)

// TestConfigEnvVarsCoversConfigGo is the drift gate for the generated
// Configuration table: every environment variable internal/config/config.go names
// must be documented in configEnvVars and vice versa, so adding one to the config
// without documenting it fails here instead of silently leaving a hole in
// llms-full.txt.
func TestConfigEnvVarsCoversConfigGo(t *testing.T) {
	src, err := os.ReadFile(configGoPath(t))
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	documented := map[string]bool{}
	for _, v := range configEnvVars() {
		documented[v.name] = true
	}
	seen := map[string]bool{}
	for _, name := range envVarRe.FindAllString(string(src), -1) {
		if seen[name] {
			continue
		}
		seen[name] = true
		if !documented[name] {
			t.Errorf("%s is named by internal/config/config.go but missing from configEnvVars", name)
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no environment variables in config.go; the scan is broken")
	}
	for name := range documented {
		if !seen[name] {
			t.Errorf("configEnvVars documents %s, which internal/config/config.go never names", name)
		}
	}
}
