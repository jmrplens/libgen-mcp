// matrix.go is the plan: which points get measured, and why each one is worth
// a run of its own.
//
// The matrix is small on purpose. Every point costs wall-clock time on a
// machine somebody is waiting at, and a point nobody can name a question for is
// a number that will be published, compared and argued about without ever
// having been asked.

package main

import (
	"fmt"
	"strings"
)

// The two transports, spelled the way the record and the flags do.
const (
	transportStdio = "stdio"
	transportHTTP  = "http"
)

// scenarioPlan is one point of the matrix before it has been measured.
type scenarioPlan struct {
	ID        string
	Transport string
	Telemetry bool
	// Clients is distinct client addresses on HTTP, server processes on stdio.
	Clients int
	// Parallel is requests each client keeps in flight.
	Parallel int
	// OutboundRPS and OutboundBurst are the mirror-facing budget the scenario
	// runs with, LIBGEN_MCP_RATE_RPS and LIBGEN_MCP_RATE_BURST.
	//
	// They are part of the plan rather than a global because the shipped
	// default is one request per second, burst one, and that single token is
	// what every tool call queues behind: measured here, sixteen searches in
	// flight took fifteen seconds to drain, which is the outbound bucket and
	// nothing about this server's own cost. A resource benchmark has to open
	// that valve to see anything else, and then close it again in one scenario
	// so the record still says what a deployment out of the box does.
	OutboundRPS   float64
	OutboundBurst int
	// Why names the question this point answers, for the terminal and for
	// anyone deciding whether to add another.
	Why string
}

// The outbound budget the measuring scenarios open the valve to, and the one
// the server ships with.
//
// Twenty is the ceiling, not a choice: config refuses LIBGEN_MCP_RATE_RPS
// outside (0, 20] and LIBGEN_MCP_RATE_BURST outside [1, 100], because the
// budget exists to be polite to somebody else's mirror rather than to be tuned
// for throughput. So the widest-open scenario is still bounded, and a reading
// taken here is a floor on the queue rather than a measurement with no queue at
// all.
const (
	benchOutboundRPS   = 20
	benchOutboundBurst = 100
	shippedOutboundRPS = 1
)

// fullMatrix is what a complete run measures.
func fullMatrix() []scenarioPlan {
	return withBenchBudget([]scenarioPlan{
		{
			ID: "stdio-1", Transport: transportStdio, Clients: 1, Parallel: 1,
			Why: "one client on the primary transport: the floor, and what a desktop client pays",
		},
		{
			ID: "stdio-8", Transport: transportStdio, Clients: 8, Parallel: 1,
			Why: "eight stdio clients are eight processes, so this is what a workstation running several clients costs",
		},
		{
			ID: "http-1", Transport: transportHTTP, Clients: 1, Parallel: 1,
			Why: "one caller against a remote deployment, for contrast with the same work over a pipe",
		},
		{
			ID: "http-8", Transport: transportHTTP, Clients: 8, Parallel: 2,
			Why: "eight addresses with two calls each in flight: one process serving a small shared deployment",
		},
		{
			ID: "http-8-otel", Transport: transportHTTP, Clients: 8, Parallel: 2, Telemetry: true,
			Why: "the same, exporting: what the sixteen OpenTelemetry modules cost when they are switched on",
		},
		{
			ID: "http-8-shipped-rate", Transport: transportHTTP, Clients: 8, Parallel: 2,
			OutboundRPS: shippedOutboundRPS, OutboundBurst: 1,
			Why: "the same again with the shipped outbound budget of one request per second: what a deployment does out of the box",
		},
	})
}

// quickMatrix is the smoke run: enough points to exercise both transports and
// the telemetry switch, few enough to finish while somebody watches.
func quickMatrix() []scenarioPlan {
	return withBenchBudget([]scenarioPlan{
		{
			ID: "stdio-1", Transport: transportStdio, Clients: 1, Parallel: 1,
			Why: "one client on the primary transport",
		},
		{
			ID: "http-1", Transport: transportHTTP, Clients: 1, Parallel: 1,
			Why: "one caller against a remote deployment",
		},
	})
}

// withBenchBudget opens the outbound valve on every scenario that did not ask
// for a budget of its own, so a plan only states the number when the number is
// the point.
func withBenchBudget(plans []scenarioPlan) []scenarioPlan {
	for i := range plans {
		if plans[i].OutboundRPS == 0 {
			plans[i].OutboundRPS = benchOutboundRPS
			plans[i].OutboundBurst = benchOutboundBurst
		}
	}
	return plans
}

// selectScenarios narrows a matrix to the ids named, in the matrix's own order.
//
// An id that matches nothing is an error rather than an empty run: a typo in
// -scenarios would otherwise write a record with no scenarios in it, which
// reads exactly like a run where everything was skipped.
func selectScenarios(plans []scenarioPlan, ids string) ([]scenarioPlan, error) {
	if strings.TrimSpace(ids) == "" {
		return plans, nil
	}
	wanted := map[string]bool{}
	for id := range strings.SplitSeq(ids, ",") {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			wanted[trimmed] = true
		}
	}
	var chosen []scenarioPlan
	for _, plan := range plans {
		if wanted[plan.ID] {
			chosen = append(chosen, plan)
			delete(wanted, plan.ID)
		}
	}
	if len(wanted) > 0 {
		return nil, fmt.Errorf("no scenario named %s", strings.Join(sortedKeys(wanted), ", "))
	}
	return chosen, nil
}

// sortedKeys lists a set in a stable order, so an error names the same ids in
// the same sequence on every run.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	// Small sets, and the only property that matters is that two runs agree.
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// clientAddress is the address the nth HTTP client presents.
//
// They are drawn from 198.18.0.0/15, which RFC 2544 reserves for benchmark
// testing of network devices — which is what this is. Nothing routes there, so
// a header that escaped into a log names an address that cannot belong to a
// real person, and the block is large enough that every client of a series gets
// one of its own.
//
// That second property is not a detail. The first version of this took a /24
// and wrapped at 254, so a step measuring five hundred callers was really
// measuring two hundred and fifty-four of them twice — and the per-caller slope
// fitted through it was a number about the wrapping.
func clientAddress(n int) string {
	const addresses = 1 << 17 // a /15
	n %= addresses
	return fmt.Sprintf("198.%d.%d.%d", 18+n>>16, n>>8&0xff, n&0xff)
}
