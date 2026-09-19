//go:build collectore2e

package collectore2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSignals_AllThreeArriveAndAreParsed is the claim the startup line and the
// server card both make, checked against a receiver that has to understand each
// one.
//
// Three signals means three pipelines, three encodings and three ways to be
// wrong, and the one most likely to be announced and empty is logs: the provider
// can be installed and nothing bridged into it, which no assertion inside the
// process can tell from a working bridge.
func TestSignals_AllThreeArriveAndAreParsed(t *testing.T) {
	c := startCollector(t)
	s := startServer(t, c, nil)

	// One call is enough for all three: it produces spans, feeds the duration
	// histograms, and writes records the bridge forwards.
	s.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"anything"}}}`)

	t.Run("traces", func(t *testing.T) {
		if _, _, ok := c.awaitSpan(t, 60*time.Second, func(otlpResourceSpans, otlpSpan) bool { return true }); !ok {
			t.Fatalf("no span was parsed.\nServer log:\n%s\nCollector log:\n%s", s.logs(), c.containerLogs(t))
		}
	})

	t.Run("metrics", func(t *testing.T) {
		name, unit, ok := c.awaitMetric(t, 60*time.Second, func(name string) bool {
			return name == "mcp.server.operation.duration"
		})
		if !ok {
			t.Fatalf("the MCP duration histogram was not parsed.\nCollector log:\n%s", c.containerLogs(t))
		}
		// A unit that contradicts its name is one of the things a stub accepts
		// and a dashboard renders wrongly: this instrument is seconds.
		if unit != "s" {
			t.Errorf("%s arrived with unit %q, want s", name, unit)
		}
	})

	t.Run("logs", func(t *testing.T) {
		record, ok := c.awaitLogRecord(t, 60*time.Second, func(body, severity string) bool {
			return body != "" && severity != ""
		})
		if !ok {
			t.Fatalf("no log record was parsed, so the logs signal is announced and empty.\nCollector log:\n%s", c.containerLogs(t))
		}
		// The severity has to survive the trip. It is the field an aggregator
		// routes on, and the one an attribute named `level` was overwriting
		// before the SDK logger was given its own group.
		if !strings.EqualFold(record.severity, "info") && !strings.EqualFold(record.severity, "warn") &&
			!strings.EqualFold(record.severity, "error") && !strings.EqualFold(record.severity, "debug") {
			t.Errorf("a record arrived with severity %q, which is not one", record.severity)
		}
	})
}

// parsedRecord is one log record as the collector re-encoded it.
type parsedRecord struct {
	body     string
	severity string
}

// awaitMetric blocks until a metric whose name the predicate accepts has been
// parsed, and returns its name and unit.
func (c *collector) awaitMetric(t *testing.T, within time.Duration, match func(string) bool) (name, unit string, ok bool) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, doc := range documents[metricDocument](t, filepath.Join(c.outDir, metricsFile)) {
			for _, rm := range doc.ResourceMetrics {
				for _, sm := range rm.ScopeMetrics {
					for _, m := range sm.Metrics {
						if match(m.Name) {
							return m.Name, m.Unit, true
						}
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", "", false
}

// awaitLogRecord blocks until a log record the predicate accepts has been
// parsed.
func (c *collector) awaitLogRecord(t *testing.T, within time.Duration, match func(body, severity string) bool) (parsedRecord, bool) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, doc := range documents[logDocument](t, filepath.Join(c.outDir, logsFile)) {
			for _, rl := range doc.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					for _, rec := range sl.LogRecords {
						if match(rec.Body.StringValue, rec.SeverityText) {
							return parsedRecord{body: rec.Body.StringValue, severity: rec.SeverityText}, true
						}
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return parsedRecord{}, false
}

// TestSignals_ASelectedSubsetIsTheWholeExport keeps the signal list from being
// decorative.
//
// Their costs differ — traces are per call, metrics are aggregated, logs
// duplicate a stream an operator may already be shipping — so a deployment that
// asked for one and paid for three would be worse off for having chosen.
func TestSignals_ASelectedSubsetIsTheWholeExport(t *testing.T) {
	c := startCollector(t)
	s := startServer(t, c, map[string]string{
		"LIBGEN_MCP_TELEMETRY_SIGNALS": "traces",
	})

	s.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if _, _, ok := c.awaitSpan(t, 60*time.Second, func(otlpResourceSpans, otlpSpan) bool { return true }); !ok {
		t.Fatalf("the selected signal did not arrive:\n%s", c.containerLogs(t))
	}

	// The other two pipelines wrote nothing. Read after the traces arrived, so
	// this is not merely early.
	for _, file := range []string{metricsFile, logsFile} {
		t.Run(file, func(t *testing.T) {
			if docs := documents[map[string]any](t, filepath.Join(c.outDir, file)); len(docs) > 0 {
				t.Errorf("%s holds %d document(s) although only traces were selected", file, len(docs))
			}
		})
	}
}
