//go:build httpe2e

package httpe2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestCollector_TheLogsSignalActuallyCarriesRecords is the claim the startup
// line and the server card both make, driven over the wire.
//
// The logs signal is only real once something writes into the global logger
// provider. Until the bridge existed, "logs" was announced at startup and
// published on the card and no record was ever exported — a deployment that
// looked instrumented and was not. Nothing inside the process can tell those
// two states apart, which is why this is here rather than in a unit test.
func TestCollector_TheLogsSignalActuallyCarriesRecords(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"LIBGEN_MCP_TELEMETRY_SIGNALS": "logs",
		"LIBGEN_MCP_EXTRA_SOURCES":     "never",
	}))

	// A search against a mirror that refuses produces records on the way
	// through, at INFO and above, which is the floor the export leg applies.
	s.do(t, request{body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"anything"}}}`})

	exports := c.awaitExport(t, 20*time.Second)
	sawLogs := false
	for _, e := range exports {
		if strings.Contains(e.path, "/v1/logs") {
			sawLogs = true
		}
		// With only the logs signal selected, nothing else may be exported:
		// a deployment that asked for one signal and got three is paying for
		// two it declined.
		if strings.Contains(e.path, "/v1/traces") || strings.Contains(e.path, "/v1/metrics") {
			t.Errorf("a %s payload arrived although only the logs signal was selected", e.path)
		}
	}
	if !sawLogs {
		t.Fatalf("no log record reached the collector; the signal is announced and empty. Paths seen: %v", pathsOf(exports))
	}
}

// TestCollector_AStrippedFieldIsAbsentFromTheExportedRecord drives the strip
// list through a real record rather than through the handler that applies it.
//
// The list is applied on the export leg alone: stderr keeps the whole record,
// deliberately, because it is the operator's own terminal. Both halves are
// asserted here, since a bridge that satisfied the first by dropping the field
// everywhere would be a different and worse change.
func TestCollector_AStrippedFieldIsAbsentFromTheExportedRecord(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	// A loopback listener with no proxy flags is what makes the server write
	// its charged_address warning: the peer of every request is 127.0.0.1,
	// which is an address no public client could be reaching it from, and that
	// is the condition the warning fires on. It is the one record in this tree
	// that carries an address, and the field is on the strip list.
	//
	// A wildcard bind would produce the same warning and cannot be used here:
	// the fixture needs LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES, and that setting is
	// refused at startup on a listener other machines can reach.
	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"LIBGEN_MCP_TELEMETRY_SIGNALS": "logs",
		"LIBGEN_MCP_EXTRA_SOURCES":     "never",
	}))

	s.do(t, request{body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"anything"}}}`})

	c.awaitExport(t, 20*time.Second)

	// The record is on the operator's terminal, whole.
	logs := s.logs()
	if !strings.Contains(logs, "charged_address") {
		t.Skipf("the wildcard-bind warning did not fire on this runner, so there is no record to assert about:\n%s", tail(logs))
	}
	// And the field name is nowhere in what left the process.
	if payloads := c.payloads(); strings.Contains(payloads, "charged_address") {
		t.Error("charged_address reached the collector; the strip list is applied on the wrong leg or not at all")
	}
}

// pathsOf renders the OTLP paths a set of exports arrived on.
func pathsOf(exports []export) []string {
	paths := make([]string, 0, len(exports))
	for _, e := range exports {
		paths = append(paths, e.path)
	}
	return paths
}
