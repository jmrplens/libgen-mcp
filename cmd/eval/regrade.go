//go:build eval

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Re-grading answers a question the harness could not answer before: "would this
// assertion have judged the last run differently?" An assertion is a pure function
// of a transcript, and the run record holds the whole transcript — so the answer
// costs nothing and takes a second, instead of another live run against the API,
// the mirrors and someone's download quota.
//
// It is valid for a change to the ASSERTIONS only. Changing the server, the tools
// or a prompt changes what a live run would produce, and no amount of re-grading an
// old record will show it: that needs a real run.

// regrade re-runs every assertion against a recorded run and reports the outcomes.
func regrade(recordPath, resultsDoc string) int {
	records, err := readRecords(recordPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	byID := map[string]scenario{}
	for _, sc := range scenarios() {
		byID[sc.ID] = sc
	}

	outcomes := make([]outcome, 0, len(records))
	failures := 0
	for _, rec := range records {
		sc, known := byID[rec.ID]
		if !known {
			fmt.Fprintf(os.Stderr, "record holds scenario %q, which no longer exists; skipping\n", rec.ID)
			continue
		}
		oc := regradeOne(sc, rec)
		outcomes = append(outcomes, oc)
		if oc.Status == statusFail || oc.Status == statusError {
			failures++
		}
		changed := ""
		if !strings.EqualFold(oc.Status, rec.Status) {
			changed = fmt.Sprintf("  (was %s)", strings.ToUpper(rec.Status))
		}
		fmt.Printf("[%-5s] %s: %s%s\n", strings.ToUpper(oc.Status), oc.ID, oc.Message, changed)
	}

	printReport(os.Stdout, outcomes)
	fmt.Println("re-graded from", recordPath, "— no live calls were made")
	if resultsDoc != "" {
		if werr := writeResultsDoc(resultsDoc, outcomes, evalModel); werr != nil {
			fmt.Fprintln(os.Stderr, werr)
			return 1
		}
	}
	if failures > 0 {
		return 1
	}
	return 0
}

// regradeOne applies one scenario's current assertion to its recorded transcript.
func regradeOne(sc scenario, rec scenarioRecord) outcome {
	tr := transcriptFromRecord(rec)
	oc := outcome{ID: rec.ID, Calls: len(tr.Calls), Remote: rec.Mode == "remote"}
	if rec.Error != "" {
		oc.Status, oc.Message = statusError, rec.Error
		return oc
	}
	ok, msg := sc.Assert(tr)
	oc.Message = msg
	switch {
	case strings.HasPrefix(msg, skipPrefix):
		oc.Status = statusSkip
	case ok:
		oc.Status = statusPass
	default:
		oc.Status = statusFail
	}
	return oc
}

// transcriptFromRecord rebuilds the transcript an assertion grades. The tool
// results are reconstructed rather than replayed: an assertion reads a call's
// error flag, its text and its structured output, and the record holds all three.
func transcriptFromRecord(rec scenarioRecord) transcript {
	tr := transcript{
		FinalText:    rec.FinalAnswer,
		Fetched:      rec.Fetched,
		Turns:        rec.Turns,
		Elicitations: rec.Elicitations,
	}
	for _, c := range rec.Calls {
		tr.Calls = append(tr.Calls, toolCall{
			Name:       c.Name,
			Input:      c.Input,
			Structured: c.Structured,
			Result: &mcp.CallToolResult{
				IsError: c.IsError,
				Content: []mcp.Content{&mcp.TextContent{Text: c.Text}},
			},
			ServerLogs: c.ServerLogs,
		})
	}
	for _, p := range rec.Progress {
		tr.Progress = append(tr.Progress, mcp.ProgressNotificationParams{
			ProgressToken: p.Token, Progress: p.Progress, Total: p.Total, Message: p.Message,
		})
	}
	// The confirmation count is derived rather than stored: it is exactly the
	// save-confirmation prompts the host accepted, which the elicitation log names.
	for _, e := range rec.Elicitations {
		if e.Action == "accept" && !strings.Contains(strings.ToLower(e.Field), "email") &&
			!strings.Contains(strings.ToLower(e.Field), "key") {
			tr.ConfirmElicits++
		}
	}
	return tr
}

// readRecords loads a JSONL run record.
func readRecords(path string) ([]scenarioRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open record: %w", err)
	}
	defer func() { _ = f.Close() }()

	var out []scenarioRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // records carry whole tool payloads
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec scenarioRecord
		if uerr := json.Unmarshal([]byte(line), &rec); uerr != nil {
			return nil, fmt.Errorf("decode record line %d: %w", len(out)+1, uerr)
		}
		out = append(out, rec)
	}
	if serr := sc.Err(); serr != nil {
		return nil, fmt.Errorf("read record: %w", serr)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("record %s holds no scenarios", path)
	}
	return out, nil
}
