package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSurface is a surface small enough to reason about, standing in for
// the real registration in every test but the one that reads this repository.
var fixtureSurface = Surface{
	EnvNames: []string{"LIBGEN_MCP_TIMEOUT", "LIBGEN_MCP_SOURCES"},
	Sources:  []string{"libgen", "annas"},
	Tools:    []string{"search", "get_details"},
	Prompts:  []string{"acquire_book"},
}

// page builds a Page from its text, so a case is written as the lines it is
// about.
func page(path, text string) Page {
	return Page{Path: path, Lines: strings.Split(text, "\n")}
}

// names renders a report as one line per finding, so a case asserts on what
// the audit concluded rather than on where it printed it.
func names(report Report) []string {
	var lines []string
	for _, finding := range report.Findings {
		lines = append(lines, finding.Kind+" "+finding.Name)
	}
	return lines
}

// TestAudit_ChecksTheThreeNamespacesItCanMatch is the audit's whole claim: a
// variable, a source named in a shape that can only mean a source, and every
// registered name appearing somewhere.
func TestAudit_ChecksTheThreeNamespacesItCanMatch(t *testing.T) {
	testCases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "a variable the server reads",
			text: "Set `LIBGEN_MCP_TIMEOUT` to widen it. search get_details acquire_book",
			want: nil,
		},
		{
			name: "a variable nothing reads",
			text: "Set `LIBGEN_MCP_NOWHERE` to widen it. search get_details acquire_book",
			want: []string{"env LIBGEN_MCP_NOWHERE"},
		},
		{
			name: "a source named as a JSON value",
			text: `Call it with {"source": "annas"}. search get_details acquire_book`,
			want: nil,
		},
		{
			name: "a source that is not one",
			text: `Call it with {"source": "nowhere"}. search get_details acquire_book`,
			want: []string{"source nowhere"},
		},
		{
			name: "a source list with one bad entry",
			text: "LIBGEN_MCP_SOURCES=libgen,nowhere,annas — search get_details acquire_book",
			want: []string{"source nowhere"},
		},
		{
			name: "a source name in prose is not checked",
			text: "The annas source and a nowhere of its own. search get_details acquire_book",
			want: nil,
		},
		{
			name: "a tool nothing documents",
			text: "This page mentions search and acquire_book only.",
			want: []string{"tool get_details"},
		},
		{
			name: "a prompt nothing documents",
			text: "This page mentions search and get_details only.",
			want: []string{"prompt acquire_book"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := names(audit([]Page{page("docs/x.md", tc.text)}, fixtureSurface))
			if strings.Join(got, "; ") != strings.Join(tc.want, "; ") {
				t.Errorf("audit reported %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAudit_ADeclarationExcusesACounterExample pins the mechanism the first
// run of this audit made necessary: every one of its twelve findings was a
// page naming a variable in order to say it is not read, which is
// documentation doing its job.
func TestAudit_ADeclarationExcusesACounterExample(t *testing.T) {
	text := "`LIBGEN_MCP_HTTP` would read as a switch rather than an address.\n" +
		"<!-- libgen:allow-name LIBGEN_MCP_HTTP: the spelling this server deliberately does not use -->\n" +
		"search get_details acquire_book"
	if got := names(audit([]Page{page("docs/x.md", text)}, fixtureSurface)); got != nil {
		t.Errorf("audit reported %v, want the declared counter-example excused", got)
	}
}

// TestAudit_ADeclarationThatExcusesNothingIsAFinding verifies the other half:
// one left behind by a later edit cannot quietly widen the gate.
func TestAudit_ADeclarationThatExcusesNothingIsAFinding(t *testing.T) {
	text := "<!-- libgen:allow-name LIBGEN_MCP_GONE: the page used to name it -->\nsearch get_details acquire_book"
	got := names(audit([]Page{page("docs/x.md", text)}, fixtureSurface))

	if strings.Join(got, "; ") != "stale LIBGEN_MCP_GONE" {
		t.Errorf("audit reported %v, want the declaration that excuses nothing", got)
	}
}

// TestAudit_ADeclarationExcusesItsOwnPageOnly pins the scope: the same
// counter-example in two pages is two sentences, each of which has to be true
// on its own.
func TestAudit_ADeclarationExcusesItsOwnPageOnly(t *testing.T) {
	declared := page("docs/a.md", "`LIBGEN_MCP_HTTP` is not read.\n"+
		"<!-- libgen:allow-name LIBGEN_MCP_HTTP: the spelling this server does not use -->\n"+
		"search get_details acquire_book")
	bare := page("docs/b.md", "`LIBGEN_MCP_HTTP` is not read.")

	got := names(audit([]Page{declared, bare}, fixtureSurface))
	if strings.Join(got, "; ") != "env LIBGEN_MCP_HTTP" {
		t.Errorf("audit reported %v, want the undeclared page alone", got)
	}
}

// TestParseDirective_RequiresBothHalves verifies a declaration with no reason
// is not read as one: the reason is what lets the next reader tell a
// counter-example from a mistake.
func TestParseDirective_RequiresBothHalves(t *testing.T) {
	testCases := []struct {
		name    string
		comment string
		want    string
	}{
		{
			name:    "a name and a reason",
			comment: "<!-- libgen:allow-name LIBGEN_MCP_HTTP: a reason -->",
			want:    "LIBGEN_MCP_HTTP",
		},
		{name: "no reason", comment: "<!-- libgen:allow-name LIBGEN_MCP_HTTP -->"},
		{name: "an empty reason", comment: "<!-- libgen:allow-name LIBGEN_MCP_HTTP: -->"},
		{name: "no name", comment: "<!-- libgen:allow-name : a reason -->"},
		{name: "an ordinary comment", comment: "<!-- a note to a later reader -->"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			match := nameDirective.FindStringSubmatch(tc.comment)
			got := ""
			if match != nil {
				got = match[1]
			}
			if got != tc.want {
				t.Errorf("the directive read %q from %q, want %q", got, tc.comment, tc.want)
			}
		})
	}
}

// TestWordAt_MatchesAWholeNameOnly pins what keeps get_details from matching
// get_details_extra, and a tool name inside a longer identifier from counting
// as a mention.
func TestWordAt_MatchesAWholeNameOnly(t *testing.T) {
	testCases := []struct {
		name string
		line string
		want bool
	}{
		{name: "alone", line: "read", want: true},
		{name: "in a code span", line: "call `read` first", want: true},
		{name: "at the end of a sentence", line: "the tool is read.", want: true},
		{name: "as part of a longer name", line: "read_file", want: false},
		{name: "as a suffix", line: "unread", want: false},
		{name: "absent", line: "nothing here", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wordAt(tc.line, "read"); got != tc.want {
				t.Errorf("wordAt(%q, %q) = %v, want %v", tc.line, "read", got, tc.want)
			}
		})
	}
}

// TestSkipped_LeavesTheHistoricalTreeAlone pins the ruling made before the
// gate turned red: that tree already names a variable this server never
// shipped, and a gate whose first finding is a document nobody will edit
// teaches its reader to pass a flag rather than read the finding.
func TestSkipped_LeavesTheHistoricalTreeAlone(t *testing.T) {
	testCases := []struct {
		path string
		want bool
	}{
		{path: "docs/superpowers/specs/x.md", want: true},
		{path: "docs/superpowers", want: true},
		{path: "docs/configuration.md", want: false},
		{path: "docs/superpowers-elsewhere/x.md", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			if got := skipped(tc.path); got != tc.want {
				t.Errorf("skipped(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestRun_ThisRepositoryResolvesEveryName is the gate itself, run from the
// suite so a page naming something that does not exist fails the package's own
// tests and not only the Makefile target.
func TestRun_ThisRepositoryResolvesEveryName(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}

	var out, errOut bytes.Buffer
	if status := run([]string{"-check", "-dir", root}, &out, &errOut); status != 0 {
		t.Fatalf("the gate refused this tree (status %d):\n%s%s", status, out.String(), errOut.String())
	}
}

// TestRun_ExitStatusSaysWhichOfTheThreeHappened pins the split every gate in
// this repository uses: 0 clean, 1 refused, 2 could not run.
func TestRun_ExitStatusSaysWhichOfTheThreeHappened(t *testing.T) {
	testCases := []struct {
		name string
		args []string
		want int
	}{
		{name: "help", args: []string{"-h"}, want: 0},
		{name: "an unknown flag", args: []string{"-nonsense"}, want: 2},
		{name: "a directory with no documentation", args: []string{"-dir", t.TempDir()}, want: 2},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got := run(tc.args, &out, &errOut); got != tc.want {
				t.Errorf("run(%v) = %d, want %d: %s", tc.args, got, tc.want, errOut.String())
			}
		})
	}
}

// TestReadPages_ReadsBothMarkdownFlavoursAndSkipsTheRest verifies the walk
// takes .md and .mdx and leaves everything else, since the site's pages are
// MDX and the repository's are Markdown.
func TestReadPages_ReadsBothMarkdownFlavoursAndSkipsTheRest(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/a.md":                    "a\n",
		"docs/b.mdx":                   "b\n",
		"docs/c.txt":                   "c\n",
		"docs/superpowers/d.md":        "d\n",
		"site/src/content/docs/e.mdx":  "e\n",
		"README.md":                    "r\n",
		"llms-install.md":              "l\n",
		"CLAUDE.md":                    "c\n",
		"npm/libgen-mcp/README.md":     "n\n",
		"docs/decisions/2026-01-01.md": "adr\n",
	})

	pages, err := readPages(root)
	if err != nil {
		t.Fatalf("readPages() error = %v", err)
	}
	var read []string
	for _, p := range pages {
		read = append(read, p.Path)
	}
	want := "CLAUDE.md README.md docs/a.md docs/b.mdx docs/decisions/2026-01-01.md " +
		"llms-install.md npm/libgen-mcp/README.md site/src/content/docs/e.mdx"
	if got := strings.Join(read, " "); got != want {
		t.Errorf("readPages() read %q, want %q", got, want)
	}
}

// writeTree writes a file tree under a temporary directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// TestDeclarations_ADirectiveInACodeSpanIsShownNotMade pins the case the gate
// record produced the moment it documented this mechanism: a row printing the
// syntax read as a declaration, so the record's own row became a stale one.
func TestDeclarations_ADirectiveInACodeSpanIsShownNotMade(t *testing.T) {
	quoted := page("docs/gates.md", "Declare it with `<!-- libgen:allow-name NAME: reason -->` in the page.")
	if declared, _ := declarations(quoted); len(declared) != 0 {
		t.Errorf("declarations() read %v from a quoted example, want none", declared)
	}

	made := page("docs/x.md", "<!-- libgen:allow-name LIBGEN_MCP_HTTP: a reason -->")
	declared, at := declarations(made)
	if _, ok := declared["LIBGEN_MCP_HTTP"]; !ok {
		t.Errorf("declarations() read %v from a real declaration, want the name", declared)
	}
	if at["LIBGEN_MCP_HTTP"] != 1 {
		t.Errorf("the declaration is recorded at line %d, want 1", at["LIBGEN_MCP_HTTP"])
	}
}
