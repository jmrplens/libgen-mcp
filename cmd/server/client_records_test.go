package main

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jmrplens/libgen-mcp/v2/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/v2/internal/toolutil"
	"github.com/jmrplens/libgen-mcp/v2/internal/transport"
)

// testRecords builds a table with bounds small enough to reach in a test and a
// clock the test moves itself.
func testRecords(t *testing.T, capacity int) (*clientRecords, func(time.Duration)) {
	t.Helper()

	now := time.Now()
	records := newClientRecords(func() *toolutil.RateLimiter { return toolutil.NewRateLimiter(1, 1) }, chargePolicy{})
	records.maxRecords = capacity
	records.ttl = time.Minute
	records.sweepEvery = 10 * time.Second
	records.now = func() time.Time { return now }

	return records, func(d time.Duration) { now = now.Add(d) }
}

// track begins and immediately ends a request from address, which is what an
// ordinary finished request leaves behind.
func track(t *testing.T, c *clientRecords, address string) {
	t.Helper()

	_, end := c.begin(t.Context(), address)
	end()
}

// TestARecordIsKeptPerAddress is the premise everything else rests on: two
// callers get two buckets, and the same caller gets the same one.
func TestARecordIsKeptPerAddress(t *testing.T) {
	records, _ := testRecords(t, 8)

	first, endFirst := records.begin(t.Context(), "203.0.113.7")
	defer endFirst()
	again, endAgain := records.begin(t.Context(), "203.0.113.7")
	defer endAgain()
	other, endOther := records.begin(t.Context(), "198.51.100.23")
	defer endOther()

	if first != again {
		t.Error("the same address was given two records, so its budget resets on every request")
	}
	if first == other {
		t.Error("two addresses share one record, which is one budget for both of them")
	}
	if records.len() != 2 {
		t.Errorf("the table holds %d records, want 2", records.len())
	}
}

// TestANewAddressEvictsTheLeastRecentlyUsedRatherThanBeingRefused is the rule
// this table exists to get right, and the one the sibling project deliberately
// inverts for a different table.
//
// Its table holds authentication evidence, where evicting a record is how a
// block would be cleared on demand, so refusing a new key is correct there. A
// rate bucket is a budget, and this server has no second budget behind it:
// refuse at the cap and a few thousand addresses lock out every new legitimate
// client, and refusing to *track* at the cap makes the next address unlimited,
// which is the bypass. Evicting costs at most one full burst.
func TestANewAddressEvictsTheLeastRecentlyUsedRatherThanBeingRefused(t *testing.T) {
	records, advance := testRecords(t, 3)

	track(t, records, "a")
	advance(time.Second)
	track(t, records, "b")
	advance(time.Second)
	track(t, records, "c")
	advance(time.Second)

	rec, end := records.begin(t.Context(), "d")
	defer end()
	if rec == nil {
		t.Fatal("a new address at the cap got no record at all, so it is unlimited")
	}
	if records.len() != 3 {
		t.Errorf("the table holds %d records, want the cap of 3", records.len())
	}
	if _, held := records.byAddress["a"]; held {
		t.Error("the least recently used record survived; something else was evicted")
	}
	if _, held := records.byAddress["d"]; !held {
		t.Error("the new address was not admitted")
	}
}

// TestABusyRecordIsSkippedInFavorOfAnIdleOne keeps a caller mid-call from losing
// the bucket that is bounding them, which is precisely when losing it matters.
func TestABusyRecordIsSkippedInFavorOfAnIdleOne(t *testing.T) {
	records, advance := testRecords(t, 3)

	// The oldest record, and the one with work in flight.
	_, endBusy := records.begin(t.Context(), "busy")
	defer endBusy()
	advance(time.Second)
	track(t, records, "idle")
	advance(time.Second)
	track(t, records, "newer")
	advance(time.Second)

	_, end := records.begin(t.Context(), "arriving")
	defer end()

	if _, held := records.byAddress["busy"]; !held {
		t.Error("the record with a request in flight was evicted although an idle one was older")
	}
	if _, held := records.byAddress["idle"]; held {
		t.Error("the idle record survived; the eviction did not prefer it")
	}
}

// TestEveryRecordBusyStillEvicts is the other half of that rule, and the one
// that keeps the table bounded.
//
// A record is only ever skipped in favor of another record, never in favor of
// growing past the cap. A bound that yields under pressure is not a bound, and
// the pressure here is a caller's to apply.
func TestEveryRecordBusyStillEvicts(t *testing.T) {
	records, advance := testRecords(t, 3)

	for _, address := range []string{"a", "b", "c"} {
		_, end := records.begin(t.Context(), address)
		defer end()
		advance(time.Second)
	}

	_, end := records.begin(t.Context(), "d")
	defer end()

	if records.len() != 3 {
		t.Errorf("the table holds %d records with every one busy, want the cap of 3", records.len())
	}
	if _, held := records.byAddress["a"]; held {
		t.Error("the oldest busy record survived, so the table grew past its cap or refused the new key")
	}
}

// TestLapsedRecordsAreSweptBeforeAnythingIsEvicted is what makes the cap a
// ceiling on live keys rather than on everything ever seen. Without it a
// deployment that has served a few thousand addresses over a day starts evicting
// live ones to make room.
func TestLapsedRecordsAreSweptBeforeAnythingIsEvicted(t *testing.T) {
	records, advance := testRecords(t, 3)

	track(t, records, "old-1")
	track(t, records, "old-2")
	track(t, records, "recent")
	advance(2 * time.Minute)    // past the TTL for all three
	track(t, records, "recent") // refreshes only this one
	advance(time.Second)

	_, end := records.begin(t.Context(), "arriving")
	defer end()

	if _, held := records.byAddress["recent"]; !held {
		t.Error("the live record was evicted although two lapsed ones were there to sweep")
	}
	if records.len() != 2 {
		t.Errorf("the table holds %d records, want the swept pair gone and two live ones", records.len())
	}
}

// TestABusyRecordIsNotSwept covers the record whose clock says lapsed and whose
// caller is still mid-call — a download can legitimately outlive the TTL.
func TestABusyRecordIsNotSwept(t *testing.T) {
	records, advance := testRecords(t, 3)

	_, endBusy := records.begin(t.Context(), "downloading")
	defer endBusy()
	track(t, records, "old-1")
	track(t, records, "old-2")
	advance(2 * time.Minute)

	_, end := records.begin(t.Context(), "arriving")
	defer end()

	if _, held := records.byAddress["downloading"]; !held {
		t.Error("a record with a request in flight was swept for being old")
	}
}

// TestTheSweepIsRateLimited keeps the cap from becoming its own amplifier.
//
// Sweeping on every admission at the cap costs a full pass over the ceiling per
// new address, and the rate of new addresses is a caller's to choose. The window
// is what bounds that; within it, admissions evict instead.
func TestTheSweepIsRateLimited(t *testing.T) {
	records, advance := testRecords(t, 2)

	track(t, records, "a")
	track(t, records, "b")
	advance(2 * time.Minute) // both lapsed, and past the sweep window

	// The first admission at the cap sweeps, which clears both.
	_, endFirst := records.begin(t.Context(), "c")
	defer endFirst()
	if records.len() != 1 {
		t.Fatalf("after the first sweep the table holds %d records, want 1", records.len())
	}
	swept := records.lastSweep

	// A second admission moments later must not sweep again.
	advance(time.Second)
	track(t, records, "d")
	_, endThird := records.begin(t.Context(), "e")
	defer endThird()

	if !records.lastSweep.Equal(swept) {
		t.Error("the sweep ran twice inside its own window, so each new address costs a full pass")
	}
	if records.len() != 2 {
		t.Errorf("the table holds %d records, want the cap of 2", records.len())
	}
}

// TestTheTableExistsForEveryHTTPDeployment pins what it is for.
//
// It outlives the rate limit being off, because the in-flight ceiling is keyed
// on the same record: a deployment whose limiter is off still bounds how many
// files one caller is moving. With the limit off the records simply carry no
// bucket, which the limiter reads as disabled. stdio gets no table at all —
// there is one caller, nobody to be fair to, and the download semaphore is
// already theirs alone.
func TestTheTableExistsForEveryHTTPDeployment(t *testing.T) {
	if got := newClientRecordsFor(false, rateLimitDecision{rps: 10, burst: 40}, chargePolicy{}); got != nil {
		t.Error("a table was built for a deployment that serves no HTTP")
	}

	t.Run("with the limit on", func(t *testing.T) {
		records := newClientRecordsFor(true, rateLimitDecision{rps: 10, burst: 40}, chargePolicy{})
		rec, end := records.begin(t.Context(), "203.0.113.7")
		defer end()
		if rec.limiter == nil {
			t.Error("the record carries no bucket, so the limit is on and meters nothing")
		}
	})

	t.Run("with the limit off", func(t *testing.T) {
		records := newClientRecordsFor(true, rateLimitDecision{off: true, reason: "test"}, chargePolicy{})
		if records == nil {
			t.Fatal("no table at all, so the in-flight ceiling has nothing to count on")
		}
		rec, end := records.begin(t.Context(), "203.0.113.7")
		defer end()
		if rec.limiter != nil {
			t.Error("the record carries a bucket although the limit is off")
		}
	})
}

// TestBusyEvictionIsCountedOnlyOnTheFallbackPath is the instrument that tells a
// thrashing table from a quiet one.
//
// The LRU drops an idle record when it can and a busy one only when it cannot,
// and the second case is the one that says something is wrong: the table is full
// of callers with work in flight. Counting it on both paths, or on neither,
// makes a table under a flood of spoofed addresses look exactly like a table
// nobody is touching.
func TestBusyEvictionIsCountedOnlyOnTheFallbackPath(t *testing.T) {
	records := newClientRecords(func() *toolutil.RateLimiter { return nil }, chargePolicy{})
	records.maxRecords = 2

	t.Run("an idle record is dropped without the busy count", func(t *testing.T) {
		got := collectClientRecordMetrics(t, func() {
			records.mu.Lock()
			records.byAddress = map[string]*clientRecord{
				"a": {lastSeen: time.Now().Add(-time.Minute)},
				"b": {lastSeen: time.Now()},
			}
			evicted := records.evictLocked(t.Context())
			records.mu.Unlock()
			if !evicted {
				t.Fatal("nothing was evicted")
			}
		})
		if got.busy != 0 {
			t.Errorf("a busy eviction was counted for an idle record: %+v", got)
		}
		if got.evictions["size_pressure"] != 1 {
			t.Errorf("evictions = %v, want one under size pressure", got.evictions)
		}
	})

	t.Run("a busy record is dropped with the count", func(t *testing.T) {
		got := collectClientRecordMetrics(t, func() {
			records.mu.Lock()
			busy := &clientRecord{lastSeen: time.Now()}
			busy.inFlight = 1
			records.byAddress = map[string]*clientRecord{"a": busy}
			evicted := records.evictLocked(t.Context())
			records.mu.Unlock()
			if !evicted {
				t.Fatal("nothing was evicted, so the fallback path did not run")
			}
		})
		if got.busy != 1 {
			t.Errorf("busy evictions = %d, want one: a table with nothing idle to drop is the case worth seeing", got.busy)
		}
	})
}

// TestASweptRecordIsCountedAsLapsedRatherThanAsPressure keeps the two ways a
// record leaves the table distinguishable.
//
// They ask for opposite changes: a table shedding lapsed records is the TTL doing
// its job, and one shedding records under pressure is a cap that is too small for
// how many callers this deployment actually has. Recorded under one reason — or
// under each other's — the operator reads the wrong one and tunes the wrong knob.
func TestASweptRecordIsCountedAsLapsedRatherThanAsPressure(t *testing.T) {
	records, advance := testRecords(t, 2)

	got := collectClientRecordMetrics(t, func() {
		track(t, records, "old-1")
		track(t, records, "old-2")
		advance(2 * time.Minute) // past the TTL for both, and past the sweep window

		// The admission at the cap sweeps first, and the sweep clears the table,
		// so nothing here is evicted under pressure.
		_, end := records.begin(t.Context(), "arriving")
		defer end()
	})

	if got.evictions["lapsed"] != 2 || got.evictions["size_pressure"] != 0 {
		t.Errorf("evictions = %v, want two lapsed and nothing under pressure", got.evictions)
	}
	if got.busy != 0 {
		t.Errorf("busy evictions = %d, want none: every record was idle", got.busy)
	}
}

// clientRecordMetrics is what one collection saw about the table.
type clientRecordMetrics struct {
	evictions map[string]int64
	busy      int64
	entries   int64
	capacity  int64
}

// collectClientRecordMetrics runs work against a fresh meter provider and reads
// back what the table reported.
func collectClientRecordMetrics(t *testing.T, work func()) clientRecordMetrics {
	t.Helper()

	reader := recordingMeterProvider(t)
	work()

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatalf("collecting: %v", err)
	}
	got := clientRecordMetrics{evictions: map[string]int64{}}
	for _, scope := range metrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			got.read(m)
		}
	}
	return got
}

// recordingMeterProvider installs a provider this test can read back from, and
// undoes both halves afterwards.
//
// Unregistering matters as much as restoring the provider: a callback holds the
// table it reads, so leaving it registered has every later test's collection
// reporting on this test's table.
func recordingMeterProvider(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		mcpotel.UnobserveAll()
		otel.SetMeterProvider(previous)
	})
	return reader
}

// read folds one collected instrument into what the test asserts on.
func (c *clientRecordMetrics) read(m metricdata.Metrics) {
	switch m.Name {
	case mcpotel.InstrumentClientRecordsEvictions:
		for _, point := range counterPoints(m) {
			reason, _ := point.Attributes.Value("reason")
			c.evictions[reason.String()] += point.Value
		}
	case mcpotel.InstrumentClientRecordsBusyEviction:
		for _, point := range counterPoints(m) {
			c.busy += point.Value
		}
	case mcpotel.InstrumentClientRecordsEntries:
		c.entries = soleGaugeValue(m)
	case mcpotel.InstrumentClientRecordsCapacity:
		c.capacity = soleGaugeValue(m)
	}
}

// counterPoints reads a counter's points, or none when the instrument turned out
// to be something else.
func counterPoints(m metricdata.Metrics) []metricdata.DataPoint[int64] {
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return nil
	}
	return sum.DataPoints
}

// soleGaugeValue reads the one point a resource gauge carries.
//
// These gauges have no attributes, so a second point would mean two resources
// answering under one name — which is the registration leak this asserts is not
// happening, and reads as zero rather than as an arbitrary one of them.
func soleGaugeValue(m metricdata.Metrics) int64 {
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(gauge.DataPoints) != 1 {
		return 0
	}
	return gauge.DataPoints[0].Value
}

// TestServingPublishesTheTable is the line in run that turns all of this on, and
// the one nothing else would notice the loss of.
//
// Every assertion above drives observe directly, so each of them passes against a
// binary that never calls it — the table would then be instrumented in the test
// binary and invisible in the deployment, which is the only place it matters. So
// this one starts the server the way main does and reads the gauge back.
func TestServingPublishesTheTable(t *testing.T) {
	records := newClientRecordsFor(true, rateLimitDecision{rps: 10, burst: 40}, chargePolicy{})

	got := collectClientRecordMetrics(t, func() {
		awaitReturn(t, func() {
			_ = run(canceledContext(),
				listenSpec{addr: "127.0.0.1:0", records: records},
				transport.DefaultOptions(),
				transportDecision{HTTP: true, Addr: "127.0.0.1:0"})
		})
	})

	if got.capacity != int64(records.maxRecords) {
		t.Errorf("client-record capacity = %d, want the table's own %d: serving did not publish it",
			got.capacity, records.maxRecords)
	}
}

// TestClientRecordsGaugeFollowsTheTable is the other half of the shape: how full
// it is, beside what it is bounded by, so "how close to the bound" needs no flag
// value typed into a dashboard.
func TestClientRecordsGaugeFollowsTheTable(t *testing.T) {
	records := newClientRecords(func() *toolutil.RateLimiter { return nil }, chargePolicy{})
	records.maxRecords = 4

	got := collectClientRecordMetrics(t, func() {
		records.observe()
		records.mu.Lock()
		records.byAddress = map[string]*clientRecord{
			"a": {lastSeen: time.Now()},
			"b": {lastSeen: time.Now()},
			"c": {lastSeen: time.Now()},
		}
		records.mu.Unlock()
	})

	if got.entries != 3 || got.capacity != 4 {
		t.Errorf("entries = %d of %d, want 3 of 4", got.entries, got.capacity)
	}

	// A nil table publishes nothing, which is the honest answer on stdio: one
	// caller, no table to describe.
	var absent *clientRecords
	nothing := collectClientRecordMetrics(t, absent.observe)
	if nothing.capacity != 0 {
		t.Errorf("a nil table published a capacity of %d", nothing.capacity)
	}
}
