package mcpotel

import (
	"slices"
	"testing"

	"go.opentelemetry.io/otel"
	attributekey "go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collected runs one collection against a fresh provider and returns what it
// gathered, with the registrations removed afterwards.
//
// The cleanup is not tidiness: a callback holds a reference to the resource it
// reads and the provider holds the callback, so a registration left in place
// outlives what it reports — in a test binary that is one test's resource being
// read by every later test's collection.
func collected(t *testing.T, register func()) recorded {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		UnobserveAll()
		otel.SetMeterProvider(previous)
	})

	register()

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatalf("collecting: %v", err)
	}
	return recorded{metrics: metrics}
}

// gauge returns the single value of a named observable gauge.
func (r recorded) gauge(t *testing.T, name string) int64 {
	t.Helper()

	for _, scope := range r.metrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("%s is %T, want an int64 gauge", name, m.Data)
			}
			if len(data.DataPoints) != 1 {
				t.Fatalf("%s has %d points, want one", name, len(data.DataPoints))
			}
			return data.DataPoints[0].Value
		}
	}
	t.Fatalf("no gauge named %s was collected", name)
	return 0
}

// counterPoints returns a named counter's points as reason to value.
func (r recorded) counterPoints(t *testing.T, name, dimension string) map[string]int64 {
	t.Helper()

	out := make(map[string]int64)
	for _, scope := range r.metrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T, want an int64 sum", name, m.Data)
			}
			for _, point := range data.DataPoints {
				key := ""
				if value, present := point.Attributes.Value(attributekey.Key(dimension)); present {
					key = value.String()
				}
				out[key] += point.Value
			}
		}
	}
	return out
}

// TestObserveBoundedFollowsItsResource is the whole contract of a gauge: it
// reports what the resource says now, not what it said at registration.
func TestObserveBoundedFollowsItsResource(t *testing.T) {
	entries := int64(0)
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		UnobserveAll()
		otel.SetMeterProvider(previous)
	})

	ObserveBounded(InstrumentReadCacheBytes, InstrumentReadCacheCapacity, Gauges{
		Current:  func() int64 { return entries },
		Capacity: func() int64 { return 100 },
	})

	read := func() (current, capacity int64) {
		var metrics metricdata.ResourceMetrics
		if err := reader.Collect(t.Context(), &metrics); err != nil {
			t.Fatalf("collecting: %v", err)
		}
		got := recorded{metrics: metrics}
		return got.gauge(t, InstrumentReadCacheBytes), got.gauge(t, InstrumentReadCacheCapacity)
	}

	if current, capacity := read(); current != 0 || capacity != 100 {
		t.Fatalf("first collection = %d of %d, want 0 of 100", current, capacity)
	}
	entries = 42
	if current, capacity := read(); current != 42 || capacity != 100 {
		t.Errorf("second collection = %d of %d, want 42 of 100: the gauge is not following its resource", current, capacity)
	}
}

// TestObserveBoundedReplacesAPreviousRegistration keeps a process that builds
// several clients from reporting several caches.
//
// Each registration is one callback, and two callbacks on one instrument report
// two values per collection — which a backend reads as two resources rather than
// as one described twice.
func TestObserveBoundedReplacesAPreviousRegistration(t *testing.T) {
	got := collected(t, func() {
		ObserveBounded(InstrumentDownloadsInflight, InstrumentDownloadsCapacity, Gauges{
			Current:  func() int64 { return 1 },
			Capacity: func() int64 { return 2 },
		})
		ObserveBounded(InstrumentDownloadsInflight, InstrumentDownloadsCapacity, Gauges{
			Current:  func() int64 { return 7 },
			Capacity: func() int64 { return 8 },
		})
	})

	// gauge fails on more than one point, so this asserts the replacement as
	// well as the value.
	if current := got.gauge(t, InstrumentDownloadsInflight); current != 7 {
		t.Errorf("inflight = %d, want the second registration's 7", current)
	}
}

// TestObserveBoundedIgnoresAnIncompletePair keeps a caller that wired half of it
// from registering a gauge that panics on collection.
func TestObserveBoundedIgnoresAnIncompletePair(t *testing.T) {
	got := collected(t, func() {
		ObserveBounded(InstrumentClientRecordsEntries, InstrumentClientRecordsCapacity, Gauges{
			Current: func() int64 { return 1 },
		})
	})
	if len(got.metrics.ScopeMetrics) != 0 {
		t.Errorf("an incomplete pair registered something: %v", got.metrics.ScopeMetrics)
	}
}

// TestObserveBoundedRegistersNothingUnderAnUnusableName keeps a name the SDK
// rejects from becoming a half-registration.
//
// Go's Meter returns a working instrument alongside the error, so the shape that
// ignores it registers a callback under a name nothing exports — a gauge that
// looks wired at every call site and reports to nobody. Refusing the pair is what
// makes the failure reach the error handler instead.
func TestObserveBoundedRegistersNothingUnderAnUnusableName(t *testing.T) {
	previous := otel.GetErrorHandler()
	var handled error
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { handled = err }))
	t.Cleanup(func() { otel.SetErrorHandler(previous) })

	got := collected(t, func() {
		// A leading digit is invalid wherever the name is validated, which is
		// what an instrument name assembled from a value rather than written out
		// tends to produce.
		ObserveBounded("9nope", InstrumentReadCacheCapacity, Gauges{
			Current:  func() int64 { return 1 },
			Capacity: func() int64 { return 2 },
		})
	})

	if len(got.metrics.ScopeMetrics) != 0 {
		t.Errorf("an unusable name registered %v", got.metrics.ScopeMetrics)
	}
	if handled == nil {
		t.Error("the rejection reached nobody: the error handler is how an operator learns an instrument is missing")
	}
}

// TestEvictionReasonsAreClosed is the drift gate on a dimension that cannot be
// withdrawn once a dashboard groups by it.
func TestEvictionReasonsAreClosed(t *testing.T) {
	t.Parallel()

	want := []string{"lapsed", "size_pressure", "ttl"}
	got := []string{string(ReasonSizePressure), string(ReasonTTL), string(ReasonLapsed)}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the eviction reasons are %q, want %q", got, want)
	}

	wantKinds := []string{"article", "book"}
	kinds := []string{string(KindBook), string(KindArticle)}
	slices.Sort(kinds)
	if !slices.Equal(kinds, wantKinds) {
		t.Errorf("the item kinds are %q, want %q", kinds, wantKinds)
	}
}

// TestCountersRecordTheirReason checks each counter reaches the instrument its
// name promises, with the dimension it promises.
func TestCountersRecordTheirReason(t *testing.T) {
	got := collected(t, func() {
		RecordReadCacheEviction(t.Context(), ReasonTTL)
		RecordReadCacheEviction(t.Context(), ReasonTTL)
		RecordReadCacheEviction(t.Context(), ReasonSizePressure)
		RecordClientRecordEviction(t.Context(), ReasonLapsed)
		RecordClientRecordBusyEviction(t.Context())
		RecordSourceCooldownBypassed(t.Context(), KindArticle)
	})

	cache := got.counterPoints(t, InstrumentReadCacheEvictions, "reason")
	if cache["ttl"] != 2 || cache["size_pressure"] != 1 {
		t.Errorf("read-cache evictions = %v, want two ttl and one size_pressure", cache)
	}
	if records := got.counterPoints(t, InstrumentClientRecordsEvictions, "reason"); records["lapsed"] != 1 {
		t.Errorf("client-record evictions = %v", records)
	}
	if busy := got.counterPoints(t, InstrumentClientRecordsBusyEviction, ""); busy[""] != 1 {
		t.Errorf("busy evictions = %v, want one", busy)
	}
	if bypassed := got.counterPoints(t, InstrumentSourceCooldownBypassed, "kind"); bypassed["article"] != 1 {
		t.Errorf("cooldown bypasses = %v, want one for an article", bypassed)
	}
}
