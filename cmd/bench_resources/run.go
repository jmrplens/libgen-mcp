// run.go measures one point of the matrix: start the server, watch what it
// weighs, drive it, and write down what happened.
//
// The order the phases run in is the measurement. Idle is read before anything
// is asked, because a resident set read after the first call has already paid
// for the first call. Startup is read once, because it is paid once. The steady
// phase is the only one whose numbers divide by a call count, and the sampler
// is reset before it so the peak it reports belongs to that phase rather than
// to the process's whole life.

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// runner holds what every scenario needs and nothing a scenario decides.
type runner struct {
	binary    string
	catalog   *catalog
	collector *collector
	settings  Settings
	workDir   string
	verbose   bool
	// env is added to every target this runner starts, after the per-scenario
	// settings.
	//
	// It is the seam that lets the measurement path be exercised against a
	// stand-in process rather than against ./cmd/server: the phases, the
	// sampler's window and the per-call arithmetic are this command's, and a
	// test of them should not depend on a twenty-second build or on the
	// server's own behavior. A measuring run leaves it empty.
	env map[string]string
}

// measure runs one scenario end to end.
func (r *runner) measure(ctx context.Context, plan scenarioPlan) (Scenario, error) {
	if plan.Transport == transportStdio {
		return r.measureStdio(ctx, plan)
	}
	return r.measureHTTP(ctx, plan)
}

// scenarioFrom fills in what a scenario knows before it is measured.
func scenarioFrom(plan scenarioPlan, rounds int) Scenario {
	return Scenario{
		ID:            plan.ID,
		Transport:     plan.Transport,
		Telemetry:     plan.Telemetry,
		Clients:       plan.Clients,
		Parallel:      plan.Parallel,
		Rounds:        rounds,
		OutboundRPS:   plan.OutboundRPS,
		OutboundBurst: plan.OutboundBurst,
	}
}

// targetOpts builds the options one scenario's processes are started with.
func (r *runner) targetOpts(plan scenarioPlan, index int) targetOptions {
	dir := filepath.Join(r.workDir, fmt.Sprintf("%s-%d", plan.ID, index))
	_ = os.MkdirAll(dir, 0o750)
	opts := targetOptions{
		binary:      r.binary,
		mirror:      r.catalog.url,
		downloadDir: dir,
		extraEnv: map[string]string{
			"LIBGEN_MCP_RATE_RPS":   strconv.FormatFloat(plan.OutboundRPS, 'f', -1, 64),
			"LIBGEN_MCP_RATE_BURST": strconv.Itoa(plan.OutboundBurst),
		},
	}
	maps.Copy(opts.extraEnv, r.env)
	if plan.Telemetry {
		opts.telemetry = r.collector.url
	}
	return opts
}

// measureHTTP measures one HTTP process serving N distinct client addresses.
func (r *runner) measureHTTP(ctx context.Context, plan scenarioPlan) (Scenario, error) {
	out := scenarioFrom(plan, r.settings.Rounds)

	started := time.Now()
	t, err := startHTTP(ctx, r.targetOpts(plan, 0))
	if err != nil {
		return out, err
	}
	defer t.stop()
	out.Startup.ProcessReadyMs = msSince(started)

	sam := newSampler(ctx, r.sampleInterval(), func() []int { return alivePids(ctx, t) })
	sam.start()
	defer sam.stop()

	out.Memory.IdleMiB = mibOf(sam.peakRSS())

	callers := make([]caller, plan.Clients)
	for i := range callers {
		callers[i] = newHTTPCaller(t.baseURL, clientAddress(i))
	}
	defer closeAll(callers)

	if sErr := r.measureStartupAndSurface(ctx, &out, callers[0]); sErr != nil {
		return out, sErr
	}
	exportsBefore := r.collector.requestCount()
	if sErr := r.measureSteady(ctx, &out, callers, sam); sErr != nil {
		return out, sErr
	}
	if plan.Telemetry {
		// What reached the collector is recorded rather than asserted. The
		// exporter batches on its own schedule, so a short scenario can
		// legitimately end with nothing sent — and the cost of switching
		// telemetry on is the machinery either way. What must not happen is a
		// reader assuming exports flowed when the record cannot say they did.
		out.Notes = appendNote(out.Notes,
			fmt.Sprintf("%d OTLP exports reached the collector during this scenario",
				r.collector.requestCount()-exportsBefore))
	}
	if info, infoErr := serverInfo(ctx, t); infoErr == nil {
		// Both halves, because a version alone cannot tell two builds of the
		// same tag apart and this record is meant to be compared with a later
		// one.
		out.Notes = appendNote(out.Notes, "measured build "+info.Version+" ("+info.Commit+")")
	}
	return out, nil
}

// measureStdio measures N server processes, one per client, which is what a
// client is on this transport.
func (r *runner) measureStdio(ctx context.Context, plan scenarioPlan) (Scenario, error) {
	out := scenarioFrom(plan, r.settings.Rounds)

	targets := make([]*target, 0, plan.Clients)
	defer func() {
		for _, t := range targets {
			t.stop()
		}
	}()

	// Ready means the same thing on both transports: the process will answer.
	// On HTTP that is /health; here it is the handshake the stdio binding
	// requires, because a client cannot call anything before it. Timing the
	// exec instead would report half a millisecond and compare a fork against
	// a served request.
	started := time.Now()
	callers := make([]caller, 0, plan.Clients)
	for i := range plan.Clients {
		t, err := startStdio(ctx, r.targetOpts(plan, i))
		if err != nil {
			return out, err
		}
		targets = append(targets, t)
		c, err := newStdioCaller(ctx, t)
		if err != nil {
			return out, fmt.Errorf("handshake: %w", err)
		}
		callers = append(callers, c)
	}
	out.Startup.ProcessReadyMs = round(msSince(started) / float64(plan.Clients))

	sam := newSampler(ctx, r.sampleInterval(), func() []int { return alivePidsOf(ctx, targets) })
	sam.start()
	defer sam.stop()
	out.Memory.IdleMiB = mibOf(sam.peakRSS())

	if err := r.measureStartupAndSurface(ctx, &out, callers[0]); err != nil {
		return out, err
	}
	err := r.measureSteady(ctx, &out, callers, sam)
	return out, err
}

// measureStartupAndSurface times the first tools/list and one after it, and
// records how big the catalog a client downloads actually is.
func (r *runner) measureStartupAndSurface(ctx context.Context, out *Scenario, first caller) error {
	cold, err := first.call(ctx, methodToolsList, map[string]any{})
	if err != nil {
		return fmt.Errorf("first %s: %w", methodToolsList, err)
	}
	if sErr := served(cold); sErr != nil {
		return fmt.Errorf("first %s: %w", methodToolsList, sErr)
	}
	out.Startup.FirstListMs = round(float64(cold.Duration.Microseconds()) / 1000)
	out.ListBytes = cold.Bytes

	warm, err := first.call(ctx, methodToolsList, map[string]any{})
	if err != nil {
		return fmt.Errorf("warm %s: %w", methodToolsList, err)
	}
	if sErr := served(warm); sErr != nil {
		return fmt.Errorf("warm %s: %w", methodToolsList, sErr)
	}
	out.Startup.WarmListMs = round(float64(warm.Duration.Microseconds()) / 1000)
	return nil
}

// served reports why a completed call was not service, and nil when it was.
//
// One check for every measured call, because there are three ways to come back
// quickly without having done the work: the envelope carries an error, the tool
// inside it reports one, or the server refused the call for rate. All three
// take a fraction of the time serving does, so any of them averaged into a
// timing makes the server look faster the less of its job it did.
func served(res callResult) error {
	switch {
	case res.Err != nil:
		return fmt.Errorf("the server answered %s", errText(res.Err))
	case res.ToolError:
		return fmt.Errorf("the tool reported a failure: %s", res.ToolErrText)
	case res.Throttled:
		return errors.New("the server refused the call for rate (HTTP 429)")
	default:
		return nil
	}
}

// measureSteady drives every client through every method for the configured
// number of rounds, with the sampler watching.
func (r *runner) measureSteady(ctx context.Context, out *Scenario, callers []caller, sam *sampler) error {
	sam.reset()
	cpuBefore, cpuReadable := sam.cpuSeconds()
	catalogBefore := r.catalog.requestCount()

	timings := newTimings()
	var wg sync.WaitGroup
	errs := make([]error, len(callers))
	for i, c := range callers {
		wg.Go(func() {
			errs[i] = driveClient(ctx, c, out.Parallel, out.Rounds, timings)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	out.Memory.PeakMiB = mibOf(sam.peakRSS())
	out.Memory.MeanMiB = mibOf(sam.meanRSS())
	out.Latency = timings.methods()
	out.CPU = cpuOver(sam, cpuBefore, cpuReadable, timings.total())
	if failures := sam.sampleFailures(); failures > 0 {
		out.Notes = appendNote(out.Notes,
			fmt.Sprintf("%d memory samples could not be taken on %s", failures, runtimeGOOS))
	}
	// What the searches actually did is recorded, not assumed. A tool call that
	// answered without reaching the stand-in catalog measured something other
	// than a search, and the count is the only thing that can say so.
	out.Notes = appendNote(out.Notes,
		fmt.Sprintf("%d requests reached the stand-in catalog", r.catalog.requestCount()-catalogBefore))
	return assertServed(out, timings.diagnosis())
}

// assertServed refuses a scenario whose calls did not work.
//
// A failing call is fast: a search that cannot reach its catalog answers in
// under a millisecond, and a run that published that would be reporting the
// cost of an error path as the cost of a search. There is no honest way to
// average the two, so a method that failed at all stops the run and says which
// one and how often.
func assertServed(out *Scenario, diagnosis string) error {
	for _, m := range out.Latency {
		if m.Errors > 0 {
			return fmt.Errorf("%d of %d %s calls failed; the timings would be an error path: %s",
				m.Errors, m.Calls, m.Method, diagnosis)
		}
		if m.Calls == 0 {
			return fmt.Errorf("no %s call completed", m.Method)
		}
	}
	return nil
}

// driveClient runs one client's share of the work: every method, every round,
// with Parallel calls in flight at a time.
func driveClient(ctx context.Context, c caller, parallel, rounds int, timings *timings) error {
	for range rounds {
		for _, method := range []string{methodToolsList, methodPromptsList, methodToolsCall} {
			if err := callInParallel(ctx, c, method, parallel, timings); err != nil {
				return err
			}
		}
	}
	return nil
}

// callInParallel makes n calls of one method at once and records each.
func callInParallel(ctx context.Context, c caller, method string, n int, timings *timings) error {
	if n < 1 {
		n = 1
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			res, err := c.call(ctx, method, paramsFor(method))
			if err != nil {
				errs[i] = err
				return
			}
			timings.record(method, res)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// paramsFor is what each measured method is called with.
func paramsFor(method string) any {
	if method == methodToolsCall {
		return map[string]any{"name": "search", "arguments": searchArguments}
	}
	return map[string]any{}
}

// timings collects every call's result, keyed by method.
type timings struct {
	mu       sync.Mutex
	byMethod map[string][]time.Duration
	errors   map[string]int
	throttle map[string]int
	order    []string
	// said is the first failure's own words, kept so a run that stops names the
	// diagnosis rather than only the count.
	said string
}

// errText renders a JSON-RPC error for a message, empty when there was none.
func errText(err *rpcError) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("JSON-RPC %d: %s", err.Code, err.Message)
}

// firstNonEmpty returns the first of its arguments that says something.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// diagnosis reports the first failure's words.
func (t *timings) diagnosis() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.said
}

// newTimings starts an empty collection.
func newTimings() *timings {
	return &timings{
		byMethod: map[string][]time.Duration{},
		errors:   map[string]int{},
		throttle: map[string]int{},
	}
}

// record folds one call into the collection.
func (t *timings) record(method string, res callResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, seen := t.byMethod[method]; !seen {
		t.order = append(t.order, method)
	}
	t.byMethod[method] = append(t.byMethod[method], res.Duration)
	if failure := served(res); failure != nil {
		t.errors[method]++
		if t.said == "" {
			t.said = failure.Error()
		}
	}
	if res.Throttled {
		t.throttle[method]++
	}
}

// total reports how many calls were recorded across every method.
func (t *timings) total() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, samples := range t.byMethod {
		n += len(samples)
	}
	return n
}

// methods renders the collection as the record's latency rows, in the order the
// methods were first called so two runs of the same matrix agree.
func (t *timings) methods() []MethodLatency {
	t.mu.Lock()
	defer t.mu.Unlock()
	rows := make([]MethodLatency, 0, len(t.order))
	for _, method := range t.order {
		samples := t.byMethod[method]
		rows = append(rows, MethodLatency{
			Method: method,
			Calls:  len(samples),
			P50Ms:  percentile(samples, 0.50),
			P99Ms:  percentile(samples, 0.99),
			MaxMs:  percentile(samples, 1),
			Errors: t.errors[method],
		})
	}
	return rows
}

// cpuOver turns the processor time consumed during a phase into the record's
// CPU block.
//
// A platform that cannot report processor time says so rather than publishing
// zero: zero milliseconds per call is a claim, and "this kernel would not tell
// us" is a fact.
func cpuOver(sam *sampler, before float64, readable bool, calls int) CPU {
	after, stillReadable := sam.cpuSeconds()
	if !readable || !stillReadable {
		return CPU{Calls: calls, Unreadable: true}
	}
	seconds := after - before
	if seconds < 0 {
		seconds = 0
	}
	out := CPU{Seconds: round(seconds), Calls: calls}
	if calls > 0 {
		out.MsPerCall = round(seconds * 1000 / float64(calls))
	}
	return out
}

// sampleInterval is how often the resident set is read.
func (r *runner) sampleInterval() time.Duration {
	return time.Duration(r.settings.SampleIntervalMs) * time.Millisecond
}

// alivePids names the one process of an HTTP scenario, or none once it has
// exited.
func alivePids(ctx context.Context, t *target) []int {
	if pid := t.pid(); pid > 0 && processAlive(ctx, pid) {
		return []int{pid}
	}
	return nil
}

// alivePidsOf names every process of a stdio scenario that is still running.
func alivePidsOf(ctx context.Context, targets []*target) []int {
	pids := make([]int, 0, len(targets))
	for _, t := range targets {
		if pid := t.pid(); pid > 0 && processAlive(ctx, pid) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// closeAll releases every caller of a scenario.
func closeAll(callers []caller) {
	for _, c := range callers {
		c.close()
	}
}

// appendNote adds a note, keeping the slice nil until there is one so the
// record omits the field entirely rather than carrying an empty list.
func appendNote(notes []string, note string) []string {
	return append(notes, note)
}

// msSince is the elapsed milliseconds at the record's precision.
func msSince(started time.Time) float64 {
	return round(float64(time.Since(started).Microseconds()) / 1000)
}
