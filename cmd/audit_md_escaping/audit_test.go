package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureToolutil is the escaping vocabulary a fixture tree is judged against.
// The bodies are stubs: the sweep reads syntax, so what matters is that the
// names are declared in a package at internal/toolutil.
const fixtureToolutil = `package toolutil

// Dash is what a fixture writes for an empty value.
const Dash = "—"

// EscapeMdTableCell neutralizes a value for a table cell.
func EscapeMdTableCell(s string) string { return s }

// EscapeMdHeading neutralizes a value for a heading.
func EscapeMdHeading(s string) string { return s }

// StripControlBytes removes the control bytes and nothing else.
func StripControlBytes(s string) string { return s }
`

// fixtureRenderer wraps one renderer body in a package that imports what a
// formatter needs, so a case is written as the lines under test alone.
func fixtureRenderer(body string) string {
	return "package render\n\nimport (\n\t\"fmt\"\n\t\"strconv\"\n\t\"strings\"\n\n" +
		"\t\"example.test/internal/toolutil\"\n)\n\n" +
		"// record is the catalog record a fixture renders.\ntype record struct {\n\tTitle string\n\tURL   string\n\tPages int\n}\n\n" +
		body + "\n\n// use keeps every import referenced whatever the case writes.\n" +
		"func use(b *strings.Builder, s string) { fmt.Fprint(b, toolutil.StripControlBytes(s)) }\n"
}

// writeFixture writes a package tree under a temporary root and returns it.
func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(name), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// auditFixture sweeps one renderer body and returns what the audit made of it,
// with no entry points named: the list names the renderers of this repository,
// and a fixture declares none of them.
func auditFixture(t *testing.T, body string) Report {
	t.Helper()
	return auditTree(t, nil, map[string]string{
		"internal/toolutil/markdown.go": fixtureToolutil,
		"internal/render/render.go":     fixtureRenderer(body),
	}, "internal/render", "internal/toolutil")
}

// auditTree sweeps a whole fixture tree under the entry points it is given,
// for the cases that need more than one renderer or a package of their own.
func auditTree(t *testing.T, entryPoints map[string][]string, files map[string]string, dirs ...string) Report {
	t.Helper()
	declared := markdownEntryPoints
	markdownEntryPoints = entryPoints
	t.Cleanup(func() { markdownEntryPoints = declared })

	root := writeFixture(t, files)
	prog, err := loadProgram(root, dirs)
	if err != nil {
		t.Fatalf("load the fixture: %v", err)
	}
	sel, err := parseContexts(allContexts)
	if err != nil {
		t.Fatalf("parse the contexts: %v", err)
	}
	return audit(prog, sel, root)
}

// verdicts renders a report as one line per finding, so a case asserts on what
// the audit concluded rather than on where it printed it.
func verdicts(report Report) []string {
	var lines []string
	for _, group := range [][]Finding{report.Findings, report.Unresolved} {
		for _, finding := range group {
			lines = append(lines, finding.Verdict+" "+finding.Context+" "+finding.Expression)
		}
	}
	return lines
}

// TestAudit_JudgesAValueByTheConstructItLandsIn is the audit's whole claim: the
// same raw field is a finding in a table cell and not one in a paragraph.
func TestAudit_JudgesAValueByTheConstructItLandsIn(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "a raw field in a table cell",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", r.Title)\n}",
			want: []string{"unescaped table-cell r.Title"},
		},
		{
			name: "the same field in a paragraph",
			body: "// para writes a sentence.\nfunc para(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"Found %s today.\\n\", r.Title)\n}",
			want: nil,
		},
		{
			name: "the same field escaped",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", toolutil.EscapeMdTableCell(r.Title))\n}",
			want: nil,
		},
		{
			name: "a number needs no escaping",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %d |\\n\", r.Pages)\n}",
			want: nil,
		},
		{
			name: "a raw field in a heading",
			body: "// head writes a heading.\nfunc head(b *strings.Builder, r record) {\n\tb.WriteString(\"## \")\n\tb.WriteString(r.Title)\n}",
			want: []string{"unescaped heading r.Title"},
		},
		{
			name: "a raw field in a list item",
			body: "// item writes a bullet.\nfunc item(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"- Title: %s\\n\", r.Title)\n}",
			want: []string{"unescaped list-item r.Title"},
		},
		{
			name: "a raw URL in a link destination",
			body: "// link writes a link.\nfunc link(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"[%s](%s)\\n\", toolutil.EscapeMdTableCell(r.Title), r.URL)\n}",
			want: []string{"unescaped link-destination r.URL"},
		},
		{
			name: "a raw value inside a fence",
			body: "// fenced writes a block.\nfunc fenced(b *strings.Builder, r record) {\n\tb.WriteString(\"```\\n\")\n\tb.WriteString(r.Title)\n\tb.WriteString(\"\\n```\\n\")\n}",
			want: []string{"unescaped fence r.Title"},
		},
		{
			name: "stripping control bytes is not escaping",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", toolutil.StripControlBytes(r.Title))\n}",
			want: []string{"unescaped table-cell toolutil.StripControlBytes(r.Title)"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := verdicts(auditFixture(t, tc.body))
			if strings.Join(got, "; ") != strings.Join(tc.want, "; ") {
				t.Errorf("audit reported %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAudit_FollowsAValueToWhereItCameFrom pins the chains the classifier walks
// backwards: a local, a helper's return, a parameter every caller binds, and
// the elements a row was appended from.
func TestAudit_FollowsAValueToWhereItCameFrom(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "a local assigned from an escaper",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tcell := toolutil.EscapeMdTableCell(r.Title)\n\tfmt.Fprintf(b, \"| %s |\\n\", cell)\n}",
			want: nil,
		},
		{
			name: "a local assigned raw on one branch",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tcell := toolutil.EscapeMdTableCell(r.Title)\n\tif r.Pages == 0 {\n\t\tcell = r.Title\n\t}\n\tfmt.Fprintf(b, \"| %s |\\n\", cell)\n}",
			want: []string{"unescaped table-cell cell"},
		},
		{
			name: "a package-local helper that delegates",
			body: "// mdCell is the package's own spelling of the escaper.\nfunc mdCell(s string) string { return toolutil.EscapeMdTableCell(s) }\n\n// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", mdCell(r.Title))\n}",
			want: nil,
		},
		{
			name: "a parameter every caller escapes",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, cell string) {\n\tfmt.Fprintf(b, \"| %s |\\n\", cell)\n}\n\n// table writes the rows.\nfunc table(b *strings.Builder, r record) {\n\trow(b, toolutil.EscapeMdTableCell(r.Title))\n}",
			want: nil,
		},
		{
			name: "a parameter one caller leaves raw",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, cell string) {\n\tfmt.Fprintf(b, \"| %s |\\n\", cell)\n}\n\n// table writes the rows.\nfunc table(b *strings.Builder, r record) {\n\trow(b, toolutil.EscapeMdTableCell(r.Title))\n\trow(b, r.Title)\n}",
			want: []string{"unescaped table-cell cell"},
		},
		{
			name: "cells appended into a row",
			body: "// table writes a row built cell by cell.\nfunc table(b *strings.Builder, r record) {\n\tcells := make([]string, 0, 2)\n\tcells = append(cells, toolutil.EscapeMdTableCell(r.Title))\n\tb.WriteString(\"| \")\n\tb.WriteString(strings.Join(cells, \" | \"))\n}",
			want: nil,
		},
		{
			name: "a raw cell appended into a row",
			body: "// table writes a row built cell by cell.\nfunc table(b *strings.Builder, r record) {\n\tcells := make([]string, 0, 2)\n\tcells = append(cells, r.Title)\n\tb.WriteString(\"| \")\n\tb.WriteString(strings.Join(cells, \" | \"))\n}",
			want: []string{"unescaped table-cell strings.Join(cells, \" | \")"},
		},
		{
			name: "a label read out of the literal that holds it",
			body: "// fields writes one row per field.\nfunc fields(b *strings.Builder, r record) {\n\tfor _, f := range []struct{ label, key string }{{\"Title\", \"title\"}} {\n\t\tfmt.Fprintf(b, \"- %s: %s\\n\", f.label, toolutil.EscapeMdTableCell(r.Title))\n\t}\n}",
			want: nil,
		},
		{
			name: "a value composed with Sprintf out of a raw half",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tcell := fmt.Sprintf(\"%s (%d)\", r.Title, r.Pages)\n\tfmt.Fprintf(b, \"| %s |\\n\", cell)\n}",
			want: []string{"unescaped table-cell cell"},
		},
		{
			name: "a value the audit cannot follow",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record, lookup func(string) string) {\n\tfmt.Fprintf(b, \"| %s |\\n\", lookup(r.Title))\n}",
			want: []string{"unresolved table-cell lookup(r.Title)"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := verdicts(auditFixture(t, tc.body))
			if strings.Join(got, "; ") != strings.Join(tc.want, "; ") {
				t.Errorf("audit reported %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAudit_IndentationStillOpensItsLine pins the one write the cursor reads
// through rather than over: a run of spaces a formatter composed leaves the
// value after it at the start of its line, which is where a bullet counts.
func TestAudit_IndentationStillOpensItsLine(t *testing.T) {
	body := "// outline writes an indented entry.\nfunc outline(b *strings.Builder, r record) {\n" +
		"\tindent := strings.Repeat(\"  \", r.Pages)\n\tfmt.Fprintf(b, \"%s- %s\\n\", indent, r.Title)\n}"
	got := verdicts(auditFixture(t, body))
	want := []string{"unescaped list-item r.Title"}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("audit reported %v, want %v", got, want)
	}
}

// TestAudit_ExcusesADeclaredValueAndReportsAStaleDeclaration verifies both
// halves of the exemption: a value declared with a reason stops being a
// finding, and a declaration that excuses nothing becomes one.
func TestAudit_ExcusesADeclaredValueAndReportsAStaleDeclaration(t *testing.T) {
	body := "//libgen:allow-unescaped r.Title: the fixture's title is a compiled-in constant\n" +
		"//libgen:allow-unescaped r.Missing: nothing writes this any more\n" +
		"// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", r.Title)\n}"
	report := auditFixture(t, body)

	if len(report.Findings) != 0 {
		t.Errorf("the declared value is still a finding: %v", verdicts(report))
	}
	if len(report.Excused) != 1 || report.Excused[0].Expression != "r.Title" {
		t.Errorf("Excused = %v, want the declared value", report.Excused)
	}
	if len(report.Stale) != 1 || report.Stale[0].Expression != "r.Missing" {
		t.Errorf("Stale = %v, want the declaration that excuses nothing", report.Stale)
	}
}

// TestAudit_ReportsARendererItWasToldToFind pins the other direction of the
// sweep. A renderer that is renamed drops out of it silently, and a gate that
// reports nothing because it found nothing to look at reads exactly like a
// gate that passed.
func TestAudit_ReportsARendererItWasToldToFind(t *testing.T) {
	files := map[string]string{
		"internal/toolutil/markdown.go": fixtureToolutil,
		"internal/render/render.go":     fixtureRenderer("// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", toolutil.EscapeMdTableCell(r.Title))\n}"),
	}
	entryPoints := map[string][]string{"internal/render": {"row", "renamedAway"}}
	report := auditTree(t, entryPoints, files, "internal/render", "internal/toolutil")

	if len(report.Missing) != 1 || report.Missing[0] != "internal/render.renamedAway" {
		t.Fatalf("Missing = %v, want the renderer that is no longer declared", report.Missing)
	}
	if failing(report, false, nil) == 0 {
		t.Errorf("a missing renderer did not fail the gate")
	}
}

// TestFailing_CountsUnresolvedOnlyWherePolicySaysSo pins the staging rule: a
// value the audit cannot follow is reported everywhere and fails the gate only
// in the packages held to the stricter rule.
func TestFailing_CountsUnresolvedOnlyWherePolicySaysSo(t *testing.T) {
	report := Report{Unresolved: []Finding{{Package: toolutilDir}, {Package: "internal/tools"}}}
	testCases := []struct {
		name  string
		all   bool
		in    []string
		count int
	}{
		{name: "reported only", count: 0},
		{name: "one package held to the rule", in: []string{toolutilDir}, count: 1},
		{name: "every package held to the rule", all: true, count: 2},
		{name: "an empty prefix holds nothing", in: []string{""}, count: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failing(report, tc.all, tc.in); got != tc.count {
				t.Errorf("failing() = %d, want %d", got, tc.count)
			}
		})
	}
}
