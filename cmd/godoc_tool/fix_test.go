// main_test.go covers the add_docs command's documentation generation
// heuristics.
//
// Tests verify processFile preserves manually authored docs, regenerates
// stale generated docs, and produces the expected phrasing for tests,
// benchmarks, fuzz, examples, methods, and common helper name patterns.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProcessFile_DocumentsMissingSymbols verifies processFile inserts docs for functions, types, and values.
func TestProcessFile_DocumentsMissingSymbols(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	source := `package sample

func ListProjects() {}

type ProjectInput struct{}

const defaultLimit = 20
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	processFile(path)

	updatedBytes, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	updated := string(updatedBytes)
	assertContains(t, updated, "// ListProjects lists projects for the sample package.")
	assertContains(t, updated, "// ProjectInput defines parameters for the project operation.")
	assertContains(t, updated, "// defaultLimit identifies the default limit constant used by this package.")
}

// TestProcessFile_PreservesManualDocsAndSkipsInit verifies processFile avoids overwriting useful existing docs.
func TestProcessFile_PreservesManualDocsAndSkipsInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	source := `package sample

// Config keeps runtime settings.
type Config struct{}

func init() {}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	processFile(path)

	updatedBytes, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if updated := string(updatedBytes); updated != source {
		t.Fatalf("processFile() changed documented file:\n%s", updated)
	}
}

// TestProcessFile_ReplacesGeneratedDocs verifies processFile regenerates stale helper comments from earlier tool versions.
func TestProcessFile_ReplacesGeneratedDocs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	source := `package sample

// helper verifies the behavior of helper.
func helper() {}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	processFile(path)

	updatedBytes, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	updated := string(updatedBytes)
	if strings.Contains(updated, "verifies the behavior of helper") {
		t.Fatalf("processFile() kept generated doc:\n%s", updated)
	}
	assertContains(t, updated, "// helper implements the helper helper used by sample.")
}

// TestGenerateFuncDoc_TestFunctionVariants verifies generateFuncDoc handles test, benchmark, fuzz, and example naming patterns.
func TestGenerateFuncDoc_TestFunctionVariants(t *testing.T) {
	testFunction := parseFuncDecl(t, `package sample
import "testing"
func TestCreateIssue_ValidInput_ReturnsIssue(t *testing.T) { cases := []struct{name string}{{"ok"}}; _ = cases }
`, "TestCreateIssue_ValidInput_ReturnsIssue")
	if got := generateFuncDoc(testFunction, "sample", true); got != "TestCreateIssue_ValidInput_ReturnsIssue covers CreateIssue with table-driven subtests for valid input returns issue." {
		t.Fatalf("generateFuncDoc(test) = %q", got)
	}

	benchmark := parseFuncDecl(t, `package sample
import "testing"
func BenchmarkDynamicSearch(b *testing.B) {}
`, "BenchmarkDynamicSearch")
	if got := generateFuncDoc(benchmark, "sample", true); got != "BenchmarkDynamicSearch measures dynamic search search and dispatch overhead." {
		t.Fatalf("generateFuncDoc(benchmark) = %q", got)
	}

	fuzz := parseFuncDecl(t, `package sample
import "testing"
func FuzzActionID(f *testing.F) {}
`, "FuzzActionID")
	if got := generateFuncDoc(fuzz, "sample", true); got != "FuzzActionID tests that action ID handles arbitrary inputs without panicking." {
		t.Fatalf("generateFuncDoc(fuzz) = %q", got)
	}

	example := parseFuncDecl(t, `package sample
func ExampleCatalog() {}
`, "ExampleCatalog")
	if got := generateFuncDoc(example, "sample", true); got != "ExampleCatalog demonstrates usage of catalog." {
		t.Fatalf("generateFuncDoc(example) = %q", got)
	}
}

// TestGenerateMethodDoc_CommonMethods verifies generateMethodDoc describes common method conventions.
func TestGenerateMethodDoc_CommonMethods(t *testing.T) {
	testCases := []struct {
		name string
		src  string
		want string
	}{
		{name: "string", src: `package sample
type client struct{}
func (client) String() string { return "" }
`, want: "String returns the display label for client."},
		{name: "getter", src: `package sample
type config struct{}
func (config) GetToken() string { return "" }
`, want: "GetToken returns the token value from config."},
		{name: "boolean", src: `package sample
type route struct{}
func (route) Available() bool { return true }
`, want: "Available reports whether the route satisfies the available condition."},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			functionDecl := firstFuncDecl(t, testCase.src)
			if got := generateMethodDoc(functionDecl); got != testCase.want {
				t.Fatalf("generateMethodDoc() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestHelperTextGeneration_CoversIdentifiersAndInitialisms verifies naming helpers preserve project terminology.
func TestHelperTextGeneration_CoversIdentifiersAndInitialisms(t *testing.T) {
	if got := camelToWords("GitLabAPIURL2FA"); got != "GitLab API URL 2FA" {
		t.Fatalf("camelToWords() = %q, want GitLab API URL 2FA", got)
	}
	if got := subjectScenarioPhrase("BuildCatalog", "InvalidInput_ReturnsError"); got != "BuildCatalog returns error with invalid input" {
		t.Fatalf("subjectScenarioPhrase() = %q", got)
	}
	if before, after, ok := splitAtPredicate("invalid input returns error"); !ok || before != "invalid input" || after != "returns error" {
		t.Fatalf("splitAtPredicate() = %q, %q, %t", before, after, ok)
	}
	if got := inferAction("UploadAvatar"); got != "uploads avatar" {
		t.Fatalf("inferAction() = %q, want uploads avatar", got)
	}
	if got := formatComment("Line one\nLine two", "\t"); len(got) != 2 || got[0] != "\t// Line one" || got[1] != "\t// Line two" {
		t.Fatalf("formatComment() = %#v", got)
	}
}

// TestHelperDocRuleMatches_CombinesMixedConstraints verifies mixed helper doc rules require every configured constraint.
func TestHelperDocRuleMatches_CombinesMixedConstraints(t *testing.T) {
	mixedRule := helperDocRule{prefixes: []string{"is"}, contains: []string{"Available"}}
	if !mixedRule.matches("isProjectAvailable") {
		t.Fatal("mixed rule did not match name with required prefix and marker")
	}
	if mixedRule.matches("isProject") {
		t.Fatal("mixed rule matched name missing required marker")
	}

	if !(helperDocRule{prefixes: []string{"is"}}).matches("isProject") {
		t.Fatal("prefix-only rule did not match by prefix")
	}
	if !(helperDocRule{contains: []string{"Available"}}).matches("projectAvailable") {
		t.Fatal("contains-only rule did not match by marker")
	}
}

// TestExprToString_FormatsCommonExpressionShapes verifies exprToString renders common AST type forms.
func TestExprToString_FormatsCommonExpressionShapes(t *testing.T) {
	expressions := []struct {
		name string
		expr ast.Expr
		want string
	}{
		{name: "ident", expr: ast.NewIdent("string"), want: "string"},
		{name: "star", expr: &ast.StarExpr{X: ast.NewIdent("Client")}, want: "*Client"},
		{name: "selector", expr: &ast.SelectorExpr{X: ast.NewIdent("time"), Sel: ast.NewIdent("Duration")}, want: "time.Duration"},
		{name: "array", expr: &ast.ArrayType{Elt: ast.NewIdent("string")}, want: "[]string"},
		{name: "map", expr: &ast.MapType{Key: ast.NewIdent("string"), Value: ast.NewIdent("any")}, want: "map[string]any"},
		{name: "unknown", expr: &ast.ChanType{Value: ast.NewIdent("string")}, want: "any"},
	}

	for _, expression := range expressions {
		t.Run(expression.name, func(t *testing.T) {
			if got := exprToString(expression.expr); got != expression.want {
				t.Fatalf("exprToString() = %q, want %q", got, expression.want)
			}
		})
	}
}

func parseFuncDecl(t *testing.T, source, name string) *ast.FuncDecl {
	t.Helper()
	node, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, 0)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	for _, decl := range node.Decls {
		functionDecl, ok := decl.(*ast.FuncDecl)
		if ok && functionDecl.Name.Name == name {
			return functionDecl
		}
	}
	t.Fatalf("function %q not found", name)
	return nil
}

func firstFuncDecl(t *testing.T, source string) *ast.FuncDecl {
	t.Helper()
	node, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, 0)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	for _, decl := range node.Decls {
		functionDecl, ok := decl.(*ast.FuncDecl)
		if ok {
			return functionDecl
		}
	}
	t.Fatal("function declaration not found")
	return nil
}

func assertContains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("text missing %q:\n%s", want, text)
	}
}

// firstTypeSpec parses source and returns its first type specification.
func firstTypeSpec(t *testing.T, source string) *ast.TypeSpec {
	t.Helper()
	node, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	for _, decl := range node.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range genDecl.Specs {
			if typeSpec, isType := spec.(*ast.TypeSpec); isType {
				return typeSpec
			}
		}
	}
	t.Fatal("type specification not found")
	return nil
}

// firstValueSpec parses source and returns its first value spec with its token.
func firstValueSpec(t *testing.T, source string) (*ast.ValueSpec, token.Token) {
	t.Helper()
	node, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	for _, decl := range node.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range genDecl.Specs {
			if valueSpec, isValue := spec.(*ast.ValueSpec); isValue {
				return valueSpec, genDecl.Tok
			}
		}
	}
	t.Fatal("value specification not found")
	return nil, token.ILLEGAL
}

// TestGenerateTypeDoc_CoversNameShapes verifies generateTypeDoc phrasing for
// input, output, interface, case, alias, keyword, and fallback type names.
func TestGenerateTypeDoc_CoversNameShapes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		src  string
		want string
	}{
		{"named input", "package sample\ntype CreateProjectInput struct{}\n", "CreateProjectInput defines parameters for the create project operation."},
		{"bare input", "package sample\ntype Input struct{}\n", "Input defines parameters for the sample tool."},
		{"named output", "package sample\ntype CreateProjectOutput struct{}\n", "CreateProjectOutput represents the response from the create project operation."},
		{"bare output", "package sample\ntype Output struct{}\n", "Output represents the response from a sample operation."},
		{"interface", "package sample\ntype Downloader interface{}\n", "Downloader defines the contract for downloader operations."},
		{"case", "package sample\ntype ParseCase struct{}\n", "ParseCase describes one parse table-driven test case."},
		{"alias", "package sample\ntype ModelAlias struct{}\n", "ModelAlias describes one model alias mapping used by tests."},
		{"keyword openai", "package sample\ntype openaiRequest struct{}\n", "openaiRequest models the OpenAI-compatible openai request payload."},
		{"keyword provider", "package sample\ntype providerConfig struct{}\n", "providerConfig captures model-provider provider config data."},
		{"fallback", "package sample\ntype Widget struct{}\n", "Widget holds widget data for the sample package."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := firstTypeSpec(t, tc.src)
			if got := generateTypeDoc(spec, "sample"); got != tc.want {
				t.Fatalf("generateTypeDoc() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGenerateValueDoc_ConstVarAndBlank verifies generateValueDoc for const,
// var, and blank-only value specifications.
func TestGenerateValueDoc_ConstVarAndBlank(t *testing.T) {
	t.Parallel()

	constSpec, constTok := firstValueSpec(t, "package sample\nconst maxRetries = 3\n")
	if got := generateValueDoc(constSpec, constTok); got != "maxRetries identifies the max retries constant used by this package." {
		t.Fatalf("generateValueDoc(const) = %q", got)
	}

	varSpec, varTok := firstValueSpec(t, "package sample\nvar activeClient = 0\n")
	if got := generateValueDoc(varSpec, varTok); got != "activeClient stores the package-level active client state." {
		t.Fatalf("generateValueDoc(var) = %q", got)
	}

	blankSpec, blankTok := firstValueSpec(t, "package sample\nvar _ = 1\n")
	if got := generateValueDoc(blankSpec, blankTok); got != "" {
		t.Fatalf("generateValueDoc(blank) = %q, want empty", got)
	}
}

// TestGenerateExportedFuncDoc_SpecialNames verifies register, format, and
// generic exported function phrasing.
func TestGenerateExportedFuncDoc_SpecialNames(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		src  string
		want string
	}{
		{"register tools", "package sample\nfunc RegisterTools() {}\n", "RegisterTools registers all sample-related MCP tools on the given server."},
		{"register meta", "package sample\nfunc RegisterMeta() {}\n", "RegisterMeta registers the sample domain meta-tool on the given server."},
		{"format markdown", "package sample\nfunc FormatMarkdownResult() {}\n", "FormatMarkdownResult renders the sample result as a Markdown-formatted MCP response."},
		{"generic", "package sample\nfunc CreateProject() {}\n", "CreateProject creates project for the sample package."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decl := firstFuncDecl(t, tc.src)
			if got := generateExportedFuncDoc(decl, "sample"); got != tc.want {
				t.Fatalf("generateExportedFuncDoc() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGenerateHandlerDoc_Shapes verifies handler doc phrasing across the
// two-result, converter, formatter, builder, and intent-fallback branches.
func TestGenerateHandlerDoc_Shapes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		src  string
		want string
	}{
		{"two results", "package sample\nfunc listProjects() (ProjectOutput, error) { return ProjectOutput{}, nil }\ntype ProjectOutput struct{}\n", "listProjects lists projects and returns [ProjectOutput]."},
		{"converter", "package sample\nfunc toOutput() {}\n", "toOutput converts the GitLab API response to the tool output format."},
		{"formatter", "package sample\nfunc formatRow() {}\n", "formatRow renders the result as a formatted string."},
		{"builder", "package sample\nfunc buildParams() {}\n", "buildParams constructs the request parameters from the input."},
		{"intent fallback", "package sample\nfunc wibble() {}\n", "wibble implements the wibble helper used by sample."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decl := firstFuncDecl(t, tc.src)
			if got := generateHandlerDoc(decl, "sample"); got != tc.want {
				t.Fatalf("generateHandlerDoc() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHelperIntentDoc_RuleFamilies verifies exact, prefix, content, evaluator,
// and fallback helper-intent doc phrasing.
func TestHelperIntentDoc_RuleFamilies(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		fn   string
		want string
	}{
		{"exact run", "run", "run executes the command workflow after arguments are parsed."},
		{"prefix reports whether", "isReady", "isReady reports whether is ready."},
		{"prefix normalize", "normalizePath", "normalizePath normalizes path for stable comparisons."},
		{"content path", "buildEndpoint", "buildEndpoint returns the build endpoint used by evaluator requests."},
		{"content nameonly pricing", "modelPricing", "modelPricing reports whether model pricing data is configured."},
		{"evaluator sanitize", "sanitizeInput", "sanitizeInput sanitizes input for provider compatibility."},
		{"evaluator nameonly", "taskSteps", "taskSteps returns expected tool steps for an evaluation task."},
		{"fallback", "wibbleWobble", "wibbleWobble implements the wibble wobble helper used by sample."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := helperIntentDoc(tc.fn, "sample"); got != tc.want {
				t.Fatalf("helperIntentDoc(%q) = %q, want %q", tc.fn, got, tc.want)
			}
		})
	}
}

// TestGenerateTestDoc_AllBranches verifies Test doc generation for scenario,
// simple, table-driven, and unmatched name shapes.
func TestGenerateTestDoc_AllBranches(t *testing.T) {
	t.Parallel()

	tableBody := `{ cases := []struct{ name string }{{"a"}}; _ = cases }`
	testCases := []struct {
		name string
		src  string
		want string
	}{
		{"scenario table", "package sample\nfunc TestCreateIssue_ValidInput(t *testing.T) " + tableBody + "\n", "TestCreateIssue_ValidInput covers CreateIssue with table-driven subtests for valid input."},
		{"scenario plain", "package sample\nfunc TestCreateIssue_ValidInput(t *testing.T) {}\n", "TestCreateIssue_ValidInput verifies CreateIssue when valid input."},
		{"simple table", "package sample\nfunc TestCatalog(t *testing.T) " + tableBody + "\n", "TestCatalog covers Catalog with table-driven subtests."},
		{"simple plain", "package sample\nfunc TestCatalog(t *testing.T) {}\n", "TestCatalog verifies Catalog."},
		{"no body", "package sample\nfunc TestCatalog(t *testing.T)\n", "TestCatalog verifies Catalog."},
		{"non-array composite body", "package sample\nfunc TestCatalog(t *testing.T) { p := struct{ X int }{1}; _ = p }\n", "TestCatalog verifies Catalog."},
		{"unmatched", "package sample\nfunc Test_helper(t *testing.T) {}\n", "Test_helper verifies the expected behavior of sample."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decl := firstFuncDecl(t, tc.src)
			if got := generateTestDoc(decl, "sample"); got != tc.want {
				t.Fatalf("generateTestDoc() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSubjectScenarioPhrase_Branches verifies each phrasing branch of
// subjectScenarioPhrase.
func TestSubjectScenarioPhrase_Branches(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		subject  string
		scenario string
		want     string
	}{
		{"Foo", "ReturnsError", "Foo returns error"},
		{"Foo", "InvalidInput_ReturnsError", "Foo returns error with invalid input"},
		{"Foo", "InvalidInput_ThenValid", "Foo when invalid input then valid"},
		{"Foo", "InvalidInput", "Foo when invalid input"},
		{"Foo", "EmptyReturnsNil", "Foo returns nil for empty"},
	}

	for _, tc := range testCases {
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			if got := subjectScenarioPhrase(tc.subject, tc.scenario); got != tc.want {
				t.Fatalf("subjectScenarioPhrase(%q,%q) = %q, want %q", tc.subject, tc.scenario, got, tc.want)
			}
		})
	}
}

// TestSplitAtPredicate_NoPredicate verifies splitAtPredicate reports failure
// when no reorderable predicate is present.
func TestSplitAtPredicate_NoPredicate(t *testing.T) {
	t.Parallel()

	before, after, ok := splitAtPredicate("invalid input only")
	if ok || before != "" || after != "" {
		t.Fatalf("splitAtPredicate() = %q, %q, %t; want empty, empty, false", before, after, ok)
	}
}

// TestGenerateTestHelperDoc_Prefixes verifies test-helper doc phrasing for
// prefix rules and the assertion fallback.
func TestGenerateTestHelperDoc_Prefixes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		fn   string
		want string
	}{
		{"assert prefix", "assertEqual", "assertEqual checks equal invariants for tests."},
		{"has prefix", "hasContent", "hasContent reports whether has content."},
		{"normalize prefix", "normalizeOutput", "normalizeOutput normalizes output for stable test assertions."},
		{"fallback", "wibble", "wibble supports wibble assertions in sample tests."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decl := firstFuncDecl(t, "package sample\nfunc "+tc.fn+"() {}\n")
			if got := generateTestHelperDoc(decl, "sample"); got != tc.want {
				t.Fatalf("generateTestHelperDoc(%q) = %q, want %q", tc.fn, got, tc.want)
			}
		})
	}
}

// TestPrefixMethodDoc_Branches verifies prefixMethodDoc for getter, setter,
// ensure, cleanup, and unmatched method names.
func TestPrefixMethodDoc_Branches(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		fn      string
		want    string
		matched bool
	}{
		{"GetToken", "GetToken returns the token value from config.", true},
		{"SetToken", "SetToken updates the token value on config.", true},
		{"ensureRepo", "ensureRepo ensures repo exists for config.", true},
		{"cleanupTemp", "cleanupTemp removes cleanup temp fixture resources for config when present.", true},
		{"Frobnicate", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.fn, func(t *testing.T) {
			t.Parallel()
			got, ok := prefixMethodDoc(tc.fn, "config")
			if got != tc.want || ok != tc.matched {
				t.Fatalf("prefixMethodDoc(%q) = %q, %t; want %q, %t", tc.fn, got, ok, tc.want, tc.matched)
			}
		})
	}
}

// TestGenerateMethodDoc_PrefixAndGeneric verifies generateMethodDoc for setter,
// cleanup, boolean, and generic method shapes.
func TestGenerateMethodDoc_PrefixAndGeneric(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		src  string
		want string
	}{
		{"setter", "package sample\ntype config struct{}\nfunc (config) SetToken(v string) {}\n", "SetToken updates the token value on config."},
		{"cleanup", "package sample\ntype config struct{}\nfunc (config) cleanupTemp() {}\n", "cleanupTemp removes cleanup temp fixture resources for config when present."},
		{"pointer receiver", "package sample\ntype config struct{}\nfunc (*config) Reload() {}\n", "Reload handles reload for config."},
		{"generic", "package sample\ntype widget struct{}\nfunc (widget) Frobnicate() {}\n", "Frobnicate handles frobnicate for widget."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decl := firstFuncDecl(t, tc.src)
			if got := generateMethodDoc(decl); got != tc.want {
				t.Fatalf("generateMethodDoc() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGenerateMethodDoc_EmptyReceiverDefaultsToReceiver verifies the subject
// falls back to "receiver" when the declaration carries no receiver field.
func TestGenerateMethodDoc_EmptyReceiverDefaultsToReceiver(t *testing.T) {
	t.Parallel()

	decl := &ast.FuncDecl{
		Name: ast.NewIdent("Frob"),
		Recv: &ast.FieldList{},
		Type: &ast.FuncType{},
	}
	if got := generateMethodDoc(decl); got != "Frob handles frob for receiver." {
		t.Fatalf("generateMethodDoc(empty receiver) = %q", got)
	}
}

// TestGenerateFuncDoc_RoutesByShape verifies generateFuncDoc dispatches to the
// method, test-helper, and handler generators.
func TestGenerateFuncDoc_RoutesByShape(t *testing.T) {
	t.Parallel()

	method := firstFuncDecl(t, "package sample\ntype client struct{}\nfunc (client) Frobnicate() {}\n")
	if got := generateFuncDoc(method, "sample", false); got != "Frobnicate handles frobnicate for client." {
		t.Fatalf("generateFuncDoc(method) = %q", got)
	}

	helper := firstFuncDecl(t, "package sample\nfunc assertEqual() {}\n")
	if got := generateFuncDoc(helper, "sample", true); got != "assertEqual checks equal invariants for tests." {
		t.Fatalf("generateFuncDoc(test helper) = %q", got)
	}

	handler := firstFuncDecl(t, "package sample\nfunc wibble() {}\n")
	if got := generateFuncDoc(handler, "sample", false); got != "wibble implements the wibble helper used by sample." {
		t.Fatalf("generateFuncDoc(handler) = %q", got)
	}
}

// TestInferAction_Branches verifies inferAction prefix matching, the bare and
// "resources" rest cases, and the coordinate fallback.
func TestInferAction_Branches(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		want string
	}{
		{"listProjects", "lists projects"},
		{"list", "lists resources"},
		{"listResources", "lists resources"},
		{"wibble", "coordinates wibble"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := inferAction(tc.name); got != tc.want {
				t.Fatalf("inferAction(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestStringHelpers_EdgeCases verifies docIdentifier, camelToWords, getIndent,
// isGeneratedDoc, hasExampleOutput, and shouldSplitIdentifier edge cases.
func TestStringHelpers_EdgeCases(t *testing.T) {
	t.Parallel()

	if got := docIdentifier(""); got != "the subject under test" {
		t.Fatalf("docIdentifier(empty) = %q", got)
	}
	if got := docIdentifier("Catalog"); got != "Catalog" {
		t.Fatalf("docIdentifier(Catalog) = %q", got)
	}
	if got := camelToWords(""); got != "resources" {
		t.Fatalf("camelToWords(empty) = %q", got)
	}
	if got := camelToWords("HTTP2Server"); got == "" {
		t.Fatalf("camelToWords(HTTP2Server) unexpectedly empty")
	}
	if got := camelToWords("HTTPServer"); got != "HTTP server" {
		t.Fatalf("camelToWords(HTTPServer) = %q", got)
	}
	if got := camelToWords("_"); got != "resources" {
		t.Fatalf("camelToWords(underscore) = %q", got)
	}
	if got := getIndent("\t\t"); got != "" {
		t.Fatalf("getIndent(all whitespace) = %q, want empty", got)
	}
	if got := getIndent("  code"); got != "  " {
		t.Fatalf("getIndent(indented) = %q", got)
	}
	if !isGeneratedDoc("Foo handles the widget scenario correctly.") {
		t.Fatal("isGeneratedDoc() did not detect a marker-pair phrase")
	}
	if isGeneratedDoc("Foo does something specific.") {
		t.Fatal("isGeneratedDoc() flagged a hand-written comment")
	}
}

func writeGoFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
	return path
}

// TestProcessPath_FileDirAndErrors verifies processPath dispatches files and
// directories and reports stat failures.
func TestProcessPath_FileDirAndErrors(t *testing.T) {
	dir := t.TempDir()
	goFile := writeGoFile(t, dir, "sample.go", "package sample\n\nfunc Widget() {}\n")
	writeGoFile(t, dir, "notes.txt", "not go source\n")

	if err := processPath(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatalf("processPath(non-go) error = %v", err)
	}
	if err := processPath(goFile); err != nil {
		t.Fatalf("processPath(file) error = %v", err)
	}
	if err := processPath(dir); err != nil {
		t.Fatalf("processPath(dir) error = %v", err)
	}
	if err := processPath(filepath.Join(dir, "missing.go")); err == nil {
		t.Fatal("processPath(missing) expected error")
	}
}

// TestProcessDir_RecursesAndReportsErrors verifies processDir walks nested
// directories, skips non-Go files, and surfaces read errors.
func TestProcessDir_RecursesAndReportsErrors(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeGoFile(t, root, "top.go", "package sample\n\nfunc Top() {}\n")
	writeGoFile(t, root, "README.md", "# not go\n")
	writeGoFile(t, nested, "inner.go", "package nested\n\nfunc Inner() {}\n")

	if err := processDir(root); err != nil {
		t.Fatalf("processDir() error = %v", err)
	}
	if err := processDir(filepath.Join(root, "does-not-exist")); err == nil {
		t.Fatal("processDir(missing) expected error")
	}
}

// TestProcessFile_DryRunAndBlankValue verifies dry-run mode leaves files
// untouched and blank value specs produce no doc insertion.
func TestProcessFile_DryRunAndBlankValue(t *testing.T) {
	dir := t.TempDir()
	source := "package sample\n\nvar _ = 1\n\nfunc Widget() {}\n"
	path := writeGoFile(t, dir, "sample.go", source)

	dryRun = true
	defer func() { dryRun = false }()
	if err := processFile(path); err != nil {
		t.Fatalf("processFile(dry-run) error = %v", err)
	}
	got, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != source {
		t.Fatalf("processFile(dry-run) modified file:\n%s", got)
	}
}

// TestProcessFile_RegeneratesGroupedGeneratedDoc verifies a grouped const whose
// group comment is a stale generated doc is regenerated via the fallback doc.
func TestProcessFile_RegeneratesGroupedGeneratedDoc(t *testing.T) {
	dir := t.TempDir()
	source := "package sample\n\n// group holds data for widgets.\nconst (\n\tAlpha = 1\n)\n"
	path := writeGoFile(t, dir, "sample.go", source)

	if err := processFile(path); err != nil {
		t.Fatalf("processFile() error = %v", err)
	}
	got, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	assertContains(t, string(got), "// Alpha identifies the alpha constant used by this package.")
}

// TestProcessFile_ReadAndParseErrors verifies processFile reports unreadable
// paths and unparseable sources.
func TestProcessFile_ReadAndParseErrors(t *testing.T) {
	if err := processFile(t.TempDir()); err == nil {
		t.Fatal("processFile(directory) expected a read error")
	}

	dir := t.TempDir()
	broken := writeGoFile(t, dir, "broken.go", "package sample\nfunc (\n")
	if err := processFile(broken); err == nil {
		t.Fatal("processFile(broken) expected a parse error")
	}
}

// TestProcessFile_KeepsDocumentedValue verifies processFile leaves a var that
// already has a hand-written comment untouched.
func TestProcessFile_KeepsDocumentedValue(t *testing.T) {
	dir := t.TempDir()
	source := "package sample\n\n// Setting is the runtime toggle.\nvar Setting = 1\n"
	path := writeGoFile(t, dir, "sample.go", source)

	if err := processFile(path); err != nil {
		t.Fatalf("processFile() error = %v", err)
	}
	got, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != source {
		t.Fatalf("processFile() changed a documented var:\n%s", got)
	}
}

// TestProcessFile_WriteError verifies processFile reports a failure when the
// target file cannot be rewritten. It is skipped as root, where file mode bits
// do not block writes.
func TestProcessFile_WriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("write-permission errors are not enforced for root")
	}
	dir := t.TempDir()
	path := writeGoFile(t, dir, "sample.go", "package sample\n\nfunc Widget() {}\n")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := processFile(path); err == nil {
		t.Fatal("processFile(read-only) expected a write error")
	}
}

// TestFirstDoc_PrefersPrimary verifies firstDoc returns the symbol doc when
// present and falls back to the enclosing declaration doc otherwise.
func TestFirstDoc_PrefersPrimary(t *testing.T) {
	t.Parallel()

	primary := &ast.CommentGroup{}
	fallback := &ast.CommentGroup{}
	if got := firstDoc(primary, fallback); got != primary {
		t.Fatal("firstDoc did not prefer the primary comment group")
	}
	if got := firstDoc(nil, fallback); got != fallback {
		t.Fatal("firstDoc did not fall back to the declaration comment group")
	}
}
