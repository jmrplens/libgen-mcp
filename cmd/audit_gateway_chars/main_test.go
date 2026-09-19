// main_test.go covers what the audit decides and what it prints: which
// characters offend, where in a schema prose is looked for, and the three
// exit codes the command can end with.
package main

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"
)

// TestScanText_WhatOffendsAndWhatDoesNot pins the policy itself: every rune
// above U+007F, plus the listed ASCII characters, and nothing else.
func TestScanText_WhatOffendsAndWhatDoesNot(t *testing.T) {
	testCases := []struct {
		name string
		text string
		want bool
	}{
		{name: "plain ASCII prose", text: "Download a file to a local directory.", want: false},
		{name: "every other ASCII punctuation mark", text: `a,b.c:d!e?f'g"h(i)j[k]l{m}n/o\p|q-r_s+t=u*v&w^x%y$z#~@`, want: false},
		{name: "a semicolon", text: "Provide md5; at least one is required", want: true},
		{name: "an em dash", text: "Full metadata — identifiers", want: true},
		{name: "an ellipsis", text: "truncated…", want: true},
		{name: "a non-breaking space", text: "two words", want: true},
		{name: "an accented letter", text: "José", want: true},
		{name: "empty", text: "", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanText("tools", "somewhere", tc.text)
			if (len(got) > 0) != tc.want {
				t.Errorf("scanText(%q) found %d offender(s), want offending=%t", tc.text, len(got), tc.want)
			}
		})
	}
}

// TestScanText_ReportsOneOffenderPerStringWithContext verifies the report
// shape: one row per string however many characters offend, with an excerpt
// around the first, so a fix is findable without reading the whole surface.
func TestScanText_ReportsOneOffenderPerStringWithContext(t *testing.T) {
	text := strings.Repeat("a", 50) + "; and then — more"
	got := scanText("tools", "tool search description", text)
	if len(got) != 1 {
		t.Fatalf("scanText() returned %d offenders, want exactly one per string", len(got))
	}
	if !strings.Contains(got[0].excerpt, ";") {
		t.Errorf("excerpt %q does not show the offending character", got[0].excerpt)
	}
	if len(got[0].excerpt) >= len(text) {
		t.Errorf("excerpt %q is not an excerpt", got[0].excerpt)
	}
}

// TestScanText_FullPrintsTheWholeStringOnOneLine verifies -full, which exists
// so the reported text can be pasted into a grep of the source.
func TestScanText_FullPrintsTheWholeStringOnOneLine(t *testing.T) {
	fullStrings = true
	t.Cleanup(func() { fullStrings = false })

	got := scanText("tools", "where", "first line; second\nthird")
	if len(got) != 1 {
		t.Fatalf("scanText() returned %d offenders, want one", len(got))
	}
	if strings.Contains(got[0].excerpt, "\n") {
		t.Errorf("excerpt %q carries a raw newline, which breaks one-line output", got[0].excerpt)
	}
	if !strings.Contains(got[0].excerpt, "third") {
		t.Errorf("excerpt %q is not the whole string", got[0].excerpt)
	}
}

// TestScanSchema_LooksOnlyWhereProseIs verifies that a schema's prose keys are
// scanned and its machinery is not: a pattern is a regular expression, and a
// gateway refusing one would not be reporting this problem.
func TestScanSchema_LooksOnlyWhereProseIs(t *testing.T) {
	schema := map[string]any{
		"type":    "object",
		"pattern": "^[0-9a-f]{32};$",
		"properties": map[string]any{
			"md5": map[string]any{
				"type":        "string",
				"description": "file md5; from a search result",
			},
			"nested": map[string]any{
				"items": []any{
					map[string]any{"title": "a title — with an em dash"},
				},
			},
		},
	}

	got := scanSchema("tool x input schema", schema)
	if len(got) != 2 {
		t.Fatalf("scanSchema() found %d offenders, want the description and the nested title only: %+v", len(got), got)
	}
}

// TestScanSchema_UnserializableOrAbsentSchemasAreNotFindings verifies the two
// ways a schema can yield nothing without that being a problem to report.
func TestScanSchema_UnserializableOrAbsentSchemasAreNotFindings(t *testing.T) {
	if got := scanSchema("where", nil); got != nil {
		t.Errorf("scanSchema(nil) = %+v, want no offenders", got)
	}
	if got := scanSchema("where", make(chan int)); got != nil {
		t.Errorf("scanSchema(unserializable) = %+v, want no offenders", got)
	}
}

// TestReport_VerdictAndExitCodes verifies that -check is what decides the exit
// code, and that a clean surface says so rather than printing nothing.
func TestReport_VerdictAndExitCodes(t *testing.T) {
	testCases := []struct {
		name     string
		found    []offender
		check    bool
		wantCode int
		wantText string
	}{
		{name: "clean without check", wantCode: 0, wantText: "nothing served carries"},
		{name: "clean with check", check: true, wantCode: 0, wantText: "nothing served carries"},
		{
			name:     "offenders without check are reported and tolerated",
			found:    []offender{{surface: "tools", where: "tool a description", excerpt: "x; y"}},
			wantCode: 0,
			wantText: "1 served string(s)",
		},
		{
			name:     "offenders with check fail",
			found:    []offender{{surface: "tools", where: "tool a description", excerpt: "x; y"}},
			check:    true,
			wantCode: 1,
			wantText: "1 served string(s)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			stdout = &out
			t.Cleanup(func() { stdout = os.Stdout })

			if got := report(tc.found, tc.check); got != tc.wantCode {
				t.Errorf("report() = %d, want %d", got, tc.wantCode)
			}
			if !strings.Contains(out.String(), tc.wantText) {
				t.Errorf("report() printed %q, want it to mention %q", out.String(), tc.wantText)
			}
		})
	}
}

// TestReport_OrdersBySurfaceThenLocation verifies the report is stable, so a
// diff between two runs shows what changed rather than how the map iterated.
func TestReport_OrdersBySurfaceThenLocation(t *testing.T) {
	var out bytes.Buffer
	stdout = &out
	t.Cleanup(func() { stdout = os.Stdout })

	report([]offender{
		{surface: "tools", where: "tool z", excerpt: ";"},
		{surface: "prompts", where: "prompt b", excerpt: ";"},
		{surface: "tools", where: "tool a", excerpt: ";"},
		{surface: "prompts", where: "prompt a", excerpt: ";"},
	}, false)

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"prompt a", "prompt b", "tool a", "tool z"}
	for i, fragment := range want {
		if !strings.Contains(lines[i], fragment) {
			t.Errorf("line %d = %q, want it to name %q", i, lines[i], fragment)
		}
	}
}

// TestRun_TheRealSurfaceIsClean drives the command against the catalog this
// binary carries, which is the gate itself: it is the assertion that the
// served surface a client receives carries nothing a gateway refuses.
func TestRun_TheRealSurfaceIsClean(t *testing.T) {
	var out bytes.Buffer
	stdout = &out
	t.Cleanup(func() { stdout = os.Stdout })

	if got := run(true); got != 0 {
		t.Errorf("run(check) = %d, want 0. The report:\n%s", got, out.String())
	}
}

// TestMainEntry_ParsesItsFlagsAndExitsWithTheStatusRunReturned covers the one
// path the tests above do not: the command line main assembles.
func TestMainEntry_ParsesItsFlagsAndExitsWithTheStatusRunReturned(t *testing.T) {
	oldArgs, oldFlags, oldExit := os.Args, flag.CommandLine, osExit
	t.Cleanup(func() {
		os.Args, flag.CommandLine, osExit = oldArgs, oldFlags, oldExit
		fullStrings = false
		stdout = os.Stdout
	})

	var out bytes.Buffer
	stdout = &out
	code := -1
	osExit = func(c int) { code = c }
	flag.CommandLine = flag.NewFlagSet("audit_gateway_chars", flag.ContinueOnError)
	os.Args = []string{"audit_gateway_chars", "-check", "-full"}

	main()

	if code != 0 {
		t.Errorf("main() exited %d, want 0 on a clean surface. The report:\n%s", code, out.String())
	}
	if !fullStrings {
		t.Error("main() did not parse -full")
	}
}
