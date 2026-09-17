package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestGetenvAppliesThePrefix covers the accessor itself: a read is of the
// prefixed name and of nothing else.
func TestGetenvAppliesThePrefix(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "30s")

	if got := Getenv("TIMEOUT"); got != "30s" {
		t.Errorf("Getenv(%q) = %q, want the value of LIBGEN_MCP_TIMEOUT", "TIMEOUT", got)
	}
	if got := EnvName("TIMEOUT"); got != "LIBGEN_MCP_TIMEOUT" {
		t.Errorf("EnvName(%q) = %q, want the prefixed spelling", "TIMEOUT", got)
	}
}

// TestGetenvDoesNotReadTheBareName is the collision this prefix exists to
// prevent: an unprefixed TIMEOUT belongs to whatever else is running in that
// shell, and reading it would mean behaving in a way nobody configured.
func TestGetenvDoesNotReadTheBareName(t *testing.T) {
	t.Setenv("TIMEOUT", "99s")
	if err := os.Unsetenv("LIBGEN_MCP_TIMEOUT"); err != nil {
		t.Fatal(err)
	}

	if got := Getenv("TIMEOUT"); got != "" {
		t.Errorf("Getenv(%q) = %q; it read the unprefixed name, which belongs to another tool", "TIMEOUT", got)
	}
}

// TestTrimmedGetenvTrims covers the variant for settings where a stray space is
// a typo rather than a value.
func TestTrimmedGetenvTrims(t *testing.T) {
	t.Setenv("LIBGEN_MCP_ANNAS_KEY", "  k3y  ")
	if got := TrimmedGetenv("ANNAS_KEY"); got != "k3y" {
		t.Errorf("TrimmedGetenv() = %q, want the value trimmed", got)
	}

	t.Setenv("LIBGEN_MCP_ANNAS_KEY", "   ")
	if got := TrimmedGetenv("ANNAS_KEY"); got != "" {
		t.Errorf("TrimmedGetenv() = %q, want whitespace alone to read as unset", got)
	}
}

// TestKnownEnvNamesAreSortedAndPrefixed pins what an audit or a docs generator
// would enumerate rather than grepping for a prefix.
func TestKnownEnvNamesAreSortedAndPrefixed(t *testing.T) {
	names := KnownEnvNames()
	if len(names) != len(knownNames) {
		t.Fatalf("KnownEnvNames() returned %d names, want %d", len(names), len(knownNames))
	}
	if !slices.IsSorted(names) {
		t.Error("KnownEnvNames() is not sorted; a generated list that reorders itself produces noise diffs")
	}
	for _, name := range names {
		if !strings.HasPrefix(name, EnvPrefix) {
			t.Errorf("%q does not carry %q", name, EnvPrefix)
		}
	}
}

// envAccessors are the functions whose first argument is a setting's short name.
// A call to any of them is a read this package performs, and every one must name
// something knownNames covers.
var envAccessors = []string{
	"Getenv", "TrimmedGetenv", "EnvName",
	"envBool", "envBoolPtr", "envInt", "envInt64", "envFloat", "envDuration",
}

// envVarInLiteral finds a fully spelled variable name inside a string literal,
// which is how the validation messages name a setting to the operator.
var envVarInLiteral = regexp.MustCompile(`LIBGEN_MCP_[A-Z0-9_]+`)

// TestEveryEnvNameIsKnown is the mechanical half of the policy, and it is the
// reason knownNames is worth keeping: a list maintained by hand drifts, and a
// list a test walks the syntax to check does not.
//
// It reads this package's own source and fails on three things: a setting read
// under a name knownNames does not carry, a variable named in a message that no
// longer exists, and a read that goes around the accessor to os.Getenv with a
// prefixed name spelled out.
//
// Adding a variable therefore means adding it here as well as reading it, which
// is the point — the policy is checked rather than remembered.
func TestEveryEnvNameIsKnown(t *testing.T) {
	// The files are listed and parsed one by one rather than with
	// parser.ParseDir, which is deprecated: it cannot see build tags, and here
	// the set of files is simple enough to name outright.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	parsed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		checkFileEnvNames(t, name, file)
		parsed++
	}
	if parsed == 0 {
		t.Fatal("no source files were parsed, so this test checked nothing")
	}
}

// checkFileEnvNames applies the three rules above to one parsed file.
func checkFileEnvNames(t *testing.T, path string, file *ast.File) {
	t.Helper()

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			checkAccessorCall(t, path, node)
		case *ast.BasicLit:
			checkLiteralNames(t, path, node)
		}
		return true
	})
}

// checkAccessorCall asserts a read names a setting knownNames covers, and that
// it is not a prefixed name handed to the accessor by mistake.
func checkAccessorCall(t *testing.T, path string, call *ast.CallExpr) {
	t.Helper()

	fn, ok := call.Fun.(*ast.Ident)
	if !ok || !slices.Contains(envAccessors, fn.Name) || len(call.Args) == 0 {
		return
	}
	name, ok := stringLiteral(call.Args[0])
	if !ok {
		return
	}
	if strings.HasPrefix(name, EnvPrefix) {
		t.Errorf("%s: %s(%q) passes a prefixed name; the accessor applies %s itself",
			path, fn.Name, name, EnvPrefix)
		return
	}
	if !slices.Contains(knownNames, name) {
		t.Errorf("%s: %s(%q) reads a setting missing from knownNames in env_name.go",
			path, fn.Name, name)
	}
}

// checkLiteralNames asserts that a fully spelled variable in any string literal
// is one this package actually reads. It also catches the bypass: an
// os.Getenv("LIBGEN_MCP_…") would put the prefixed name in a literal here.
func checkLiteralNames(t *testing.T, path string, lit *ast.BasicLit) {
	t.Helper()

	if lit.Kind != token.STRING {
		return
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return
	}
	for _, match := range envVarInLiteral.FindAllString(value, -1) {
		suffix := strings.TrimPrefix(match, EnvPrefix)
		if suffix == "" {
			continue // EnvPrefix itself.
		}
		if !slices.Contains(knownNames, suffix) {
			t.Errorf("%s: %q names %s, which is not in knownNames in env_name.go",
				path, value, match)
		}
	}
}

// stringLiteral returns the value of expr when it is an untagged string literal.
func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// TestEveryKnownNameIsRead is the other direction: a name on the list that
// nothing reads is a setting an operator would set to no effect, and a docs
// generator would publish it.
//
// It searches every non-test file in the package rather than config.go alone.
// That used to be the same thing and stopped being it when the dotenv loader
// arrived with a setting of its own; keeping the narrow spelling would have made
// this test refuse a variable that is read, which is the failure mode that gets
// a test deleted rather than fixed.
func TestEveryKnownNameIsRead(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("reading %s: %v", name, readErr)
		}
		body.Write(source)
	}
	if body.Len() == 0 {
		t.Fatal("no source files were read, so this test checked nothing")
	}

	for _, name := range knownNames {
		if !strings.Contains(body.String(), strconv.Quote(name)) {
			t.Errorf("knownNames carries %q but nothing in this package reads it; "+
				"either wire it up or take it off the list", name)
		}
	}
}
