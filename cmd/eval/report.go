//go:build eval

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// scenario outcome statuses.
const (
	statusPass  = "pass"
	statusFail  = "fail"
	statusSkip  = "skip"
	statusError = "error"
)

// outcome is the graded result of one scenario.
type outcome struct {
	ID      string
	Status  string
	Message string
	Calls   int
	// Remote marks the scenario as part of the remote block (server in remote
	// mode: download returns a link that the harness fetches to local disk).
	Remote bool
}

// blockLabel names the eval block an outcome belongs to: "remote" for the
// remote block (server in `--http` mode returning a link), "local" otherwise.
func (o outcome) blockLabel() string {
	if o.Remote {
		return "remote"
	}
	return "local"
}

// printReport writes a human-readable summary of all outcomes to w, labeling
// each scenario with its block (local vs. remote) and breaking the pass/fail
// tally down by block.
func printReport(w io.Writer, outcomes []outcome) {
	fmt.Fprintln(w, "\nlibgen-mcp live LLM eval results")
	fmt.Fprintln(w, "================================")
	var pass, fail, skip, fail2 int
	var localPass, localTotal, remotePass, remoteTotal int
	for _, o := range outcomes {
		fmt.Fprintf(w, "%-4s %-6s %-6s %s\n", o.ID, o.blockLabel(), strings.ToUpper(o.Status), o.Message)
		if o.Remote {
			remoteTotal++
			if o.Status == statusPass {
				remotePass++
			}
		} else {
			localTotal++
			if o.Status == statusPass {
				localPass++
			}
		}
		switch o.Status {
		case statusPass:
			pass++
		case statusFail:
			fail++
		case statusSkip:
			skip++
		case statusError:
			fail2++
		}
	}
	fmt.Fprintf(w, "--------------------------------\n%d passed, %d failed, %d errored, %d skipped (of %d)\n",
		pass, fail, fail2, skip, len(outcomes))
	fmt.Fprintf(w, "local block: %d/%d passed | remote block: %d/%d passed\n",
		localPass, localTotal, remotePass, remoteTotal)
}

// writeResultsDoc writes a markdown results table to path, with a Mode column
// distinguishing the local and remote blocks.
func writeResultsDoc(path string, outcomes []outcome, model string) error {
	return writeResultsDocAt(path, outcomes, model, time.Now())
}

// writeResultsDocAt is writeResultsDoc with the measurement instant injected, so
// the merge is testable without depending on today's date.
//
// A run MERGES into the doc already at path rather than replacing it: a partial
// run (--only S61,S62) refreshes just the scenarios it executed and leaves every
// other row as it was. That is what makes it possible to re-measure one source
// without spending a full suite — 66 scenarios against a real API, real mirrors
// and real downloads — to publish the result.
//
// Two things keep the merged table honest. Every row carries the date it was
// measured, so a reader can see which rows are old; and a run whose model differs
// from the recorded one is REFUSED, because pass rates from two models in one
// table invite a comparison the table cannot support.
func writeResultsDocAt(path string, outcomes []outcome, model string, now time.Time) error {
	rows, err := mergedResultRows(path, outcomes, model, now)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# libgen-mcp live LLM eval results\n\n")
	fmt.Fprintf(&b, "Model: `%s`\n\n", model)
	b.WriteString("| Scenario | Mode | Status | Measured | Detail |\n| --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", r.ID, r.Mode, r.Status, r.Measured, r.Detail)
	}
	if werr := os.WriteFile(path, []byte(b.String()), 0o600); werr != nil {
		return fmt.Errorf("write results doc: %w", werr)
	}
	return nil
}

// recordedResult is one row of a results doc.
type recordedResult struct {
	// ID is the scenario id the row reports on.
	ID string
	// Mode is the block the scenario ran in ("local" or "remote").
	Mode string
	// Status is the graded outcome, upper-cased (PASS/FAIL/SKIP/ERROR).
	Status string
	// Measured is the date the row was produced, as YYYY-MM-DD.
	Measured string
	// Detail is the evidence sentence the assertion returned.
	Detail string
}

// measuredDateLayout is how a row's measurement date is written: a plain date,
// because the suite is re-run at human intervals and a timestamp would suggest a
// precision the number does not carry.
const measuredDateLayout = "2006-01-02"

// mergedResultRows overlays this run's outcomes onto the rows already recorded at
// path and returns them in suite order.
//
// Ordering follows scenarios() rather than the file, so the table reads in run
// order however the rows accumulated. A recorded row whose scenario no longer
// exists is DROPPED: leaving it would publish a result for something the suite can
// no longer produce.
func mergedResultRows(path string, outcomes []outcome, model string, now time.Time) ([]recordedResult, error) {
	previous, err := readResultsDoc(path, model)
	if err != nil {
		return nil, err
	}
	measured := now.Format(measuredDateLayout)
	for _, o := range outcomes {
		previous[o.ID] = recordedResult{
			ID:       o.ID,
			Mode:     o.blockLabel(),
			Status:   strings.ToUpper(o.Status),
			Measured: measured,
			Detail:   escapeCell(o.Message),
		}
	}
	var rows []recordedResult
	for _, sc := range scenarios() {
		if row, ok := previous[sc.ID]; ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// resultsDocModel matches the model line a results doc opens with.
var resultsDocModel = regexp.MustCompile("^Model: `([^`]+)`")

// resultsDocRow matches one row of a results doc's table, capturing the five
// cells. The header and separator rows do not match, since neither opens with a
// scenario id.
var resultsDocRow = regexp.MustCompile(`^\|\s*(S\w+)\s*\|([^|]*)\|([^|]*)\|([^|]*)\|(.*)\|\s*$`)

// readResultsDoc reads the rows already recorded at path, keyed by scenario id. A
// missing file is not an error — that is simply the first run — but a file
// recorded against a different model is, for the reason writeResultsDocAt gives.
func readResultsDoc(path, model string) (map[string]recordedResult, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- operator-supplied output path
	if errors.Is(err, os.ErrNotExist) {
		return map[string]recordedResult{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read results doc: %w", err)
	}
	rows := map[string]recordedResult{}
	for line := range strings.SplitSeq(string(body), "\n") {
		if m := resultsDocModel.FindStringSubmatch(line); len(m) > 1 && m[1] != model {
			return nil, fmt.Errorf(
				"results doc %s was recorded against model %q but this run used %q; "+
					"pass rates from two models in one table are not comparable, so re-run the "+
					"full suite or write to a different --results-doc", path, m[1], model)
		}
		m := resultsDocRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows[m[1]] = recordedResult{
			ID:       m[1],
			Mode:     strings.TrimSpace(m[2]),
			Status:   strings.TrimSpace(m[3]),
			Measured: strings.TrimSpace(m[4]),
			Detail:   strings.TrimSpace(m[5]),
		}
	}
	return rows, nil
}

// escapeCell makes a message safe for a single markdown table cell.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}
