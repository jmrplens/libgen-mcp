//go:build eval

// Command eval is a LIVE, LLM-driven eval harness for libgen-mcp. It drives a
// small Anthropic model (claude-haiku-4-5) over the real libgen-mcp tools,
// registered on an in-process MCP server, and grades whether the model selects
// the right tool with well-formed arguments and gets a usable real response.
//
// It is LIVE: it spends Anthropic API tokens, hits real Library Genesis mirrors
// and article sources, and downloads real files into a temporary directory. It
// is compiled only under the "eval" build tag and, even then, refuses to run
// unless LIBGEN_EVAL=1 and ANTHROPIC_API_KEY are both set.
//
// Usage:
//
//	LIBGEN_EVAL=1 ANTHROPIC_API_KEY=sk-... go run -tags eval ./cmd/eval
//	go run -tags eval ./cmd/eval --only S1,S6 --results-doc out.md --keep-downloads
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxDownloadBytes caps every download the harness makes (25 MiB) so a runaway
// file never fills the disk.
const maxDownloadBytes = 25 * 1024 * 1024

func main() {
	only := flag.String("only", "", "comma-separated scenario IDs to run (e.g. S1,S6); empty runs all")
	keep := flag.Bool("keep-downloads", false, "keep the temporary download directory instead of removing it")
	resultsDoc := flag.String("results-doc", "", "write a markdown results table to this path")
	record := flag.String("record", "", "write a full JSONL record of every scenario to this path (prompt, turns, tool calls, server logs, responses, final answer)")
	regradeFrom := flag.String("regrade", "", "re-run the assertions against a previously recorded run instead of calling anything live; valid for assertion changes only")
	flag.Parse()

	// Re-grading makes no network calls and spends nothing, so it is not gated.
	if *regradeFrom != "" {
		os.Exit(regrade(*regradeFrom, *resultsDoc))
	}

	if os.Getenv("LIBGEN_EVAL") != "1" || strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		fmt.Println("libgen-mcp eval is gated. This is a LIVE harness (real Anthropic API + real libgen mirrors + real downloads).")
		fmt.Println("Set LIBGEN_EVAL=1 and ANTHROPIC_API_KEY to run it. Skipping.")
		return
	}
	os.Exit(runEval(*only, *keep, *resultsDoc, *record))
}

// runEval sets up the temporary download sandbox, runs the selected scenarios,
// prints the report, and returns the process exit code (non-zero when any
// scenario failed or errored).
func runEval(only string, keep bool, resultsDoc, recordPath string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir, err := os.MkdirTemp("", "libgen-eval-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create temp download dir:", err)
		return 1
	}
	defer func() {
		if keep {
			fmt.Println("kept downloads in", dir)
			return
		}
		_ = os.RemoveAll(dir)
	}()

	// Point the server at the sandbox and cap download size BEFORE any
	// config.Load (newHostSession loads config per scenario).
	_ = os.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", dir)
	_ = os.Setenv("LIBGEN_MCP_MAX_DOWNLOAD_BYTES", strconv.Itoa(maxDownloadBytes))

	scs := selectScenarios(scenarios(), only)
	if len(scs) == 0 {
		fmt.Fprintf(os.Stderr, "no scenarios matched --only=%q\n", only)
		return 1
	}

	rec, err := newRecorder(recordPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer rec.close()

	ac := newAnthropicClient(os.Getenv("ANTHROPIC_API_KEY"))
	outcomes := make([]outcome, 0, len(scs))
	failures := 0
	for _, sc := range scs {
		oc := runOne(ctx, ac, sc, rec)
		outcomes = append(outcomes, oc)
		if oc.Status == statusFail || oc.Status == statusError {
			failures++
		}
		fmt.Printf("[%-5s] %s: %s\n", strings.ToUpper(oc.Status), oc.ID, oc.Message)
	}
	if recordPath != "" {
		fmt.Println("full run record written to", recordPath)
	}

	printReport(os.Stdout, outcomes)
	if resultsDoc != "" {
		if writeErr := writeResultsDoc(resultsDoc, outcomes, evalModel); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
			return 1
		}
	}
	if failures > 0 {
		return 1
	}
	return 0
}

// runOne runs a single scenario and grades its transcript into an outcome. A
// harness/API error is an "error"; an assertion SKIP message is a "skip".
func runOne(ctx context.Context, ac *anthropicClient, sc scenario, rec *recorder) outcome {
	started := time.Now()
	tr, err := runScenario(ctx, ac, sc)
	oc := outcome{ID: sc.ID, Calls: len(tr.Calls), Remote: sc.Remote}
	if err != nil {
		oc.Status = statusError
		oc.Message = err.Error()
		rec.write(buildRecord(sc, tr, oc, started, err))
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
	rec.write(buildRecord(sc, tr, oc, started, nil))
	return oc
}

// buildRecord assembles one scenario's complete account for the run record. It
// records everything the run produced, not only what the assertion looked at: an
// assertion can check only what someone thought to check, and the record is what
// makes the unthought-of visible later.
func buildRecord(sc scenario, tr transcript, oc outcome, started time.Time, runErr error) scenarioRecord {
	rec := scenarioRecord{
		ID:           sc.ID,
		Mode:         oc.blockLabel(),
		Model:        evalModel,
		Prompt:       sc.Prompt,
		SetupEnv:     sc.SetupEnv,
		StartedAt:    started,
		DurationMS:   time.Since(started).Milliseconds(),
		ToolsOffered: tr.Tools,
		Turns:        tr.Turns,
		Elicitations: tr.Elicitations,
		Fetched:      tr.Fetched,
		FinalAnswer:  tr.FinalText,
		Status:       strings.ToUpper(oc.Status),
		Detail:       oc.Message,
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}
	for i := range tr.Calls {
		rec.Calls = append(rec.Calls, newCallRecord(tr.Calls[i]))
	}
	for i := range tr.Progress {
		p := tr.Progress[i]
		rec.Progress = append(rec.Progress, progressRecord{
			Token: fmt.Sprint(p.ProgressToken), Progress: p.Progress, Total: p.Total, Message: p.Message,
		})
	}
	return rec
}

// newCallRecord flattens one executed tool call, keeping both channels the model
// sees — the Markdown it reads and the structured output beside it — plus what the
// server logged while serving it.
func newCallRecord(c toolCall) callRecord {
	rec := callRecord{
		Name:       c.Name,
		Input:      c.Input,
		DurationMS: c.Duration.Milliseconds(),
		Structured: c.Structured,
		ServerLogs: c.ServerLogs,
	}
	if c.Result != nil {
		rec.IsError = c.Result.IsError
		rec.Text = textOfResult(c.Result)
	}
	return rec
}

// textOfResult joins a tool result's text content blocks.
func textOfResult(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
