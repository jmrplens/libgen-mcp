// series.go steps one HTTP process through more and more client addresses.
//
// The point scenarios say what a handful of callers cost. A shared deployment
// meets hundreds, and the per-caller figure those scenarios imply, multiplied
// out, puts such a deployment far beyond anything they measured. This measures
// it instead of extrapolating: one process, more addresses at each step, a
// steady phase of calls at each count, and a reading taken twice.
//
// Twice, because "what N callers cost" is two questions. The steady phase
// answers what they cost while all of them are calling, which is what a host
// has to survive; the settled reading taken once that phase stops, with a
// collection forced, answers what they cost to hold. Reporting only the first
// publishes the load as the tenancy.
//
// Two guards keep it from taking the host down. Before each step the resident
// set it would reach is estimated from the steps so far, and the rest of the
// list is skipped when that estimate exceeds the memory budget; and a step whose
// tool calls take longer than a client would wait is the last one run. Both are
// recorded, so the page can say where the series stopped and why rather than
// presenting a shorter series as the whole.

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// latencyCeiling ends a series once a step's tools/call tail crosses it.
//
// Past this a client has given up, so the next step would be measuring timeouts
// rather than the server. A variable so a test can lower it to something a
// stand-in crosses.
var latencyCeiling = 30 * time.Second

// budgetShare is how much of the host's available memory a series will plan
// into. The rest is left for everything else on the machine, including the
// measuring process itself.
const budgetShare = 0.8

// seriesPlan is one concurrency series before it has been measured.
type seriesPlan struct {
	ID string
	// Clients is the ascending list of client-address counts to step through.
	Clients []int
	// Parallel is how many calls each address keeps in flight.
	Parallel int
	// StepDuration is the length of each step's steady phase.
	StepDuration time.Duration
	// PerClientRPS is how fast one caller calls.
	//
	// The series is paced rather than driven flat out, and that is a modeling
	// decision before it is a practical one: a real caller does not spin. It is
	// also the only way the numbers mean anything here, because this server
	// limits inbound requests per client address — at the shipped ten per
	// second, a caller spinning had 4,361 of its 4,420 calls refused, and a
	// series measuring that would be measuring the limiter. Five per second is
	// a busy client and stays inside the shipped allowance, so what grows with
	// the step is the number of callers rather than the refusal rate.
	PerClientRPS float64
	// Method is what every caller drives for the length of a step.
	//
	// It is tools/list rather than tools/call, and the reason is the outbound
	// budget. A tool call reaches the catalog, every catalog request takes a
	// token from a bucket that refills at most twenty times a second, and a
	// series driving flat out exhausts it in the first step: measured, 2,758 of
	// 2,817 calls came back refused by the limiter. What that measures is the
	// limiter. tools/list is served entirely inside the process, so what it
	// measures is what this series is about — what another caller costs the
	// process — and it is also the call every client really does make on every
	// reconnect. The cost of a tool call is the matrix's question, and the
	// matrix answers it.
	Method string
	// OutboundRPS and OutboundBurst are the mirror-facing budget. A series run
	// at the shipped one request per second would measure that queue at every
	// step and nothing else.
	OutboundRPS   float64
	OutboundBurst int
}

// defaultSeriesClients is the ladder a full run steps through.
//
// It doubles rather than walking, because what the fit needs is a wide spread
// of counts and not a dense one: ten steps from one to five hundred say more
// about the slope than fifty steps from one to fifty.
func defaultSeriesClients() []int { return []int{1, 2, 5, 10, 25, 50, 100, 200, 500} }

// quickSeriesClients is the smoke ladder, short enough to watch.
func quickSeriesClients() []int { return []int{1, 2, 5} }

// runSeries measures one concurrency series.
func (r *runner) runSeries(ctx context.Context, plan seriesPlan) (SeriesScenario, error) {
	out := SeriesScenario{
		ID:            plan.ID,
		Transport:     transportHTTP,
		Method:        plan.Method,
		Parallel:      plan.Parallel,
		PerClientRPS:  plan.PerClientRPS,
		StepSeconds:   plan.StepDuration.Seconds(),
		Clients:       plan.Clients,
		OutboundRPS:   plan.OutboundRPS,
		OutboundBurst: plan.OutboundBurst,
		BudgetMiB:     round(availableMemoryMiB() * budgetShare),
	}
	if out.BudgetMiB == 0 {
		out.Notes = appendNote(out.Notes,
			"this host does not report its available memory, so no step was skipped for a budget")
	}

	pprofPort, err := freePort(ctx)
	if err != nil {
		return out, err
	}
	pprofAddr := fmt.Sprintf("127.0.0.1:%d", pprofPort)

	opts := r.seriesTargetOpts(plan, pprofAddr)
	t, err := startHTTP(ctx, opts)
	if err != nil {
		return out, err
	}
	defer t.stop()

	sam := newSampler(ctx, r.sampleInterval(), func() []int { return alivePids(ctx, t) })
	sam.start()
	defer sam.stop()

	r.stepThrough(ctx, &out, t, sam, plan, pprofAddr)
	return out, nil
}

// seriesTargetOpts builds the options the series' one process is started with.
func (r *runner) seriesTargetOpts(plan seriesPlan, pprofAddr string) targetOptions {
	opts := r.targetOpts(scenarioPlan{
		ID:            plan.ID,
		Transport:     transportHTTP,
		Clients:       1,
		Parallel:      plan.Parallel,
		OutboundRPS:   plan.OutboundRPS,
		OutboundBurst: plan.OutboundBurst,
	}, 0)
	// The profiling listener is what makes the settled reading possible: it is
	// the only way to ask this process to collect before its memory is read,
	// and without it the settled figure would be whatever the scavenger had got
	// around to. It binds loopback, which is the only thing the server accepts.
	opts.extraArgs = append(opts.extraArgs, "--pprof-addr", pprofAddr)
	return opts
}

// stepThrough runs the steps in order, stopping at the first guard that fires.
func (r *runner) stepThrough(
	ctx context.Context, out *SeriesScenario, t *target, sam *sampler, plan seriesPlan, pprofAddr string,
) {
	for i, count := range plan.Clients {
		if stop := r.budgetStop(out, count); stop != nil {
			out.Stop, out.StopReason = stop, stopBudget
			out.Skipped = plan.Clients[i:]
			return
		}
		step, err := r.measureStep(ctx, t, sam, plan, count, pprofAddr)
		if err != nil {
			out.Stop = &SeriesStop{Kind: stopFailure, NextClients: count, Error: err.Error()}
			out.StopReason = stopFailure
			out.Skipped = plan.Clients[i:]
			return
		}
		out.Steps = append(out.Steps, step)
		out.StoppedAt = count

		if step.P99Ms > float64(latencyCeiling.Milliseconds()) {
			// NextClients is the count that was not started, not the one that
			// crossed: the one that crossed already ran and is in StoppedAt.
			// Zero when the ladder had nothing left, which is not a stop at all
			// so much as a coincidence of ending.
			out.Stop = &SeriesStop{Kind: stopLatency, NextClients: nextIn(plan.Clients, i), P99Ms: step.P99Ms}
			out.StopReason = stopLatency
			out.Skipped = plan.Clients[i+1:]
			return
		}
	}
	out.StopReason = stopComplete
}

// nextIn names the count after the one at index i, zero when there is none.
func nextIn(clients []int, i int) int {
	if i+1 < len(clients) {
		return clients[i+1]
	}
	return 0
}

// budgetStop reports whether the next count is expected to cost more memory
// than the host can spare, and is nil when it is not — including when there is
// no budget or not enough steps to extrapolate from.
//
// The estimate comes from the steps already measured rather than from a
// constant, because the per-caller cost is what the series is measuring and a
// guess made before it started would be the extrapolation this exists to avoid.
func (r *runner) budgetStop(out *SeriesScenario, next int) *SeriesStop {
	if out.BudgetMiB == 0 {
		return nil
	}
	estimate := estimatePeakMiB(out.Steps, next)
	if estimate <= out.BudgetMiB {
		return nil
	}
	return &SeriesStop{Kind: stopBudget, NextClients: next, EstimateMiB: estimate}
}

// bootstrapPerClientMiB is what a caller is assumed to cost before enough steps
// have run to fit a line through.
//
// It is an order of magnitude above anything measured — 0.23 MiB per caller on
// eight cores — and that is the point: a guess used to protect a host should err
// towards refusing, not towards trying. Without it the guard did nothing until
// two steps had completed, so a ladder of 1,500000 would start its second step
// and allocate half a million callers on both sides of the socket before
// anything had an opinion about it.
const bootstrapPerClientMiB = 1.0

// estimatePeakMiB is what a step at this count is expected to reach.
//
// With two or more steps behind it the estimate is the fit through them, which
// is the measurement doing the work. With fewer it is the largest peak seen so
// far plus the bootstrap allowance, which is deliberately pessimistic.
func estimatePeakMiB(steps []SeriesStep, next int) float64 {
	if len(steps) >= 2 {
		if slope, intercept, ok := fitLine(stepCounts(steps), stepPeaks(steps)); ok {
			return round(intercept + slope*float64(next))
		}
	}
	var base float64
	for _, step := range steps {
		base = max(base, step.RSSPeakMiB)
	}
	return round(base + bootstrapPerClientMiB*float64(next))
}

// stepCounts and stepPeaks are the fit's two axes.
func stepCounts(steps []SeriesStep) []float64 {
	xs := make([]float64, len(steps))
	for i, step := range steps {
		xs[i] = float64(step.Clients)
	}
	return xs
}

// stepPeaks is the peak resident set of each step, which is what a budget is
// measured against.
func stepPeaks(steps []SeriesStep) []float64 {
	ys := make([]float64, len(steps))
	for i, step := range steps {
		ys[i] = step.RSSPeakMiB
	}
	return ys
}

// measureStep drives one client count for the step's duration and reads the
// process twice.
func (r *runner) measureStep(
	ctx context.Context, t *target, sam *sampler, plan seriesPlan, count int, pprofAddr string,
) (SeriesStep, error) {
	callers := make([]caller, count)
	for i := range callers {
		callers[i] = newHTTPCaller(t.baseURL, clientAddress(i))
	}
	defer closeAll(callers)

	sam.reset()
	cpuBefore, cpuReadable := sam.cpuSeconds()
	timings := newTimings()

	phase, cancel := context.WithTimeout(ctx, plan.StepDuration)
	defer cancel()
	driveUntilDone(phase, callers, plan, timings)

	step := SeriesStep{
		Clients:    count,
		RSSMeanMiB: mibOf(sam.meanRSS()),
		RSSPeakMiB: mibOf(sam.peakRSS()),
		Calls:      timings.total(),
	}
	for _, m := range timings.methods() {
		step.Errors += m.Errors
		step.P50Ms, step.P99Ms = m.P50Ms, m.P99Ms
	}
	if step.Calls == 0 {
		return step, fmt.Errorf("no call completed at %d clients: %s", count, timings.diagnosis())
	}
	if step.Errors > 0 {
		return step, fmt.Errorf("%d of %d calls failed at %d clients: %s",
			step.Errors, step.Calls, count, timings.diagnosis())
	}
	if cpu := cpuOver(sam, cpuBefore, cpuReadable, step.Calls); !cpu.Unreadable {
		step.CPUMsPerCall = cpu.MsPerCall
	}

	// The settled reading is taken with the load stopped and a collection
	// forced. Everything the phase allocated is still on the heap until that
	// collection runs, so a figure read a moment earlier would be the load
	// again under a different name.
	closeAll(callers)
	if heap, hErr := settledHeapMiB(ctx, pprofAddr); hErr == nil {
		step.SettledHeapMiB = heap
	} else {
		step.Notes = appendNote(step.Notes, "the settled heap could not be read: "+hErr.Error())
	}
	step.SettledRSSMiB = mibOf(sam.currentRSS())
	return step, nil
}

// driveUntilDone keeps every caller working until the phase's context ends.
//
// A step is a duration rather than a round count because the question is the
// steady state at N callers: a fixed number of rounds would take longer at every
// step, so each step would be measuring a different length of time and the peak
// resident set would grow with the schedule rather than with the load.
func driveUntilDone(ctx context.Context, callers []caller, plan seriesPlan, timings *timings) {
	interval := batchInterval(plan)
	var wg sync.WaitGroup
	for _, c := range callers {
		wg.Go(func() {
			// The first batch goes out before the first tick, so a step always
			// measures at least one. A ticker's first tick is one interval
			// away, and a step shorter than that interval would otherwise
			// report that no call completed — which reads like a broken server
			// rather than like a phase too short to contain a call.
			//
			// The error is dropped rather than returned, here and below: a call
			// cut off by the phase ending is the ordinary way this loop stops,
			// and a call that failed for a real reason is already counted by
			// timings and asserted on by the caller.
			_ = callInParallel(ctx, c, plan.Method, plan.Parallel, timings)

			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = callInParallel(ctx, c, plan.Method, plan.Parallel, timings)
				}
			}
		})
	}
	wg.Wait()
}

// batchInterval is how long one caller waits between batches, so that Parallel
// calls per batch work out to PerClientRPS calls a second.
//
// It never returns less than a millisecond: a ticker panics on a non-positive
// interval, and a plan with no rate stated is a plan to spin, which is what the
// pacing exists to prevent.
func batchInterval(plan seriesPlan) time.Duration {
	parallel := max(plan.Parallel, 1)
	if plan.PerClientRPS <= 0 {
		return time.Millisecond
	}
	interval := time.Duration(float64(parallel) / plan.PerClientRPS * float64(time.Second))
	return max(interval, time.Millisecond)
}

// heapAlloc matches the live-heap line the text heap profile ends with.
var heapAlloc = regexp.MustCompile(`# HeapAlloc = (\d+)`)

// settledHeapMiB asks the measured process to collect and reports the live heap
// that survived, in mebibytes.
//
// It reads the profiling listener's text heap profile rather than its protobuf
// one: `debug=1` ends the document with the runtime's own MemStats, so one line
// of it answers the question with no profile decoder and no dependency. `gc=1`
// is what makes the answer mean anything — without it the figure includes
// everything the phase allocated and has not been collected yet.
func settledHeapMiB(ctx context.Context, pprofAddr string) (float64, error) {
	url := "http://" + pprofAddr + "/debug/pprof/heap?gc=1&debug=1" // NOSONAR: a loopback profiler this command started
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("build the heap request: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("read the heap profile: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("read the heap profile: %w", err)
	}
	return parseHeapAlloc(string(body))
}

// parseHeapAlloc pulls the live heap out of a text heap profile.
func parseHeapAlloc(profile string) (float64, error) {
	match := heapAlloc.FindStringSubmatch(profile)
	if match == nil {
		return 0, fmt.Errorf("no HeapAlloc line in %q", truncate(strings.TrimSpace(profile), 120))
	}
	bytes, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse HeapAlloc %q: %w", match[1], err)
	}
	return mibOf(bytes), nil
}
