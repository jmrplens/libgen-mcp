package main

import (
	"encoding/json"
	"strings"
	"testing"
)

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
