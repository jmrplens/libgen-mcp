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
)

// maxDownloadBytes caps every download the harness makes (25 MiB) so a runaway
// file never fills the disk.
const maxDownloadBytes = 25 * 1024 * 1024

func main() {
	only := flag.String("only", "", "comma-separated scenario IDs to run (e.g. S1,S6); empty runs all")
	keep := flag.Bool("keep-downloads", false, "keep the temporary download directory instead of removing it")
	resultsDoc := flag.String("results-doc", "", "write a markdown results table to this path")
	flag.Parse()

	if os.Getenv("LIBGEN_EVAL") != "1" || strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		fmt.Println("libgen-mcp eval is gated. This is a LIVE harness (real Anthropic API + real libgen mirrors + real downloads).")
		fmt.Println("Set LIBGEN_EVAL=1 and ANTHROPIC_API_KEY to run it. Skipping.")
		return
	}
	os.Exit(runEval(*only, *keep, *resultsDoc))
}

// runEval sets up the temporary download sandbox, runs the selected scenarios,
// prints the report, and returns the process exit code (non-zero when any
// scenario failed or errored).
func runEval(only string, keep bool, resultsDoc string) int {
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

	ac := newAnthropicClient(os.Getenv("ANTHROPIC_API_KEY"))
	outcomes := make([]outcome, 0, len(scs))
	failures := 0
	for _, sc := range scs {
		oc := runOne(ctx, ac, sc)
		outcomes = append(outcomes, oc)
		if oc.Status == statusFail || oc.Status == statusError {
			failures++
		}
		fmt.Printf("[%-5s] %s: %s\n", strings.ToUpper(oc.Status), oc.ID, oc.Message)
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
func runOne(ctx context.Context, ac *anthropicClient, sc scenario) outcome {
	tr, err := runScenario(ctx, ac, sc)
	oc := outcome{ID: sc.ID, Calls: len(tr.Calls), Remote: sc.Remote}
	if err != nil {
		oc.Status = statusError
		oc.Message = err.Error()
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
