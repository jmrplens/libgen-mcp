//go:build httpe2e

package httpe2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// collectorCredential stands in for the token a collector would want.
//
// Worded as a fixture rather than as a plausible token: a string shaped like a
// real credential in a test file is what a secret scanner reports, and a
// repository whose scanner cries wolf over its own fixtures is one where the
// next real finding is dismissed.
const collectorCredential = "not-a-credential-only-a-test-fixture-7c21"

// TestCollector_TheConfiguredCredentialArrivesOnTheExport is why the stub keeps
// raw headers.
//
// The credential is the operator's, it travels on every export, and no real
// collector would hand it back — so this is the only place the wire form can be
// asserted at all. What is checked is that the header arrives exactly as
// configured: the value is percent-encoded in the variable, per the
// specification, and a server that forwarded it still encoded would
// authenticate to nothing while looking configured.
func TestCollector_TheConfiguredCredentialArrivesOnTheExport(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><table></table></body></html>"))
	})

	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"OTEL_EXPORTER_OTLP_HEADERS": "authorization=Bearer%20" + collectorCredential,
		"LIBGEN_MCP_EXTRA_SOURCES":   "never",
	}))

	s.do(t, request{body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`})

	want := "Bearer " + collectorCredential
	for _, e := range c.awaitExport(t, 20*time.Second) {
		if got := e.headers.Get("Authorization"); got != want {
			t.Fatalf("export to %s carried Authorization %q, want %q", e.path, got, want)
		}
	}
}

// TestCollector_APlaintextCredentialOnAnotherHostIsWarnedAbout covers the one
// line an operator gets about a mistake nothing else reports.
//
// The endpoint and the headers are both their configuration, so this is a
// warning and never a refusal — but a credential crossing a network in the
// clear is worth one line at startup, and it names the signals affected because
// the Go exporters disagree per signal about what INSECURE means.
func TestCollector_APlaintextCredentialOnAnotherHostIsWarnedAbout(t *testing.T) {
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	// A host that is not loopback, and never dialed: the warning is about the
	// configuration rather than about any export succeeding.
	s := startServer(t, withEnv(mirrorEnv(m), map[string]string{
		"LIBGEN_MCP_TELEMETRY":        "1",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector.invalid:4318",
		"OTEL_EXPORTER_OTLP_HEADERS":  "authorization=Bearer%20" + collectorCredential,
		"OTEL_BSP_SCHEDULE_DELAY":     "100",
	}))

	logs := s.logs()
	if !strings.Contains(logs, "crosses the network in the clear") {
		t.Errorf("no warning about the plaintext credential:\n%s", tail(logs))
	}
	// The warning must not quote what it is warning about; naming the signals
	// is the whole of what the operator needs.
	if strings.Contains(logs, collectorCredential) {
		t.Errorf("the warning quoted the credential it is about:\n%s", tail(logs))
	}
}

// TestCollector_ALoopbackCredentialIsNotWarnedAbout is what keeps the warning
// worth reading.
//
// A sidecar collector on 127.0.0.1 is the most common local setup there is, and
// a credential that never leaves the machine cannot be observed on a network.
// Warning there would put the line in front of everybody, which is how a
// warning stops being read.
func TestCollector_ALoopbackCredentialIsNotWarnedAbout(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"OTEL_EXPORTER_OTLP_HEADERS": "authorization=Bearer%20" + collectorCredential,
	}))

	if logs := s.logs(); strings.Contains(logs, "crosses the network in the clear") {
		t.Errorf("a loopback collector was warned about:\n%s", tail(logs))
	}
}
