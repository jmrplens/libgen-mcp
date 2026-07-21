package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

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
