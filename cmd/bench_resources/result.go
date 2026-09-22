// result.go is the record: the shape every number this command measures is
// written into, and the only thing anything downstream reads.
//
// It is versioned because it is an artifact other things are generated from. A
// reader that finds a schema it does not know must say so rather than guess at
// a field that moved, and a record written by an older build must still be
// renderable, which is why every field added since schema 1 is omitempty and
// every consumer treats its zero as "not measured" rather than as zero.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// recordSchema is the version this build writes and the only one it renders.
const recordSchema = 1

// Run is one complete measurement: what was measured, on what, and with which
// knobs, so a later run can be compared against this one on equal terms.
type Run struct {
	Schema      int        `json:"schema"`
	GeneratedAt string     `json:"generated_at"`
	Server      ServerInfo `json:"server"`
	Host        HostInfo   `json:"host"`
	Settings    Settings   `json:"settings"`
	Scenarios   []Scenario `json:"scenarios"`
	// Series are the concurrency series, kept apart from the scenarios because
	// nothing that reads a scenario knows what to do with a list of steps: the
	// point tables iterate Scenarios, and a series entry among them would be
	// drawn as a point with no numbers.
	Series []SeriesScenario `json:"series,omitempty"`
}

// SeriesScenario is one concurrency series: one HTTP process given more and
// more distinct client addresses, measured at each count.
//
// The point scenarios say what a handful of callers cost. A shared deployment
// meets hundreds, and a per-caller figure multiplied out puts such a deployment
// far beyond anything those scenarios measured. This measures it instead of
// extrapolating.
//
// Clients is the plan; Steps is what actually ran, in the same order, and
// Skipped is the rest. When the two differ, Stop says why: the series refuses to
// take the host down, so a step whose resident set is estimated beyond the
// memory budget is never started, and a step whose tool calls take longer than a
// client would wait is the last one run.
type SeriesScenario struct {
	ID        string `json:"id"`
	Transport string `json:"transport"`
	// Method is what every caller drove. It is recorded because the answer
	// depends on it: a method that reaches the catalog is bounded by the
	// outbound budget rather than by the process.
	Method string `json:"method"`
	// Parallel is requests in flight per client address during a step's steady
	// phase, and PerClientRPS is how fast one address calls: the series is
	// paced rather than driven flat out, because a real caller does not spin
	// and because this server limits inbound requests per address.
	Parallel     int     `json:"parallel"`
	PerClientRPS float64 `json:"per_client_rps"`
	// StepSeconds is the length of every step's steady phase.
	StepSeconds float64 `json:"step_seconds"`
	// OutboundRPS and OutboundBurst are the mirror-facing budget, as for a
	// point scenario: a series run at the shipped one request per second would
	// measure the queue at every step rather than the server.
	OutboundRPS   float64 `json:"outbound_rps"`
	OutboundBurst int     `json:"outbound_burst"`
	// Clients is the planned list of client-address counts.
	Clients []int `json:"clients"`
	// BudgetMiB is the resident set the series would not plan a step beyond;
	// zero when the host did not report its available memory.
	BudgetMiB float64 `json:"budget_mib"`
	// StoppedAt is the last client count that ran.
	StoppedAt  int          `json:"stopped_at"`
	StopReason string       `json:"stop_reason"`
	Stop       *SeriesStop  `json:"stop,omitempty"`
	Skipped    []int        `json:"skipped,omitempty"`
	Steps      []SeriesStep `json:"steps"`
	Notes      []string     `json:"notes,omitempty"`
}

// The reasons a series stops early, plus the one that means it did not.
const (
	stopComplete = "complete"
	stopBudget   = "budget"
	stopLatency  = "latency"
	stopFailure  = "failure"
)

// SeriesStop is why a series ended where it did, in a form a renderer can write
// in its own words rather than parsing a sentence.
type SeriesStop struct {
	Kind string `json:"kind"`
	// NextClients is the count that was not started.
	NextClients int `json:"next_clients,omitempty"`
	// EstimateMiB is what the next step was expected to reach, for a budget
	// stop.
	EstimateMiB float64 `json:"estimate_mib,omitempty"`
	// P99Ms is the tools/call tail that crossed the ceiling, for a latency
	// stop.
	P99Ms float64 `json:"p99_ms,omitempty"`
	// Error is what failed, for a failure stop.
	Error string `json:"error,omitempty"`
}

// SeriesStep is one client-address count, measured twice: over its steady
// phase, and again once that phase has stopped.
//
// The two are different questions, and reporting only the first publishes the
// load as the tenancy. RSSMeanMiB and RSSPeakMiB are what N addresses cost while
// all of them are calling, which is what a host has to survive. SettledHeapMiB
// is what they cost to hold, taken with the load stopped and a collection
// forced, which is what a reader means by "what does another caller cost me".
type SeriesStep struct {
	Clients int `json:"clients"`
	// RSSMeanMiB and RSSPeakMiB are the resident set over the steady phase.
	RSSMeanMiB float64 `json:"rss_mean_mib"`
	RSSPeakMiB float64 `json:"rss_peak_mib"`
	// SettledHeapMiB is the live heap with the load stopped and a collection
	// forced, read from the profiling listener. It is the honest tenancy
	// figure: nothing a request allocated while it was being served is still
	// in it.
	SettledHeapMiB float64 `json:"settled_heap_mib,omitempty"`
	// SettledRSSMiB is the resident set read at that same moment.
	//
	// It must be read knowing that it lags: the forced collection frees the
	// heap, and Go returns the pages behind it to the operating system on the
	// scavenger's own schedule, minutes later and only under memory pressure. A
	// settled resident set well above the settled heap is the expected reading,
	// not a contradiction of it, and the heap is what moves with the count.
	SettledRSSMiB float64 `json:"settled_rss_mib,omitempty"`
	// CPUMsPerCall is the processor time the server consumed during the phase
	// divided by the calls that completed, in milliseconds.
	CPUMsPerCall float64  `json:"cpu_ms_per_call"`
	Calls        int      `json:"calls"`
	Errors       int      `json:"errors,omitempty"`
	P50Ms        float64  `json:"p50_ms"`
	P99Ms        float64  `json:"p99_ms"`
	Notes        []string `json:"notes,omitempty"`
}

// hasSettled reports whether any step of the series carries a settled reading,
// so a renderer leaves the columns out entirely rather than printing "n/a"
// against every row, which reads as a measurement rather than as an absence.
func (s *SeriesScenario) hasSettled() bool {
	for _, step := range s.Steps {
		// Either field, because they are taken separately: the resident set is
		// read from the kernel and always available, while the heap comes from
		// the measured process's profiling listener and can fail on its own. A
		// check on the heap alone would hide a resident reading that was taken.
		if step.SettledHeapMiB > 0 || step.SettledRSSMiB > 0 {
			return true
		}
	}
	return false
}

// loadSlopeMiB is how much the peak resident set grows per client address while
// every address is calling, in mebibytes.
//
// It answers what a deployment needs while N callers are all working at once.
// It is only wrong when it is read as the cost of holding a caller, which is
// what tenancySlopeKiB answers.
func (s *SeriesScenario) loadSlopeMiB() (float64, bool) {
	slope, ok := s.slopePerClient(func(step SeriesStep) float64 { return step.RSSPeakMiB })
	return round(slope), ok
}

// tenancySlopeKiB is how much the settled live heap grows per client address,
// in kibibytes: what holding one more caller costs with nothing in flight.
//
// Kibibytes because that is the size of the thing. In mebibytes it is a row of
// zeros, which reads as "nothing" rather than as "small".
func (s *SeriesScenario) tenancySlopeKiB() (float64, bool) {
	slope, ok := s.slopePerClient(func(step SeriesStep) float64 { return step.SettledHeapMiB })
	return round(slope * 1024), ok
}

// slopePerClient fits a least-squares line through the steps, taking each
// step's y from pick, and reports its slope: the growth per client address.
//
// A step whose figure is zero is left out of the fit rather than dragged
// through it, because zero is how this record spells "that reading could not be
// taken", and a fit that believed it would publish a slope nobody measured.
func (s *SeriesScenario) slopePerClient(pick func(SeriesStep) float64) (float64, bool) {
	xs := make([]float64, 0, len(s.Steps))
	ys := make([]float64, 0, len(s.Steps))
	for _, step := range s.Steps {
		if value := pick(step); value > 0 {
			xs = append(xs, float64(step.Clients))
			ys = append(ys, value)
		}
	}
	slope, _, ok := fitLine(xs, ys)
	// A fit that comes out at or below zero has not measured a cost. It happens
	// on a short ladder, where the noise between two steps is larger than what
	// a caller adds, and the honest answer there is that the measurement could
	// not separate the two — not "each caller costs minus fifty kilobytes",
	// which is what publishing the fitted number would say.
	if slope <= 0 {
		return 0, false
	}
	// Unrounded. Each caller rounds in the unit it publishes, because rounding
	// here rounds in mebibytes: a tenancy slope of 0.1056 MiB became 0.11, and
	// the multiply to kibibytes turned that into 113 against a true 108.
	return slope, ok
}

// fitLine fits a least-squares line through the points and reports its slope
// and intercept.
//
// ok is false for fewer than two points, and for points that all share one x:
// they fix no line, the fit's denominator is zero there, and dividing by it
// would publish an infinity as a measurement.
func fitLine(xs, ys []float64) (slope, intercept float64, ok bool) {
	if len(xs) < 2 {
		return 0, 0, false
	}
	var sumX, sumY, sumXY, sumXX float64
	for i, x := range xs {
		sumX += x
		sumY += ys[i]
		sumXY += x * ys[i]
		sumXX += x * x
	}
	n := float64(len(xs))
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return 0, 0, false
	}
	slope = (n*sumXY - sumX*sumY) / denominator
	return slope, (sumY - slope*sumX) / n, true
}

// ServerInfo identifies the build that was measured, as the binary itself
// reports it rather than as this command assumes.
type ServerInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	// BytesOnDisk is the size of the binary that was measured. It is here
	// because "what does the process cost" includes what it costs to ship, and
	// because the npm launcher asserts a size floor per platform: a number
	// nobody records is a number nobody notices doubling.
	BytesOnDisk int64 `json:"bytes_on_disk,omitempty"`
}

// HostInfo is the machine the numbers came from.
type HostInfo struct {
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	CPUModel    string  `json:"cpu_model"`
	CPUs        int     `json:"cpus"`
	MemTotalGiB float64 `json:"mem_total_gib"`
	Kernel      string  `json:"kernel"`
	GoVersion   string  `json:"go_version"`
}

// Settings records the knobs the matrix was run with.
type Settings struct {
	Rounds           int  `json:"rounds"`
	SampleIntervalMs int  `json:"sample_interval_ms"`
	Quick            bool `json:"quick"`
}

// Scenario is one point of the matrix, fully measured.
//
// There is no tool-surface dimension, unlike the sibling project this was
// adapted from: this server registers four tools and a handful of prompts, and
// the only thing that changes what a client is shown is whether the deployment
// is remote (where read is not registered, LIBGEN_MCP_SERVER_FETCH). That is a
// property of the transport here, so the transport is the dimension.
type Scenario struct {
	ID        string `json:"id"`
	Transport string `json:"transport"`
	Telemetry bool   `json:"telemetry"`
	// Clients is the number of distinct client addresses on HTTP and the number
	// of server processes on stdio, because that is what a client is on each
	// transport: HTTP charges by address, stdio spawns one process per client.
	Clients int `json:"clients"`
	// Parallel is how many requests each client keeps in flight at once.
	Parallel int `json:"parallel"`
	Rounds   int `json:"rounds"`
	// OutboundRPS and OutboundBurst are the mirror-facing budget the scenario
	// ran with. They are recorded because they dominate: with the shipped
	// default of one request per second, every tool call queues behind one
	// token and the latency published is the queue rather than the server.
	OutboundRPS   float64 `json:"outbound_rps"`
	OutboundBurst int     `json:"outbound_burst"`
	// ListBytes is the size of one tools/list response body, which is the
	// surface's cost to every client on every reconnect.
	ListBytes int             `json:"list_bytes"`
	Startup   Startup         `json:"startup"`
	Memory    Memory          `json:"memory"`
	CPU       CPU             `json:"cpu"`
	Latency   []MethodLatency `json:"latency"`
	Notes     []string        `json:"notes,omitempty"`
}

// Startup separates the two waits a client can experience, because they are
// different: the process answers in milliseconds while the surface behind it is
// still being built, so reporting one number would hide the one that hurts.
type Startup struct {
	// ProcessReadyMs is spawn to a process that will answer: /health on HTTP,
	// the binding's handshake on stdio.
	ProcessReadyMs float64 `json:"process_ready_ms"`
	// FirstListMs is the first tools/list of the first client, which is what
	// pays for registration.
	FirstListMs float64 `json:"first_list_ms"`
	// WarmListMs is a tools/list once the surface is built, for contrast.
	WarmListMs float64 `json:"warm_list_ms"`
}

// Memory is the resident set at the three moments an operator sizes for.
type Memory struct {
	// IdleMiB is the process with nothing asked of it yet.
	IdleMiB float64 `json:"idle_mib"`
	// MeanMiB and PeakMiB are what it weighs while serving, and the worst it
	// reached doing so. A container limit is set against the peak.
	MeanMiB float64 `json:"mean_mib"`
	PeakMiB float64 `json:"peak_mib"`
}

// CPU is the processor time the measured phase consumed, and what that works
// out to per call.
type CPU struct {
	Seconds    float64 `json:"seconds"`
	MsPerCall  float64 `json:"ms_per_call"`
	Calls      int     `json:"calls"`
	Unreadable bool    `json:"unreadable,omitempty"`
}

// MethodLatency is one MCP method's timing over the measured rounds.
type MethodLatency struct {
	Method string  `json:"method"`
	Calls  int     `json:"calls"`
	P50Ms  float64 `json:"p50_ms"`
	P99Ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`
	Errors int     `json:"errors,omitempty"`
}

// percentile reports the value at q (0..1) of the sorted-by-value copy of
// samples, in milliseconds.
//
// Nearest-rank rather than interpolated, because these are latencies: the
// number published should be one the run actually observed, and an interpolated
// p99 between two real samples is a call nobody made.
func percentile(samples []time.Duration, q float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	slices.Sort(sorted)
	// Nearest rank is ceil(q*n), one-based, which is index ceil(q*n)-1 here.
	// Truncating q*n instead lands one place too high for every q that does not
	// divide the sample count evenly — a p50 of four samples would report the
	// third, which is a number above the median presented as the median.
	index := min(max(int(math.Ceil(q*float64(len(sorted))))-1, 0), len(sorted)-1)
	return round(float64(sorted[index].Microseconds()) / 1000)
}

// writeRecord writes the run as indented JSON, creating the directory it goes
// in. Indented because it is committed: a one-line document makes every diff
// the whole file, which is what stops anybody reading the change.
func writeRecord(path string, run *Run) error {
	body, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o750); mkErr != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), mkErr)
	}
	if wErr := os.WriteFile(path, append(body, '\n'), 0o600); wErr != nil {
		return fmt.Errorf("write %s: %w", path, wErr)
	}
	return nil
}

// readRecord reads a run back, refusing a schema this build does not know
// rather than rendering a field that moved.
func readRecord(path string) (*Run, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- an operator-named path
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var run Run
	if uErr := json.Unmarshal(body, &run); uErr != nil {
		return nil, fmt.Errorf("decode %s: %w", path, uErr)
	}
	if run.Schema != recordSchema {
		return nil, fmt.Errorf("%s is schema %d; this build reads %d", path, run.Schema, recordSchema)
	}
	return &run, nil
}

// newRun starts a record with everything that is true before any measurement is
// taken.
func newRun(ctx context.Context, settings Settings, server ServerInfo) *Run {
	return &Run{
		Schema:      recordSchema,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Server:      server,
		Host:        hostInfo(ctx),
		Settings:    settings,
	}
}
