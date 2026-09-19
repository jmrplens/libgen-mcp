package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// sampleReport is a report with one of everything, so the printer is driven
// over each list it can write.
func sampleReport() Report {
	return Report{
		Contexts: "table-cell",
		Findings: []Finding{{
			Package: "internal/tools", File: "internal/tools/markdown.go", Line: 42,
			Function: "renderSearchMarkdown", Context: "table-cell", Verb: "%s",
			Expression: "r.Title", Verdict: "unescaped",
			Reason: "r.Title is external text with no escaper applied",
			Wants:  "toolutil.EscapeMdTableCell",
		}},
		Unresolved: []Finding{{
			Package: toolutilDir, File: "internal/toolutil/markdown.go", Line: 7,
			Function: "MdTitleLink", Context: "link-destination", Verb: "%s",
			Expression: "lookup(url)", Verdict: "unresolved",
		}},
		Excused: []Finding{{
			Package: "internal/prompts", File: "internal/prompts/prompts.go", Line: 9,
			Function: "renderTable", Context: "heading", Verb: verbWrite,
			Expression: "heading", Verdict: "unescaped",
		}},
		Stale:   []Directive{{File: "internal/tools/markdown.go", Line: 3, Expression: "r.Gone"}},
		Missing: []string{"internal/tools.renderSearchMarkdown"},
		Summary: Summary{Holes: 9, Judged: 4, Safe: 1, Unescaped: 1, Unresolved: 1, Excused: 1, Stale: 1, Missing: 1, Packages: 3},
	}
}

// TestWriteReport_PrintsTheFailingListAndKeepsTheRestForVerbose verifies the
// default report is the work list and nothing else: a run that prints its
// excused and unresolved values every time trains its reader to scroll past
// the ones that matter.
func TestWriteReport_PrintsTheFailingListAndKeepsTheRestForVerbose(t *testing.T) {
	var plain, verbose bytes.Buffer
	writeReport(&plain, sampleReport(), false)
	writeReport(&verbose, sampleReport(), true)

	testCases := []struct {
		name      string
		text      string
		inPlain   bool
		inVerbose bool
	}{
		{name: "the unescaped value", text: "r.Title", inPlain: true, inVerbose: true},
		{name: "the helper it wants", text: "wants toolutil.EscapeMdTableCell", inPlain: true, inVerbose: true},
		{name: "the stale declaration", text: "r.Gone", inPlain: true, inVerbose: true},
		{name: "the missing renderer", text: "internal/tools.renderSearchMarkdown", inPlain: true, inVerbose: true},
		{name: "the unresolved value", text: "lookup(url)", inVerbose: true},
		{name: "the excused value", text: "Excused (1)", inVerbose: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Contains(plain.String(), tc.text); got != tc.inPlain {
				t.Errorf("the plain report contains %q = %v, want %v\n%s", tc.text, got, tc.inPlain, plain.String())
			}
			if got := strings.Contains(verbose.String(), tc.text); got != tc.inVerbose {
				t.Errorf("the verbose report contains %q = %v, want %v\n%s", tc.text, got, tc.inVerbose, verbose.String())
			}
		})
	}
}

// TestWriteReport_ACleanRunPrintsOnlyItsCounts verifies a clean sweep says so
// in one place rather than printing empty headings.
func TestWriteReport_ACleanRunPrintsOnlyItsCounts(t *testing.T) {
	var out bytes.Buffer
	writeReport(&out, Report{Summary: Summary{Holes: 12, Judged: 4, Safe: 4, Packages: 3}}, true)
	text := out.String()

	if strings.Contains(text, "Unescaped") || strings.Contains(text, "Exemptions") {
		t.Errorf("a clean report prints an empty heading:\n%s", text)
	}
	for _, want := range []string{"12 values in 3 packages", "unescaped 0", "in -"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(text, want) {
				t.Errorf("the summary is missing %q:\n%s", want, text)
			}
		})
	}
}

// TestSummarize_NamesEveryReasonTheGateRefused verifies the one line stderr
// carries says which of the four kinds of finding stopped the run.
func TestSummarize_NamesEveryReasonTheGateRefused(t *testing.T) {
	testCases := []struct {
		name   string
		report Report
		want   string
	}{
		{name: "a clean run", report: Report{}, want: "0 unescaped, 0 unresolved"},
		{name: "everything at once", report: sampleReport(), want: "1 unescaped, 1 unresolved, 1 stale exemptions, 1 renderers missing"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarize(tc.report); got != tc.want {
				t.Errorf("summarize() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRelativePath_NamesAFileTheWayAReportDoes verifies a finding points at a
// path a reader can open, with forward slashes on every platform.
func TestRelativePath_NamesAFileTheWayAReportDoes(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	inside := filepath.Join(root, "internal", "tools", "markdown.go")
	if got := relativePath(inside, root); got != "internal/tools/markdown.go" {
		t.Errorf("relativePath() = %q, want the repository-relative path", got)
	}
	if got := relativePath(inside, "relative-root"); !strings.Contains(got, "markdown.go") {
		t.Errorf("relativePath() with an unrelatable root = %q, want the path itself", got)
	}
}

// TestSortFindings_OrdersByFileThenLineThenExpression pins the comparator two
// findings on one line rest on, so a report is stable across runs.
func TestSortFindings_OrdersByFileThenLineThenExpression(t *testing.T) {
	findings := []Finding{
		{File: "b.go", Line: 1, Expression: "x"},
		{File: "a.go", Line: 9, Expression: "x"},
		{File: "a.go", Line: 2, Expression: "z"},
		{File: "a.go", Line: 2, Expression: "y"},
	}
	sortFindings(findings)

	var order []string
	for _, finding := range findings {
		order = append(order, finding.File+":"+finding.Expression)
	}
	want := "a.go:y a.go:z a.go:x b.go:x"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("sortFindings() = %q, want %q", got, want)
	}
}
