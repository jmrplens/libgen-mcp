package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeResultsFixture writes a results doc body to a temp file and returns its path.
func writeResultsFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestReadResultsParsesMeasured verifies the results parser reads the Measured
// column and keeps a detail containing an escaped pipe in one piece.
func TestReadResultsParsesMeasured(t *testing.T) {
	path := writeResultsFixture(t, `# run

Model: `+"`m`"+`

| Scenario | Mode | Status | Measured | Detail |
| --- | --- | --- | --- | --- |
| S1 | local | PASS | 2026-01-02 | plain |
| S2 | remote | SKIP | 2026-03-04 | has a \| pipe |
`)
	rows, err := readResults(path)
	if err != nil {
		t.Fatalf("readResults: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("read %d rows, want 2", len(rows))
	}
	if rows[0].Measured != "2026-01-02" || rows[0].Detail != "plain" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Mode != "remote" || rows[1].Measured != "2026-03-04" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if !strings.Contains(rows[1].Detail, "pipe") {
		t.Errorf("an escaped pipe split the detail: %q", rows[1].Detail)
	}
}

// TestSummarizeSpansMeasurementDates verifies the summary reports the earliest and
// latest dates in the table, which is what tells the prose whether it may still call
// itself a single run.
func TestSummarizeSpansMeasurementDates(t *testing.T) {
	sum := summarize("m", []resultRow{
		{ID: "S1", Mode: "local", Status: "PASS", Measured: "2026-03-04"},
		{ID: "S2", Mode: "remote", Status: "PASS", Measured: "2026-01-02"},
		{ID: "S3", Mode: "local", Status: "FAIL", Measured: "2026-02-03"},
	})
	if sum.First != "2026-01-02" || sum.Last != "2026-03-04" {
		t.Errorf("span = %s..%s, want 2026-01-02..2026-03-04", sum.First, sum.Last)
	}
	if sum.Pass != 2 || sum.Fail != 1 || sum.Remote != 1 {
		t.Errorf("tallies = %+v", sum)
	}
}

// TestMeasuredSpanWording verifies the prose stops claiming a single sweep once the
// table holds rows from more than one run, and says so in both languages.
func TestMeasuredSpanWording(t *testing.T) {
	one := runSummary{First: "2026-01-02", Last: "2026-01-02"}
	if got := measuredSpanEN(one); !strings.Contains(got, "a single live run") {
		t.Errorf("single-date EN wording = %q", got)
	}
	if got := measuredTailEN(one); got != "" {
		t.Errorf("a single-run table needs no explanation, got %q", got)
	}

	many := runSummary{First: "2026-01-02", Last: "2026-03-04"}
	gotEN := measuredSpanEN(many)
	if strings.Contains(gotEN, "single") || !strings.Contains(gotEN, "2026-03-04") {
		t.Errorf("multi-date EN wording = %q", gotEN)
	}
	if measuredTailEN(many) == "" {
		t.Error("a multi-run table must explain why the dates differ")
	}
	gotES := measuredSpanES(many)
	if strings.Contains(gotES, "única") || !strings.Contains(gotES, "2026-03-04") {
		t.Errorf("multi-date ES wording = %q", gotES)
	}
	if measuredTailES(many) == "" {
		t.Error("the Spanish page must carry the same explanation")
	}
}

// TestMeasuredSpanWithoutDates verifies a table whose rows carry no date (an older
// results doc) still produces a sentence rather than an empty claim.
func TestMeasuredSpanWithoutDates(t *testing.T) {
	none := runSummary{}
	if got := measuredSpanEN(none); got == "" || strings.Contains(got, "between") {
		t.Errorf("dateless EN wording = %q", got)
	}
	if got := measuredSpanES(none); got == "" {
		t.Error("dateless ES wording is empty")
	}
}
