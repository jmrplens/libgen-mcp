// resources.go instruments the bounded resources this server holds.
//
// Four of them, and the shape is the same for each: how full it is, how big it
// may get, and why something was thrown out. The third is the one worth having —
// a gauge alone says a cache is full, and a cache that is full because nothing
// expires is a different deployment from one that is full because it is busy.

package mcpotel

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The resources this server bounds, named once.
//
// Under libgen_mcp.* rather than mcp.*, because none of them is the protocol's
// concept: they are this deployment's own limits. Once shipped they cannot be
// renamed without breaking every dashboard built on them, which is why the set
// is small and each one is written out here rather than composed at a call site.
const (
	// InstrumentReadCacheEntries and its capacity twin describe the temp cache
	// a paginated read reuses across pages.
	//
	// Measured in bytes rather than in files, which is what the bound is: a
	// deployment with a 512 MiB cap and four hundred small files is nowhere
	// near it, and an entry count would say it was.
	InstrumentReadCacheBytes    = "libgen_mcp.read_cache.bytes"
	InstrumentReadCacheCapacity = "libgen_mcp.read_cache.capacity"
	// InstrumentReadCacheEvictions counts what was thrown out and why.
	InstrumentReadCacheEvictions = "libgen_mcp.read_cache.evictions"

	// InstrumentDownloadsInflight and its capacity twin describe the
	// concurrent-download semaphore.
	InstrumentDownloadsInflight = "libgen_mcp.downloads.inflight"
	InstrumentDownloadsCapacity = "libgen_mcp.downloads.capacity"

	// InstrumentSourceCooldownEntries is how many download sources are
	// currently being passed over because they failed recently.
	InstrumentSourceCooldownEntries = "libgen_mcp.source_cooldown.entries"
	// InstrumentSourceCooldownCapacity is the length of the chain, which is the
	// honest denominator: "three of seventeen sources are being passed over" is
	// the sentence an operator wants, and the table has no bound of its own.
	InstrumentSourceCooldownCapacity = "libgen_mcp.source_cooldown.capacity"
	// InstrumentSourceCooldownBypassed counts the case worth alerting on:
	// every source that could serve an item is in cooldown, so the chain tries
	// them anyway. One source down is routing; all of them down is an outage
	// wearing a routing decision's clothes.
	//
	// The name is flagged as a hardcoded credential because "bypassed" contains
	// "pass", which is one of the substrings G101 looks for; it is an instrument
	// name, and renaming it to satisfy the pattern would make the published
	// surface worse to read.
	InstrumentSourceCooldownBypassed = "libgen_mcp.source_cooldown.bypassed" //nolint:gosec // G101 matched "pass" inside "bypassed"; this is a metric name.

	// InstrumentClientRecordsEntries and its capacity twin describe the
	// per-caller table the rate limiter and the in-flight ceiling are keyed on.
	InstrumentClientRecordsEntries  = "libgen_mcp.client_records.entries"
	InstrumentClientRecordsCapacity = "libgen_mcp.client_records.capacity"
	// InstrumentClientRecordsEvictions counts what left the table and why.
	InstrumentClientRecordsEvictions = "libgen_mcp.client_records.evictions"
	// InstrumentClientRecordsBusyEviction counts the eviction taken while every
	// record in the table was busy.
	//
	// It is separate from the reason above because it is the only one that says
	// something is wrong rather than something is working: the table is full of
	// callers with work in flight, so the LRU had nothing idle to drop and took
	// a busy record anyway. Without it, a table thrashing under a flood of
	// spoofed addresses looks exactly like a quiet one.
	InstrumentClientRecordsBusyEviction = "libgen_mcp.client_records.busy_evictions"
)

// EvictionReason is why something left a bounded table.
//
// A named type and a closed set, for the reason every metric dimension on this
// surface is one: the value cannot be withdrawn without breaking the dashboards
// built on it, so a reason outside the set does not compile.
type EvictionReason string

const (
	// ReasonSizePressure is the bound itself: the resource was full and the
	// least recently used entry made room.
	ReasonSizePressure EvictionReason = "size_pressure"
	// ReasonTTL is an entry that simply went idle long enough.
	ReasonTTL EvictionReason = "ttl"
	// ReasonLapsed is the client-records equivalent of a TTL: a caller that has
	// not been seen inside the window.
	ReasonLapsed EvictionReason = "lapsed"
)

// ItemKind tells a book from an article, which is the only dimension the
// cooldown bypass carries.
//
// Two values, so it cannot grow: the question it answers is whether a total
// outage is on the md5 chain or the DOI chain, and those have different sources
// and different causes.
type ItemKind string

const (
	// KindBook is an item identified by md5 or ISBN.
	KindBook ItemKind = "book"
	// KindArticle is an item identified by DOI.
	KindArticle ItemKind = "article"
)

// Gauges is what a bounded resource offers this package: a current value and the
// bound it is measured against.
//
// Functions rather than numbers, because the instruments are asynchronous: the
// SDK asks at collection time, so a resource that changes between collections is
// read when somebody looks rather than pushed on every change. That is what
// keeps a hot path free of instrumentation — a download slot taken and returned
// a thousand times costs nothing here.
//
// The callbacks run on the SDK's collection goroutine, so each one must be safe
// to call from anywhere and must not block: taking the resource's own lock is
// expected, holding it across I/O is not.
type Gauges struct {
	// Current reads how full the resource is now.
	Current func() int64
	// Capacity reads the bound. Published beside Current so "how close to the
	// bound" needs no flag value typed into a dashboard by hand — and read
	// rather than fixed, because more than one of these is resolved from
	// configuration at startup.
	Capacity func() int64
}

// observed holds the registrations, so a second Start in one process replaces
// them rather than adding a second callback reporting the same numbers twice.
var observed struct {
	mu         sync.Mutex
	registered map[string]metric.Registration
}

// ObserveBounded publishes a resource's current value and its capacity.
//
// Registering the same pair of names again replaces the previous registration,
// which is what makes this safe to call from a test and from a second Start.
// An error is reported through the SDK's handler rather than returned: a metric
// that cannot be registered is not a reason to refuse to serve, and the
// diagnostics wiring turns it into a structured log record.
func ObserveBounded(currentName, capacityName string, g Gauges) {
	if g.Current == nil || g.Capacity == nil {
		return
	}
	meter := otel.Meter(scopeName)

	current, err := meter.Int64ObservableGauge(currentName)
	if err != nil {
		otel.Handle(err)
		return
	}
	capacity, err := meter.Int64ObservableGauge(capacityName)
	if err != nil {
		otel.Handle(err)
		return
	}

	registration, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(current, g.Current())
		o.ObserveInt64(capacity, g.Capacity())
		return nil
	}, current, capacity)
	if err != nil {
		otel.Handle(err)
		return
	}
	replaceRegistration(currentName, registration)
}

// replaceRegistration stores a registration, unregistering whatever was there.
func replaceRegistration(name string, registration metric.Registration) {
	observed.mu.Lock()
	defer observed.mu.Unlock()
	if observed.registered == nil {
		observed.registered = make(map[string]metric.Registration)
	}
	if previous, ok := observed.registered[name]; ok {
		_ = previous.Unregister()
	}
	observed.registered[name] = registration
}

// UnobserveAll removes every registration this package made.
//
// A callback holds a reference to the resource it reads, and the meter provider
// holds the callback — so leaving them registered outlives what they report. In
// a test binary that is one test's resource being read by every later test's
// collection.
func UnobserveAll() {
	observed.mu.Lock()
	defer observed.mu.Unlock()
	for name, registration := range observed.registered {
		_ = registration.Unregister()
		delete(observed.registered, name)
	}
}

// counter looks up one counter at the moment it is recorded.
//
// # Why this is not built once
//
// The obvious shape is a package-level sync.OnceValue holding the four
// instruments, and it is wrong in a way that only shows up later. The global
// meter delegates to whatever provider is installed, but it adopts a delegate
// **once**: an instrument created before the first SetMeterProvider follows that
// provider and every later one is ignored. So a counter built at package
// initialization, or by the first eviction of a process, binds to whatever was
// installed then — the no-op provider, if anything recorded before telemetry
// started — and stays bound for the life of the process, recording into nothing
// while looking entirely healthy. It is the same trap the error handler's
// delegation has, written down in diagnostics.go.
//
// Looking it up per call costs a map lookup in the SDK, which caches instruments
// by name: these paths run per eviction, not per byte.
//
// The error is checked rather than discarded. Go's Meter ends every creation
// method with "return i, validateInstrumentName(name)", handing back a working
// instrument alongside a non-nil error, so the constructor-shaped call invites
// ignoring it — and an invalid name would then record and export nothing.
func counter(name, description string) metric.Int64Counter {
	instrument, err := otel.Meter(scopeName).Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		otel.Handle(err)
	}
	return instrument
}

// RecordReadCacheEviction counts one entry leaving the read cache.
func RecordReadCacheEviction(ctx context.Context, reason EvictionReason) {
	counter(InstrumentReadCacheEvictions, "Read-cache entries evicted, by reason.").
		Add(ctx, 1, metric.WithAttributes(attrReason(reason)))
}

// RecordClientRecordEviction counts one caller leaving the per-caller table.
func RecordClientRecordEviction(ctx context.Context, reason EvictionReason) {
	counter(InstrumentClientRecordsEvictions, "Per-caller records evicted, by reason.").
		Add(ctx, 1, metric.WithAttributes(attrReason(reason)))
}

// RecordClientRecordBusyEviction counts an eviction taken with nothing idle to
// drop. See [InstrumentClientRecordsBusyEviction] for why it is its own
// instrument rather than a fourth reason.
func RecordClientRecordBusyEviction(ctx context.Context) {
	counter(InstrumentClientRecordsBusyEviction, "Per-caller records evicted while every record in the table was busy.").
		Add(ctx, 1)
}

// RecordSourceCooldownBypassed counts one chain run that found every capable
// source in cooldown and tried them regardless.
//
// Once per run rather than once per source: the question is how often a
// deployment is in that state, and counting per source would make a long chain
// look worse than a short one for the same outage.
func RecordSourceCooldownBypassed(ctx context.Context, kind ItemKind) {
	counter(InstrumentSourceCooldownBypassed, "Chain runs where every capable source was in cooldown and was tried anyway.").
		Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(kind))))
}

// attrReason renders an eviction reason as its dimension.
func attrReason(reason EvictionReason) attribute.KeyValue {
	return attribute.String("reason", string(reason))
}
