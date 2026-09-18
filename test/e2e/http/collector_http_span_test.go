//go:build httpe2e

package httpe2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestCollector_ARefusedRequestIsStillTraced is the whole reason the HTTP span
// exists in front of the MCP one.
//
// The MCP span starts inside the handler, so a request one of the guards
// answered instead of forwarding never produces one — and on a published
// endpoint that is the traffic an operator most wants to watch: how much is
// being refused, and how long the refusal takes. The refusal here is the Host
// guard, which answers before anything MCP-shaped exists at all.
//
// The payload is searched rather than decoded, for the reason the stub
// documents: protobuf writes attribute keys as literal UTF-8, so
// `http.response.status_code` appearing at all means a span carried one.
func TestCollector_ARefusedRequestIsStillTraced(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	// No flags: a listener bound to 127.0.0.1 declares that host and refuses
	// any other, which is the refusal this case needs and the default every
	// deployment starts from.
	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"LIBGEN_MCP_TELEMETRY_SIGNALS": "traces",
	}))

	// A tools/call, because the assertion below is that no MCP span exists for
	// it: the server builds its own card at startup over an in-memory session,
	// which legitimately produces spans for server/discover, tools/list and
	// prompts/list. tools/call is a method nothing but a caller reaches.
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"anything"}}}`
	got := s.do(t, request{body: call, host: "evil.example"})
	if got.status == http.StatusOK {
		t.Fatalf("the Host guard accepted evil.example, so no refusal was traced: %d", got.status)
	}

	// Waited for by content rather than by arrival: the first batch to reach
	// the collector is the startup one, and asserting against it would report
	// "no HTTP span" for a span that simply had not been flushed yet.
	payloads := c.awaitPayloadContaining(t, "http.response.status_code", 20*time.Second)

	if strings.Contains(payloads, "tools/call") {
		t.Error("an MCP span was recorded for a request the Host guard refused")
	}
}

// TestCollector_TheHealthProbeIsNotTraced pins the one route excluded, and the
// reason it is excluded by exact path.
//
// A balancer polls /health at a fixed interval forever. Instrumenting it would
// mint a span per probe for the life of the deployment and bury every real
// request under them, and it answers a handler that can only fail by the
// process being gone — which the probe itself already reports.
func TestCollector_TheHealthProbeIsNotTraced(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"LIBGEN_MCP_TELEMETRY_SIGNALS": "traces",
	}))

	for range 5 {
		if got := s.do(t, request{method: http.MethodGet, path: "/health"}); got.status != http.StatusOK {
			t.Fatalf("GET /health answered %d", got.status)
		}
	}
	// One real request, so there is something to count against — and waited for
	// by content, because counting before anything was flushed would find zero
	// and pass for the opposite of the reason this case exists.
	s.do(t, request{body: toolsListBody})
	payloads := c.awaitPayloadContaining(t, "http.response.status_code", 20*time.Second)

	// The span carries no url.path, so a traced probe cannot be recognized by
	// its route: what gives it away is the count. Exactly one, not "no more than
	// two" — the startup card is built over in-memory transports and produces no
	// HTTP span at all, so the single call above is the only one there should
	// ever be, and a looser bound would hide one traced probe.
	if got := strings.Count(payloads, "http.response.status_code"); got != 1 {
		t.Errorf("the collector holds %d HTTP status attributes after five probes and one call, want exactly 1; the probe is being traced", got)
	}
}

// TestCollector_TelemetryOffExportsNothing is the default, and the only case
// that proves the switch is a switch.
//
// Off has to mean nothing is created, nothing is started and nothing is sent.
// A deployment that never asked for telemetry and finds a connection attempt in
// its collector's logs has been given something it declined.
func TestCollector_TelemetryOffExportsNothing(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><table></table></body></html>"))
	})

	// The endpoint is configured and the switch is not: the standard variables
	// alone must turn nothing on, which is what keeps them safe to set for a
	// whole environment.
	s := startServer(t, withEnv(mirrorEnv(m), map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": c.URL,
		"OTEL_BSP_SCHEDULE_DELAY":     "100",
		"OTEL_METRIC_EXPORT_INTERVAL": "100",
		"LIBGEN_MCP_EXTRA_SOURCES":    "never",
	}))

	s.do(t, request{body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"anything"}}}`})
	// Long enough that a running exporter would have flushed twice.
	time.Sleep(2 * time.Second)

	if got := c.received(); len(got) != 0 {
		t.Errorf("a deployment that never turned telemetry on exported %d payloads: %v", len(got), pathsOf(got))
	}
	if logs := s.logs(); strings.Contains(logs, "telemetry enabled") {
		t.Errorf("the server announced telemetry it was never asked for:\n%s", tail(logs))
	}
}
