package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var errUnexpectedSuccess = errors.New("expected command to fail")

// TestAuditPackage_DetectsPackageCommentProblems verifies package-level
// documentation checks for missing, malformed, and duplicate package comments.
//
// The test builds temporary packages with file comments attached to package
// clauses, then audits them directly without invoking go list. It protects the
// Godoc rule that each package must have one canonical package comment.
func TestAuditPackage_DetectsPackageCommentProblems(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		files      map[string]string
		categories []string
	}{
		{
			name: "missing package doc",
			files: map[string]string{
				"sample.go": "package sample\n",
			},
			categories: []string{categoryPackageDocMissing},
		},
		{
			name: "malformed package doc",
			files: map[string]string{
				"sample.go": "// sample.go describes a file, not the package.\npackage sample\n",
			},
			categories: []string{categoryPackageDocForm},
		},
		{
			name: "multiple package docs",
			files: map[string]string{
				"doc.go":    "// Package sample provides a fixture.\npackage sample\n",
				"sample.go": "// sample.go should not be package documentation.\npackage sample\n",
			},
			categories: []string{categoryPackageDocMultiple, categoryPackageDocForm},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pkg := writePackageFixture(t, "sample", tc.files)

			findings, err := auditPackage(pkg, false)
			if err != nil {
				t.Fatalf("auditPackage() error = %v", err)
			}
			for _, category := range tc.categories {
				if !hasCategory(findings, category) {
					t.Fatalf("missing category %q in %#v", category, findings)
				}
			}
		})
	}
}

// TestAuditPackage_AcceptsCommandPackageDoc verifies that main packages use the
// `Command` documentation form instead of the regular `Package` form.
//
// The fixture represents a command under `cmd/`. The audit should accept the
// package comment and report no package documentation findings.
func TestAuditPackage_AcceptsCommandPackageDoc(t *testing.T) {
	t.Parallel()

	pkg := writePackageFixture(t, "main", map[string]string{
		"main.go": "// Command widget audits widgets.\npackage main\n",
	})
	findings, err := auditPackage(pkg, false)
	if err != nil {
		t.Fatalf("auditPackage() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("auditPackage() findings = %#v, want none", findings)
	}
}

// TestAuditPackage_DetectsExportedSymbolDocumentation verifies checks for
// exported functions, types, constants, and variables.
//
// The fixture intentionally mixes missing and malformed comments. The audit
// must report each exported symbol category so package cleanup can be planned
// without relying on golangci-lint output parsing.
func TestAuditPackage_DetectsExportedSymbolDocumentation(t *testing.T) {
	t.Parallel()

	pkg := writePackageFixture(t, "sample", map[string]string{
		"doc.go": "// Package sample provides a fixture.\npackage sample\n",
		"sample.go": `package sample

const MissingConst = "missing"

// Defaults for sample.
const DefaultMode = "auto"

var MissingVar = "missing"

// Values used by sample.
var DefaultName = "sample"

func MissingFunc() {}

// BadType describes a type with a valid comment.
type BadType struct{}

// Does something without starting with the method name.
func (BadType) Run() {}
`,
	})

	findings, err := auditPackage(pkg, false)
	if err != nil {
		t.Fatalf("auditPackage() error = %v", err)
	}
	for _, category := range []string{categoryConstMissing, categoryConstForm, categoryVarMissing, categoryVarForm, categoryFuncMissing, categoryMethodForm} {
		if !hasCategory(findings, category) {
			t.Fatalf("missing category %q in %#v", category, findings)
		}
	}
}

// TestAuditPackage_AcceptsGroupedConstAndVarDocumentation verifies that
// descriptive comments on grouped exported values are accepted.
//
// Go doc comments allow a grouped const or var declaration to have a group-level
// sentence that describes the set without starting with any one identifier. The
// audit should follow that convention while still requiring ungrouped exported
// values to start with their declared name.
func TestAuditPackage_AcceptsGroupedConstAndVarDocumentation(t *testing.T) {
	t.Parallel()

	pkg := writePackageFixture(t, "sample", map[string]string{
		"doc.go": "// Package sample provides a fixture.\npackage sample\n",
		"sample.go": `package sample

// States accepted by the sample workflow.
const (
	StateOpen = "open"
	StateClosed = "closed"
)

// Shared errors returned by sample operations.
var (
	ErrMissing = errors.New("missing")
	ErrInvalid = errors.New("invalid")
)
`,
	})

	findings, err := auditPackage(pkg, false)
	if err != nil {
		t.Fatalf("auditPackage() error = %v", err)
	}
	if hasCategory(findings, categoryConstForm) || hasCategory(findings, categoryVarForm) {
		t.Fatalf("grouped const/var comments should be accepted: %#v", findings)
	}
}

// TestAuditPackage_IncludeTestsDetectsTestDocs verifies the optional test
// documentation audit for Test, Benchmark, Fuzz, and Example functions.
//
// The fixture places undocumented test functions in a `_test.go` file. The
// audit should ignore them by default and report them when includeTests is true.
func TestAuditPackage_IncludeTestsDetectsTestDocs(t *testing.T) {
	t.Parallel()

	pkg := writePackageFixture(t, "sample", map[string]string{
		"doc.go":    "// Package sample provides a fixture.\npackage sample\n",
		"sample.go": "package sample\n",
		"sample_test.go": `package sample

func TestWidget(t *testing.T) {}
func BenchmarkWidget(b *testing.B) {}
func FuzzWidget(f *testing.F) {}

// ExampleWidget demonstrates widget output.
func ExampleWidget() {
}
`,
	})

	withoutTests, err := auditPackage(pkg, false)
	if err != nil {
		t.Fatalf("auditPackage(includeTests=false) error = %v", err)
	}
	if hasCategory(withoutTests, categoryTestMissing) {
		t.Fatalf("test docs should be ignored by default: %#v", withoutTests)
	}

	withTests, err := auditPackage(pkg, true)
	if err != nil {
		t.Fatalf("auditPackage(includeTests=true) error = %v", err)
	}
	for _, category := range []string{categoryTestMissing, categoryBenchmarkMissing, categoryFuzzMissing, categoryExampleOutput} {
		if !hasCategory(withTests, category) {
			t.Fatalf("missing category %q in %#v", category, withTests)
		}
	}
}

// TestRun_UnsupportedFormat_ReturnsError verifies that the command rejects
// unknown report formats.
//
// The test invokes run through the test seam with an unsupported format. It
// confirms CLI validation fails before any repository scan occurs.
func TestRun_UnsupportedFormat_ReturnsError(t *testing.T) {
	t.Parallel()

	_, err := runForTest([]string{"--format=xml"})
	if err == nil {
		t.Fatal(errUnexpectedSuccess)
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("runForTest() error = %q, want unsupported format", err)
	}
}

func writePackageFixture(t *testing.T, packageName string, files map[string]string) packageInfo {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return packageInfo{Dir: dir, ImportPath: "example.com/" + packageName, Name: packageName}
}

func hasCategory(findings []finding, category string) bool {
	for _, finding := range findings {
		if finding.Category == category {
			return true
		}
	}
	return false
}

func runForTest(args []string) (string, error) {
	var out bytes.Buffer
	err := run(args, &out)
	return out.String(), err
}

// TestAuditPackage_EmptySourceAndParseErrors verifies auditPackage skips
// packages with no matching source files and surfaces read/parse errors.
func TestAuditPackage_EmptySourceAndParseErrors(t *testing.T) {
	t.Parallel()

	mismatched := writePackageFixture(t, "sample", map[string]string{
		"other.go": "package other\n",
	})
	findings, err := auditPackage(mismatched, false)
	if err != nil {
		t.Fatalf("auditPackage(mismatched) error = %v", err)
	}
	if findings != nil {
		t.Fatalf("auditPackage(mismatched) findings = %#v, want nil", findings)
	}

	broken := writePackageFixture(t, "sample", map[string]string{
		"broken.go": "package sample\nfunc (\n",
	})
	if _, brokenErr := auditPackage(broken, false); brokenErr == nil {
		t.Fatal("auditPackage(broken) expected parse error")
	}

	missing := packageInfo{Dir: filepath.Join(t.TempDir(), "nope"), ImportPath: "example.com/missing", Name: "missing"}
	if _, missingErr := auditPackage(missing, false); missingErr == nil {
		t.Fatal("auditPackage(missing dir) expected read error")
	}
}

// TestCheckPackageDocs_MainCommandForm verifies a main package with a
// non-"Command" comment is reported as malformed.
func TestCheckPackageDocs_MainCommandForm(t *testing.T) {
	t.Parallel()

	pkg := writePackageFixture(t, "main", map[string]string{
		"main.go": "// widget does things but omits the Command prefix.\npackage main\n",
	})
	findings, err := auditPackage(pkg, false)
	if err != nil {
		t.Fatalf("auditPackage() error = %v", err)
	}
	if !hasCategory(findings, categoryPackageDocForm) {
		t.Fatalf("expected %q for malformed command doc: %#v", categoryPackageDocForm, findings)
	}
}

// TestCheckExportedDocs_TypeMembers verifies documentation checks for a type's
// constructors, methods, constants, and variables.
func TestCheckExportedDocs_TypeMembers(t *testing.T) {
	t.Parallel()

	pkg := writePackageFixture(t, "sample", map[string]string{
		"doc.go": "// Package sample provides a fixture.\npackage sample\n",
		"sample.go": `package sample

// Widget holds widget state.
type Widget struct{}

func NewWidget() *Widget { return &Widget{} }

func (Widget) Process() {}

const WidgetLimit Widget = Widget{}

var WidgetDefault Widget = Widget{}
`,
	})

	findings, err := auditPackage(pkg, false)
	if err != nil {
		t.Fatalf("auditPackage() error = %v", err)
	}
	for _, category := range []string{categoryFuncMissing, categoryMethodMissing, categoryConstMissing, categoryVarMissing} {
		if !hasCategory(findings, category) {
			t.Fatalf("missing category %q in %#v", category, findings)
		}
	}
}

// TestCheckValueDoc_UnexportedAndForm verifies value checks skip unexported
// groups and flag malformed single-value comments.
func TestCheckValueDoc_UnexportedAndForm(t *testing.T) {
	t.Parallel()

	var findings []finding
	pkg := packageInfo{ImportPath: "example.com/sample", Name: "sample"}

	checkValueDoc(pkg, categoryConstMissing, categoryConstForm, "const", []string{"internalOnly"}, "", &findings)
	if len(findings) != 0 {
		t.Fatalf("unexported names should be skipped: %#v", findings)
	}

	checkValueDoc(pkg, categoryConstMissing, categoryConstForm, "const", []string{"MaxRetries"}, "The maximum retries allowed.", &findings)
	if !hasCategory(findings, categoryConstForm) {
		t.Fatalf("expected %q for malformed const doc: %#v", categoryConstForm, findings)
	}

	var wellFormed []finding
	checkValueDoc(pkg, categoryConstMissing, categoryConstForm, "const", []string{"MaxRetries"}, "MaxRetries is the retry ceiling.", &wellFormed)
	if len(wellFormed) != 0 {
		t.Fatalf("well-formed const doc should be accepted: %#v", wellFormed)
	}
}

// TestParseOptions_RejectsBadFlagsAndArgs verifies parseOptions fails on unknown
// flags and unexpected positional arguments.
func TestParseOptions_RejectsBadFlagsAndArgs(t *testing.T) {
	t.Parallel()

	if _, err := parseOptions([]string{"--nope"}); err == nil {
		t.Fatal("parseOptions(unknown flag) expected an error")
	}
	if _, err := parseOptions([]string{"extra"}); err == nil {
		t.Fatal("parseOptions(positional arg) expected an error")
	}
}

// TestCheckTestDocs_SkipsMethodsAndFlagsForm verifies test-doc checks ignore
// methods and report malformed test comments.
func TestCheckTestDocs_SkipsMethodsAndFlagsForm(t *testing.T) {
	t.Parallel()

	pkg := writePackageFixture(t, "sample", map[string]string{
		"doc.go":    "// Package sample provides a fixture.\npackage sample\n",
		"sample.go": "package sample\n",
		"sample_test.go": `package sample

type helper struct{}

func (helper) Run() {}

func plainHelper() {}

// Something unrelated to the test name.
func TestWidget(t *testing.T) {}
`,
	})

	findings, err := auditPackage(pkg, true)
	if err != nil {
		t.Fatalf("auditPackage() error = %v", err)
	}
	if !hasCategory(findings, categoryTestForm) {
		t.Fatalf("expected %q for malformed test doc: %#v", categoryTestForm, findings)
	}
}

// TestCheckTestFunctionDoc_ExampleWithOutput verifies a well-documented example
// with an Output comment produces no findings.
func TestCheckTestFunctionDoc_ExampleWithOutput(t *testing.T) {
	t.Parallel()

	pkg := packageInfo{ImportPath: "example.com/sample", Name: "sample"}
	src := `package sample

// ExampleWidget demonstrates widget usage.
//
// Output:
// ok
func ExampleWidget() {}
`
	node, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range node.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok {
			fn = f
		}
	}
	var findings []finding
	checkTestFunctionDoc(pkg, "x_test.go", fn, &findings)
	if len(findings) != 0 {
		t.Fatalf("well-documented example should have no findings: %#v", findings)
	}
}

// TestTestDocCategories_AllPrefixes verifies category resolution for every
// supported test-function prefix and the unmatched default.
func TestTestDocCategories_AllPrefixes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		missing string
		ok      bool
	}{
		{"TestMain", categoryTestMissing, true},
		{"TestWidget", categoryTestMissing, true},
		{"BenchmarkWidget", categoryBenchmarkMissing, true},
		{"FuzzWidget", categoryFuzzMissing, true},
		{"ExampleWidget", categoryExampleMissing, true},
		{"helperFunc", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			missing, _, ok := testDocCategories(tc.name)
			if ok != tc.ok || missing != tc.missing {
				t.Fatalf("testDocCategories(%q) = %q, %t; want %q, %t", tc.name, missing, ok, tc.missing, tc.ok)
			}
		})
	}
}

// TestHasExampleOutput_Variants verifies output-comment detection for Output,
// Unordered output, and missing markers.
func TestHasExampleOutput_Variants(t *testing.T) {
	t.Parallel()

	if !hasExampleOutput("demo\nOutput:\nok") {
		t.Fatal("hasExampleOutput did not detect Output:")
	}
	if !hasExampleOutput("demo\nUnordered output:\nok") {
		t.Fatal("hasExampleOutput did not detect Unordered output:")
	}
	if hasExampleOutput("demo with no output marker") {
		t.Fatal("hasExampleOutput falsely detected an output marker")
	}
}

// TestRelativePath_InsideAndOutsideCwd verifies path relativization for empty,
// in-tree, and out-of-tree inputs.
func TestRelativePath_InsideAndOutsideCwd(t *testing.T) {
	t.Parallel()

	if got := relativePath(""); got != "" {
		t.Fatalf("relativePath(empty) = %q", got)
	}
	if got := relativePath("/nonexistent/outside/deep/file.go"); !strings.HasSuffix(got, "file.go") {
		t.Fatalf("relativePath(outside) = %q", got)
	}

	if got := relativePath(filepath.Join("pkg", "file.go")); got != "pkg/file.go" {
		t.Fatalf("relativePath(relative) = %q, want pkg/file.go", got)
	}
}

func sampleFindings() []finding {
	return []finding{
		{Category: categoryFuncMissing, ImportPath: "example.com/b", Package: "b", File: "b.go", Name: "Beta", Detail: "missing func documentation"},
		{Category: categoryTypeMissing, ImportPath: "example.com/a", Package: "a", File: "a.go", Name: "Alpha", Detail: "missing type doc | with pipe"},
	}
}

// TestSortFindings_OrdersByImportPathThenFields verifies deterministic ordering
// across import path, file, category, and name.
func TestSortFindings_OrdersByImportPathThenFields(t *testing.T) {
	t.Parallel()

	findings := []finding{
		{ImportPath: "z", File: "z.go", Category: "b", Name: "n2"},
		{ImportPath: "a", File: "b.go", Category: "a", Name: "n1"},
		{ImportPath: "a", File: "a.go", Category: "b", Name: "n1"},
		{ImportPath: "a", File: "a.go", Category: "a", Name: "n2"},
		{ImportPath: "a", File: "a.go", Category: "a", Name: "n1"},
	}
	sortFindings(findings)

	got := make([]string, 0, len(findings))
	for _, f := range findings {
		got = append(got, f.ImportPath+"/"+f.File+"/"+f.Category+"/"+f.Name)
	}
	want := []string{"a/a.go/a/n1", "a/a.go/a/n2", "a/a.go/b/n1", "a/b.go/a/n1", "z/z.go/b/n2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sortFindings order = %v, want %v", got, want)
	}
}

// TestCountBy_AggregatesByKey verifies countBy tallies findings per key.
func TestCountBy_AggregatesByKey(t *testing.T) {
	t.Parallel()

	findings := []finding{
		{Category: "x"}, {Category: "x"}, {Category: "y"},
	}
	counts := countBy(findings, func(f finding) string { return f.Category })
	if counts["x"] != 2 || counts["y"] != 1 {
		t.Fatalf("countBy() = %v", counts)
	}
}

// TestMd_EscapesPipesAndEmpty verifies md renders empty values as a dash and
// escapes table-breaking pipe characters.
func TestMd_EscapesPipesAndEmpty(t *testing.T) {
	t.Parallel()

	if got := md(""); got != "-" {
		t.Fatalf("md(empty) = %q", got)
	}
	if got := md("a|b"); got != "a\\|b" {
		t.Fatalf("md(pipe) = %q", got)
	}
}

// TestWriteCountTable_EmptyAndLimited verifies the count table renders an empty
// notice and honors the row limit.
func TestWriteCountTable_EmptyAndLimited(t *testing.T) {
	t.Parallel()

	var empty strings.Builder
	writeCountTable(&empty, "## Empty", map[string]int{}, 0)
	if !strings.Contains(empty.String(), "No entries.") {
		t.Fatalf("writeCountTable(empty) = %q", empty.String())
	}

	var limited strings.Builder
	writeCountTable(&limited, "## Limited", map[string]int{"a": 3, "b": 2, "c": 1}, 1)
	out := limited.String()
	if !strings.Contains(out, "| a | 3 |") {
		t.Fatalf("writeCountTable(limited) missing top row: %q", out)
	}
	if strings.Contains(out, "| c | 1 |") {
		t.Fatalf("writeCountTable(limited) exceeded limit: %q", out)
	}
}

// TestRenderMarkdown_WithAndWithoutFindings verifies the Markdown report body
// for populated and empty finding sets.
func TestRenderMarkdown_WithAndWithoutFindings(t *testing.T) {
	t.Parallel()

	populated := renderMarkdown(report{
		Packages:   2,
		Findings:   sampleFindings(),
		ByCategory: countBy(sampleFindings(), func(f finding) string { return f.Category }),
		ByPackage:  countBy(sampleFindings(), func(f finding) string { return f.ImportPath }),
	})
	if !strings.Contains(populated, "# Godoc Audit Report") {
		t.Fatalf("renderMarkdown missing header: %q", populated)
	}
	if !strings.Contains(populated, "missing type doc \\| with pipe") {
		t.Fatalf("renderMarkdown did not escape pipe: %q", populated)
	}

	empty := renderMarkdown(report{Packages: 1})
	if !strings.Contains(empty, "No findings.") {
		t.Fatalf("renderMarkdown(empty) = %q", empty)
	}
}

// TestRenderReport_FormatsAndError verifies renderReport emits Markdown, JSON,
// and an error for an unsupported format.
func TestRenderReport_FormatsAndError(t *testing.T) {
	t.Parallel()

	rep := report{Packages: 1, Findings: sampleFindings()}

	markdown, err := renderReport(rep, formatMarkdown)
	if err != nil {
		t.Fatalf("renderReport(markdown) error = %v", err)
	}
	if !strings.Contains(string(markdown), "# Godoc Audit Report") {
		t.Fatalf("renderReport(markdown) = %q", markdown)
	}

	jsonOut, err := renderReport(rep, formatJSON)
	if err != nil {
		t.Fatalf("renderReport(json) error = %v", err)
	}
	var decoded report
	if decodeErr := json.Unmarshal(jsonOut, &decoded); decodeErr != nil {
		t.Fatalf("renderReport(json) invalid JSON: %v", decodeErr)
	}
	if decoded.Packages != 1 || len(decoded.Findings) != 2 {
		t.Fatalf("renderReport(json) decoded = %#v", decoded)
	}

	if _, xmlErr := renderReport(rep, "xml"); xmlErr == nil {
		t.Fatal("renderReport(xml) expected error")
	}
}

// failingWriter always fails, exercising report write-error paths.
type failingWriter struct{}

// Write reports a synthetic failure for every call.
func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

// setupModule writes a temporary Go module, changes into it, and returns its
// root directory. Files may include subdirectory paths.
func setupModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o750); mkErr != nil {
			t.Fatalf("MkdirAll(%s) error = %v", name, mkErr)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	t.Chdir(dir)
	return dir
}

const documentedModule = "module example.com/tm\n\ngo 1.26\n"

// TestGoExecutable_ReturnsRuntimeGo verifies goExecutable resolves to the Go
// tool inside the active runtime installation.
func TestGoExecutable_ReturnsRuntimeGo(t *testing.T) {
	t.Parallel()

	got := goExecutable()
	want := "go"
	if runtime.GOOS == "windows" {
		want = "go.exe"
	}
	if filepath.Base(got) != want {
		t.Fatalf("goExecutable() = %q, want base %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("goExecutable() = %q is not an existing file: %v", got, err)
	}
}

// TestListPackages_ReturnsModulePackages verifies listPackages reports the
// packages of the enclosing module.
func TestListPackages_ReturnsModulePackages(t *testing.T) {
	setupModule(t, map[string]string{
		"go.mod":   documentedModule,
		"tm.go":    "// Package tm is a fixture.\npackage tm\n\n// Widget is documented.\nfunc Widget() {}\n",
		"sub/s.go": "// Package sub is a fixture.\npackage sub\n",
	})

	packages, err := listPackages()
	if err != nil {
		t.Fatalf("listPackages() error = %v", err)
	}
	if len(packages) < 2 {
		t.Fatalf("listPackages() = %#v, want at least 2 packages", packages)
	}
	found := false
	for _, pkg := range packages {
		if pkg.ImportPath == "example.com/tm" && pkg.Name == "tm" {
			found = true
		}
	}
	if !found {
		t.Fatalf("listPackages() missing root package: %#v", packages)
	}
}

// TestListPackages_OutsideModuleErrors verifies listPackages fails when go list
// cannot resolve a module.
func TestListPackages_OutsideModuleErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := listPackages(); err == nil {
		t.Fatal("listPackages() outside a module expected an error")
	}
}

// TestAuditRepository_IgnoreInternal verifies auditRepository skips internal
// packages when requested.
func TestAuditRepository_IgnoreInternal(t *testing.T) {
	setupModule(t, map[string]string{
		"go.mod":                    documentedModule,
		"tm.go":                     "// Package tm is a fixture.\npackage tm\n",
		"internal/helper/helper.go": "// Package helper is a fixture.\npackage helper\n",
	})

	withInternal, err := auditRepository(options{format: formatMarkdown})
	if err != nil {
		t.Fatalf("auditRepository(include) error = %v", err)
	}
	withoutInternal, err := auditRepository(options{format: formatMarkdown, ignoreInternal: true})
	if err != nil {
		t.Fatalf("auditRepository(ignore) error = %v", err)
	}
	if withoutInternal.Packages >= withInternal.Packages {
		t.Fatalf("ignoreInternal did not skip packages: %d >= %d", withoutInternal.Packages, withInternal.Packages)
	}
	if _, ok := withoutInternal.ByPackage["example.com/tm/internal/helper"]; ok {
		t.Fatalf("ignoreInternal kept an internal package: %#v", withoutInternal.ByPackage)
	}
}

// TestRun_AuditModes verifies run writes reports to stdout and files, honors
// fail-on-findings, and reports scan and write errors.
func TestRun_AuditModes(t *testing.T) {
	t.Run("stdout success", func(t *testing.T) {
		setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n\n// Widget is documented.\nfunc Widget() {}\n",
		})
		out, err := runForTest([]string{"--format=markdown"})
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
		if !strings.Contains(out, "# Godoc Audit Report") {
			t.Fatalf("run() stdout = %q", out)
		}
	})

	t.Run("file output json", func(t *testing.T) {
		dir := setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n",
		})
		outPath := filepath.Join(dir, "report.json")
		if err := run([]string{"--format=json", "--output=" + outPath}, &failingWriter{}); err != nil {
			t.Fatalf("run(file output) error = %v", err)
		}
		data, err := os.ReadFile(outPath) //#nosec G304 -- test fixture path from t.TempDir.
		if err != nil {
			t.Fatalf("ReadFile(report) error = %v", err)
		}
		if !strings.Contains(string(data), "\"packages\"") {
			t.Fatalf("run(file output) content = %q", data)
		}
	})

	t.Run("fail on findings", func(t *testing.T) {
		setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n\nfunc Undocumented() {}\n",
		})
		if _, err := runForTest([]string{"--fail-on-findings"}); err == nil {
			t.Fatal("run(--fail-on-findings) expected an error")
		}
	})

	t.Run("write error", func(t *testing.T) {
		setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n",
		})
		badPath := filepath.Join(t.TempDir(), "missing-dir", "report.md")
		if err := run([]string{"--output=" + badPath}, &failingWriter{}); err == nil {
			t.Fatal("run(bad output path) expected a write error")
		}
	})

	t.Run("stdout write error", func(t *testing.T) {
		setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n",
		})
		if err := run([]string{"--format=markdown"}, &failingWriter{}); err == nil {
			t.Fatal("run(failing stdout) expected a write error")
		}
	})

	t.Run("scan error", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if _, err := runForTest([]string{"--format=markdown"}); err == nil {
			t.Fatal("run() outside a module expected an error")
		}
	})
}
