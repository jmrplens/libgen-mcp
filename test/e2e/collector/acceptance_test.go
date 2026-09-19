//go:build collectore2e

package collectore2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The OTLP JSON the collector's file exporter writes, decoded only as far as
// these cases read it.
//
// Hand-written rather than generated from the protobuf: the fields below are the
// ones being asserted on, and a generated type would bring a dependency and a
// version to keep in step for no assertion it enables.
type (
	traceDocument struct {
		ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
	}
	otlpResourceSpans struct {
		Resource   otlpResource     `json:"resource"`
		ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
	}
	otlpScopeSpans struct {
		Scope otlpScope  `json:"scope"`
		Spans []otlpSpan `json:"spans"`
	}
	otlpScope struct {
		Name string `json:"name"`
	}
	otlpSpan struct {
		Name       string          `json:"name"`
		Kind       int             `json:"kind"`
		Attributes []otlpAttribute `json:"attributes"`
	}
	otlpResource struct {
		Attributes []otlpAttribute `json:"attributes"`
	}
	otlpAttribute struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
			IntValue    string `json:"intValue"`
		} `json:"value"`
	}

	metricDocument struct {
		ResourceMetrics []struct {
			ScopeMetrics []struct {
				Metrics []struct {
					Name string `json:"name"`
					Unit string `json:"unit"`
				} `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}

	logDocument struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct {
					SeverityText string `json:"severityText"`
					Body         struct {
						StringValue string `json:"stringValue"`
					} `json:"body"`
					Attributes []otlpAttribute `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
)

// attr returns a span attribute's string value.
func (s otlpSpan) attr(key string) (string, bool) {
	for _, a := range s.Attributes {
		if a.Key == key {
			if a.Value.StringValue != "" {
				return a.Value.StringValue, true
			}
			return a.Value.IntValue, true
		}
	}
	return "", false
}

// attr returns a resource attribute's string value.
func (r otlpResource) attr(key string) (string, bool) {
	for _, a := range r.Attributes {
		if a.Key == key {
			return a.Value.StringValue, true
		}
	}
	return "", false
}

// TestAcceptance_ARealCollectorParsesTheSpanThisServerSends is the whole point
// of running a container.
//
// The stub in test/e2e/http answers 200 to anything, so it cannot tell a valid
// export from one no backend could read. Here the span has to survive being
// decoded from protobuf, routed through a pipeline and re-encoded as JSON before
// this can read it back — and it is read back by the fields a dashboard would
// group on, not merely counted.
func TestAcceptance_ARealCollectorParsesTheSpanThisServerSends(t *testing.T) {
	c := startCollector(t)
	s := startServer(t, c, nil)

	s.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"anything"}}}`)

	resource, span, ok := c.awaitSpan(t, 60*time.Second, func(_ otlpResourceSpans, sp otlpSpan) bool {
		method, has := sp.attr("mcp.method.name")
		return has && method == "tools/call"
	})
	if !ok {
		t.Fatalf("no tools/call span reached the collector.\nServer log:\n%s\nCollector log:\n%s", s.logs(), c.containerLogs(t))
	}

	if tool, has := span.attr("gen_ai.tool.name"); !has || tool != "search" {
		t.Errorf("the span does not name the tool: %+v", span.Attributes)
	}
	if transport, has := span.attr("network.transport"); !has || transport != "tcp" {
		t.Errorf("network.transport = %q, want tcp for an HTTP listener", transport)
	}
	// The resource is what a backend groups every signal of one process by, and
	// it is assembled by this server rather than by the SDK alone.
	if name, has := resource.Resource.attr("service.name"); !has || name != "libgen-mcp-collectore2e" {
		t.Errorf("service.name = %q, want the configured one", name)
	}
	// A span kind out of range is one of the things a stub accepts and a
	// backend cannot use. SERVER is 2 in the protocol's own numbering.
	if span.Kind != 2 {
		t.Errorf("span kind = %d, want 2 (SERVER)", span.Kind)
	}
}

// TestAcceptance_TheCollectorLogsNoRejection is the assertion a stub cannot
// make at all.
//
// A receiver that refused a batch — a malformed payload, an attribute it cannot
// accept, a resource it will not route — says so in its own log and answers the
// exporter with an error the server then records. Both sides are read here, so
// a rejection cannot pass as a slow export.
func TestAcceptance_TheCollectorLogsNoRejection(t *testing.T) {
	c := startCollector(t)
	s := startServer(t, c, nil)

	s.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if _, _, ok := c.awaitSpan(t, 60*time.Second, func(otlpResourceSpans, otlpSpan) bool { return true }); !ok {
		t.Fatalf("nothing reached the collector at all:\n%s", c.containerLogs(t))
	}

	logs := c.containerLogs(t)
	for _, sign := range []string{"error", "Permanent error", "failed to"} {
		if strings.Contains(logs, sign) {
			t.Errorf("the collector reported %q, so it did not accept what was sent:\n%s", sign, logs)
		}
	}
	// And the server's own SDK error handler stayed quiet: an export the
	// receiver refused arrives here as a logged failure rather than as silence.
	if serverLog := s.logs(); strings.Contains(serverLog, "traces export") || strings.Contains(serverLog, "exporter") {
		t.Errorf("the server reported an export failure:\n%s", serverLog)
	}
}

// awaitSpan blocks until the collector has parsed a span the predicate accepts.
//
// Waiting is not optional: the batch processor exports on a schedule and the
// file exporter flushes on another, so a case that read immediately would find
// an empty file and could only assert emptiness — which every broken server also
// satisfies.
//
// The outcome is returned rather than fataled on, because the interesting
// failure is diagnosed from two logs this type can only see one of: a receiver
// that refused the export produces exactly this timeout, and the sentence that
// explains it is in the server's log.
func (c *collector) awaitSpan(t *testing.T, within time.Duration, match func(otlpResourceSpans, otlpSpan) bool) (otlpResourceSpans, otlpSpan, bool) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, doc := range documents[traceDocument](t, filepath.Join(c.outDir, tracesFile)) {
			for _, rs := range doc.ResourceSpans {
				for _, ss := range rs.ScopeSpans {
					for _, span := range ss.Spans {
						if match(rs, span) {
							return rs, span, true
						}
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return otlpResourceSpans{}, otlpSpan{}, false
}
