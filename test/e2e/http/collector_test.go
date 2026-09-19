//go:build httpe2e

package httpe2e

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// export is one OTLP payload the server sent us.
type export struct {
	path    string
	headers http.Header
	body    []byte
}

// collector is an OTLP receiver that keeps what it was sent.
//
// # Why a stub rather than a real collector
//
// A real one in a container would prove that a genuine receiver accepts this
// server's protobuf, which is worth having and belongs beside the nginx case.
// It would prove less about the two things these tests exist for. The
// credential assertion needs the raw Authorization header, which a collector
// consumes and does not report. The leak assertion needs the payload bytes
// themselves, and a collector forwards them onward rather than handing them
// back.
//
// It also keeps this module what it is: a suite that needs no daemon, no
// network and no credentials, and therefore runs on every push.
//
// # Why searching raw bytes is legitimate
//
// The leak assertions below look for substrings in the protobuf payload rather
// than decoding it. That is not laziness: protobuf encodes strings as UTF-8
// literals with no framing inside them, so a query, an identifier or a
// credential that reached any attribute, any span name or any log body appears
// verbatim in these bytes. Searching them proves the negative across every
// field at once, including fields nobody thought to check — which a decoder
// driven by a field list cannot.
type collector struct {
	URL string

	mu      sync.Mutex
	exports []export
}

// startCollector runs an OTLP receiver for the duration of the test.
func startCollector(t *testing.T) *collector {
	t.Helper()

	c := &collector{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		c.mu.Lock()
		c.exports = append(c.exports, export{
			path:    r.URL.Path,
			headers: r.Header.Clone(),
			body:    body,
		})
		c.mu.Unlock()

		// An empty ExportTraceServiceResponse is zero bytes of protobuf, which
		// is what a successful export looks like. Answering anything else makes
		// the exporter retry and the test slower for no reason.
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c.URL = srv.URL
	return c
}

// awaitExport blocks until at least one payload has arrived, or fails.
//
// The batch processors export on a schedule, so a test that read immediately
// would assert against an empty slice and pass for the wrong reason. This is
// the difference between proving a value did not leave and proving nothing left
// at all.
func (c *collector) awaitExport(t *testing.T, within time.Duration) []export {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if got := c.received(); len(got) > 0 {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no OTLP export arrived within %s; nothing was verified", within)
	return nil
}

// awaitPayloadContaining blocks until some payload carries want, and returns
// everything received by then.
//
// The batch a server exports first is the one its own startup produced — the
// card builder opens an in-memory session and its spans are real — so a case
// that waited for "an export" and then asserted would be asking about the wrong
// batch, and would report a missing span for one that had simply not been
// flushed yet.
func (c *collector) awaitPayloadContaining(t *testing.T, want string, within time.Duration) string {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if all := c.payloads(); strings.Contains(all, want) {
			return all
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no exported payload carried %q within %s; %d payload(s) arrived", want, within, len(c.received()))
	return ""
}

// awaitCallsExported blocks until the collector holds at least want spans for
// tool calls, and returns everything received by then.
//
// It is what a negative assertion has to wait for. The first batch a server
// exports is its own startup — the card builder opens an in-memory session and
// emits server/discover, tools/list and prompts/list — so "wait for an export,
// then check the planted value is absent" is satisfied by a batch that never
// carried the operation under test. Every case here would pass against a server
// that exported the secret, as long as it was slow about it.
//
// The marker is the method name, which is in every MCP span and is neither
// sensitive nor planted: a case must never synchronize on the value it is
// asserting the absence of, since that assertion would then be waiting for its
// own failure.
func (c *collector) awaitCallsExported(t *testing.T, want int, within time.Duration) string {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if all := c.payloads(); strings.Count(all, "tools/call") >= want {
			return all
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("fewer than %d tools/call spans reached the collector within %s; the assertions below would have been made against the startup batch",
		want, within)
	return ""
}

// received returns what has arrived so far.
func (c *collector) received() []export {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]export(nil), c.exports...)
}

// payloads renders every byte this collector holds, for an assertion about what
// is absent from all of them at once.
func (c *collector) payloads() string {
	var all strings.Builder
	for _, e := range c.received() {
		all.WriteString(e.path)
		all.WriteByte(0)
		all.Write(e.body)
		all.WriteByte('\n')
	}
	return all.String()
}

// assertNoPayloadContains fails when any exported payload carries a value that
// must never leave the process.
func (c *collector) assertNoPayloadContains(t *testing.T, forbidden ...string) {
	t.Helper()

	for _, e := range c.received() {
		text := string(e.body)
		for _, secret := range forbidden {
			if strings.Contains(text, secret) {
				t.Errorf("%q reached the collector in a %s payload; it must never leave this process",
					secret, e.path)
			}
		}
	}
}

// collectorEnv points a server at this collector, exporting promptly.
//
// The durations are integers because the specification defines every OTEL_
// timeout as an integer number of milliseconds. Writing "200ms" would parse as
// nothing and silently keep the ten-second default, which here would mean every
// case timing out with no export to inspect — the first of the four traps the
// telemetry guide describes, reached from the test side.
func collectorEnv(c *collector) map[string]string {
	return map[string]string{
		"LIBGEN_MCP_TELEMETRY":        "1",
		"OTEL_EXPORTER_OTLP_ENDPOINT": c.URL,
		"OTEL_EXPORTER_OTLP_TIMEOUT":  "2000",
		"OTEL_BSP_SCHEDULE_DELAY":     "100",
		"OTEL_BLRP_SCHEDULE_DELAY":    "100",
		"OTEL_METRIC_EXPORT_INTERVAL": "100",
	}
}

// withEnv merges overrides into a copy of base, so a case can start from
// mirrorEnv or collectorEnv without editing what the next case gets.
func withEnv(base map[string]string, overrides ...map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overrides))
	maps.Copy(merged, base)
	for _, o := range overrides {
		maps.Copy(merged, o)
	}
	return merged
}
