// gen_test.go raises statement coverage of the generator by exercising the
// orchestration (run), the in-memory MCP session (newSession/listTools), every
// llms-full.txt writer against real tool metadata, and the remaining branches of
// the pure formatting helpers and validators. All tests are offline: listTools
// builds an in-memory MCP server with the real tool set and performs no network
// I/O.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
// natural search/get_details/download workflow order.
func TestListTools_ReturnsOrderedRealTools(t *testing.T) {
	toolList, err := listTools()
	if err != nil {
		t.Fatalf("listTools() error: %v", err)
	}
	if len(toolList) != 3 {
		t.Fatalf("listTools() returned %d tools, want 3", len(toolList))
	}
	want := []string{"search", "get_details", "download"}
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
	for _, want := range []string{"## Tools", "### search", "### get_details", "### download", "## Configuration"} {
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
