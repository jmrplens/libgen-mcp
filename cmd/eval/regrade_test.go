//go:build eval

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fullRecord is a scenario record with every field an assertion can read set to
// something distinguishable, so a restore that drops one is visible.
func fullRecord() scenarioRecord {
	return scenarioRecord{
		ID:           "S48",
		Mode:         "remote",
		Model:        "claude-test-1",
		Status:       "PASS",
		FinalAnswer:  "here is what I found",
		ToolsOffered: toolsWithSourceEnum([]string{"europepmc", "biorxiv", "fatcat", "scihub", "scidb"}),
		Calls: []callRecord{{
			Name:       "download",
			Input:      map[string]any{"doi": openAccessDOI},
			IsError:    true,
			Text:       "the markdown the model read",
			Structured: map[string]any{"source": "europepmc", "size_bytes": float64(12)},
			ServerLogs: []string{"source in cooldown, skipping"},
		}},
		Elicitations: []elicitRecord{
			{Field: "email", Action: "accept"},
			{Field: "confirm_save", Action: "accept"},
			{Field: "annas key", Action: "decline"},
		},
		Progress: []progressRecord{{Token: downloadProgressToken, Progress: 7, Total: 9, Message: "downloading"}},
		Fetched:  []fetchedFile{{URL: "https://example.invalid/x.pdf", Size: 3}},
		Turns:    []turnRecord{{N: 1, Text: "let me look"}},
	}
}

// TestTranscriptFromRecordRestoresEverySurface is the property --regrade depends
// on and the one that has already broken twice: an assertion is only a pure
// function of the transcript if the transcript comes back whole. A field left
// nil does not fail loudly — it regrades as an empty slice and the assertion
// quietly agrees with whatever emptiness implies.
func TestTranscriptFromRecordRestoresEverySurface(t *testing.T) {
	tr := transcriptFromRecord(fullRecord())

	if tr.FinalText != "here is what I found" {
		t.Errorf("final answer = %q", tr.FinalText)
	}
	// The tool surface matters most: a scenario grading the download tool's source
	// enum reads it, and an empty slice makes that scenario pass for the wrong reason.
	enum, ok := downloadSourceEnum(tr)
	if !ok || len(enum) != 5 {
		t.Fatalf("the tool surface did not survive the record: enum=%v ok=%v", enum, ok)
	}
	if len(tr.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(tr.Calls))
	}
	call := tr.Calls[0]
	if call.Result == nil || !call.Result.IsError {
		t.Error("the call's error flag did not survive")
	}
	if len(call.ServerLogs) != 1 {
		t.Error("server logs did not survive, so the cooldown scenario cannot regrade")
	}
	if _, found := cooldownDecision(tr); !found {
		t.Error("cooldownDecision reads the restored logs and found nothing")
	}
	if len(tr.Progress) != 1 || tr.Progress[0].Progress != 7 {
		t.Errorf("progress did not survive: %+v", tr.Progress)
	}
	// The token is what ties a notification to the call that emitted it; storing a
	// notification without it once made a working stream regrade as missing.
	if got := tr.Progress[0].ProgressToken; got != downloadProgressToken {
		t.Errorf("progress token = %v, want %q", got, downloadProgressToken)
	}
	if len(tr.Fetched) != 1 || len(tr.Turns) != 1 || len(tr.Elicitations) != 3 {
		t.Errorf("fetched/turns/elicitations did not all survive: %+v", tr)
	}
	// The confirmation count is derived: exactly the accepted prompts that are
	// neither the contact email nor the membership key.
	if tr.ConfirmElicits != 1 {
		t.Errorf("ConfirmElicits = %d, want 1 (the email and the declined key must not count)", tr.ConfirmElicits)
	}
}

// TestRestoredResultReadsAsItDidLive guards the divergence --regrade exists to
// rule out: resultText prefers the structured payload over the text content, so a
// restored result that carried only the text read differently from the live one it
// was recorded from.
func TestRestoredResultReadsAsItDidLive(t *testing.T) {
	tr := transcriptFromRecord(fullRecord())
	got := resultText(tr.Calls[0].Result)
	want, err := json.Marshal(fullRecord().Calls[0].Structured)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("restored result reads as %q, want the structured payload %q", got, want)
	}
}

// TestRegradeOneStatuses checks the three outcomes a re-grade can produce: a
// harness error is carried through untouched, a skip-prefixed message is a skip
// rather than a pass, and an ordinary assertion decides the rest.
func TestRegradeOneStatuses(t *testing.T) {
	rec := fullRecord()

	broken := rec
	broken.Error = "scenario exceeded its budget"
	if oc := regradeOne(scenario{ID: rec.ID}, broken); oc.Status != statusError || oc.Message != broken.Error {
		t.Errorf("a recorded harness error must regrade as an error, got %+v", oc)
	}

	skipping := scenario{ID: rec.ID, Assert: func(transcript) (bool, string) {
		return true, skipPrefix + " nothing to grade"
	}}
	if oc := regradeOne(skipping, rec); oc.Status != statusSkip {
		t.Errorf("a skip-prefixed message must regrade as a skip, got %+v", oc)
	}

	failing := scenario{ID: rec.ID, Assert: func(transcript) (bool, string) { return false, "no" }}
	oc := regradeOne(failing, rec)
	if oc.Status != statusFail || !oc.Remote {
		t.Errorf("regradeOne = %+v, want a failed remote outcome", oc)
	}
}

// TestReadRecordsRejectsAnEmptyFile checks the loader says so rather than
// reporting a clean re-grade of nothing.
func TestReadRecordsRejectsAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readRecords(path)
	if err == nil || !strings.Contains(err.Error(), "no scenarios") {
		t.Fatalf("readRecords on an empty record = %v, want a no-scenarios error", err)
	}
}
