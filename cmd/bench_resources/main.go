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

	"github.com/jmrplens/libgen-mcp/cmd/internal/docgen"
)

// Default output locations, relative to the module root.
const (
	defaultRecord = "docs/benchmarks/resource-benchmark.json"
	defaultPage   = "docs/benchmarks/resource-benchmark.md"
)

// options are the command's flags.
type options struct {
	binary         string
	record         string
	page           string
	scenarios      string
	rounds         int
	sampleInterval time.Duration
	quick          bool
	verbose        bool
	noWrite        bool
	render         bool
	check          bool
	// targetEnv is added to every measured process, after the per-scenario
	// settings. It exists for measuring a deployment with a knob this command
	// does not expose, and it is the seam the tests drive a stand-in process
	// through.
	targetEnv map[string]string
	// The concurrency series: whether to run it at all, the ladder of client
	// counts, and how long each step's steady phase lasts.
	noSeries      bool
	seriesClients string
	stepDuration  time.Duration
}

// exitProcess is the exit main takes on failure, so a test can drive main
// through it and read the status back instead of ending the test binary. main
// returns after calling it for the same reason: os.Exit never returns, and a
// test's replacement does.
var exitProcess = os.Exit

func main() {
	opts := parseFlags()
	if err := execute(context.Background(), opts); err != nil {
		fmt.Fprintf(os.Stderr, "bench_resources: %v\n", err)
		exitProcess(1)
		return
	}
}

// parseFlags reads the command line.
func parseFlags() options {
	var opts options
	flag.StringVar(&opts.binary, "binary", "", "server binary to measure; empty builds one into a temporary directory")
	flag.StringVar(&opts.record, "json", defaultRecord, "measurement record to write")
	flag.StringVar(&opts.page, "page", defaultPage, "Markdown record to write beside it")
	flag.StringVar(&opts.scenarios, "scenarios", "", "comma-separated scenario ids to measure; empty runs the whole matrix")
	flag.IntVar(&opts.rounds, "rounds", 3, "measured rounds per method per client")
	flag.DurationVar(&opts.sampleInterval, "sample-interval", 100*time.Millisecond, "how often the resident set is sampled")
	flag.BoolVar(&opts.quick, "quick", false, "short smoke matrix, for verifying a change to this command")
	flag.BoolVar(&opts.verbose, "v", false, "print progress for every scenario")
	flag.BoolVar(&opts.noWrite, "no-write", false, "measure and print, then stop without touching the record")
	flag.BoolVar(&opts.render, "render", false, "skip measurement: redraw the page from the committed record")
	flag.BoolVar(&opts.check, "check", false, "verify the committed page matches the committed record; implies -render")
	flag.BoolVar(&opts.noSeries, "no-series", false, "measure the matrix and stop, without the concurrency series")
	flag.StringVar(&opts.seriesClients, "series-clients", "",
		"comma-separated client-address counts for the concurrency series, ascending; empty uses the default ladder")
	flag.DurationVar(&opts.stepDuration, "step-duration", 0,
		"steady phase per series step; empty uses 10s, or 2s with -quick")
	opts.targetEnv = map[string]string{}
	flag.Func("target-env", "NAME=VALUE added to every measured process; repeatable", func(entry string) error {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" {
			return fmt.Errorf("want NAME=VALUE, got %q", entry)
		}
		opts.targetEnv[name] = value
		return nil
	})
	flag.Parse()
	return opts
}

// execute runs the matrix and writes the record, or redraws the page from an
// existing one.
func execute(ctx context.Context, opts options) error {
	if opts.render || opts.check {
		return redraw(opts)
	}

	if err := validate(opts); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	plans, err := selectScenarios(matrixFor(opts.quick), opts.scenarios)
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "libgen-mcp-bench-")
	if err != nil {
		return fmt.Errorf("work directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	binary := opts.binary
	if binary == "" {
		fmt.Fprintln(os.Stderr, "bench_resources: building "+serverPackage)
		if binary, err = buildServer(ctx, workDir); err != nil {
			return err
		}
	}

	stub, stopCatalog := startCatalog()
	defer stopCatalog()
	otlp, stopCollector := startCollector()
	defer stopCollector()

	settings := Settings{
		Rounds:           opts.rounds,
		SampleIntervalMs: int(opts.sampleInterval / time.Millisecond),
		Quick:            opts.quick,
	}
	run := newRun(ctx, settings, ServerInfo{BytesOnDisk: binarySize(binary)})
	fmt.Fprintf(os.Stderr, "bench_resources: %s\n", run.Host.describe())

	r := &runner{
		binary:    binary,
		catalog:   stub,
		collector: otlp,
		settings:  settings,
		workDir:   workDir,
		verbose:   opts.verbose,
		env:       opts.targetEnv,
	}
	if mErr := r.measureMatrix(ctx, run, plans, opts.verbose); mErr != nil {
		return mErr
	}
	if sErr := r.measureSeries(ctx, run, opts); sErr != nil {
		return sErr
	}
	fillServerInfo(run)
	return writeArtifacts(opts, run)
}

// measureMatrix measures every point of the plan into the record.
func (r *runner) measureMatrix(ctx context.Context, run *Run, plans []scenarioPlan, verbose bool) error {
	for _, plan := range plans {
		fmt.Fprintf(os.Stderr, "bench_resources: %s — %s\n", plan.ID, plan.Why)
		scenario, err := r.measure(ctx, plan)
		if err != nil {
			return fmt.Errorf("scenario %s: %w", plan.ID, err)
		}
		run.Scenarios = append(run.Scenarios, scenario)
		if verbose {
			fmt.Fprintln(os.Stderr, describeScenario(scenario))
		}
	}
	return nil
}

// measureSeries adds the concurrency series to the record, unless the run asked
// for the matrix alone.
func (r *runner) measureSeries(ctx context.Context, run *Run, opts options) error {
	if opts.noSeries {
		return nil
	}
	plan, err := seriesPlanFor(opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr,
		"bench_resources: %s — what each extra caller costs, measured rather than extrapolated\n", plan.ID)
	series, err := r.runSeries(ctx, plan)
	if err != nil {
		return fmt.Errorf("series %s: %w", plan.ID, err)
	}
	run.Series = append(run.Series, series)
	if opts.verbose {
		fmt.Fprintln(os.Stderr, describeSeries(series))
	}
	return nil
}

// writeArtifacts writes the record and the page, or says why it wrote neither.
func writeArtifacts(opts options, run *Run) error {
	if opts.noWrite {
		fmt.Fprintln(os.Stderr, "bench_resources: -no-write, record not written")
		return nil
	}
	if err := writeRecord(opts.record, run); err != nil {
		return err
	}
	if err := writePage(opts.page, run); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "bench_resources: wrote %s and %s\n", opts.record, opts.page)
	return nil
}

// validate refuses a run whose settings cannot produce a measurement.
//
// Both of these fail late and badly otherwise. A sample interval under a
// millisecond becomes zero or negative once it is recorded in milliseconds, and
// time.NewTicker panics on either — halfway through the first scenario, after a
// build. A round count below one drives no calls at all, and a record written
// from that carries a scenario with empty timings, which reads like a
// measurement of a very fast server.
func validate(opts options) error {
	if opts.rounds < 1 {
		return fmt.Errorf("-rounds is %d; a run with no rounds measures nothing", opts.rounds)
	}
	if opts.sampleInterval < time.Millisecond {
		return fmt.Errorf("-sample-interval is %s; the record keeps it in milliseconds, so anything under 1ms is no interval at all",
			opts.sampleInterval)
	}
	return nil
}

// redraw rewrites the page from the committed record, or reports that it
// differs.
//
// The gate redraws rather than re-measuring on purpose. A record measured on one
// machine is not reproducible on a CI runner, so a check that re-measured would
// fail for being on different hardware — which is a gate that says nothing and
// has to be switched off. Redrawing asks the only question that has an answer
// anywhere: does the page still say what the numbers say?
func redraw(opts options) error {
	run, err := readRecord(opts.record)
	if err != nil {
		return err
	}
	if cErr := docgen.WriteOrCheck(opts.page, []byte(renderPage(run)), opts.check, "`make bench-resources-render`"); cErr != nil {
		return cErr
	}
	if opts.check {
		fmt.Fprintln(os.Stderr, "bench_resources: the benchmark page matches its record")
		return nil
	}
	fmt.Fprintf(os.Stderr, "bench_resources: redrew %s from %s\n", opts.page, opts.record)
	return nil
}

// seriesPlanFor builds the concurrency series a run measures, from the flags
// and the defaults the run's shape implies.
func seriesPlanFor(opts options) (seriesPlan, error) {
	clients := defaultSeriesClients()
	if opts.quick {
		clients = quickSeriesClients()
	}
	if strings.TrimSpace(opts.seriesClients) != "" {
		parsed, err := parseClientLadder(opts.seriesClients)
		if err != nil {
			return seriesPlan{}, err
		}
		clients = parsed
	}
	step := opts.stepDuration
	if step == 0 {
		step = 10 * time.Second
		if opts.quick {
			step = 2 * time.Second
		}
	}
	return seriesPlan{
		ID:            "series-http",
		Clients:       clients,
		Parallel:      2,
		PerClientRPS:  5,
		StepDuration:  step,
		Method:        methodToolsList,
		OutboundRPS:   benchOutboundRPS,
		OutboundBurst: benchOutboundBurst,
	}, nil
}

// parseClientLadder reads a comma-separated list of client counts, refusing one
// that is not ascending.
//
// Ascending is not a style rule: the budget guard fits a line through the steps
// measured so far to decide whether the next one is affordable, and a ladder
// that went down would have it extrapolating backwards into a step it had
// already run.
func parseClientLadder(list string) ([]int, error) {
	var clients []int
	for field := range strings.SplitSeq(list, ",") {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" {
			continue
		}
		count, err := strconv.Atoi(trimmed)
		if err != nil {
			return nil, fmt.Errorf("-series-clients %q: %w", trimmed, err)
		}
		if count < 1 {
			return nil, fmt.Errorf("-series-clients %q: a step needs at least one client", trimmed)
		}
		if len(clients) > 0 && count <= clients[len(clients)-1] {
			return nil, fmt.Errorf("-series-clients must ascend; %d follows %d", count, clients[len(clients)-1])
		}
		clients = append(clients, count)
	}
	if len(clients) == 0 {
		return nil, fmt.Errorf("-series-clients %q names no step", list)
	}
	return clients, nil
}

// describeSeries is the one-line summary the terminal prints under -v.
func describeSeries(s SeriesScenario) string {
	load, hasLoad := s.loadSlopeMiB()
	tenancy, hasTenancy := s.tenancySlopeKiB()
	reason := s.StopReason
	if s.Stop != nil && s.Stop.Error != "" {
		reason += ": " + s.Stop.Error
	}
	parts := []string{fmt.Sprintf("    %d steps, stopped at %d clients (%s)", len(s.Steps), s.StoppedAt, reason)}
	if hasLoad {
		parts = append(parts, fmt.Sprintf("%.2f MiB/client under load", load))
	}
	if hasTenancy {
		parts = append(parts, fmt.Sprintf("%.0f KiB/client held", tenancy))
	}
	return strings.Join(parts, ", ")
}

// matrixFor picks the plan a run measures.
func matrixFor(quick bool) []scenarioPlan {
	if quick {
		return quickMatrix()
	}
	return fullMatrix()
}

// fillServerInfo lifts the measured build out of the scenario notes, so the
// record names it once at the top rather than only inside a scenario.
//
// Both halves are carried. Two builds of one tag are different bytes, and a
// record meant to be compared with a later one has to be able to say which of
// them it measured.
func fillServerInfo(run *Run) {
	for _, scenario := range run.Scenarios {
		for _, note := range scenario.Notes {
			stamped, found := strings.CutPrefix(note, "measured build ")
			if !found {
				continue
			}
			version, commit, hasCommit := strings.Cut(stamped, " (")
			run.Server.Version = version
			if hasCommit {
				run.Server.Commit = strings.TrimSuffix(commit, ")")
			}
			return
		}
	}
}

// describeScenario is the one-line summary the terminal prints under -v.
func describeScenario(s Scenario) string {
	return fmt.Sprintf("    ready %.1fms, first list %.1fms, idle %.1f MiB, peak %.1f MiB, %d calls",
		s.Startup.ProcessReadyMs, s.Startup.FirstListMs, s.Memory.IdleMiB, s.Memory.PeakMiB, s.CPU.Calls)
}
