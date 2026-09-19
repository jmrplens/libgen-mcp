package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// marshalReport is encoding/json behind a variable: a Report is strings and
// counts, which the encoder cannot be made to refuse, so the branch that
// answers for a failed marshal is reachable from a test and from nowhere else.
var marshalReport = json.MarshalIndent

// writeReport prints what the sweep found, in the order a person would work
// through it.
func writeReport(out io.Writer, report Report, verbose bool) {
	writeGroup(out, "Unescaped", report.Findings)
	if verbose {
		writeGroup(out, "Unresolved", report.Unresolved)
		writeGroup(out, "Excused", report.Excused)
	}
	writeStale(out, report.Stale)
	writeMissing(out, report.Missing)
	writeSummary(out, report.Summary, report.Contexts)
}

// writeGroup prints one list of findings under a heading.
func writeGroup(out io.Writer, heading string, findings []Finding) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s (%d):\n", heading, len(findings))
	for _, finding := range findings {
		fmt.Fprintf(out, "  %s:%d  %s  %s in %s\n", finding.File, finding.Line,
			finding.Context, finding.Expression, finding.Function)
		if finding.Reason != "" {
			fmt.Fprintf(out, "      %s\n", finding.Reason)
		}
		if finding.Wants != "" {
			fmt.Fprintf(out, "      wants %s\n", finding.Wants)
		}
	}
}

// writeStale prints the exemptions that excuse nothing, which are as much a
// finding as a raw value: one left behind by a later change would otherwise
// widen the gate in silence.
func writeStale(out io.Writer, stale []Directive) {
	if len(stale) == 0 {
		return
	}
	fmt.Fprintf(out, "\nExemptions that excuse nothing (%d):\n", len(stale))
	for _, directive := range stale {
		fmt.Fprintf(out, "  %s:%d  %s\n", directive.File, directive.Line, directive.Expression)
	}
}

// writeMissing prints the named renderers the sweep did not find.
func writeMissing(out io.Writer, missing []string) {
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(out, "\nRenderers named in the audit but not declared (%d):\n", len(missing))
	for _, name := range missing {
		fmt.Fprintf(out, "  %s\n", name)
	}
}

// writeSummary prints the counts the sweep tracks.
func writeSummary(out io.Writer, summary Summary, contexts string) {
	fmt.Fprintf(out, "\n%s: %d values in %d packages, %d judged in %s\n",
		toolName, summary.Holes, summary.Packages, summary.Judged, orDash(contexts))
	fmt.Fprintf(out, "  escaped %d, unescaped %d, unresolved %d, excused %d, stale %d, missing %d\n",
		summary.Safe, summary.Unescaped, summary.Unresolved, summary.Excused, summary.Stale, summary.Missing)
}

// orDash renders an empty value as a dash, so a column is never blank.
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// writeJSON writes the work list to a path.
func writeJSON(path string, report Report) error {
	data, err := marshalReport(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// relativePath renders a path the way a report names it: relative to the
// repository root, with forward slashes on every platform.
func relativePath(path, root string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

// summarize renders the one line a caller of execute logs when it needs the
// outcome in a sentence rather than in a report.
func summarize(report Report) string {
	parts := []string{
		fmt.Sprintf("%d unescaped", report.Summary.Unescaped),
		fmt.Sprintf("%d unresolved", report.Summary.Unresolved),
	}
	if report.Summary.Stale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale exemptions", report.Summary.Stale))
	}
	if report.Summary.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d renderers missing", report.Summary.Missing))
	}
	return strings.Join(parts, ", ")
}
