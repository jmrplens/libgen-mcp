package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestBatchInterval_PacesACallerRatherThanLettingItSpin verifies the pacing
// that makes a series measurable at all.
//
// This server limits inbound requests per client address, and a caller spinning
// had 4,361 of its 4,420 calls refused: a series measuring that would be
// measuring the limiter. The interval is also never zero, because a ticker
// panics on one.
func TestBatchInterval_PacesACallerRatherThanLettingItSpin(t *testing.T) {
	testCases := []struct {
		name string
		plan seriesPlan
		want time.Duration
	}{
		{
			name: "two in flight at five a second",
			plan: seriesPlan{Parallel: 2, PerClientRPS: 5},
			want: 400 * time.Millisecond,
		},
		{
			name: "one in flight at four a second",
			plan: seriesPlan{Parallel: 1, PerClientRPS: 4},
			want: 250 * time.Millisecond,
		},
		{name: "no rate stated", plan: seriesPlan{Parallel: 2}, want: time.Millisecond},
		{name: "a negative rate", plan: seriesPlan{Parallel: 2, PerClientRPS: -1}, want: time.Millisecond},
		{
			name: "a rate faster than the clock",
			plan: seriesPlan{Parallel: 1, PerClientRPS: 1e9},
			want: time.Millisecond,
		},
		{name: "no parallelism stated", plan: seriesPlan{PerClientRPS: 2}, want: 500 * time.Millisecond},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := batchInterval(tc.plan); got != tc.want {
				t.Errorf("batchInterval(%+v) = %v, want %v", tc.plan, got, tc.want)
			}
		})
	}
}

// TestParseHeapAlloc_ReadsTheLineTheTextProfileEndsWith verifies the settled
// heap is read from the runtime's own MemStats, which is what `debug=1` appends
// to a heap profile — one line, no profile decoder and no dependency.
func TestParseHeapAlloc_ReadsTheLineTheTextProfileEndsWith(t *testing.T) {
	testCases := []struct {
		name, profile string
		want          float64
		wantErr       bool
	}{
		{
			name:    "a real profile's tail",
			profile: "heap profile: 1: 2 [3: 4] @ heap/1048576\n\n# HeapAlloc = 4194304\n# Sys = 8388608\n",
			want:    4,
		},
		{name: "no such line", profile: "heap profile: 0: 0 [0: 0]\n", wantErr: true},
		{name: "an empty document", profile: "", wantErr: true},
		{name: "a value too large to be a count", profile: "# HeapAlloc = 99999999999999999999999\n", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHeapAlloc(tc.profile)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHeapAlloc: %v", err)
			}
			if got != tc.want {
				t.Errorf("parseHeapAlloc() = %v MiB, want %v", got, tc.want)
			}
		})
	}
}

// TestBudgetStop_RefusesAStepItCannotAfford verifies the guard that keeps a
// series from taking the host down, and the three cases in which it declines to
// judge: no budget, too few steps to extrapolate from, and an estimate inside
// what the host can spare.
func TestBudgetStop_RefusesAStepItCannotAfford(t *testing.T) {
	var r runner
	measured := []SeriesStep{
		{Clients: 1, RSSPeakMiB: 30},
		{Clients: 2, RSSPeakMiB: 40},
		{Clients: 3, RSSPeakMiB: 50},
	}

	t.Run("a step the host can spare", func(t *testing.T) {
		out := &SeriesScenario{BudgetMiB: 1000, Steps: measured}
		if stop := r.budgetStop(out, 10); stop != nil {
			t.Errorf("budgetStop() = %+v, want nil", stop)
		}
	})
	t.Run("a step it cannot", func(t *testing.T) {
		out := &SeriesScenario{BudgetMiB: 100, Steps: measured}
		stop := r.budgetStop(out, 100)
		if stop == nil {
			t.Fatal("expected the step to be refused")
		}
		if stop.Kind != stopBudget || stop.NextClients != 100 || stop.EstimateMiB <= 100 {
			t.Errorf("budgetStop() = %+v, want a budget stop naming the estimate", stop)
		}
	})
	t.Run("a host with no budget", func(t *testing.T) {
		out := &SeriesScenario{Steps: measured}
		if stop := r.budgetStop(out, 1000); stop != nil {
			t.Errorf("budgetStop() = %+v, want nil with nothing to measure against", stop)
		}
	})
	// Before two steps exist there is no line to fit, and the guard used to do
	// nothing at all there. A ladder of 1,500000 would then start its second
	// step and allocate half a million callers on both sides of the socket
	// before anything had an opinion about it. The bootstrap allowance is what
	// stands in until the measurement can speak for itself, and it is
	// deliberately an order of magnitude above anything measured.
	t.Run("a huge step before there is a line to fit", func(t *testing.T) {
		out := &SeriesScenario{BudgetMiB: 1000, Steps: measured[:1]}
		stop := r.budgetStop(out, 500000)
		if stop == nil {
			t.Fatal("expected the step to be refused on the bootstrap allowance")
		}
		if stop.EstimateMiB < 500000 {
			t.Errorf("estimate = %v, want it built from the allowance", stop.EstimateMiB)
		}
	})
	t.Run("a modest step before there is a line to fit", func(t *testing.T) {
		out := &SeriesScenario{BudgetMiB: 1000, Steps: measured[:1]}
		if stop := r.budgetStop(out, 10); stop != nil {
			t.Errorf("budgetStop() = %+v, want a small step to run", stop)
		}
	})
	t.Run("the very first step of all", func(t *testing.T) {
		out := &SeriesScenario{BudgetMiB: 1000}
		if stop := r.budgetStop(out, 1); stop != nil {
			t.Errorf("budgetStop() = %+v, want the first step to run", stop)
		}
		if stop := r.budgetStop(out, 500000); stop == nil {
			t.Error("expected a ladder that starts enormous to be refused before anything runs")
		}
	})
	t.Run("steps that fix no line fall back to the allowance", func(t *testing.T) {
		flat := []SeriesStep{{Clients: 2, RSSPeakMiB: 30}, {Clients: 2, RSSPeakMiB: 40}}
		out := &SeriesScenario{BudgetMiB: 1000, Steps: flat}
		if stop := r.budgetStop(out, 10); stop != nil {
			t.Errorf("budgetStop() = %+v, want a small step to run", stop)
		}
		if stop := r.budgetStop(out, 5000); stop == nil {
			t.Error("expected the allowance to refuse a step no fit could judge")
		}
	})
}

// TestSlopePerClient_DoesNotPublishANegativeCost verifies the rule that keeps a
// short ladder from publishing nonsense.
//
// A least-squares fit over a few noisy steps can come out below zero, and
// "each caller costs minus fifty kilobytes" is not a measurement — it is the
// noise being larger than the signal, which is a fact about the ladder rather
// than about the server.
func TestSlopePerClient_DoesNotPublishANegativeCost(t *testing.T) {
	rising := &SeriesScenario{Steps: []SeriesStep{
		{Clients: 1, RSSPeakMiB: 30, SettledHeapMiB: 10},
		{Clients: 10, RSSPeakMiB: 40, SettledHeapMiB: 11},
	}}
	falling := &SeriesScenario{Steps: []SeriesStep{
		{Clients: 1, RSSPeakMiB: 40, SettledHeapMiB: 11},
		{Clients: 10, RSSPeakMiB: 30, SettledHeapMiB: 10},
	}}

	t.Run("a cost that was measured", func(t *testing.T) {
		load, ok := rising.loadSlopeMiB()
		if !ok || load <= 0 {
			t.Errorf("loadSlopeMiB() = %v, %t; want a positive slope", load, ok)
		}
		tenancy, ok := rising.tenancySlopeKiB()
		if !ok || tenancy <= 0 {
			t.Errorf("tenancySlopeKiB() = %v, %t; want a positive slope", tenancy, ok)
		}
	})
	t.Run("a fit that came out negative", func(t *testing.T) {
		if load, ok := falling.loadSlopeMiB(); ok {
			t.Errorf("loadSlopeMiB() = %v, true; want it withheld", load)
		}
		if tenancy, ok := falling.tenancySlopeKiB(); ok {
			t.Errorf("tenancySlopeKiB() = %v, true; want it withheld", tenancy)
		}
	})
}

// TestRunSeries_StepsThroughTheLadderAgainstARealProcess verifies the whole
// series path: a process is started with a profiling listener, each step is
// driven for its duration, and both readings are taken.
func TestRunSeries_StepsThroughTheLadderAgainstARealProcess(t *testing.T) {
	r := fakeRunner(t)
	r.env = map[string]string{fakeServerEnv: transportHTTP}

	got, err := r.runSeries(t.Context(), seriesPlan{
		ID:            "series-test",
		Clients:       []int{1, 2},
		Parallel:      1,
		PerClientRPS:  200,
		StepDuration:  250 * time.Millisecond,
		Method:        methodToolsList,
		OutboundRPS:   benchOutboundRPS,
		OutboundBurst: benchOutboundBurst,
	})
	if err != nil {
		t.Fatalf("runSeries: %v", err)
	}
	if got.StopReason != stopComplete {
		t.Errorf("StopReason = %q (%+v), want every step to have run", got.StopReason, got.Stop)
	}
	if len(got.Steps) != 2 || got.StoppedAt != 2 {
		t.Fatalf("ran %d steps, stopped at %d; want two steps", len(got.Steps), got.StoppedAt)
	}
	for _, step := range got.Steps {
		t.Run(itoa(step.Clients)+" clients", func(t *testing.T) {
			if step.Calls == 0 {
				t.Error("no call completed")
			}
			if step.Errors != 0 {
				t.Errorf("%d calls failed", step.Errors)
			}
			if step.SettledHeapMiB != 4 {
				t.Errorf("SettledHeapMiB = %v, want the figure the profile stated", step.SettledHeapMiB)
			}
			if runtimeGOOS == "linux" && step.RSSPeakMiB <= 0 {
				t.Errorf("RSSPeakMiB = %v on a platform that reports one", step.RSSPeakMiB)
			}
		})
	}
	if got.Method != methodToolsList || got.PerClientRPS != 200 {
		t.Errorf("the series did not record what it drove: %+v", got)
	}
}

// TestRunSeries_StopsWhenTheTailCrossesTheCeiling verifies the second guard: a
// step whose calls take longer than a client would wait is the last one run,
// because the next would be measuring timeouts rather than the server.
func TestRunSeries_StopsWhenTheTailCrossesTheCeiling(t *testing.T) {
	original := latencyCeiling
	latencyCeiling = time.Nanosecond
	t.Cleanup(func() { latencyCeiling = original })

	r := fakeRunner(t)
	r.env = map[string]string{fakeServerEnv: transportHTTP}

	got, err := r.runSeries(t.Context(), seriesPlan{
		ID: "series-test", Clients: []int{1, 2, 3}, Parallel: 1, PerClientRPS: 200,
		StepDuration: 200 * time.Millisecond, Method: methodToolsList,
		OutboundRPS: benchOutboundRPS, OutboundBurst: benchOutboundBurst,
	})
	if err != nil {
		t.Fatalf("runSeries: %v", err)
	}
	if got.StopReason != stopLatency {
		t.Fatalf("StopReason = %q, want the latency guard to have fired", got.StopReason)
	}
	if len(got.Steps) != 1 || got.StoppedAt != 1 {
		t.Errorf("ran %d steps, want it to stop after the first", len(got.Steps))
	}
	if len(got.Skipped) != 2 {
		t.Errorf("Skipped = %v, want the two counts that were not run", got.Skipped)
	}
	if got.Stop == nil || got.Stop.P99Ms <= 0 {
		t.Errorf("Stop = %+v, want the tail that crossed recorded", got.Stop)
	}
}

// TestRunSeries_ReportsAProcessItCannotStart verifies a series that never got a
// server says so rather than returning an empty measurement.
func TestRunSeries_ReportsAProcessItCannotStart(t *testing.T) {
	r := fakeRunner(t)
	r.binary = os.DevNull

	_, err := r.runSeries(t.Context(), seriesPlan{
		ID: "series-test", Clients: []int{1}, Parallel: 1, PerClientRPS: 10,
		StepDuration: 100 * time.Millisecond, Method: methodToolsList,
	})
	if err == nil {
		t.Error("expected an error from a binary that cannot be started")
	}
}

// TestSeriesPlanFor_ReadsTheLadderOrTakesTheDefault verifies the ladder, the
// step duration and the refusals.
func TestSeriesPlanFor_ReadsTheLadderOrTakesTheDefault(t *testing.T) {
	t.Run("a full run", func(t *testing.T) {
		plan, err := seriesPlanFor(options{})
		if err != nil {
			t.Fatalf("seriesPlanFor: %v", err)
		}
		if len(plan.Clients) != len(defaultSeriesClients()) || plan.StepDuration != 10*time.Second {
			t.Errorf("plan = %+v, want the default ladder at ten seconds a step", plan)
		}
		if plan.Method != methodToolsList || plan.PerClientRPS <= 0 {
			t.Errorf("plan = %+v, want a paced tools/list load", plan)
		}
	})

	t.Run("a quick run", func(t *testing.T) {
		plan, err := seriesPlanFor(options{quick: true})
		if err != nil {
			t.Fatalf("seriesPlanFor: %v", err)
		}
		if len(plan.Clients) != len(quickSeriesClients()) || plan.StepDuration != 2*time.Second {
			t.Errorf("plan = %+v, want the short ladder at two seconds a step", plan)
		}
	})

	t.Run("a ladder of the caller's own", func(t *testing.T) {
		plan, err := seriesPlanFor(options{seriesClients: "1, 4 ,16", stepDuration: time.Second})
		if err != nil {
			t.Fatalf("seriesPlanFor: %v", err)
		}
		if len(plan.Clients) != 3 || plan.Clients[2] != 16 || plan.StepDuration != time.Second {
			t.Errorf("plan = %+v, want the ladder that was asked for", plan)
		}
	})

	t.Run("a ladder that is refused", func(t *testing.T) {
		if _, err := seriesPlanFor(options{seriesClients: "4,2"}); err == nil {
			t.Error("expected a descending ladder to be refused")
		}
	})
}

// TestParseClientLadder_RefusesWhatTheBudgetGuardCannotUse verifies the ladder
// is ascending and positive.
//
// Ascending is not a style rule: the budget guard fits a line through the steps
// measured so far to decide whether the next one is affordable, and a ladder
// that went down would have it extrapolating backwards into a step it had
// already run.
func TestParseClientLadder_RefusesWhatTheBudgetGuardCannotUse(t *testing.T) {
	testCases := []struct {
		name, list string
		want       int
		wantErr    string
	}{
		{name: "a plain ladder", list: "1,2,5", want: 3},
		{name: "spaces and an empty element", list: " 1 , 2 ,, 5 ", want: 3},
		{name: "not a number", list: "1,two", wantErr: "two"},
		{name: "a step with no clients", list: "0,1", wantErr: "at least one client"},
		{name: "a repeated count", list: "1,1", wantErr: "must ascend"},
		{name: "a descending ladder", list: "5,1", wantErr: "must ascend"},
		{name: "nothing at all", list: " , ", wantErr: "names no step"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseClientLadder(tc.list)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %v, want it to say %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseClientLadder: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("parseClientLadder() = %v, want %d steps", got, tc.want)
			}
		})
	}
}

// TestDescribeSeries_SaysWhereItStoppedAndWhy verifies the terminal line under
// -v, including the failure text a stopped series needs to be diagnosable.
func TestDescribeSeries_SaysWhereItStoppedAndWhy(t *testing.T) {
	complete := SeriesScenario{
		StoppedAt: 10, StopReason: stopComplete,
		Steps: []SeriesStep{
			{Clients: 1, RSSPeakMiB: 30, SettledHeapMiB: 10},
			{Clients: 10, RSSPeakMiB: 40, SettledHeapMiB: 11},
		},
	}
	got := describeSeries(complete)
	for _, want := range []string{"2 steps", "10 clients", "under load", "held"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(got, want) {
				t.Errorf("describeSeries() = %q, want it to carry %q", got, want)
			}
		})
	}

	t.Run("a series that failed", func(t *testing.T) {
		failed := SeriesScenario{
			StopReason: stopFailure,
			Stop:       &SeriesStop{Kind: stopFailure, Error: "the mirror refused"},
		}
		if !strings.Contains(describeSeries(failed), "the mirror refused") {
			t.Errorf("describeSeries() = %q, want the failure's text", describeSeries(failed))
		}
	})
}
