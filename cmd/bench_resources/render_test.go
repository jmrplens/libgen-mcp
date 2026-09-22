package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRun is a record with one scenario of each shape, for the renderers.
func fixtureRun() *Run {
	return &Run{
		Schema:      recordSchema,
		GeneratedAt: "2026-09-22T00:00:00Z",
		Server:      ServerInfo{Version: "1.7.3", Commit: "abc1234", BytesOnDisk: 37 * 1024 * 1024},
		Host: HostInfo{
			OS: "linux", Arch: "amd64", CPUModel: "Test CPU", CPUs: 8,
			MemTotalGiB: 61, Kernel: "6.1.0", GoVersion: "go1.27.0",
		},
		Settings: Settings{Rounds: 3, SampleIntervalMs: 100},
		Scenarios: []Scenario{
			{
				ID: "stdio-1", Transport: transportStdio, Clients: 1, Parallel: 1, Rounds: 3,
				OutboundRPS: 20, OutboundBurst: 100, ListBytes: 26897,
				Startup: Startup{ProcessReadyMs: 31.1, FirstListMs: 8.8, WarmListMs: 5.2},
				Memory:  Memory{IdleMiB: 22.2, MeanMiB: 24.5, PeakMiB: 26.7},
				CPU:     CPU{Seconds: 0.4, MsPerCall: 4.4, Calls: 9},
				Latency: []MethodLatency{{Method: methodToolsList, Calls: 3, P50Ms: 5.2, P99Ms: 5.8, MaxMs: 5.8}},
				Notes:   []string{"3 requests reached the stand-in catalog"},
			},
			{
				ID: "http-8", Transport: transportHTTP, Clients: 8, Parallel: 2, Rounds: 3,
				OutboundRPS: 1, OutboundBurst: 1,
				CPU:     CPU{Unreadable: true, Calls: 144},
				Latency: []MethodLatency{{Method: methodToolsCall, Calls: 48, P50Ms: 14984.5, P99Ms: 16000.6, MaxMs: 16000.6}},
			},
		},
	}
}

// TestRenderPage_SaysWhatWasMeasuredAndAgainstWhat verifies the document
// carries the machine, the build and the settings, which is what makes a number
// on it comparable with a later one rather than just a number.
func TestRenderPage_SaysWhatWasMeasuredAndAgainstWhat(t *testing.T) {
	page := renderPage(fixtureRun())

	for _, want := range []string{
		"# What this server costs to run",
		"Test CPU",
		"1.7.3",
		"37.0 MiB on disk",
		"3 per method per client",
		"## Startup and surface",
		"## Memory",
		"## Latency per method",
		"stdio-1",
		"http-8",
		"`tools/call`",
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(page, want) {
				t.Errorf("the page does not carry %q", want)
			}
		})
	}
}

// TestRenderPage_ReadsAnUnreadableCPUAsSuch verifies a platform that would not
// report processor time prints as much, rather than as zero milliseconds per
// call — which is a claim nobody measured.
func TestRenderPage_ReadsAnUnreadableCPUAsSuch(t *testing.T) {
	page := renderPage(fixtureRun())
	if !strings.Contains(page, "n/a") {
		t.Errorf("the memory table does not mark the unreadable CPU reading:\n%s", page)
	}
}

// TestRenderPage_PutsTheOutboundBudgetBesideTheLatency verifies the number
// without which the latency table misleads.
//
// tools/call reaches the catalog, and every catalog request waits for a token
// from the outbound bucket. A p50 of fifteen seconds beside a budget of one
// request per second is a queue; the same figure with no budget printed is a
// slow server.
func TestRenderPage_PutsTheOutboundBudgetBesideTheLatency(t *testing.T) {
	page := renderPage(fixtureRun())
	if !strings.Contains(page, "Outbound rps") {
		t.Error("the latency table has no outbound budget column")
	}
	if !strings.Contains(page, "LIBGEN_MCP_RATE_RPS") {
		t.Error("the page does not name the variable the budget comes from")
	}
	if !strings.Contains(page, "only\nmoves the queue") {
		t.Errorf("the page does not say what an inbound limit above the outbound bucket does:\n%s", page)
	}
}

// TestRenderNotes_WritesNothingWhenThereIsNothingToSay verifies an empty run
// does not get a heading with no list under it.
func TestRenderNotes_WritesNothingWhenThereIsNothingToSay(t *testing.T) {
	quiet := fixtureRun()
	for i := range quiet.Scenarios {
		quiet.Scenarios[i].Notes = nil
	}
	if got := renderNotes(quiet); got != "" {
		t.Errorf("renderNotes() = %q, want nothing", got)
	}
	if strings.Contains(renderPage(quiet), "## Notes from the run") {
		t.Error("the page carries an empty notes section")
	}

	loud := fixtureRun()
	if !strings.Contains(renderNotes(loud), "stdio-1") {
		t.Error("renderNotes() does not name the scenario a note came from")
	}
}

// TestRenderPreamble_MarksAQuickRunAsNotAMeasurement verifies a smoke run says
// so on its own page, so a number from one is never quoted as a result.
func TestRenderPreamble_MarksAQuickRunAsNotAMeasurement(t *testing.T) {
	quick := fixtureRun()
	quick.Settings.Quick = true
	if !strings.Contains(renderPreamble(quick), "not a measurement to publish") {
		t.Error("a quick run's page does not say it is a smoke run")
	}

	full := fixtureRun()
	if strings.Contains(renderPreamble(full), "Quick run") {
		t.Error("a full run's page claims to be a smoke run")
	}
}

// TestRenderPreamble_LeavesOutWhatItCouldNotLearn verifies a record with no
// build or no binary size prints neither line, rather than an empty label.
func TestRenderPreamble_LeavesOutWhatItCouldNotLearn(t *testing.T) {
	bare := fixtureRun()
	bare.Server = ServerInfo{}
	got := renderPreamble(bare)
	if strings.Contains(got, "**Build**") || strings.Contains(got, "**Binary**") {
		t.Errorf("the preamble carries an empty label:\n%s", got)
	}
}

// TestWritePage_CreatesTheDirectoryItGoesIn verifies the page can be written
// somewhere that does not exist yet, which is what the first run on a clean
// checkout does.
func TestWritePage_CreatesTheDirectoryItGoesIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "benchmarks", "page.md")
	if err := writePage(path, fixtureRun()); err != nil {
		t.Fatalf("writePage: %v", err)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.HasPrefix(string(body), "# What this server costs to run") {
		t.Errorf("the written page starts %q", truncate(string(body), 60))
	}
}

// TestNumberFormatting_WritesEachKindAsItself verifies a measurement, a count
// and a rate each print in their own shape.
func TestNumberFormatting_WritesEachKindAsItself(t *testing.T) {
	testCases := []struct {
		name, got, want string
	}{
		{name: "a measurement keeps two decimals", got: decimal(1.5), want: "1.50"},
		{name: "a count has none", got: itoa(42), want: "42"},
		{name: "a whole rate has no trailing zeros", got: trimNumber(20), want: "20"},
		{name: "a fractional rate keeps what it needs", got: trimNumber(0.5), want: "0.5"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// fixtureSeries is a measured series, for the renderers.
func fixtureSeries() SeriesScenario {
	return SeriesScenario{
		ID: "series-http", Transport: transportHTTP, Method: methodToolsList,
		Parallel: 2, PerClientRPS: 5, StepSeconds: 10, BudgetMiB: 30000,
		Clients: []int{1, 10, 100}, StoppedAt: 100, StopReason: stopComplete,
		OutboundRPS: 20, OutboundBurst: 100,
		Steps: []SeriesStep{
			{Clients: 1, RSSMeanMiB: 25, RSSPeakMiB: 26, Calls: 50, P50Ms: 1, P99Ms: 3, SettledHeapMiB: 3, SettledRSSMiB: 26},
			{Clients: 10, RSSMeanMiB: 28, RSSPeakMiB: 31, Calls: 500, P50Ms: 2, P99Ms: 9, SettledHeapMiB: 4, SettledRSSMiB: 30},
			{Clients: 100, RSSMeanMiB: 45, RSSPeakMiB: 55, Calls: 5000, P50Ms: 4, P99Ms: 20, SettledHeapMiB: 16, SettledRSSMiB: 50},
		},
	}
}

// TestRenderSeries_PublishesBothCostsAndTellsThemApart verifies the section
// that exists to stop one figure being read as the other: what N callers cost
// while they are all working, and what one more costs to hold.
func TestRenderSeries_PublishesBothCostsAndTellsThemApart(t *testing.T) {
	run := fixtureRun()
	run.Series = []SeriesScenario{fixtureSeries()}
	page := renderPage(run)

	for _, want := range []string{
		"## What each extra caller costs",
		"`tools/list`",
		"per caller under load",
		"per caller held",
		"Held heap (MiB)",
		"Every planned step ran",
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(page, want) {
				t.Errorf("the page does not carry %q", want)
			}
		})
	}

	t.Run("a run with no series writes no section", func(t *testing.T) {
		if strings.Contains(renderPage(fixtureRun()), "What each extra caller costs") {
			t.Error("a run that measured no series still got the section")
		}
	})

	t.Run("a series with no settled reading leaves the columns out", func(t *testing.T) {
		bare := fixtureSeries()
		for i := range bare.Steps {
			bare.Steps[i].SettledHeapMiB = 0
		}
		if strings.Contains(renderOneSeries(&bare), "Held heap") {
			t.Error("the settled columns were drawn with nothing to put in them")
		}
	})
}

// TestRenderStop_SaysWhereTheSeriesEndedAndWhy verifies a shorter series is
// never presented as the whole one, in each of the three ways it can end early.
func TestRenderStop_SaysWhereTheSeriesEndedAndWhy(t *testing.T) {
	testCases := []struct {
		name string
		stop *SeriesStop
		want string
	}{
		{name: "every step ran", stop: nil, want: "Every planned step ran"},
		{
			name: "the budget", want: "was not started",
			stop: &SeriesStop{Kind: stopBudget, NextClients: 200, EstimateMiB: 40000},
		},
		{
			name: "the latency ceiling", want: "a client has given up",
			stop: &SeriesStop{Kind: stopLatency, NextClients: 100, P99Ms: 31000},
		},
		{
			name: "a failure", want: "the mirror refused",
			stop: &SeriesStop{Kind: stopFailure, NextClients: 100, Error: "the mirror refused"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := fixtureSeries()
			s.Stop = tc.stop
			if got := renderStop(&s); !strings.Contains(got, tc.want) {
				t.Errorf("renderStop() = %q, want it to say %q", got, tc.want)
			}
		})
	}
}

// TestRenderSlopes_WithholdsACostItCouldNotMeasure verifies the page says so
// rather than publishing a negative per-caller cost, which is what a fit over a
// short, noisy ladder produces.
func TestRenderSlopes_WithholdsACostItCouldNotMeasure(t *testing.T) {
	noisy := fixtureSeries()
	noisy.Steps = []SeriesStep{
		{Clients: 1, RSSPeakMiB: 40, SettledHeapMiB: 11},
		{Clients: 10, RSSPeakMiB: 30, SettledHeapMiB: 10},
	}
	got := renderSlopes(&noisy)
	if !strings.Contains(got, "a negative cost is not a measurement") {
		t.Errorf("renderSlopes() = %q, want it to withhold the figure and say why", got)
	}
	if strings.Contains(got, "per caller under load") {
		t.Error("renderSlopes() published a figure the fit could not support")
	}
}

// TestBuildLabel_NamesTheRevisionWhenThereIsOne verifies two builds of one tag
// can be told apart, and that an unstamped binary does not get "(none)".
func TestBuildLabel_NamesTheRevisionWhenThereIsOne(t *testing.T) {
	testCases := []struct {
		name   string
		server ServerInfo
		want   string
	}{
		{name: "a stamped build", server: ServerInfo{Version: "1.7.3", Commit: "abc1234"}, want: "1.7.3 (abc1234)"},
		{name: "no revision", server: ServerInfo{Version: "1.7.3"}, want: "1.7.3"},
		{name: "an unstamped revision", server: ServerInfo{Version: "1.7.3", Commit: "none"}, want: "1.7.3"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildLabel(tc.server); got != tc.want {
				t.Errorf("buildLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}
