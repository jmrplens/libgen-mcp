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
