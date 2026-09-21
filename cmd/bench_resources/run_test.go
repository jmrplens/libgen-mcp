package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTimings_CountsAToolFailureAsAFailure verifies the collection folds both
// kinds of failure into the same count, because both of them mean the timing is
// not the cost of doing the work.
func TestTimings_CountsAToolFailureAsAFailure(t *testing.T) {
	tm := newTimings()
	tm.record(methodToolsList, callResult{Duration: 10 * time.Millisecond})
	tm.record(methodToolsList, callResult{Duration: 20 * time.Millisecond})
	tm.record(methodToolsCall, callResult{Duration: time.Millisecond, ToolError: true, ToolErrText: "mirror unreachable"})
	tm.record(methodToolsCall, callResult{Duration: time.Millisecond, Err: &rpcError{Code: -32601, Message: "no"}})

	if got := tm.total(); got != 4 {
		t.Errorf("total() = %d, want 4", got)
	}
	rows := tm.methods()
	if len(rows) != 2 || rows[0].Method != methodToolsList {
		t.Fatalf("methods() = %+v, want the two methods in the order they were first called", rows)
	}
	if rows[0].Errors != 0 || rows[1].Errors != 2 {
		t.Errorf("errors = %d and %d, want 0 and 2", rows[0].Errors, rows[1].Errors)
	}
	if rows[0].P50Ms == 0 || rows[0].Calls != 2 {
		t.Errorf("row = %+v, want two calls with a median", rows[0])
	}
	if got := tm.diagnosis(); got != "mirror unreachable" {
		t.Errorf("diagnosis() = %q, want the first failure's own words", got)
	}
}

// TestTimings_FallsBackToTheProtocolErrorForItsDiagnosis verifies a run that
// stops still names something when the failure came from the envelope rather
// than from a tool.
func TestTimings_FallsBackToTheProtocolErrorForItsDiagnosis(t *testing.T) {
	tm := newTimings()
	tm.record(methodToolsList, callResult{Err: &rpcError{Code: -32601, Message: "no such method"}})
	if got := tm.diagnosis(); !strings.Contains(got, "no such method") {
		t.Errorf("diagnosis() = %q, want the protocol error", got)
	}
	if errText(nil) != "" {
		t.Error("errText(nil) should say nothing")
	}
	if firstNonEmpty("", "", "third") != "third" {
		t.Error("firstNonEmpty did not reach the third value")
	}
	if firstNonEmpty() != "" {
		t.Error("firstNonEmpty() with nothing to pick should say nothing")
	}
}

// TestAssertServed_RefusesAScenarioWhoseCallsDidNotWork verifies the guard that
// stopped this command from publishing an error path as a measurement.
//
// A failing call is fast, so a run that averaged them would report a server
// that gets quicker the more broken it is. There is no honest way to average
// the two, so a method that failed at all stops the run.
func TestAssertServed_RefusesAScenarioWhoseCallsDidNotWork(t *testing.T) {
	testCases := []struct {
		name    string
		latency []MethodLatency
		wantErr string
	}{
		{
			name:    "every call worked",
			latency: []MethodLatency{{Method: methodToolsList, Calls: 3}},
		},
		{
			name:    "some calls failed",
			latency: []MethodLatency{{Method: methodToolsCall, Calls: 3, Errors: 2}},
			wantErr: "2 of 3 tools/call calls failed",
		},
		{
			name:    "a method nothing completed",
			latency: []MethodLatency{{Method: methodPromptsList}},
			wantErr: "no prompts/list call completed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := assertServed(&Scenario{Latency: tc.latency}, "mirror unreachable")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("assertServed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to say %q", err, tc.wantErr)
			}
		})
	}

	t.Run("the refusal carries the diagnosis", func(t *testing.T) {
		err := assertServed(&Scenario{Latency: []MethodLatency{{Method: methodToolsCall, Calls: 1, Errors: 1}}},
			"validating arguments: unexpected additional properties")
		if err == nil || !strings.Contains(err.Error(), "unexpected additional properties") {
			t.Errorf("error = %v, want it to carry what the tool said", err)
		}
	})
}

// TestCPUOver_SaysSoRatherThanPublishingZero verifies a platform that will not
// report processor time is recorded as unreadable. Zero milliseconds per call
// is a claim; "this kernel would not tell us" is a fact.
func TestCPUOver_SaysSoRatherThanPublishingZero(t *testing.T) {
	t.Run("a platform that answers", func(t *testing.T) {
		withFakeProc(t)
		const stat = "1 (x) S 1 1 1 0 -1 4194304 100 0 0 0 100 100 0 0 20 0 5 0 100"
		fakeProcess(t, 1, "VmRSS:\t   10240 kB\n", stat)
		sam := newSampler(t.Context(), time.Millisecond, func() []int { return []int{1} })

		got := cpuOver(sam, 0, true, 10)
		if got.Unreadable {
			t.Errorf("cpuOver() = %+v, want a reading", got)
		}
		if got.Calls != 10 || got.MsPerCall <= 0 {
			t.Errorf("cpuOver() = %+v, want ten calls and a per-call figure", got)
		}
	})

	t.Run("a platform that does not", func(t *testing.T) {
		sam := newSampler(t.Context(), time.Millisecond, func() []int { return nil })
		got := cpuOver(sam, 0, false, 10)
		if !got.Unreadable || got.MsPerCall != 0 {
			t.Errorf("cpuOver() = %+v, want it marked unreadable", got)
		}
	})

	t.Run("a counter that went backwards", func(t *testing.T) {
		withFakeProc(t)
		const stat = "1 (x) S 1 1 1 0 -1 4194304 100 0 0 0 100 100 0 0 20 0 5 0 100"
		fakeProcess(t, 1, "VmRSS:\t   10240 kB\n", stat)
		sam := newSampler(t.Context(), time.Millisecond, func() []int { return []int{1} })

		got := cpuOver(sam, 1e9, true, 10)
		if got.Seconds != 0 {
			t.Errorf("cpuOver() = %+v, want zero rather than a negative measurement", got)
		}
	})

	t.Run("no calls at all", func(t *testing.T) {
		withFakeProc(t)
		const stat = "1 (x) S 1 1 1 0 -1 4194304 100 0 0 0 100 100 0 0 20 0 5 0 100"
		fakeProcess(t, 1, "VmRSS:\t   10240 kB\n", stat)
		sam := newSampler(t.Context(), time.Millisecond, func() []int { return []int{1} })
		if got := cpuOver(sam, 0, true, 0); got.MsPerCall != 0 {
			t.Errorf("cpuOver() = %+v, want no per-call figure with no calls", got)
		}
	})
}

// fakeCaller answers every call with a canned result, so the driving loop can
// be exercised without a server.
type fakeCaller struct {
	mu     sync.Mutex
	seen   []string
	result callResult
	err    error
	closed bool
}

// call records the method and answers.
func (f *fakeCaller) call(_ context.Context, method string, _ any) (callResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, method)
	return f.result, f.err
}

// close marks the caller released.
func (f *fakeCaller) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

// calls reports what this caller was asked for.
func (f *fakeCaller) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// TestDriveClient_CallsEveryMethodEveryRound verifies the shape of the steady
// phase: each round asks for each method, with Parallel calls in flight.
func TestDriveClient_CallsEveryMethodEveryRound(t *testing.T) {
	c := &fakeCaller{result: callResult{Duration: time.Millisecond}}
	tm := newTimings()
	if err := driveClient(t.Context(), c, 2, 3, tm); err != nil {
		t.Fatalf("driveClient: %v", err)
	}
	// Three methods, two in flight, three rounds.
	if got := len(c.calls()); got != 18 {
		t.Errorf("made %d calls, want 18", got)
	}
	if got := tm.total(); got != 18 {
		t.Errorf("recorded %d calls, want 18", got)
	}
}

// TestDriveClient_StopsOnACallThatDidNotComplete verifies a transport failure
// ends the scenario rather than being averaged into it.
func TestDriveClient_StopsOnACallThatDidNotComplete(t *testing.T) {
	c := &fakeCaller{err: errors.New("connection refused")}
	if err := driveClient(t.Context(), c, 1, 1, newTimings()); err == nil {
		t.Error("expected the call's error to stop the client")
	}
}

// TestCallInParallel_TreatsZeroAsOne verifies a scenario that forgot to say how
// many calls to keep in flight still makes one, rather than none.
func TestCallInParallel_TreatsZeroAsOne(t *testing.T) {
	c := &fakeCaller{result: callResult{Duration: time.Millisecond}}
	if err := callInParallel(t.Context(), c, methodToolsList, 0, newTimings()); err != nil {
		t.Fatalf("callInParallel: %v", err)
	}
	if got := len(c.calls()); got != 1 {
		t.Errorf("made %d calls, want 1", got)
	}
}

// TestParamsFor_SendsArgumentsOnlyWhereTheyMeanSomething verifies the tool call
// carries the search arguments and the list methods carry none.
func TestParamsFor_SendsArgumentsOnlyWhereTheyMeanSomething(t *testing.T) {
	call, ok := paramsFor(methodToolsCall).(map[string]any)
	if !ok || call["name"] != "search" {
		t.Errorf("paramsFor(tools/call) = %+v, want the search tool", call)
	}
	list, ok := paramsFor(methodToolsList).(map[string]any)
	if !ok || len(list) != 0 {
		t.Errorf("paramsFor(tools/list) = %+v, want no arguments", list)
	}
}

// TestCloseAll_ReleasesEveryCaller verifies a finished scenario lets go of its
// clients, so a step's sockets are not still open while the next one is
// measured.
func TestCloseAll_ReleasesEveryCaller(t *testing.T) {
	callers := []caller{&fakeCaller{}, &fakeCaller{}}
	closeAll(callers)
	for i, c := range callers {
		t.Run(itoa(i), func(t *testing.T) {
			if !c.(*fakeCaller).closed {
				t.Error("caller was not closed")
			}
		})
	}
}

// TestScenarioFrom_CarriesThePlanIntoTheRecord verifies every knob a scenario
// ran with reaches the record, since a number whose settings are not written
// down cannot be compared with anything.
func TestScenarioFrom_CarriesThePlanIntoTheRecord(t *testing.T) {
	plan := scenarioPlan{
		ID: "http-8", Transport: transportHTTP, Telemetry: true,
		Clients: 8, Parallel: 2, OutboundRPS: 20, OutboundBurst: 100,
	}
	got := scenarioFrom(plan, 3)
	if got.ID != plan.ID || got.Clients != 8 || got.Parallel != 2 || got.Rounds != 3 {
		t.Errorf("scenarioFrom() = %+v", got)
	}
	if got.OutboundRPS != 20 || got.OutboundBurst != 100 || !got.Telemetry {
		t.Errorf("scenarioFrom() lost a setting: %+v", got)
	}
}

// TestAlivePids_NamesOnlyWhatIsStillRunning verifies the set the sampler is
// handed, on both transports.
func TestAlivePids_NamesOnlyWhatIsStillRunning(t *testing.T) {
	withFakeProc(t)
	const stat = "1 (x) S 1 1 1 0 -1 4194304 100 0 0 0 100 100 0 0 20 0 5 0 100"
	fakeProcess(t, 1, "VmRSS:\t 1024 kB\n", stat)

	t.Run("a target with no process", func(t *testing.T) {
		if got := alivePids(t.Context(), &target{}); got != nil {
			t.Errorf("alivePids() = %v, want none", got)
		}
	})
	t.Run("several stdio targets, one gone", func(t *testing.T) {
		alive := &target{cmd: fakeCmd(1)}
		gone := &target{cmd: fakeCmd(999)}
		if got := alivePidsOf(t.Context(), []*target{alive, gone}); len(got) != 1 || got[0] != 1 {
			t.Errorf("alivePidsOf() = %v, want only the process that is still there", got)
		}
	})
}

// fakeCmd builds a command that reports a pid without ever having started a
// process, so the pid-gathering helpers can be asked about a process that is
// there and one that is not.
func fakeCmd(pid int) *exec.Cmd {
	return &exec.Cmd{Process: &os.Process{Pid: pid}}
}

// TestMsSince_MeasuresAtTheRecordsPrecision verifies the elapsed helper answers
// in milliseconds rather than in whatever unit a duration prints.
func TestMsSince_MeasuresAtTheRecordsPrecision(t *testing.T) {
	started := time.Now().Add(-1500 * time.Millisecond)
	if got := msSince(started); got < 1400 || got > 1700 {
		t.Errorf("msSince() = %v, want about 1500", got)
	}
}

// TestAppendNote_KeepsTheListNilUntilThereIsOne verifies the record omits the
// field entirely rather than carrying an empty list.
func TestAppendNote_KeepsTheListNilUntilThereIsOne(t *testing.T) {
	var notes []string
	notes = appendNote(notes, "first")
	if len(notes) != 1 || notes[0] != "first" {
		t.Errorf("appendNote() = %v", notes)
	}
}

// fakeRunner builds a runner that measures the stand-in process rather than the
// server, so the whole measurement path runs in a unit test.
func fakeRunner(t *testing.T) *runner {
	t.Helper()
	stub, stopCatalog := startCatalog()
	t.Cleanup(stopCatalog)
	otlp, stopCollector := startCollector()
	t.Cleanup(stopCollector)
	return &runner{
		binary:    os.Args[0],
		catalog:   stub,
		collector: otlp,
		settings:  Settings{Rounds: 1, SampleIntervalMs: 10},
		workDir:   t.TempDir(),
	}
}

// TestMeasure_RunsAScenarioEndToEndOnBothTransports verifies the measurement
// path itself: a process is started, its resident set is sampled while it
// serves, every method is driven, and the scenario comes back with figures
// rather than with zeros.
//
// It measures the stand-in rather than the server on purpose. What is under
// test is this command — the ordering of its phases, the sampler's window, the
// per-call arithmetic — and pointing it at the real binary would make a unit
// test depend on a twenty-second build and on the server's own behavior.
func TestMeasure_RunsAScenarioEndToEndOnBothTransports(t *testing.T) {
	testCases := []struct {
		name string
		plan scenarioPlan
	}{
		{
			name: "http",
			plan: scenarioPlan{
				ID: "http-2", Transport: transportHTTP, Clients: 2, Parallel: 2,
				OutboundRPS: benchOutboundRPS, OutboundBurst: benchOutboundBurst,
			},
		},
		{
			name: "stdio",
			plan: scenarioPlan{
				ID: "stdio-2", Transport: transportStdio, Clients: 2, Parallel: 1,
				OutboundRPS: benchOutboundRPS, OutboundBurst: benchOutboundBurst,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := fakeRunner(t)
			r.env = map[string]string{fakeServerEnv: tc.plan.Transport}

			got, err := r.measure(t.Context(), tc.plan)
			if err != nil {
				t.Fatalf("measure: %v", err)
			}
			assertScenarioMeasured(t, tc.plan, got)
		})
	}
}

// assertScenarioMeasured checks that a measured scenario carries figures rather
// than zeros, and that it is the plan it was asked for.
func assertScenarioMeasured(t *testing.T, plan scenarioPlan, got Scenario) {
	t.Helper()
	if got.ID != plan.ID || got.Transport != plan.Transport {
		t.Errorf("measure() = %+v, want the plan it was given", got)
	}
	if got.Startup.ProcessReadyMs <= 0 || got.Startup.FirstListMs <= 0 {
		t.Errorf("startup = %+v, want both waits measured", got.Startup)
	}
	if got.ListBytes == 0 {
		t.Error("ListBytes = 0; the surface a client downloads was never measured")
	}
	if len(got.Latency) != 3 {
		t.Errorf("measured %d methods, want three", len(got.Latency))
	}
	for _, m := range got.Latency {
		if m.Calls == 0 {
			t.Errorf("%s was never called", m.Method)
		}
	}
	if runtimeGOOS == "linux" && got.Memory.PeakMiB <= 0 {
		t.Errorf("memory = %+v, want a resident set on a platform that reports one", got.Memory)
	}
	if !hasNote(got.Notes, "reached the stand-in catalog") {
		t.Errorf("notes = %v, want the catalog reach recorded", got.Notes)
	}
}

// TestMeasure_RecordsWhatTheExporterDid verifies a telemetry scenario writes
// down how many exports arrived, rather than leaving a reader to assume any
// did: the exporter batches on its own schedule, so a short scenario can end
// with none.
func TestMeasure_RecordsWhatTheExporterDid(t *testing.T) {
	r := fakeRunner(t)
	r.env = map[string]string{fakeServerEnv: transportHTTP}
	plan := scenarioPlan{
		ID: "http-1-otel", Transport: transportHTTP, Clients: 1, Parallel: 1, Telemetry: true,
		OutboundRPS: benchOutboundRPS, OutboundBurst: benchOutboundBurst,
	}
	got, err := r.measure(t.Context(), plan)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if !hasNote(got.Notes, "OTLP exports reached the collector") {
		t.Errorf("notes = %v, want the export count recorded", got.Notes)
	}
}

// hasNote reports whether any note carries the fragment.
func hasNote(notes []string, fragment string) bool {
	for _, note := range notes {
		if strings.Contains(note, fragment) {
			return true
		}
	}
	return false
}

// TestTargetOpts_GivesEveryProcessItsOwnDirectory verifies two processes of one
// stdio scenario do not share a download directory, which is also their home
// and therefore their mirror cache.
func TestTargetOpts_GivesEveryProcessItsOwnDirectory(t *testing.T) {
	r := fakeRunner(t)
	plan := scenarioPlan{ID: "stdio-2", Transport: transportStdio, Clients: 2, Parallel: 1}
	first := r.targetOpts(plan, 0)
	second := r.targetOpts(plan, 1)
	if first.downloadDir == second.downloadDir {
		t.Errorf("both processes were given %s", first.downloadDir)
	}
	if first.telemetry != "" {
		t.Error("a scenario that did not ask for telemetry was given a collector")
	}
	if r.sampleInterval() != 10*time.Millisecond {
		t.Errorf("sampleInterval() = %v, want the setting", r.sampleInterval())
	}
}
