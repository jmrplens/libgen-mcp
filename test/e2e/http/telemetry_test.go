//go:build httpe2e

package httpe2e

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestTelemetry_TheCardTellsACallerWhatIsRecorded is the answer a stranger gets
// without having to ask the operator.
//
// A caller reaching a published endpoint has no relationship with whoever runs
// it, so "am I being traced, and what of mine is kept" is a question with no
// recipient. The card answers it — and deliberately does not answer "where do
// the records go", which names the operator's own infrastructure.
func TestTelemetry_TheCardTellsACallerWhatIsRecorded(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"LIBGEN_MCP_TELEMETRY_IDENTITY": "pseudonymous",
		"LIBGEN_MCP_TELEMETRY_SIGNALS":  "traces,metrics",
	}))

	got := s.do(t, request{method: http.MethodGet, path: serverCardLegacyPath})
	if got.status != http.StatusOK {
		t.Fatalf("GET %s answered %d", serverCardLegacyPath, got.status)
	}

	var card struct {
		Observability *struct {
			Enabled     bool              `json:"enabled"`
			Signals     []string          `json:"signals"`
			Identity    string            `json:"identity"`
			Discloses   string            `json:"discloses"`
			Recorded    map[string]string `json:"recorded"`
			NotRecorded string            `json:"not_recorded"`
		} `json:"observability"`
	}
	if err := json.Unmarshal([]byte(got.body), &card); err != nil {
		t.Fatalf("the card is not JSON: %v", err)
	}
	if card.Observability == nil {
		t.Fatalf("an instrumented deployment published no observability block: %s", got.body)
	}
	if !card.Observability.Enabled {
		t.Error("the block says telemetry is off on a deployment that is exporting")
	}

	// Every field is compared rather than merely found non-empty. These are
	// caller-facing commitments: "some text is here" is satisfied by text that
	// says the wrong thing, which on this block is worse than saying nothing.
	if got, want := card.Observability.Signals, []string{"traces", "metrics"}; !slices.Equal(got, want) {
		t.Errorf("signals = %v, want exactly the two configured %v", got, want)
	}
	if card.Observability.Identity != "pseudonymous" {
		t.Errorf("identity = %q, want the resolved policy", card.Observability.Identity)
	}
	if want := "a keyed digest of the caller's address, with no readable identity"; card.Observability.Discloses != want {
		t.Errorf("discloses = %q, want %q", card.Observability.Discloses, want)
	}
	if want := "search queries, record titles, tool arguments, tool results, " +
		"and any credential supplied for a single call"; card.Observability.NotRecorded != want {
		t.Errorf("not_recorded = %q, want %q", card.Observability.NotRecorded, want)
	}
	// The per-signal claims: the two exported ones carry the sentence that
	// belongs to them, and the one that is off carries nothing at all.
	if _, present := card.Observability.Recorded["logs"]; present {
		t.Errorf("the card claims a signal it does not export: %+v", card.Observability.Recorded)
	}
	if traces := card.Observability.Recorded["traces"]; !strings.Contains(traces, "mirror host it came from") {
		t.Errorf("the traces claim does not describe what a trace carries: %q", traces)
	}
	if metrics := card.Observability.Recorded["metrics"]; !strings.Contains(metrics, "never the mirror host") {
		t.Errorf("the metrics claim does not say what metrics deliberately omit: %q", metrics)
	}
	// The collector's address is the operator's infrastructure, and this
	// document is fetched by whoever asks.
	if strings.Contains(got.body, c.URL) {
		t.Errorf("the card names the collector: %s", got.body)
	}
}

// TestTelemetry_TheCardOmitsTheBlockWhenNothingIsRecorded is the other half,
// and the one every ordinary deployment publishes.
//
// Absent rather than "enabled": false, so a consumer never has to parse a
// negation to learn that nothing is recorded.
func TestTelemetry_TheCardOmitsTheBlockWhenNothingIsRecorded(t *testing.T) {
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})
	s := startServer(t, mirrorEnv(m))

	got := s.do(t, request{method: http.MethodGet, path: serverCardLegacyPath})
	if got.status != http.StatusOK {
		t.Fatalf("GET %s answered %d", serverCardLegacyPath, got.status)
	}

	var card map[string]any
	if err := json.Unmarshal([]byte(got.body), &card); err != nil {
		t.Fatalf("the card is not JSON: %v", err)
	}
	if _, present := card["observability"]; present {
		t.Errorf("a deployment with telemetry off published an observability block: %s", got.body)
	}
}

// TestTelemetry_TheStartupLineNamesTheCollectorAndThePolicy covers the evidence
// the operator gets, which is the other audience and a different one.
//
// It is at WARN because LIBGEN_MCP_LOG_LEVEL is free to suppress everything
// below it, and this is the only local sign that anything about the deployment
// leaves the machine. The exported copy is no help to somebody looking for
// where it went.
func TestTelemetry_TheStartupLineNamesTheCollectorAndThePolicy(t *testing.T) {
	c := startCollector(t)
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nothing here", http.StatusNotFound)
	})

	s := startServer(t, withEnv(mirrorEnv(m), collectorEnv(c), map[string]string{
		"LIBGEN_MCP_LOG_LEVEL":          "warn",
		"LIBGEN_MCP_TELEMETRY_IDENTITY": "pseudonymous",
	}))

	logs := s.logs()
	for _, want := range []string{"telemetry enabled", c.URL, "pseudonymous", "keyed digest"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(logs, want) {
				t.Errorf("the startup log does not carry %q, at a level a warn deployment keeps:\n%s", want, tail(logs))
			}
		})
	}
}
