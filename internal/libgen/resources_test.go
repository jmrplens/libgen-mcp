package libgen

import (
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	attributekey "go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/mcpotel"
)

// collectResourceMetrics runs work against a fresh meter provider and returns
// what one collection gathered.
//
// The registrations are removed afterwards, which is not tidiness: a callback
// holds the client it reads and the provider holds the callback, so a
// registration left in place has every later test's collection reporting on this
// test's client.
func collectResourceMetrics(t *testing.T, work func()) metricdata.ResourceMetrics {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		mcpotel.UnobserveAll()
		otel.SetMeterProvider(previous)
	})

	work()

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatalf("collecting: %v", err)
	}
	return metrics
}

// gaugeValue returns the single value of a named observable gauge.
//
// More than one point means two resources answered under one name, which is the
// registration leak [mcpotel.ObserveBounded] exists to prevent, so it fails here
// rather than picking one of them.
func gaugeValue(t *testing.T, metrics metricdata.ResourceMetrics, name string) int64 {
	t.Helper()

	for _, scope := range metrics.ScopeMetrics {
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

// counterValues returns a named counter's points keyed by one dimension.
func counterValues(t *testing.T, metrics metricdata.ResourceMetrics, name, dimension string) map[string]int64 {
	t.Helper()

	out := make(map[string]int64)
	for _, scope := range metrics.ScopeMetrics {
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

// namedSource is a source these tests only ever name: they measure which sources
// the chain set aside, never what one resolved.
func namedSource(name string) countingSource {
	return countingSource{name: name, attempts: new(atomic.Int32)}
}

// TestTheClientsGaugesReadTheResourceTheyName is what makes the three pairs worth
// publishing: each one has to be wired to its own resource and to its own bound.
//
// Three resources with the same shape are three chances to read the wrong one,
// and every such mistake produces a plausible number — a capacity where a current
// value belongs reports a cache that is permanently full, and reads as an
// exhausted deployment on every dashboard built on it.
func TestTheClientsGaugesReadTheResourceTheyName(t *testing.T) {
	metrics := collectResourceMetrics(t, func() {
		cfg := &config.Config{
			Timeout:                time.Second,
			RateRPS:                1000,
			RateBurst:              100,
			RetryAttempts:          1,
			MaxConcurrentDownloads: 3,
			ReadCacheBytes:         4096,
			ReadCacheTTL:           time.Hour,
		}
		c := New(staticMirrors{}, cfg)
		// Two named sources, so the cooldown table's denominator is the chain
		// length rather than whatever the ambient configuration assembled.
		c.sources = []DownloadSource{
			namedSource("one"),
			namedSource("two"),
		}

		// One cached file of a known size, one download slot taken, one source
		// set aside: every number below is distinct from every other, so a
		// crossed pair cannot pass.
		c.tempCache.entries["md5-a"] = &tempEntry{path: "unread", size: 1200, refs: 1, atime: time.Now()}
		c.dlSem <- struct{}{}
		c.markSourceCooldown("one")
	})

	for _, tc := range []struct {
		name string
		want int64
	}{
		{mcpotel.InstrumentReadCacheBytes, 1200},
		{mcpotel.InstrumentReadCacheCapacity, 4096},
		{mcpotel.InstrumentDownloadsInflight, 1},
		{mcpotel.InstrumentDownloadsCapacity, 3},
		{mcpotel.InstrumentSourceCooldownEntries, 1},
		{mcpotel.InstrumentSourceCooldownCapacity, 2},
	} {
		if got := gaugeValue(t, metrics, tc.name); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestTheCooldownBypassIsCountedOncePerChainRunAndByKind covers the one cooldown
// value worth alerting on.
//
// One source in cooldown is routing working; every capable source in cooldown is
// an outage wearing routing's clothes, and the chain tries them anyway. Counting
// it once per run rather than once per cooled source is what keeps a long chain
// from looking worse than a short one for the same outage, and the kind is what
// says which of the two chains — md5 or DOI — is the one that is down.
func TestTheCooldownBypassIsCountedOncePerChainRunAndByKind(t *testing.T) {
	metrics := collectResourceMetrics(t, func() {
		chain := []DownloadSource{
			namedSource("a"),
			namedSource("b"),
		}
		c := cooldownChainClient(chain...)
		c.markSourceCooldown("a")
		c.markSourceCooldown("b")

		if got := srcNames(c.eligibleSources(t.Context(), Item{MD5: testMD5}, chain)); len(got) != 2 {
			t.Fatalf("eligible sources = %v, want the whole chain: nothing was bypassed, so nothing is being counted", got)
		}
		// A second md5 run and a single DOI one. The counts are deliberately
		// unequal: two runs of one kind and one of the other is what tells a
		// dimension that is merely present from one that is right, and an
		// inverted kind — the md5 chain reported as the DOI chain — reads as a
		// perfectly plausible outage on the wrong half of the server.
		c.eligibleSources(t.Context(), Item{MD5: testMD5}, chain)
		c.eligibleSources(t.Context(), Item{DOI: "10.1000/xyz123"}, chain)
	})

	got := counterValues(t, metrics, mcpotel.InstrumentSourceCooldownBypassed, "kind")
	if got["book"] != 2 || got["article"] != 1 {
		t.Errorf("bypasses = %v, want two book runs and one article run: two cooled sources are one outage, not two", got)
	}
}

// TestARoutedChainRunCountsNoBypass is the other half, and the one that keeps the
// counter alertable: a source passed over while another can still serve is the
// feature working.
func TestARoutedChainRunCountsNoBypass(t *testing.T) {
	metrics := collectResourceMetrics(t, func() {
		chain := []DownloadSource{
			namedSource("cooled"),
			namedSource("live"),
		}
		c := cooldownChainClient(chain...)
		c.markSourceCooldown("cooled")

		if got := srcNames(c.eligibleSources(t.Context(), Item{MD5: testMD5}, chain)); len(got) != 1 {
			t.Fatalf("eligible sources = %v, want only the live one", got)
		}
	})

	if got := counterValues(t, metrics, mcpotel.InstrumentSourceCooldownBypassed, "kind"); len(got) != 0 {
		t.Errorf("bypasses = %v, want none: one source in cooldown is the chain routing around it", got)
	}
}

// TestReadCacheEvictionsCarryTheReasonThatCausedThem is the counter's whole
// point.
//
// A total says a cache is evicting; the reason says which knob is wrong. A cache
// full because nothing expires wants a shorter TTL and a cache full because it is
// busy wants more bytes, and the two are indistinguishable without this
// dimension — so a reason recorded on the wrong path is worse than no counter.
func TestReadCacheEvictionsCarryTheReasonThatCausedThem(t *testing.T) {
	underCap, size := writeTempFile(t, "small enough to keep")
	overCap, overSize := writeTempFile(t, "more bytes than the cap allows")

	metrics := collectResourceMetrics(t, func() {
		// ttl=0 expires anything unreferenced; the cap is far away, so only the
		// TTL pass can be what removes it.
		lapsing := newTempCache(1<<30, 0)
		lapsing.put(t.Context(), "idle", underCap, size)
		lapsing.release("idle")
		lapsing.evict(t.Context())

		// The mirror image: an hour of TTL, and a cap the single entry is over.
		full := newTempCache(overSize-1, time.Hour)
		full.put(t.Context(), "big", overCap, overSize)
		full.release("big")
		full.evict(t.Context())
	})

	got := counterValues(t, metrics, mcpotel.InstrumentReadCacheEvictions, "reason")
	if got["ttl"] != 1 || got["size_pressure"] != 1 {
		t.Errorf("read-cache evictions = %v, want one of each reason", got)
	}
}
