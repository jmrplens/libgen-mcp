package main

import (
	"strings"
	"testing"
)

// TestClassify_ReadsWhatTheValueIsMadeOf pins the shapes a composed value can
// take. A value the server wrote and a value a catalog sent are told apart by
// following the expression back, not by where it is written.
func TestClassify_ReadsWhatTheValueIsMadeOf(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "a concatenation of literals",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", \"md5:\"+\"none\")\n}",
			want: nil,
		},
		{
			name: "a concatenation with a raw half",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", \"md5:\"+r.Title)\n}",
			want: []string{`unescaped table-cell "md5:" + r.Title`},
		},
		{
			name: "a comparison is not text",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %v |\\n\", r.Pages > 0)\n}",
			want: nil,
		},
		{
			name: "the package's own constant",
			body: "// fallback is what this package writes when it has nothing.\nconst fallback = \"(none)\"\n\n// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", fallback)\n}",
			want: nil,
		},
		{
			name: "another swept package's constant",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", toolutil.Dash)\n}",
			want: nil,
		},
		{
			name: "another package the sweep does not read",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", strings.Title)\n}",
			want: []string{"unresolved table-cell strings.Title"},
		},
		{
			name: "a helper returning more than one value",
			body: "// split names a record.\nfunc split(r record) (string, string) { return r.Title, r.URL }\n\n// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\ttitle, _ := split(r)\n\tfmt.Fprintf(b, \"| %s |\\n\", title)\n}",
			want: []string{"unresolved table-cell title"},
		},
		{
			name: "a number formatted by strconv",
			body: "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", strconv.Itoa(r.Pages))\n}",
			want: nil,
		},
		{
			name: "a conversion carries its operand",
			body: "// kind is a catalog-supplied label.\ntype kind string\n\n// row writes a table row.\nfunc row(b *strings.Builder, r record, k kind) {\n\tfmt.Fprintf(b, \"| %s |\\n\", string(k))\n}",
			want: []string{"unresolved table-cell string(k)"},
		},
		{
			name: "a helper that reaches itself",
			body: "// clean trims a value n times over.\nfunc clean(s string, n int) string {\n\tif n == 0 {\n\t\treturn s\n\t}\n\treturn clean(strings.TrimSpace(s), n-1)\n}\n\n// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", clean(r.Title, 2))\n}",
			want: []string{"unescaped table-cell clean(r.Title, 2)"},
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

// TestAudit_SortsWhatItFoundByWhereItIs verifies a report reads in the order a
// person would open the files, which is what makes a work list one.
func TestAudit_SortsWhatItFoundByWhereItIs(t *testing.T) {
	declared := markdownEntryPoints
	markdownEntryPoints = map[string][]string{}
	t.Cleanup(func() { markdownEntryPoints = declared })

	body := "//libgen:allow-unescaped r.Gone: nothing writes this\n" +
		"//libgen:allow-unescaped r.AlsoGone: nor this\n" +
		"// second writes the later row.\nfunc second(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", r.URL)\n}\n\n" +
		"// first writes the earlier row.\nfunc first(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", r.Title)\n}"
	report := auditFixture(t, body)

	if len(report.Findings) != 2 {
		t.Fatalf("Findings = %v, want two", verdicts(report))
	}
	if report.Findings[0].Line >= report.Findings[1].Line {
		t.Errorf("findings are at lines %d then %d, want them in source order",
			report.Findings[0].Line, report.Findings[1].Line)
	}
	if len(report.Stale) != 2 || report.Stale[0].Line >= report.Stale[1].Line {
		t.Errorf("stale declarations are not in source order: %v", report.Stale)
	}
}

// TestAudit_AFindingNamesTheFunctionAndTheFileItIsIn verifies a finding is
// actionable on its own: the file, the line, the function and the helper the
// context wants.
func TestAudit_AFindingNamesTheFunctionAndTheFileItIsIn(t *testing.T) {
	body := "// row writes a table row.\nfunc row(b *strings.Builder, r record) {\n\tfmt.Fprintf(b, \"| %s |\\n\", r.Title)\n}"
	report := auditFixture(t, body)
	if len(report.Findings) != 1 {
		t.Fatalf("Findings = %v, want one", verdicts(report))
	}

	finding := report.Findings[0]
	testCases := []struct{ name, got, want string }{
		{name: "package", got: finding.Package, want: "internal/render"},
		{name: "file", got: finding.File, want: "internal/render/render.go"},
		{name: "function", got: finding.Function, want: "row"},
		{name: "context", got: finding.Context, want: "table-cell"},
		{name: "verb", got: finding.Verb, want: "%s"},
		{name: "wants", got: finding.Wants, want: "toolutil.EscapeMdTableCell"},
		{name: "verdict", got: finding.Verdict, want: "unescaped"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
	if finding.Line == 0 || finding.Reason == "" {
		t.Errorf("finding = %+v, want a line and a reason", finding)
	}
}
