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
	index := int(q * float64(len(sorted)))
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
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
