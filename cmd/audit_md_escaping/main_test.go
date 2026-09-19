package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_ExitStatusSaysWhichOfTheThreeHappened pins the split every gate in
// this repository uses: 0 clean, 1 refused, 2 could not run. A gate that
// cannot run must not read as a gate that passed.
func TestRun_ExitStatusSaysWhichOfTheThreeHappened(t *testing.T) {
	root := repoRoot(t)
	testCases := []struct {
		name string
		args []string
		want int
	}{
		{name: "help", args: []string{"-h"}, want: 0},
		{name: "an unknown flag", args: []string{"-nonsense"}, want: 2},
		{name: "an unknown context", args: []string{"-dir", root, "-contexts", "paragraph"}, want: 2},
		{name: "a directory that is not there", args: []string{"-dir", root, "internal/nowhere"}, want: 2},
		{name: "this repository", args: []string{"-dir", root, "-check"}, want: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got := run(tc.args, &out, &errOut); got != tc.want {
				t.Errorf("run(%v) = %d, want %d\nstdout: %s\nstderr: %s", tc.args, got, tc.want, out.String(), errOut.String())
			}
		})
	}
}

// repoRoot locates the repository from the package directory the test runs in.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	return root
}

// TestRun_ThisRepositoryHasNoUnescapedValue is the gate itself, run from the
// suite so a leak introduced beside a renderer fails the package's own tests
// and not only the Makefile target.
//
// It asks for the stricter rule in internal/toolutil, which is what
// make check-md-escaping passes: a value the audit cannot follow there is a
// blind spot behind every formatter that calls it.
func TestRun_ThisRepositoryHasNoUnescapedValue(t *testing.T) {
	var out, errOut bytes.Buffer
	status := run([]string{"-dir", repoRoot(t), "-check", "-fail-unresolved-in", toolutilDir}, &out, &errOut)
	if status != 0 {
		t.Fatalf("the gate refused this tree (status %d):\n%s%s", status, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "unescaped 0") {
		t.Errorf("the summary does not say the tree is clean:\n%s", out.String())
	}
}

// TestRun_EveryRendererNamedInTheAuditIsStillDeclared drives the entry-point
// list against the real tree, which is the half of the sweep a clean report
// cannot vouch for: a renderer that was renamed drops out of it silently.
func TestRun_EveryRendererNamedInTheAuditIsStillDeclared(t *testing.T) {
	prog, err := loadProgram(repoRoot(t), auditDirs)
	if err != nil {
		t.Fatalf("load this repository: %v", err)
	}
	if missing := missingEntryPoints(prog); len(missing) > 0 {
		t.Errorf("named in markdownEntryPoints but no longer declared: %v", missing)
	}
}

// TestExecute_WritesTheJSONWorkList verifies the machine-readable half of the
// report, which is what a person fixing a backlog works through.
func TestExecute_WritesTheJSONWorkList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "escaping.json")
	var out, errOut bytes.Buffer
	if status := execute(auditRun{dir: repoRoot(t), dirs: auditDirs, jsonPath: path}, &out, &errOut); status != 0 {
		t.Fatalf("execute() = %d, want 0: %s", status, errOut.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the work list: %v", err)
	}
	var report Report
	if decodeErr := json.Unmarshal(data, &report); decodeErr != nil {
		t.Fatalf("the work list is not JSON: %v", decodeErr)
	}
	if report.Summary.Judged == 0 || report.Summary.Packages != len(auditDirs) {
		t.Errorf("summary = %+v, want every swept package and at least one judged value", report.Summary)
	}
}

// TestExecute_ReportsWhatItCouldNotDo verifies the failures that stop a run
// reach the caller as status 2 with a named reason, rather than as a clean
// exit nobody reads.
func TestExecute_ReportsWhatItCouldNotDo(t *testing.T) {
	t.Run("an unresolvable working directory", func(t *testing.T) {
		restore := absRoot
		absRoot = func(string) (string, error) { return "", errors.New("no working directory") }
		t.Cleanup(func() { absRoot = restore })

		var out, errOut bytes.Buffer
		if status := execute(auditRun{dir: ".", dirs: auditDirs}, &out, &errOut); status != 2 {
			t.Errorf("execute() = %d, want 2", status)
		}
		if !strings.Contains(errOut.String(), "no working directory") {
			t.Errorf("stderr = %q, want the reason named", errOut.String())
		}
	})

	t.Run("a work list that cannot be encoded", func(t *testing.T) {
		restore := marshalReport
		marshalReport = func(any, string, string) ([]byte, error) { return nil, errors.New("cannot encode") }
		t.Cleanup(func() { marshalReport = restore })

		var out, errOut bytes.Buffer
		cfg := auditRun{dir: repoRoot(t), dirs: auditDirs, jsonPath: filepath.Join(t.TempDir(), "x.json")}
		if status := execute(cfg, &out, &errOut); status != 2 {
			t.Errorf("execute() = %d, want 2", status)
		}
	})

	t.Run("a package that does not parse", func(t *testing.T) {
		root := writeFixture(t, map[string]string{"internal/broken/broken.go": "package broken\n\nfunc (\n"})
		var out, errOut bytes.Buffer
		if status := execute(auditRun{dir: root, dirs: []string{"internal/broken"}}, &out, &errOut); status != 2 {
			t.Errorf("execute() = %d, want 2", status)
		}
	})

	t.Run("a directory with no Go source", func(t *testing.T) {
		root := writeFixture(t, map[string]string{"internal/empty/README.md": "nothing here\n"})
		var out, errOut bytes.Buffer
		if status := execute(auditRun{dir: root, dirs: []string{"internal/empty"}}, &out, &errOut); status != 2 {
			t.Errorf("execute() = %d, want 2", status)
		}
	})
}

// TestExecute_CheckRefusesATreeWithALeak is the mutation this gate exists for:
// the same tree passes without -check and is refused with it, and the reason
// names what it found.
func TestExecute_CheckRefusesATreeWithALeak(t *testing.T) {
	declared := markdownEntryPoints
	markdownEntryPoints = map[string][]string{}
	t.Cleanup(func() { markdownEntryPoints = declared })

	root := writeFixture(t, map[string]string{
		"internal/toolutil/markdown.go": fixtureToolutil,
		"internal/render/render.go": fixtureRenderer(
			"// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", r.Title)\n}",
		),
	})
	cfg := auditRun{dir: root, dirs: []string{"internal/render", toolutilDir}}

	var out, errOut bytes.Buffer
	if status := execute(cfg, &out, &errOut); status != 0 {
		t.Fatalf("a run without -check = %d, want 0: a report is not a refusal", status)
	}
	if !strings.Contains(out.String(), "r.Title") {
		t.Errorf("the report does not name the value:\n%s", out.String())
	}

	cfg.check = true
	out.Reset()
	errOut.Reset()
	if status := execute(cfg, &out, &errOut); status != 1 {
		t.Errorf("a run with -check = %d, want 1", status)
	}
	if !strings.Contains(errOut.String(), "1 unescaped") {
		t.Errorf("stderr = %q, want the count named", errOut.String())
	}
}

// TestExecute_VerboseListsWhatTheGateDoesNotRefuse verifies the two lists a
// plain run leaves out are printed on request: a value the audit could not
// follow, and one a declaration excuses.
func TestExecute_VerboseListsWhatTheGateDoesNotRefuse(t *testing.T) {
	declared := markdownEntryPoints
	markdownEntryPoints = map[string][]string{}
	t.Cleanup(func() { markdownEntryPoints = declared })

	root := writeFixture(t, map[string]string{
		"internal/toolutil/markdown.go": fixtureToolutil,
		"internal/render/render.go": fixtureRenderer(
			"//libgen:allow-unescaped r.Title: the fixture's title is compiled in\n" +
				"// row writes a table row.\nfunc row(b *strings.Builder, r record, lookup func(string) string) {\n" +
				"\tfmt.Fprintf(b, \"| %s | %s |\\n\", r.Title, lookup(r.Title))\n}",
		),
	})
	cfg := auditRun{dir: root, dirs: []string{"internal/render", toolutilDir}, verbose: true}

	var out, errOut bytes.Buffer
	if status := execute(cfg, &out, &errOut); status != 0 {
		t.Fatalf("execute() = %d, want 0: %s", status, errOut.String())
	}
	for _, heading := range []string{"Unresolved (1)", "Excused (1)"} {
		t.Run(heading, func(t *testing.T) {
			if !strings.Contains(out.String(), heading) {
				t.Errorf("the verbose report is missing %q:\n%s", heading, out.String())
			}
		})
	}
}

// TestExecute_FailUnresolvedInRefusesOnlyTheNamedPackage verifies the staging
// flag on a real run: the same unresolved value is reported either way and
// refused only when its package was named.
func TestExecute_FailUnresolvedInRefusesOnlyTheNamedPackage(t *testing.T) {
	declared := markdownEntryPoints
	markdownEntryPoints = map[string][]string{}
	t.Cleanup(func() { markdownEntryPoints = declared })

	root := writeFixture(t, map[string]string{
		"internal/toolutil/markdown.go": fixtureToolutil,
		"internal/render/render.go": fixtureRenderer(
			"// row writes a table row.\nfunc row(b *strings.Builder, r record, lookup func(string) string) {\n" +
				"\tfmt.Fprintf(b, \"| %s |\\n\", lookup(r.Title))\n}",
		),
	})
	base := auditRun{dir: root, dirs: []string{"internal/render", toolutilDir}, check: true}

	testCases := []struct {
		name string
		in   string
		all  bool
		want int
	}{
		{name: "reported only", want: 0},
		{name: "another package named", in: toolutilDir, want: 0},
		{name: "its own package named", in: "internal/render", want: 1},
		{name: "every package held to the rule", all: true, want: 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.failUnresolvedIn = tc.in
			cfg.failUnresolved = tc.all
			var out, errOut bytes.Buffer
			if got := execute(cfg, &out, &errOut); got != tc.want {
				t.Errorf("execute() = %d, want %d: %s", got, tc.want, errOut.String())
			}
		})
	}
}

// TestSplitPrefixes_ReadsTheListTheFlagTakes verifies the -fail-unresolved-in
// value is read as a list, spacing and empty entries included.
func TestSplitPrefixes_ReadsTheListTheFlagTakes(t *testing.T) {
	testCases := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: ""},
		{name: "one prefix", value: toolutilDir, want: toolutilDir},
		{name: "two prefixes with spacing", value: " a , b ", want: "a|b"},
		{name: "empty entries are dropped", value: ",,a,", want: "a"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(splitPrefixes(tc.value), "|"); got != tc.want {
				t.Errorf("splitPrefixes(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}
