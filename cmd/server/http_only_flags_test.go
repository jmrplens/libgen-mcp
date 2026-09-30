package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
)

// nonLiteralFlagNames are the expressions a registration in this package may
// name its flag by other than a string literal, keyed by the function the
// registration is in and the expression, each with the table that classifies
// what it names. A registration by any other expression, or by the same one in
// another function, fails [TestHTTPOnlyFlags_ClassifyEveryRegisteredFlag] until
// it is added here, since the sweep cannot tell what it names.
var nonLiteralFlagNames = map[string]string{
	"registerEnvBackedFlags:entry.flagName":  "the envBackedFlags loop",
	"registerEnvBackedFlags:mirrorFlagName":  "stdioFlags",
	"registerEnvBackedFlags:envFileFlagName": "stdioFlags",
}

// notFlagRegistrations are calls shaped like a flag registration (one of
// [registrationFuncs], with at least three arguments) on a receiver that is
// neither the flag package nor flag.CommandLine, keyed the way the sweep reports
// them, each with why it registers nothing on the process's command line. It is
// empty: a helper handed a *flag.FlagSet, or a flag set of its own, is exactly
// the registration the sweep would otherwise miss.
var notFlagRegistrations = map[string]string{}

// TestHTTPOnlyFlags_ClassifyEveryRegisteredFlag holds [httpOnlyFlags] and
// [stdioFlags] to the flags this package registers, in both directions.
//
// The tables decide whether a stdio run says anything about a flag, and the
// defect they close is a flag accepted and ignored in silence. A flag added to
// the HTTP half and to neither table would be that defect again, and nothing
// else would notice: the flag parses, HTTP mode reads it, and the stdio run that
// ignores it passes every other test. So the registrations are read from the
// source rather than kept as a third list here.
func TestHTTPOnlyFlags_ClassifyEveryRegisteredFlag(t *testing.T) {
	sweep := sweepFlagRegistrations(t)
	literal := sweep.literal
	// A floor rather than a count: the sweep has to have found main's
	// registrations at all, or the comparisons below prove nothing.
	if len(literal) < 20 {
		t.Fatalf("found %d flags registered by a literal name; the source sweep is not seeing main's registrations: %v", len(literal), literal)
	}

	t.Run("every literal registration is classified", func(t *testing.T) {
		checkClassifiedOnce(t, literal)
	})
	t.Run("every other registration is one this test knows", func(t *testing.T) {
		for _, key := range sweep.other {
			if _, known := nonLiteralFlagNames[key]; !known {
				t.Errorf("a flag is registered as %s, which this test cannot resolve to a name: classify it in httpOnlyFlags or stdioFlags and add the key to nonLiteralFlagNames", key)
			}
		}
	})
	t.Run("every registration-shaped call has a receiver this test knows", func(t *testing.T) {
		for _, key := range sweep.unresolved {
			if _, known := notFlagRegistrations[key]; !known {
				t.Errorf("%s looks like a flag registration on a receiver that is neither the flag package nor flag.CommandLine, so this sweep cannot see what it registers: register on flag.CommandLine, or add the key to notFlagRegistrations with the reason it registers nothing", key)
			}
		}
	})
	t.Run("every table entry is a registered flag", func(t *testing.T) {
		checkEntriesRegistered(t, literal)
	})
	t.Run("no env-backed flag is in either table", func(t *testing.T) {
		for _, entry := range envBackedFlags {
			_, httpOnly := httpOnlyFlags[entry.flagName]
			_, stdio := stdioFlags[entry.flagName]
			if httpOnly || stdio {
				t.Errorf("-%s writes its variable for config.Load on either transport, so it is neither HTTP-only nor listed in stdioFlags", entry.flagName)
			}
		}
	})
}

// checkClassifiedOnce fails for every registered name that is in both tables
// or in neither.
func checkClassifiedOnce(t *testing.T, registered []string) {
	t.Helper()
	for _, name := range registered {
		_, httpOnly := httpOnlyFlags[name]
		_, stdio := stdioFlags[name]
		if httpOnly == stdio {
			t.Errorf("-%s is in httpOnlyFlags: %t, in stdioFlags: %t; it must be in exactly one", name, httpOnly, stdio)
		}
	}
}

// checkEntriesRegistered fails for every table entry this package does not
// register. The two flags registered under a constant are resolved here, since
// the sweep reports them by the constant's name.
func checkEntriesRegistered(t *testing.T, literal []string) {
	t.Helper()
	registered := append(slices.Clone(literal), mirrorFlagName, envFileFlagName)
	names := append(slices.Sorted(maps.Keys(httpOnlyFlags)), slices.Sorted(maps.Keys(stdioFlags))...)
	for _, name := range names {
		if !slices.Contains(registered, name) {
			t.Errorf("a table names -%s, which this package does not register", name)
		}
	}
}

// flagSweep is what [sweepFlagRegistrations] found.
type flagSweep struct {
	// literal holds the names registered as string literals, sorted.
	literal []string
	// other holds every registration naming its flag any other way, as
	// "function:expression".
	other []string
	// unresolved holds every registration-shaped call on a receiver that is
	// neither the flag package nor flag.CommandLine, as
	// "function:receiver.Method".
	unresolved []string
}

// sweepFlagRegistrations reads every registration on the flag package or on
// flag.CommandLine in this package's non-test files, under whatever name the
// file imports flag as, and every call shaped like one on any other receiver.
func sweepFlagRegistrations(t *testing.T) flagSweep {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var sweep flagSweep
	fset := token.NewFileSet()
	for _, entry := range entries {
		file := entry.Name()
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(fset, file, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", file, parseErr)
		}
		imports := importNames(t, parsed)
		for _, decl := range parsed.Decls {
			scope := "package scope"
			if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
				scope = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sweep.record(t, scope, imports, n)
				return true
			})
		}
	}
	slices.Sort(sweep.literal)
	return sweep
}

// fileImports is how one file names its imports: the name it imports flag
// under, and every other package name it can call through.
type fileImports struct {
	flag   string
	others map[string]bool
}

// importNames reads the names a file imports its packages under. A dot import
// of flag would hide every registration from the sweep, so it fails.
func importNames(t *testing.T, file *ast.File) fileImports {
	t.Helper()
	names := fileImports{others: map[string]bool{}}
	for _, spec := range file.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		name := path.Base(importPath)
		if len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
			name = path.Base(path.Dir(importPath))
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		switch {
		case importPath == "flag" && name == ".":
			t.Errorf("a file dot-imports flag, so its registrations cannot be told from any other call")
		case importPath == "flag":
			names.flag = name
		default:
			names.others[name] = true
		}
	}
	return names
}

// record files n under the sweep when it is a registration or shaped like one.
func (s *flagSweep) record(t *testing.T, scope string, imports fileImports, n ast.Node) {
	t.Helper()
	call, isCall := n.(*ast.CallExpr)
	if !isCall {
		return
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return
	}
	index, shaped := registrationFuncs[sel.Sel.Name]
	if !shaped || len(call.Args) < 3 {
		return
	}
	switch receiverKind(sel.X, imports) {
	case receiverFlag:
		s.recordName(t, scope, call.Args[index])
	case receiverOther:
		s.unresolved = append(s.unresolved, scope+":"+exprText(sel.X)+"."+sel.Sel.Name)
	case receiverPackage:
	}
}

// recordName files the argument naming a registered flag.
func (s *flagSweep) recordName(t *testing.T, scope string, nameArg ast.Expr) {
	t.Helper()
	lit, isLit := nameArg.(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		s.other = append(s.other, scope+":"+exprText(nameArg))
		return
	}
	name, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Errorf("unquoting the flag name %s: %v", lit.Value, err)
		return
	}
	s.literal = append(s.literal, name)
}

// The three things the receiver of a registration-shaped call can be.
const (
	// receiverFlag is the flag package or flag.CommandLine.
	receiverFlag = iota
	// receiverPackage is another imported package, whose functions of these
	// names (slog.String, attribute.Int) are not flag registrations.
	receiverPackage
	// receiverOther is anything else: a flag set of its own, or a value the
	// sweep cannot see the type of.
	receiverOther
)

// receiverKind classifies the receiver of a registration-shaped call.
func receiverKind(receiver ast.Expr, imports fileImports) int {
	switch x := receiver.(type) {
	case *ast.Ident:
		if imports.flag != "" && x.Name == imports.flag {
			return receiverFlag
		}
		if imports.others[x.Name] {
			return receiverPackage
		}
	case *ast.SelectorExpr:
		if pkg, isIdent := x.X.(*ast.Ident); isIdent && imports.flag != "" && pkg.Name == imports.flag && x.Sel.Name == "CommandLine" {
			return receiverFlag
		}
	}
	return receiverOther
}

// registrationFuncs are the flag package's registration functions, each with
// the index of the argument naming the flag. Every one takes at least three
// arguments, which is what tells them apart from slog.String and the like.
var registrationFuncs = map[string]int{
	"String": 0, "Bool": 0, "Int": 0, "Int64": 0, "Uint": 0, "Uint64": 0, "Float64": 0, "Duration": 0,
	"Func": 0, "BoolFunc": 0,
	"StringVar": 1, "BoolVar": 1, "IntVar": 1, "Int64Var": 1, "UintVar": 1, "Uint64Var": 1,
	"Float64Var": 1, "DurationVar": 1, "TextVar": 1, "Var": 1,
}

// TestSweepFlagRegistrations_SeesEveryShape holds the sweep to the shapes a
// registration can take beyond flag.Xxx: flag.CommandLine, an import alias, and
// a flag set handed to a helper, which it cannot resolve and must report.
func TestSweepFlagRegistrations_SeesEveryShape(t *testing.T) {
	const src = `package main

import (
	fl "flag"
	"log/slog"
)

func register(fs *fl.FlagSet) {
	fl.CommandLine.Int("via-commandline", 0, "")
	fl.Bool("via-alias", false, "")
	fs.String("via-helper", "", "")
	slog.String("not-a-flag", "")
}
`
	parsed, err := parser.ParseFile(token.NewFileSet(), "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parsing the probe: %v", err)
	}
	imports := importNames(t, parsed)
	var sweep flagSweep
	ast.Inspect(parsed, func(n ast.Node) bool {
		sweep.record(t, "register", imports, n)
		return true
	})
	if want := []string{"via-commandline", "via-alias"}; !slices.Equal(sweep.literal, want) {
		t.Errorf("literal = %v, want %v", sweep.literal, want)
	}
	if want := []string{"register:fs.String"}; !slices.Equal(sweep.unresolved, want) {
		t.Errorf("unresolved = %v, want %v", sweep.unresolved, want)
	}
}

// exprText spells an identifier or a selector the way the source does, which
// is every shape a flag name has been given by here.
func exprText(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprText(e.X) + "." + e.Sel.Name
	default:
		return fmt.Sprintf("an expression of type %T", expr)
	}
}

// TestHTTPOnlyFlags_NameTheOverlayVariable holds the variable each line names to
// the overlay that reads it, so the advice "this is what set it" is true.
//
// Every HTTP-only flag has a variable the overlay reads underneath it, and
// every flag the overlay fills is classified: the overlay is how a flag ends up
// set on a stdio run without anybody typing it.
func TestHTTPOnlyFlags_NameTheOverlayVariable(t *testing.T) {
	for _, name := range slices.Sorted(maps.Keys(httpOnlyFlags)) {
		t.Run(name, func(t *testing.T) {
			variable := overlayVariable(name)
			if !strings.HasPrefix(variable, config.EnvPrefix) {
				t.Errorf("overlayVariable(%q) = %q, want a %s variable the overlay reads", name, variable, config.EnvPrefix)
			}
		})
	}
	for _, entry := range httpEnvOverlay {
		t.Run(entry.flagName, func(t *testing.T) {
			_, httpOnly := httpOnlyFlags[entry.flagName]
			_, stdio := stdioFlags[entry.flagName]
			if !httpOnly && !stdio {
				t.Errorf("the overlay fills -%s from %s, and neither table classifies it", entry.flagName, config.EnvName(entry.envShortName))
			}
		})
	}
	if got := overlayVariable("version"); got != "" {
		t.Errorf("overlayVariable(%q) = %q, want empty for a flag the overlay does not fill", "version", got)
	}
}

// TestHTTPOnlyFlags_StdioVariablesAreKnown holds every stdio counterpart to a
// variable this server defines, since the line tells an operator to set it.
func TestHTTPOnlyFlags_StdioVariablesAreKnown(t *testing.T) {
	known := config.KnownEnvNames()
	for _, name := range slices.Sorted(maps.Keys(httpOnlyFlags)) {
		stdio := httpOnlyFlags[name].stdioVariable
		if stdio == "" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if !slices.Contains(known, config.EnvName(stdio)) {
				t.Errorf("-%s names %s as its stdio counterpart, which is not a variable this server reads", name, config.EnvName(stdio))
			}
		})
	}
}

// newIgnoredFlagSet is a flag set with one flag of each kind the tests ask
// about: two HTTP-only ones, the one with a stdio counterpart, and one stdio
// reads.
func newIgnoredFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Float64("rate-limit-rps", 1, "")
	fs.String("http-path", "/", "")
	fs.Int64("max-request-body-bytes", 0, "")
	fs.Bool("version", false, "")
	return fs
}

// TestHTTPOnlyFlagsIgnored_TellsTheCommandLineFromTheEnvironment drives the
// order main runs things in: parse, remember what was typed, then let the
// overlay set a flag through the same set.
func TestHTTPOnlyFlagsIgnored_TellsTheCommandLineFromTheEnvironment(t *testing.T) {
	fs := newIgnoredFlagSet()
	if err := fs.Parse([]string{"--rate-limit-rps=5", "--version"}); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	typed := flagsPassedIn(fs)
	if err := fs.Set("http-path", "/libgen"); err != nil {
		t.Fatalf("setting a flag the way the overlay does: %v", err)
	}

	got := httpOnlyFlagsIgnored(false, fs, typed)
	want := []ignoredHTTPFlag{
		{name: "http-path", fromEnvironment: true},
		{name: "rate-limit-rps", fromEnvironment: false},
	}
	if !slices.Equal(got, want) {
		t.Errorf("httpOnlyFlagsIgnored(stdio) = %+v, want %+v", got, want)
	}
	if onHTTP := httpOnlyFlagsIgnored(true, fs, typed); onHTTP != nil {
		t.Errorf("httpOnlyFlagsIgnored(http) = %+v, want nothing, since HTTP reads every one of them", onHTTP)
	}
}

// TestIgnoredHTTPFlag_Level pins the severity rule: WARN unless the flag has no
// stdio counterpart and was either inferred into stdio or ambient.
func TestIgnoredHTTPFlag_Level(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     ignoredHTTPFlag
		inferred bool
		want     slog.Level
	}{
		{name: "typed, stdio chosen", flag: ignoredHTTPFlag{name: "rate-limit-rps"}, want: slog.LevelWarn},
		{name: "typed, auto", flag: ignoredHTTPFlag{name: "rate-limit-rps"}, inferred: true, want: slog.LevelInfo},
		{name: "environment, stdio chosen", flag: ignoredHTTPFlag{name: "rate-limit-rps", fromEnvironment: true}, want: slog.LevelInfo},
		{name: "counterpart, typed, auto", flag: ignoredHTTPFlag{name: "max-request-body-bytes"}, inferred: true, want: slog.LevelWarn},
		{name: "counterpart, environment", flag: ignoredHTTPFlag{name: "max-request-body-bytes", fromEnvironment: true}, want: slog.LevelWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.flag.level(tc.inferred); got != tc.want {
				t.Errorf("level(%t) = %s, want %s", tc.inferred, got, tc.want)
			}
		})
	}
}

// TestExplain_NamesEveryIgnoredFlag reads the records explain writes for a
// stdio run given HTTP-only flags: one per flag, each naming the flag, where it
// came from, the variable the overlay reads for it and, where there is one, the
// variable stdio reads instead.
func TestExplain_NamesEveryIgnoredFlag(t *testing.T) {
	var captured bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })

	transportDecision{Ignored: []ignoredHTTPFlag{
		{name: "max-request-body-bytes"},
		{name: "trusted-origins", fromEnvironment: true},
	}}.explain(t.Context())

	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(captured.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("a log line is not JSON: %q: %v", line, err)
		}
		records = append(records, record)
	}
	if len(records) != 2 {
		t.Fatalf("explain wrote %d records, want one per ignored flag:\n%s", len(records), captured.String())
	}

	for _, tc := range []struct {
		name   string
		record map[string]any
		want   map[string]any
	}{
		{name: "typed, with a stdio counterpart", record: records[0], want: map[string]any{
			"level": "WARN", "msg": stdioIgnoredFlagLine, "flag": "--max-request-body-bytes", "source": "command line",
			"variable": config.EnvName("MAX_REQUEST_BODY_BYTES"), "on_stdio_set": config.EnvName("STDIO_MAX_LINE_BYTES"),
		}},
		{name: "from the environment", record: records[1], want: map[string]any{
			"level": "INFO", "msg": stdioIgnoredVariableLine, "flag": "--trusted-origins", "source": "environment",
			"variable": config.EnvName("TRUSTED_ORIGINS"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, want := range tc.want {
				if got := tc.record[key]; got != want {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}
			if _, has := tc.want["on_stdio_set"]; !has {
				if got, present := tc.record["on_stdio_set"]; present {
					t.Errorf("on_stdio_set = %v on a flag stdio has no setting for", got)
				}
			}
		})
	}
}
