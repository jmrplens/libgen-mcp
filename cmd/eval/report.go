//go:build eval

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
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
	var b strings.Builder
	b.WriteString("# libgen-mcp live LLM eval results\n\n")
	fmt.Fprintf(&b, "Model: `%s`\n\n", model)
	b.WriteString("| Scenario | Mode | Status | Detail |\n| --- | --- | --- | --- |\n")
	for _, o := range outcomes {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", o.ID, o.blockLabel(), strings.ToUpper(o.Status), escapeCell(o.Message))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write results doc: %w", err)
	}
	return nil
}

// escapeCell makes a message safe for a single markdown table cell.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}
