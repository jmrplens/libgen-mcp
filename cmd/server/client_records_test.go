package main

import (
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/toolutil"
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
func track(c *clientRecords, address string) {
	_, end := c.begin(address)
	end()
}

// TestARecordIsKeptPerAddress is the premise everything else rests on: two
// callers get two buckets, and the same caller gets the same one.
func TestARecordIsKeptPerAddress(t *testing.T) {
	records, _ := testRecords(t, 8)

	first, endFirst := records.begin("203.0.113.7")
	defer endFirst()
	again, endAgain := records.begin("203.0.113.7")
	defer endAgain()
	other, endOther := records.begin("198.51.100.23")
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

	track(records, "a")
	advance(time.Second)
	track(records, "b")
	advance(time.Second)
	track(records, "c")
	advance(time.Second)

	rec, end := records.begin("d")
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
	_, endBusy := records.begin("busy")
	defer endBusy()
	advance(time.Second)
	track(records, "idle")
	advance(time.Second)
	track(records, "newer")
	advance(time.Second)

	_, end := records.begin("arriving")
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
		_, end := records.begin(address)
		defer end()
		advance(time.Second)
	}

	_, end := records.begin("d")
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

	track(records, "old-1")
	track(records, "old-2")
	track(records, "recent")
	advance(2 * time.Minute) // past the TTL for all three
	track(records, "recent") // refreshes only this one
	advance(time.Second)

	_, end := records.begin("arriving")
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

	_, endBusy := records.begin("downloading")
	defer endBusy()
	track(records, "old-1")
	track(records, "old-2")
	advance(2 * time.Minute)

	_, end := records.begin("arriving")
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

	track(records, "a")
	track(records, "b")
	advance(2 * time.Minute) // both lapsed, and past the sweep window

	// The first admission at the cap sweeps, which clears both.
	_, endFirst := records.begin("c")
	defer endFirst()
	if records.len() != 1 {
		t.Fatalf("after the first sweep the table holds %d records, want 1", records.len())
	}
	swept := records.lastSweep

	// A second admission moments later must not sweep again.
	advance(time.Second)
	track(records, "d")
	_, endThird := records.begin("e")
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
		rec, end := records.begin("203.0.113.7")
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
		rec, end := records.begin("203.0.113.7")
		defer end()
		if rec.limiter != nil {
			t.Error("the record carries a bucket although the limit is off")
		}
	})
}
