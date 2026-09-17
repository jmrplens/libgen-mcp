package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/telemetry"
)

// collectorCredentialFixture stands in for the collector credential the
// plaintext warning is about.
//
// Worded as a fixture rather than as a plausible token: a string shaped like a
// real credential in a test file is what a secret scanner reports, and a
// repository whose scanner cries wolf over its own fixtures is one where the
// next real finding is dismissed.
const collectorCredentialFixture = "not-a-credential-only-a-test-fixture-9f3a"

// captureTelemetryLog collects the records this wiring writes while a test runs.
func captureTelemetryLog(t *testing.T) func() []slog.Record {
	t.Helper()

	sink := &recordSink{}
	previous := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return sink.records
}

// recordSink is a [slog.Handler] that keeps every record.
type recordSink struct {
	mu   sync.Mutex
	kept []slog.Record
}

// Enabled reports that every level is handled, so a case about a WARN is not
// filtered before it is seen.
func (s *recordSink) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps the record.
func (s *recordSink) Handle(_ context.Context, record slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kept = append(s.kept, record.Clone())
	return nil
}

// WithAttrs returns the sink unchanged; it keeps no attributes of its own.
func (s *recordSink) WithAttrs([]slog.Attr) slog.Handler { return s }

// WithGroup returns the sink unchanged; it keeps no groups.
func (s *recordSink) WithGroup(string) slog.Handler { return s }

// records returns what has been kept so far.
func (s *recordSink) records() []slog.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]slog.Record(nil), s.kept...)
}

// messages renders the kept records as one string, for a contains assertion.
func messages(records []slog.Record) string {
	var b strings.Builder
	for _, record := range records {
		b.WriteString(record.Level.String())
		b.WriteString(" ")
		b.WriteString(record.Message)
		record.Attrs(func(attr slog.Attr) bool {
			b.WriteString(" ")
			b.WriteString(attr.Key)
			b.WriteString("=")
			b.WriteString(attr.Value.String())
			return true
		})
		b.WriteString("\n")
	}
	return b.String()
}

// TestStartTelemetryOffIsSilentAndStillStoppable is the ordinary deployment,
// which is every deployment that did not ask for telemetry.
//
// The stop function has to be usable in that state, because the caller defers it
// unconditionally: a nil check on the exit path is the worst place to put one,
// since it runs after the work already succeeded.
func TestStartTelemetryOffIsSilentAndStillStoppable(t *testing.T) {
	logged := captureTelemetryLog(t)

	stop, err := startTelemetry(t.Context(), &config.Config{})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	if stop == nil {
		t.Fatal("startTelemetry() returned no stop function")
	}
	stop(t.Context())

	if got := messages(logged()); strings.Contains(got, "telemetry") {
		t.Errorf("a deployment that asked for nothing was told about telemetry:\n%s", got)
	}
}

// TestStartTelemetryRefusesASignalListThatDoesNotParse keeps a bad value a
// startup error.
//
// It is the one telemetry failure that is not swallowed, and the asymmetry is
// deliberate: a collector that cannot be reached is the network's problem and a
// server that can still reach the mirrors keeps serving, but a variable this
// server defines and cannot read is a deployment that does not match its own
// configuration — which must not reach production looking healthy.
func TestStartTelemetryRefusesASignalListThatDoesNotParse(t *testing.T) {
	stop, err := startTelemetry(t.Context(), &config.Config{
		Telemetry:        true,
		TelemetrySignals: "traces,metric",
	})
	if err == nil {
		t.Fatal("startTelemetry() = nil error for an unknown signal, want a refusal")
	}
	if stop != nil {
		t.Error("startTelemetry() returned a stop function alongside its error")
	}
	// The variable rather than the field name: it is what an operator would go
	// and fix.
	if !strings.Contains(err.Error(), config.EnvName("TELEMETRY_SIGNALS")) {
		t.Errorf("the refusal does not name the variable: %v", err)
	}
}

// TestStartTelemetryAnnouncesWhereTheBatchesGo covers the one line an operator
// has to find the collector by.
//
// At WARN rather than INFO, because this is the only local evidence that
// anything about this deployment leaves the machine and LOG_LEVEL is free to
// suppress everything below it. A deployment running at warn would otherwise
// export every call with nothing on its own stderr naming the destination, and
// the exported copy is no help to somebody looking for where it went.
func TestStartTelemetryAnnouncesWhereTheBatchesGo(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	logged := captureTelemetryLog(t)

	stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	t.Cleanup(func() { stop(context.WithoutCancel(t.Context())) })

	var announcement *slog.Record
	for _, record := range logged() {
		if record.Message == "telemetry enabled" {
			announcement = &record
			break
		}
	}
	if announcement == nil {
		t.Fatalf("nothing announced that telemetry was on:\n%s", messages(logged()))
	}
	if announcement.Level != slog.LevelWarn {
		t.Errorf("the announcement is at %s, want WARN: a deployment running at warn would export with nothing on its own stderr naming the collector", announcement.Level)
	}
	if got := messages([]slog.Record{*announcement}); !strings.Contains(got, "127.0.0.1:4318") {
		t.Errorf("the announcement does not name the endpoint: %s", got)
	}
}

// TestStartTelemetryWarnsAboutAPlaintextCredential is the same sentence the
// documentation carries, delivered at the moment it applies.
//
// It is a warning and never a refusal: a collector on a trusted private network
// reached over plaintext is a legitimate deployment, and the endpoint and the
// headers are both the operator's own configuration. What is this server's call
// is not letting the mistake be silent.
func TestStartTelemetryWarnsAboutAPlaintextCredential(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector.example.org:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-fixture-header="+collectorCredentialFixture)
	logged := captureTelemetryLog(t)

	stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	t.Cleanup(func() { stop(context.WithoutCancel(t.Context())) })

	got := messages(logged())
	if !strings.Contains(got, "crosses the network in the clear") {
		t.Errorf("no warning about the plaintext credential:\n%s", got)
	}
	// The credential itself is not in the line; naming the signals is the whole
	// of what the operator needs.
	if strings.Contains(got, collectorCredentialFixture) {
		t.Errorf("the warning quoted the credential it is warning about:\n%s", got)
	}
}

// TestStartTelemetrySaysNothingAboutALoopbackCredential is the other half, and
// the one that keeps the warning worth reading.
//
// A credential that never leaves the machine cannot be observed on a network, so
// a sidecar collector on 127.0.0.1 is not a disclosure. Warning there would put
// the line in front of the most common local setup there is, which is how a
// warning stops being read.
func TestStartTelemetrySaysNothingAboutALoopbackCredential(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-fixture-header="+collectorCredentialFixture)
	logged := captureTelemetryLog(t)

	stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	t.Cleanup(func() { stop(context.WithoutCancel(t.Context())) })

	if got := messages(logged()); strings.Contains(got, "crosses the network in the clear") {
		t.Errorf("a loopback collector was warned about:\n%s", got)
	}
}

// TestStartTelemetryHonorsTheSpecificationKillSwitch pins the veto.
//
// OTEL_SDK_DISABLED cannot be this server's on switch — its specified default
// means "the SDK is enabled" while telemetry here is off until asked — so it
// composes instead: our variable turns telemetry on and this overrides. Nothing
// beneath us implements it, so a deployment that sets it is relying on this.
func TestStartTelemetryHonorsTheSpecificationKillSwitch(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	logged := captureTelemetryLog(t)

	stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	t.Cleanup(func() { stop(context.WithoutCancel(t.Context())) })

	if got := messages(logged()); strings.Contains(got, "telemetry enabled") {
		t.Errorf("telemetry started although OTEL_SDK_DISABLED is set:\n%s", got)
	}
}

// TestStartTelemetrySelectsTheSignalsItWasGiven checks the list reaches the
// providers rather than stopping at the parser.
func TestStartTelemetrySelectsTheSignalsItWasGiven(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	logged := captureTelemetryLog(t)

	stop, err := startTelemetry(t.Context(), &config.Config{
		Telemetry:        true,
		TelemetrySignals: "traces",
	})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	t.Cleanup(func() { stop(context.WithoutCancel(t.Context())) })

	got := messages(logged())
	if !strings.Contains(got, "traces") {
		t.Errorf("the announcement does not name the selected signal:\n%s", got)
	}
	if strings.Contains(got, "metrics") || strings.Contains(got, "logs") {
		t.Errorf("a signal that was not selected was announced as enabled:\n%s", got)
	}
}

// TestParseSignalsIsWhatTheServerReads keeps the wiring and the parser from
// drifting: the default is all three, which is what a deployment that turned
// telemetry on without saying more should get.
func TestParseSignalsIsWhatTheServerReads(t *testing.T) {
	got, err := telemetry.ParseSignals("")
	if err != nil {
		t.Fatalf("ParseSignals(\"\") error = %v", err)
	}
	if got != telemetry.AllSignals() {
		t.Errorf("an unset selection = %+v, want all three", got)
	}
}
