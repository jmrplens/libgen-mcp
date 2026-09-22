// render.go writes the record as a page a person can read.
//
// The JSON beside it is the artifact; this is the artifact's translation. It is
// generated rather than written by hand for the reason every generated document
// in this repository is: a number typed into prose is a number that stops being
// true on the next run and says nothing about it.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jmrplens/libgen-mcp/cmd/internal/docgen"
)

// writePage renders the record as Markdown and writes it.
func writePage(path string, run *Run) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return docgen.WriteOrCheck(path, []byte(renderPage(run)), false, "`make bench-resources`")
}

// renderPage is the whole document.
func renderPage(run *Run) string {
	var b strings.Builder
	b.WriteString("# What this server costs to run\n\n")
	b.WriteString("**Reference** - for an operator sizing a deployment.\n\n")
	b.WriteString(renderPreamble(run))
	b.WriteString("\n## Startup and surface\n\n")
	b.WriteString(renderStartupTable(run))
	b.WriteString("\n## Memory\n\n")
	b.WriteString(renderMemoryTable(run))
	b.WriteString("\n## Latency per method\n\n")
	b.WriteString(renderLatencyTable(run))
	b.WriteString(renderSeries(run))
	b.WriteString(renderNotes(run))
	return b.String()
}

// renderSeries writes what each extra caller costs, and says where the series
// stopped. It writes nothing at all for a run that measured none.
func renderSeries(run *Run) string {
	if len(run.Series) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## What each extra caller costs\n\n")
	b.WriteString("Measured rather than extrapolated: one process, more client addresses at each\n")
	b.WriteString("step, and a reading taken twice. **Under load** is what N callers cost while all\n")
	b.WriteString("of them are calling, which is what a host has to survive. **Held** is what they\n")
	b.WriteString("cost with the load stopped and a collection forced, which is what a reader means\n")
	b.WriteString("by the cost of another caller.\n\n")
	b.WriteString("The load is `tools/list` rather than a tool call, and that is the honest choice\n")
	b.WriteString("rather than a convenient one: a tool call reaches the catalog, every catalog\n")
	b.WriteString("request takes a token from the outbound bucket, and a series driving flat out\n")
	b.WriteString("exhausts it in the first step — measured, 2,758 of 2,817 calls came back refused\n")
	b.WriteString("by the limiter. What that measures is the limiter. `tools/list` is served inside\n")
	b.WriteString("the process, so it measures what this section is about, and it is also the call\n")
	b.WriteString("every client really does make on every reconnect.\n")
	for i := range run.Series {
		b.WriteString(renderOneSeries(&run.Series[i]))
	}
	return b.String()
}

// renderOneSeries is one series: its table, its slopes, and why it ended.
func renderOneSeries(s *SeriesScenario) string {
	headers := []string{"Clients", "Mean (MiB)", "Peak (MiB)", "Calls", "p50 (ms)", "p99 (ms)", "CPU ms/call"}
	align := []docgen.Alignment{
		docgen.AlignRight, docgen.AlignRight, docgen.AlignRight, docgen.AlignRight,
		docgen.AlignRight, docgen.AlignRight, docgen.AlignRight,
	}
	if s.hasSettled() {
		headers = append(headers, "Held heap (MiB)", "Settled RSS (MiB)")
		align = append(align, docgen.AlignRight, docgen.AlignRight)
	}

	var rows [][]string
	for _, step := range s.Steps {
		row := []string{
			itoa(step.Clients), decimal(step.RSSMeanMiB), decimal(step.RSSPeakMiB), itoa(step.Calls),
			decimal(step.P50Ms), decimal(step.P99Ms), decimal(step.CPUMsPerCall),
		}
		if s.hasSettled() {
			row = append(row, decimal(step.SettledHeapMiB), decimal(step.SettledRSSMiB))
		}
		rows = append(rows, row)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n**%s** — every client driving `%s`, %d in flight each, %.0f s per step.\n\n",
		s.ID, s.Method, s.Parallel, s.StepSeconds)
	b.WriteString(tableWithNote(docgen.RenderMarkdownTable(headers, align, rows),
		strings.TrimPrefix(renderSlopes(s), "\n")+renderStop(s)))
	return b.String()
}

// renderSlopes is the sentence under a series table.
//
// Both figures are named for what they answer, because one of them read as the
// other is the mistake this series exists to stop: the load slope is what a
// deployment needs while N callers are all working, and the tenancy slope is
// what one more caller costs to hold.
func renderSlopes(s *SeriesScenario) string {
	var said []string
	if load, ok := s.loadSlopeMiB(); ok {
		said = append(said, fmt.Sprintf("**%.2f MiB per caller under load**", load))
	}
	if tenancy, ok := s.tenancySlopeKiB(); ok {
		said = append(said, fmt.Sprintf("**%.0f KiB per caller held**", tenancy))
	}
	if len(said) == 0 {
		return "\nNo per-caller figure is published from this series: the fit came out at or below\n" +
			"zero, which means the noise between steps was larger than what a caller adds. A\n" +
			"longer ladder is what separates them; a negative cost is not a measurement.\n"
	}
	return "\nFitted across the steps: " + strings.Join(said, " and ") +
		". The first is what a host needs while that many callers are all working at once;\n" +
		"the second is what holding one more costs with nothing in flight. Reading the first\n" +
		"as the second overstates a shared deployment by whatever its requests are carrying.\n"
}

// renderStop says where a series ended and why, so a shorter series is never
// presented as the whole one.
func renderStop(s *SeriesScenario) string {
	stop := s.Stop
	if stop == nil {
		return fmt.Sprintf("\nEvery planned step ran, up to %d clients.\n", s.StoppedAt)
	}
	switch stop.Kind {
	case stopBudget:
		return fmt.Sprintf("\n**The series stopped at %d clients.** The next step, %d, was estimated at "+
			"%.0f MiB against a budget of %.0f, so it was not started: this measures a server, "+
			"and taking the host down is not a measurement.\n",
			s.StoppedAt, stop.NextClients, stop.EstimateMiB, s.BudgetMiB)
	case stopLatency:
		return fmt.Sprintf("\n**The series stopped at %d clients**, where the `%s` tail reached "+
			"%.0f ms. Past that a client has given up, so the next step would measure timeouts "+
			"rather than the server.\n", s.StoppedAt, s.Method, stop.P99Ms)
	default:
		return fmt.Sprintf("\n**The series stopped at %d clients**, because the step at %d failed: %s\n",
			s.StoppedAt, stop.NextClients, stop.Error)
	}
}

// renderPreamble says what was measured, on what, and how to read it.
func renderPreamble(run *Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Measured on %s, from the real binary on both transports, against an\n", run.GeneratedAt)
	b.WriteString("in-process stand-in for the catalog on loopback. The run is offline, so a\n")
	b.WriteString("second machine measures this server rather than its own network.\n\n")
	fmt.Fprintf(&b, "- **Host**: %s\n", run.Host.describe())
	if run.Server.Version != "" {
		fmt.Fprintf(&b, "- **Build**: %s\n", buildLabel(run.Server))
	}
	if run.Server.BytesOnDisk > 0 {
		fmt.Fprintf(&b, "- **Binary**: %.1f MiB on disk\n", float64(run.Server.BytesOnDisk)/(1024*1024))
	}
	fmt.Fprintf(&b, "- **Rounds**: %d per method per client, sampled every %d ms\n",
		run.Settings.Rounds, run.Settings.SampleIntervalMs)
	if run.Settings.Quick {
		b.WriteString("- **Quick run**: a smoke matrix, not a measurement to publish\n")
	}
	b.WriteString("\nA client is what a client is on each transport: on HTTP it is a distinct\n")
	b.WriteString("address, which is what this server charges a caller by, and on stdio it is a\n")
	b.WriteString("process, because a client that wants a stdio server starts one.\n")
	return b.String()
}

// renderStartupTable is the two waits a client experiences, separated.
func renderStartupTable(run *Run) string {
	headers := []string{"Scenario", "Transport", "Clients", "Ready (ms)", "First list (ms)", "Warm list (ms)", "List bytes"}
	align := []docgen.Alignment{
		docgen.AlignLeft, docgen.AlignLeft, docgen.AlignRight,
		docgen.AlignRight, docgen.AlignRight, docgen.AlignRight, docgen.AlignRight,
	}
	var rows [][]string
	for _, s := range run.Scenarios {
		rows = append(rows, []string{
			s.ID, s.Transport, itoa(s.Clients),
			decimal(s.Startup.ProcessReadyMs), decimal(s.Startup.FirstListMs), decimal(s.Startup.WarmListMs),
			itoa(s.ListBytes),
		})
	}
	return tableWithNote(docgen.RenderMarkdownTable(headers, align, rows),
		"**Ready** is spawn to a process that answers; **first list** is what the first\n"+
			"client waits for, which is what pays for registration. They are reported apart\n"+
			"because they moved apart: one number would hide the one that hurts.\n")
}

// renderMemoryTable is what an operator sizes a container against.
func renderMemoryTable(run *Run) string {
	headers := []string{"Scenario", "Idle (MiB)", "Mean (MiB)", "Peak (MiB)", "CPU ms/call"}
	align := []docgen.Alignment{
		docgen.AlignLeft, docgen.AlignRight, docgen.AlignRight, docgen.AlignRight, docgen.AlignRight,
	}
	var rows [][]string
	for _, s := range run.Scenarios {
		cpu := decimal(s.CPU.MsPerCall)
		if s.CPU.Unreadable {
			cpu = "n/a"
		}
		rows = append(rows, []string{
			s.ID, decimal(s.Memory.IdleMiB), decimal(s.Memory.MeanMiB), decimal(s.Memory.PeakMiB), cpu,
		})
	}
	return tableWithNote(docgen.RenderMarkdownTable(headers, align, rows),
		"The resident set, read from the kernel rather than from inside the process: a\n"+
			"container limit is set against the resident set, and Go's own heap figure is a\n"+
			"different and smaller number. **Peak** is what a limit has to survive.\n")
}

// renderLatencyTable is the per-method timing.
func renderLatencyTable(run *Run) string {
	headers := []string{"Scenario", "Method", "Calls", "Outbound rps", "Burst", "p50 (ms)", "p99 (ms)", "max (ms)"}
	align := []docgen.Alignment{
		docgen.AlignLeft, docgen.AlignLeft, docgen.AlignRight, docgen.AlignRight,
		docgen.AlignRight, docgen.AlignRight, docgen.AlignRight, docgen.AlignRight,
	}
	var rows [][]string
	for _, s := range run.Scenarios {
		for _, m := range s.Latency {
			rows = append(rows, []string{
				s.ID, "`" + m.Method + "`", itoa(m.Calls), trimNumber(s.OutboundRPS), itoa(s.OutboundBurst),
				decimal(m.P50Ms), decimal(m.P99Ms), decimal(m.MaxMs),
			})
		}
	}
	return tableWithNote(docgen.RenderMarkdownTable(headers, align, rows),
		"Percentiles are nearest-rank, so every number published is one the run actually\n"+
			"observed rather than a point interpolated between two calls nobody made. No row\n"+
			"carries an error column because a scenario whose calls failed does not finish:\n"+
			"a search that cannot reach its catalog answers in under a millisecond, and\n"+
			"averaging that with a real one publishes an error path as a cost.\n"+
			"\n**The outbound budget is the number to read these against**, both halves of it.\n"+
			"`tools/call` reaches the catalog, and every catalog request waits for a token\n"+
			"from `LIBGEN_MCP_RATE_RPS` out of a bucket `LIBGEN_MCP_RATE_BURST` deep. It\n"+
			"ships at one per second with a burst of one. That\n"+
			"is why the shipped-rate scenario's tool calls take seconds while the same work\n"+
			"with the valve open takes milliseconds: what is being measured there is the\n"+
			"queue, not the server. An inbound rate limit set above the outbound bucket only\n"+
			"moves the queue; it does not shorten it.\n")
}

// renderNotes reproduces whatever a scenario had to say about itself, and
// writes nothing at all when none of them did.
func renderNotes(run *Run) string {
	var lines []string
	for _, s := range run.Scenarios {
		for _, note := range s.Notes {
			lines = append(lines, fmt.Sprintf("- **%s**: %s", s.ID, note))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n## Notes from the run\n\n" + strings.Join(lines, "\n") + "\n"
}

// buildLabel names the build that was measured, with its revision when the
// binary stated one: a version alone cannot tell two builds of the same tag
// apart, which is exactly what a second run of this is often comparing.
func buildLabel(server ServerInfo) string {
	if server.Commit == "" || server.Commit == "none" {
		return server.Version
	}
	return server.Version + " (" + server.Commit + ")"
}

// tableWithNote puts a paragraph under a table with exactly one blank line
// between them.
//
// The renderer owns the spacing because markdownlint refuses two consecutive
// blank lines (MD012) and the table renderer's own trailing newline is easy to
// double by hand — which it was, on the first page this command wrote.
func tableWithNote(table, note string) string {
	return strings.TrimRight(table, "\n") + "\n\n" + note
}

// decimal writes a measurement the way the record stores it.
func decimal(value float64) string {
	return fmt.Sprintf("%.2f", value)
}

// itoa writes a count.
func itoa(value int) string {
	return strconv.Itoa(value)
}

// trimNumber writes a rate without trailing zeros, so a budget of one reads as
// "1" rather than as a measurement to two decimals.
func trimNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
