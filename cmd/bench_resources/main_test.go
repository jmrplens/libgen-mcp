package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain turns this binary into the server the target helpers start when the
// switch is set, and otherwise runs the tests.
//
// Everything in target.go and run.go exists to drive a real process, and a test
// that mocked the process would be testing its own reassembly of it. Re-execing
// the test binary is the standard way to get a real process without building
// one: it is the same exec, the same pipes and the same /proc reads as a run
// against ./cmd/server, and it costs no compilation.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeServerEnv); mode != "" {
		os.Exit(runFakeServer(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// TestParseFlags_DefaultsToTheCommittedRecord verifies the paths a bare run
// writes, and that every flag can be given.
func TestParseFlags_DefaultsToTheCommittedRecord(t *testing.T) {
	originalArgs, originalFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = originalArgs, originalFlags })

	t.Run("no flags", func(t *testing.T) {
		flag.CommandLine = flag.NewFlagSet("bench", flag.ContinueOnError)
		os.Args = []string{"bench"}
		opts := parseFlags()
		if opts.record != defaultRecord || opts.page != defaultPage {
			t.Errorf("parseFlags() = %+v, want the committed paths", opts)
		}
		if opts.rounds != 3 {
			t.Errorf("rounds = %d, want 3", opts.rounds)
		}
	})

	t.Run("every flag", func(t *testing.T) {
		flag.CommandLine = flag.NewFlagSet("bench", flag.ContinueOnError)
		os.Args = []string{
			"bench", "-binary", "/tmp/b", "-json", "/tmp/r.json", "-page", "/tmp/p.md",
			"-scenarios", "http-1", "-rounds", "5", "-sample-interval", "50ms",
			"-quick", "-v", "-no-write", "-render", "-check",
		}
		opts := parseFlags()
		if opts.binary != "/tmp/b" || opts.record != "/tmp/r.json" || opts.page != "/tmp/p.md" {
			t.Errorf("paths = %+v", opts)
		}
		if opts.scenarios != "http-1" || opts.rounds != 5 || opts.sampleInterval.Milliseconds() != 50 {
			t.Errorf("knobs = %+v", opts)
		}
		if !opts.quick || !opts.verbose || !opts.noWrite || !opts.render || !opts.check {
			t.Errorf("switches = %+v", opts)
		}
	})
}

// TestRedraw_RewritesThePageOrReportsThatItDiffers verifies the gate's whole
// arrangement.
//
// It redraws from the committed record rather than re-measuring, because a
// record measured on one machine is not reproducible on a CI runner: a check
// that re-measured would fail for being on different hardware, which is a gate
// nobody can keep green. The only question with an answer anywhere is whether
// the page still says what the numbers say.
func TestRedraw_RewritesThePageOrReportsThatItDiffers(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record.json")
	page := filepath.Join(dir, "page.md")
	if err := writeRecord(record, fixtureRun()); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	dest := benchDest(t, page)
	opts := options{record: record, page: page, dest: dest}

	t.Run("render writes the page", func(t *testing.T) {
		if err := redraw(options{record: record, page: page, render: true, dest: dest}); err != nil {
			t.Fatalf("redraw: %v", err)
		}
		body, err := os.ReadFile(page) // #nosec G304 -- a path this test just wrote
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !strings.Contains(string(body), "What this server costs to run") {
			t.Error("the redrawn page is not the benchmark page")
		}
	})

	t.Run("check passes on the page it just drew", func(t *testing.T) {
		opts.check = true
		if err := redraw(opts); err != nil {
			t.Errorf("redraw -check: %v", err)
		}
	})

	t.Run("check fails on a page somebody edited", func(t *testing.T) {
		if err := os.WriteFile(page, []byte("# Hand-written\n"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		opts.check = true
		if err := redraw(opts); err == nil {
			t.Error("expected a stale page to be reported")
		}
	})

	t.Run("a record that is not there", func(t *testing.T) {
		if err := redraw(options{record: filepath.Join(dir, "absent.json"), page: page, check: true, dest: dest}); err == nil {
			t.Error("expected an error with no record to draw from")
		}
	})
}

// TestExecute_TakesTheRenderPathWithoutMeasuring verifies -check never starts a
// process, which is what makes it runnable in CI at all.
func TestExecute_TakesTheRenderPathWithoutMeasuring(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record.json")
	page := filepath.Join(dir, "page.md")
	if err := writeRecord(record, fixtureRun()); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	// A binary path that does not exist: if execute measured anything it would
	// fail on it, so reaching the end proves it did not.
	opts := options{record: record, page: page, render: true, binary: filepath.Join(dir, "no-such-binary"), dest: benchDest(t, page)}
	if err := execute(t.Context(), opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

// TestExecute_RefusesAScenarioNameNothingMatches verifies a typo stops the run
// before anything is built, rather than writing an empty record.
func TestExecute_RefusesAScenarioNameNothingMatches(t *testing.T) {
	err := execute(t.Context(), options{
		scenarios: "nonsense", quick: true, rounds: 1, sampleInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("execute() error = %v, want it to name the scenario", err)
	}
}

// TestMatrixFor_PicksThePlanTheRunAsksFor verifies the two matrices are
// reachable from the flag.
func TestMatrixFor_PicksThePlanTheRunAsksFor(t *testing.T) {
	if len(matrixFor(true)) >= len(matrixFor(false)) {
		t.Error("the quick matrix is not shorter than the full one")
	}
}

// TestFillServerInfo_NamesTheBuildOnceAtTheTop verifies the measured build is
// lifted out of the scenario notes, so a reader does not have to find it inside
// one scenario.
func TestFillServerInfo_NamesTheBuildOnceAtTheTop(t *testing.T) {
	run := &Run{Scenarios: []Scenario{
		{ID: "a", Notes: []string{"3 requests reached the stand-in catalog"}},
		{ID: "b", Notes: []string{"measured build 1.7.3"}},
	}}
	fillServerInfo(run)
	if run.Server.Version != "1.7.3" {
		t.Errorf("Server.Version = %q, want the build a scenario recorded", run.Server.Version)
	}

	t.Run("a run where nothing said", func(t *testing.T) {
		quiet := &Run{Scenarios: []Scenario{{ID: "a"}}}
		fillServerInfo(quiet)
		if quiet.Server.Version != "" {
			t.Errorf("Server.Version = %q, want nothing rather than a guess", quiet.Server.Version)
		}
	})
}

// TestFillServerInfo_ReadsBothHalvesOfTheStamp verifies the version and the
// revision are both recovered, and that a note from an older run carrying only
// a version still reads.
func TestFillServerInfo_ReadsBothHalvesOfTheStamp(t *testing.T) {
	testCases := []struct {
		name, note, wantVersion, wantCommit string
	}{
		{
			name: "a version and a revision", note: "measured build 1.7.3 (abc1234)",
			wantVersion: "1.7.3", wantCommit: "abc1234",
		},
		{name: "a version alone", note: "measured build 1.7.3", wantVersion: "1.7.3"},
		{name: "a note about something else", note: "3 requests reached the stand-in catalog"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			run := &Run{Scenarios: []Scenario{{ID: "a", Notes: []string{tc.note}}}}
			fillServerInfo(run)
			if run.Server.Version != tc.wantVersion || run.Server.Commit != tc.wantCommit {
				t.Errorf("Server = %+v, want %q / %q", run.Server, tc.wantVersion, tc.wantCommit)
			}
		})
	}
}

// TestDescribeScenario_SummarizesTheRunInOneLine verifies the terminal line
// under -v carries the figures somebody watching a run wants.
func TestDescribeScenario_SummarizesTheRunInOneLine(t *testing.T) {
	got := describeScenario(fixtureRun().Scenarios[0])
	for _, want := range []string{"ready", "first list", "idle", "peak", "calls"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(got, want) {
				t.Errorf("describeScenario() = %q, want it to name %q", got, want)
			}
		})
	}
}

// TestMain_ExitsThroughTheSeamRatherThanEndingTheTestBinary verifies main's
// failure path, which is only reachable because the exit is a variable.
func TestMain_ExitsThroughTheSeamRatherThanEndingTheTestBinary(t *testing.T) {
	originalArgs, originalFlags, originalExit := os.Args, flag.CommandLine, exitProcess
	t.Cleanup(func() { os.Args, flag.CommandLine, exitProcess = originalArgs, originalFlags, originalExit })

	var code int
	exitProcess = func(c int) { code = c }
	flag.CommandLine = flag.NewFlagSet("bench", flag.ContinueOnError)
	os.Args = []string{"bench", "-scenarios", "nonsense", "-quick"}

	main()
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

// TestExecute_MeasuresAndWritesBothArtifacts verifies the whole measuring path:
// the matrix is narrowed, a process is started, the scenario is measured, and
// both the record and the page are written.
//
// It measures the stand-in through -target-env rather than the server, for the
// reason the runner's own seam gives: what is under test is this command, and a
// unit test of it should not depend on a twenty-second build.
func TestExecute_MeasuresAndWritesBothArtifacts(t *testing.T) {
	dir := t.TempDir()
	opts := options{
		binary:         os.Args[0],
		record:         filepath.Join(dir, "record.json"),
		page:           filepath.Join(dir, "page.md"),
		scenarios:      "http-1",
		rounds:         1,
		sampleInterval: 10 * time.Millisecond,
		quick:          true,
		verbose:        true,
		// The series has a test of its own; what this one is about is the
		// matrix and the two artifacts, and running a three-step series here
		// would add six seconds to the unit suite for nothing.
		noSeries:  true,
		targetEnv: map[string]string{fakeServerEnv: transportHTTP},
		dest:      benchDest(t, filepath.Join(dir, "page.md")),
	}
	if err := execute(t.Context(), opts); err != nil {
		t.Fatalf("execute: %v", err)
	}

	run, err := readRecord(opts.record)
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if len(run.Scenarios) != 1 || run.Scenarios[0].ID != "http-1" {
		t.Errorf("the record holds %+v, want the one scenario that was asked for", run.Scenarios)
	}
	if run.Server.Version != "0.0.0-fake" {
		t.Errorf("Server.Version = %q, want the build the measured process stated", run.Server.Version)
	}
	if run.Server.BytesOnDisk == 0 {
		t.Error("the record does not say what the measured binary weighs")
	}
	body, err := os.ReadFile(opts.page) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	if !strings.Contains(string(body), "http-1") {
		t.Error("the page does not carry the scenario that was measured")
	}
}

// TestExecute_NoWriteLeavesTheRecordAlone verifies the switch that lets a run be
// watched without touching what is committed.
func TestExecute_NoWriteLeavesTheRecordAlone(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record.json")
	opts := options{
		binary:         os.Args[0],
		record:         record,
		page:           filepath.Join(dir, "page.md"),
		scenarios:      "http-1",
		rounds:         1,
		sampleInterval: 10 * time.Millisecond,
		quick:          true,
		noWrite:        true,
		noSeries:       true,
		targetEnv:      map[string]string{fakeServerEnv: transportHTTP},
	}
	if err := execute(t.Context(), opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("-no-write wrote %s", record)
	}
}

// TestExecute_ReportsAScenarioThatCouldNotBeMeasured verifies a process that
// will not start stops the run with the scenario named, rather than writing a
// record with a hole in it.
func TestExecute_ReportsAScenarioThatCouldNotBeMeasured(t *testing.T) {
	dir := t.TempDir()
	opts := options{
		binary:         filepath.Join(dir, "no-such-binary"),
		record:         filepath.Join(dir, "record.json"),
		page:           filepath.Join(dir, "page.md"),
		scenarios:      "http-1",
		rounds:         1,
		sampleInterval: 10 * time.Millisecond,
		quick:          true,
	}
	err := execute(t.Context(), opts)
	if err == nil || !strings.Contains(err.Error(), "http-1") {
		t.Errorf("execute() error = %v, want it to name the scenario", err)
	}
}

// TestParseFlags_ReadsRepeatedTargetEnv verifies the pass-through knob accepts
// several entries and refuses one that is not a NAME=VALUE pair.
func TestParseFlags_ReadsRepeatedTargetEnv(t *testing.T) {
	originalArgs, originalFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = originalArgs, originalFlags })

	flag.CommandLine = flag.NewFlagSet("bench", flag.ContinueOnError)
	os.Args = []string{"bench", "-target-env", "A=1", "-target-env", "B=2"}
	opts := parseFlags()
	if opts.targetEnv["A"] != "1" || opts.targetEnv["B"] != "2" {
		t.Errorf("targetEnv = %v, want both entries", opts.targetEnv)
	}

	// A value that is not a pair has to be refused rather than ignored, and the
	// assertion says so: with ContinueOnError the parser returns and a test that
	// accepted either outcome would pass against a flag that silently dropped
	// what it could not read. PanicOnError makes the refusal observable.
	t.Run("a value that is not a pair", func(t *testing.T) {
		set := flag.NewFlagSet("bench", flag.PanicOnError)
		set.SetOutput(io.Discard)
		flag.CommandLine = set
		os.Args = []string{"bench", "-target-env", "nonsense"}
		defer func() {
			if recover() == nil {
				t.Error("parseFlags accepted a -target-env that is not NAME=VALUE")
			}
		}()
		parseFlags()
	})
}

// TestValidate_RefusesSettingsThatCannotMeasureAnything verifies the two knobs
// that fail late and badly: a sample interval under a millisecond becomes zero
// once the record rounds it and panics the ticker halfway through the first
// scenario, and a round count below one drives no calls at all, which would
// write a record whose empty timings read like a very fast server.
func TestValidate_RefusesSettingsThatCannotMeasureAnything(t *testing.T) {
	testCases := []struct {
		name    string
		opts    options
		wantErr string
	}{
		{name: "a sane run", opts: options{rounds: 1, sampleInterval: time.Millisecond}},
		{name: "no rounds", opts: options{rounds: 0, sampleInterval: time.Millisecond}, wantErr: "-rounds"},
		{name: "negative rounds", opts: options{rounds: -1, sampleInterval: time.Millisecond}, wantErr: "-rounds"},
		{
			name: "a sub-millisecond interval",
			opts: options{rounds: 1, sampleInterval: 100 * time.Microsecond}, wantErr: "-sample-interval",
		},
		{
			name: "a negative interval",
			opts: options{rounds: 1, sampleInterval: -time.Second}, wantErr: "-sample-interval",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("validate() error = %v, want it to name %s", err, tc.wantErr)
			}
		})
	}
}

// TestBuildServer_CompilesTheBinaryThatShips verifies the build a measuring run
// starts from: no cgo and no linker flags, which is what the release builds do.
// A benchmark of a differently linked binary would measure one nobody ships.
func TestBuildServer_CompilesTheBinaryThatShips(t *testing.T) {
	dir := t.TempDir()
	path, err := buildServer(t.Context(), dir)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if binarySize(path) == 0 {
		t.Errorf("buildServer() produced %s, which weighs nothing", path)
	}
}

// TestExecute_MeasuresTheSeriesToo verifies the concurrency series reaches the
// record through the ordinary path, and that -no-series takes it out.
func TestExecute_MeasuresTheSeriesToo(t *testing.T) {
	dir := t.TempDir()
	opts := options{
		binary:         os.Args[0],
		record:         filepath.Join(dir, "record.json"),
		page:           filepath.Join(dir, "page.md"),
		scenarios:      "http-1",
		rounds:         1,
		sampleInterval: 10 * time.Millisecond,
		quick:          true,
		seriesClients:  "1,2",
		stepDuration:   150 * time.Millisecond,
		targetEnv:      map[string]string{fakeServerEnv: transportHTTP},
		dest:           benchDest(t, filepath.Join(dir, "page.md")),
	}
	if err := execute(t.Context(), opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
	run, err := readRecord(opts.record)
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if len(run.Series) != 1 || len(run.Series[0].Steps) != 2 {
		t.Fatalf("the record holds %+v, want one series of two steps", run.Series)
	}
	body, err := os.ReadFile(opts.page) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	if !strings.Contains(string(body), "What each extra caller costs") {
		t.Error("the page does not carry the series section")
	}

	t.Run("a ladder that is refused stops the run", func(t *testing.T) {
		bad := opts
		bad.seriesClients = "4,2"
		if err = execute(t.Context(), bad); err == nil {
			t.Error("expected a descending ladder to stop the run")
		}
	})
}
