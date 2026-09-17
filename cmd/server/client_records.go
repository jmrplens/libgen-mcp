// client_records.go holds the per-caller state a shared server keeps.

package main

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/internal/toolutil"
)

// clientAddressHeader carries the address a POST is charged to, from the HTTP
// layer that can see the connection to the MCP middlewares that cannot.
//
// It is stamped by the carrier middleware and read back out of
// [mcp.RequestExtra.Header]. Like the carrier token it is process-internal and
// server-controlled: any inbound value is overwritten on a POST and deleted on
// every other method, because a caller who could set it would be choosing whose
// budget their traffic counts against.
const clientAddressHeader = "X-Libgen-Mcp-Client"

// recordKey carries the caller's record, from the middleware that resolves it to
// the ones that read it.
//
// The record rather than the bucket alone: two layers inside it ask different
// questions of the same caller — may they spend a token, and are they already
// moving as many files as this deployment allows — and resolving the address
// twice is how the two come to disagree about who is calling.
type recordKey struct{}

// chargedAddressOf reads the address an MCP request is charged to, or "" when it
// arrived on a transport that stamps none — stdio, and the in-memory session the
// server card is built over.
func chargedAddressOf(req mcp.Request) string {
	if req == nil {
		return ""
	}
	extra := req.GetExtra()
	if extra == nil || extra.Header == nil {
		return ""
	}
	return extra.Header.Get(clientAddressHeader)
}

// recordFrom returns the caller's record, or nil on a transport that names no
// caller.
func recordFrom(ctx context.Context) *clientRecord {
	rec, _ := ctx.Value(recordKey{}).(*clientRecord)
	return rec
}

// limiterFrom returns the bucket [clientRecords.meter] resolved for this
// request, or nil when there is none — which the limiter treats as disabled.
func limiterFrom(ctx context.Context) *toolutil.RateLimiter {
	rec := recordFrom(ctx)
	if rec == nil {
		return nil
	}
	return rec.limiter
}

// The table's bounds.
//
// maxClientRecords is a ceiling on live keys rather than on everything ever
// seen: recordTTL is how long a record outlives its last request, and lapsed
// records are swept before the table is ever considered full.
//
// sweepInterval bounds how often that sweep runs. Sweeping on every admission at
// the cap would make the cap its own amplifier — each new address costing a full
// pass over the ceiling — which is a cost a caller controls the rate of.
const (
	maxClientRecords = 4096
	recordTTL        = 10 * time.Minute
	sweepInterval    = 30 * time.Second
)

// clientRecord is what this server remembers about one charged address.
type clientRecord struct {
	// limiter is that address's own bucket. Never nil while the table exists.
	limiter *toolutil.RateLimiter
	// lastSeen is when a request was last charged here, which is what decides
	// both the sweep and the eviction order.
	lastSeen time.Time
	// inFlight counts the metered requests this address has running right now.
	// A record with work in flight is skipped by the eviction, so a caller
	// mid-call does not lose the bucket that is bounding them.
	inFlight int
	// heavy counts the calls that move bytes — download and read — this address
	// has running right now. It is what [clientRecords.enterHeavy] bounds.
	//
	// It is counted whether or not a ceiling is configured. Counting and capping
	// are two jobs, and skipping the increment when no cap is set switches both
	// off at once: the count is also what tells the eviction that this caller is
	// busy, so a deployment that removed the ceiling would quietly make every
	// downloading record look idle.
	heavy int
}

// busy reports whether this caller has anything running.
//
// Both counters, because they answer the same question for different work and
// the eviction has to respect either: losing a record mid-request loses the
// bucket that was bounding that caller and the count that was holding their
// ceiling, and they start again from zero.
func (r *clientRecord) busy() bool { return r.inFlight > 0 || r.heavy > 0 }

// clientRecords is a bounded table of per-address state.
//
// # Why it is an LRU and not a refusal
//
// The sibling project refuses a new key rather than evicting one, and that is
// right where it does it: its table holds authentication *evidence*, a count of
// how many times a key has failed, so evicting a record is how a block would be
// cleared on demand. Saturating that table costs the attacker their own
// accumulated count and buys them nothing — and a second, socket-keyed budget
// still bounds the real source.
//
// A rate bucket is not evidence, it is a budget, and this server has no second
// budget behind it. "Refuse at the cap" applied to a public endpoint's address
// table fails in both readings: refuse the request and 4096 addresses — trivial
// over IPv6, or through a listed proxy's header — lock out every new legitimate
// client; do not track it and the 4097th address is unlimited, which is the
// bypass. Evicting instead costs at most one full burst to the address whose
// record went, which is the cheapest failure available.
//
// # Why eviction is a scan
//
// The table is capped at a few thousand and eviction happens only when it is
// full, so a linear scan for the oldest costs less than the bookkeeping an
// intrusive list would need on every single request. The rule it implements is
// the part that matters: prefer an idle record, fall back to the oldest of all
// when every record is busy, and never grow past the cap.
type clientRecords struct {
	mu sync.Mutex
	// byAddress is the table itself, keyed on the charged address.
	byAddress map[string]*clientRecord
	// lastSweep is when lapsed records were last removed.
	lastSweep time.Time
	// heavyTotal is how many byte-moving calls every caller has in flight
	// together. The per-client ceiling multiplies by however many callers there
	// are, so this is the only one that bounds the process.
	heavyTotal int
	// newLimiter mints a bucket for a new address. It is a field so a test can
	// build a table whose buckets it controls.
	newLimiter func() *toolutil.RateLimiter
	// charge is the configuration the addresses in this table came from. It is
	// held only so the first address that proves callers cannot be told apart
	// can say so; see [callerWarning.warn].
	charge chargePolicy
	// now is time.Now, replaceable by a test that needs the clock to move.
	now func() time.Time
	// maxRecords, ttl and sweepEvery are the bounds above, per table, so a test
	// can use small ones without waiting.
	maxRecords int
	ttl        time.Duration
	sweepEvery time.Duration
}

// newClientRecords builds an empty table whose records carry a bucket from
// newLimiter. A nil newLimiter yields records with no bucket, which is what a
// deployment with the limiter off has.
func newClientRecords(newLimiter func() *toolutil.RateLimiter, charge chargePolicy) *clientRecords {
	return &clientRecords{
		byAddress:  make(map[string]*clientRecord),
		newLimiter: newLimiter,
		charge:     charge,
		now:        time.Now,
		maxRecords: maxClientRecords,
		ttl:        recordTTL,
		sweepEvery: sweepInterval,
	}
}

// newClientRecordsFor builds the table an HTTP deployment keeps, or nil for one
// that keeps nothing about its callers.
//
// The table outlives the rate limit being off: the in-flight ceiling is keyed on
// the same record, and a deployment whose limiter is off — a loopback bind with
// no proxy named — still bounds how many files one caller is moving. With the
// limit off the records simply carry no bucket, which the limiter reads as
// disabled.
//
// Nil is for stdio, where there is one caller, nobody to be fair to, and the
// download semaphore is already theirs alone.
func newClientRecordsFor(serving bool, limit rateLimitDecision, charge chargePolicy) *clientRecords {
	if !serving {
		return nil
	}
	return newClientRecords(limit.newLimiter, charge)
}

// begin marks the start of a metered request from address and returns that
// address's record, together with the function that marks the end of it.
//
// The pair is what keeps inFlight honest: every caller defers the second half,
// so a record is busy for exactly as long as the request it belongs to.
func (c *clientRecords) begin(ctx context.Context, address string) (rec *clientRecord, end func()) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	rec = c.byAddress[address]
	if rec == nil {
		c.makeRoomLocked(ctx, now)
		rec = &clientRecord{limiter: c.mint()}
		c.byAddress[address] = rec
	}
	rec.lastSeen = now
	rec.inFlight++

	return rec, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if rec.inFlight > 0 {
			rec.inFlight--
		}
	}
}

// mint builds the bucket a new record starts with.
func (c *clientRecords) mint() *toolutil.RateLimiter {
	if c.newLimiter == nil {
		return nil
	}
	return c.newLimiter()
}

// makeRoomLocked ensures there is room for one more record.
//
// It sweeps first, so the cap bounds live keys rather than everything ever seen,
// and only evicts when the sweep left the table still full. The sweep is rate
// limited for the reason [sweepInterval] gives.
func (c *clientRecords) makeRoomLocked(ctx context.Context, now time.Time) {
	if len(c.byAddress) < c.maxRecords {
		return
	}
	if now.Sub(c.lastSweep) >= c.sweepEvery {
		c.sweepLocked(ctx, now)
	}
	for len(c.byAddress) >= c.maxRecords {
		if !c.evictLocked(ctx) {
			return
		}
	}
}

// sweepLocked removes every record whose last request is older than the TTL,
// busy ones excepted: a record with work in flight is live whatever its clock
// says.
func (c *clientRecords) sweepLocked(ctx context.Context, now time.Time) {
	c.lastSweep = now
	for address, rec := range c.byAddress {
		if !rec.busy() && now.Sub(rec.lastSeen) > c.ttl {
			delete(c.byAddress, address)
			mcpotel.RecordClientRecordEviction(ctx, mcpotel.ReasonLapsed)
		}
	}
}

// evictLocked removes one record and reports whether it removed anything.
//
// It prefers the least recently used idle record. When every record is busy it
// evicts the least recently used of those instead, because the alternative is
// growing past the cap, and a bound that yields under pressure is not a bound. A
// record is therefore only ever skipped in favor of another record.
func (c *clientRecords) evictLocked(ctx context.Context) bool {
	var (
		idleKey   string
		idleSeen  time.Time
		busyKey   string
		busySeen  time.Time
		foundIdle bool
		foundBusy bool
	)
	for address, rec := range c.byAddress {
		if !rec.busy() {
			if !foundIdle || rec.lastSeen.Before(idleSeen) {
				idleKey, idleSeen, foundIdle = address, rec.lastSeen, true
			}
			continue
		}
		if !foundBusy || rec.lastSeen.Before(busySeen) {
			busyKey, busySeen, foundBusy = address, rec.lastSeen, true
		}
	}
	switch {
	case foundIdle:
		delete(c.byAddress, idleKey)
		mcpotel.RecordClientRecordEviction(ctx, mcpotel.ReasonSizePressure)
	case foundBusy:
		delete(c.byAddress, busyKey)
		mcpotel.RecordClientRecordEviction(ctx, mcpotel.ReasonSizePressure)
		// Its own instrument rather than a third reason, because it is the only
		// one that says something is wrong rather than something is working:
		// the table was full of callers with work in flight, so the LRU had
		// nothing idle to drop and took a busy record anyway. Without it, a
		// table thrashing under a flood of spoofed addresses looks exactly like
		// a quiet one.
		mcpotel.RecordClientRecordBusyEviction(ctx)
	default:
		return false
	}
	return true
}

// observe publishes how full the table is and what bounds it.
//
// The callback takes the table's own lock, which is what makes it safe on the
// SDK's collection goroutine. A nil table publishes nothing: on stdio there is
// one caller and no table to describe.
func (c *clientRecords) observe() {
	if c == nil {
		return
	}
	mcpotel.ObserveBounded(mcpotel.InstrumentClientRecordsEntries, mcpotel.InstrumentClientRecordsCapacity, mcpotel.Gauges{
		Current:  func() int64 { return int64(c.len()) },
		Capacity: func() int64 { return int64(c.maxRecords) },
	})
}

// len reports how many records the table holds.
func (c *clientRecords) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byAddress)
}

// meter resolves the bucket a request draws on and marks the address busy for as
// long as the request runs.
//
// It sits directly OUTSIDE the limiter, which is the only order that works: the
// limiter reads the bucket off the context, so whatever puts it there has to run
// first. It is a separate middleware rather than part of the limiter because the
// two answer different questions — which caller is this, and may they — and only
// this half knows about addresses at all.
//
// A request with no charged address is left alone, and that is also what exempts
// the server's own listing. buildServerCard lists the catalog over an in-memory
// transport, which travels these same middlewares but carries no header at all —
// so no bucket is resolved and the card cannot be refused because of whoever
// happened to be calling at the time. stdio is exempt for the same reason, and
// there it is simply correct: one client, nobody to be fair to.
func (c *clientRecords) meter(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		address := chargedAddressOf(req)
		if c == nil || address == "" || !toolutil.IsMetered(method) {
			return next(ctx, method, req)
		}
		// Said on the first address that proves it, not at startup: on a
		// wildcard bind nothing before the first request can tell a same-host
		// proxy from a real remote peer.
		unidentifiableCallers.warn(address, c.charge)
		rec, done := c.begin(ctx, address)
		defer done()
		return next(context.WithValue(ctx, recordKey{}, rec), method, req)
	}
}
