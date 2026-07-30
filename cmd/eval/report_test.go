//go:build eval

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testModel is the model name the results-doc tests record against. The merge
// refuses to mix models, so every doc in a test has to agree on one.
const testModel = "claude-test-1"

// day builds a fixed measurement instant, so a test never depends on today.
func day(iso string) time.Time {
	t, err := time.Parse(measuredDateLayout, iso)
	if err != nil {
		panic(err)
	}
	return t
}

// writeDoc writes a results doc and fails the test if the write errors.
func writeDoc(t *testing.T, path string, outcomes []outcome, when string) {
	t.Helper()
	if err := writeResultsDocAt(path, outcomes, testModel, day(when)); err != nil {
		t.Fatalf("writeResultsDocAt: %v", err)
	}
}

// TestWriteResultsDocCreatesTable verifies the first write of a doc that does not
// exist yet: the header names the model, the table carries the Measured column,
// and the row records the injected date.
func TestWriteResultsDocCreatesTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.md")
	writeDoc(t, path, []outcome{{ID: "S1", Status: statusPass, Message: "first"}}, "2026-01-02")

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"Model: `" + testModel + "`",
		"| Scenario | Mode | Status | Measured | Detail |",
		"| S1 | local | PASS | 2026-01-02 | first |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("results doc does not contain %q; got:\n%s", want, got)
		}
	}
}

// TestWriteResultsDocMergesPartialRun is the behavior the incremental flow exists
// for: a run of one scenario must refresh that scenario's row and leave every other
// row exactly as it was, so re-measuring one source does not cost a full suite.
func TestWriteResultsDocMergesPartialRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.md")
	writeDoc(t, path, []outcome{
		{ID: "S1", Status: statusPass, Message: "old first"},
		{ID: "S61", Status: statusFail, Message: "old standards"},
	}, "2026-01-02")

	// A later, partial run: only S61 executed.
	writeDoc(t, path, []outcome{{ID: "S61", Status: statusPass, Message: "new standards"}}, "2026-03-04")

	rows, err := readResultsDoc(path, testModel)
	if err != nil {
		t.Fatalf("readResultsDoc: %v", err)
	}
	if got := rows["S1"]; got.Status != "PASS" || got.Measured != "2026-01-02" || got.Detail != "old first" {
		t.Errorf("untouched row was modified: %+v", got)
	}
	if got := rows["S61"]; got.Status != "PASS" || got.Measured != "2026-03-04" || got.Detail != "new standards" {
		t.Errorf("executed row was not refreshed: %+v", got)
	}
}

// TestWriteResultsDocOrdersBySuite verifies rows come out in run order however they
// accumulated: a scenario measured later must not jump ahead of one measured first.
func TestWriteResultsDocOrdersBySuite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.md")
	writeDoc(t, path, []outcome{{ID: "S61", Status: statusPass, Message: "later scenario, earlier run"}}, "2026-01-02")
	writeDoc(t, path, []outcome{{ID: "S1", Status: statusPass, Message: "earlier scenario, later run"}}, "2026-03-04")

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	first := strings.Index(string(body), "| S1 |")
	later := strings.Index(string(body), "| S61 |")
	if first < 0 || later < 0 {
		t.Fatalf("both rows must be present; got:\n%s", body)
	}
	if first > later {
		t.Error("rows are ordered by when they were measured, not by the order the suite runs them")
	}
}

// TestWriteResultsDocRefusesAnotherModel verifies the safeguard: merging a run into
// a table recorded against a different model would publish one pass rate built from
// two models, so it must fail rather than silently mix them.
func TestWriteResultsDocRefusesAnotherModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.md")
	writeDoc(t, path, []outcome{{ID: "S1", Status: statusPass, Message: "recorded"}}, "2026-01-02")

	err := writeResultsDocAt(path, []outcome{{ID: "S1", Status: statusPass, Message: "other model"}},
		"some-other-model", day("2026-03-04"))
	if err == nil {
		t.Fatal("merging a run from another model must fail")
	}
	if !strings.Contains(err.Error(), "not comparable") {
		t.Errorf("error should explain why the mix is refused, got: %v", err)
	}
}

// TestWriteResultsDocDropsUnknownScenario verifies a recorded row whose scenario no
// longer exists is not carried forward: publishing a result the suite can no longer
// produce would be worse than publishing none.
func TestWriteResultsDocDropsUnknownScenario(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.md")
	writeDoc(t, path, []outcome{
		{ID: "S1", Status: statusPass, Message: "still here"},
		{ID: "S999", Status: statusPass, Message: "a scenario that was deleted"},
	}, "2026-01-02")
	writeDoc(t, path, []outcome{{ID: "S1", Status: statusPass, Message: "still here"}}, "2026-03-04")

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(body), "S999") {
		t.Error("a row for a scenario the suite no longer defines was kept")
	}
}

// TestWriteResultsDocEscapesPipes verifies an evidence message containing a pipe
// cannot break the table it is written into — and survives the round-trip, since the
// merge reads its own output back.
func TestWriteResultsDocEscapesPipes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.md")
	writeDoc(t, path, []outcome{{ID: "S1", Status: statusPass, Message: "a | b"}}, "2026-01-02")

	rows, err := readResultsDoc(path, testModel)
	if err != nil {
		t.Fatalf("readResultsDoc: %v", err)
	}
	if got := rows["S1"].Detail; got != `a \| b` {
		t.Errorf("Detail = %q, want the pipe escaped", got)
	}
	if rows["S1"].Status != "PASS" {
		t.Errorf("a pipe in the detail shifted the other cells: %+v", rows["S1"])
	}
}
