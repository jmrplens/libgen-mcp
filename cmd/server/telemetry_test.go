package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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

	_, stop, err := startTelemetry(t.Context(), &config.Config{})
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
	_, stop, err := startTelemetry(t.Context(), &config.Config{
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

	_, stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
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

	_, stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
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

	_, stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
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

	_, stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
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

	_, stop, err := startTelemetry(t.Context(), &config.Config{
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

// TestResolveIdentityRefusesAPolicyNobodyDefined keeps a misspelled policy a
// startup error rather than a silent fall back to the default.
//
// The default is `none`, so falling back would be safe in the privacy direction
// and wrong in every other: an operator who wrote "pseudonymos" and got nothing
// would conclude the feature does not work, and one who wrote "ful" would
// conclude their collector is broken.
func TestResolveIdentityRefusesAPolicyNobodyDefined(t *testing.T) {
	got, err := resolveIdentity(&config.Config{TelemetryIdentity: "anonymous"})
	if err == nil {
		t.Fatalf("resolveIdentity() = %+v, nil, want a refusal", got)
	}
	if !strings.Contains(err.Error(), config.EnvName("TELEMETRY_IDENTITY")) {
		t.Errorf("the refusal does not name the variable an operator would fix: %v", err)
	}
}

// TestResolveIdentityRefusesARotationOutOfRange is the same rule for the other
// variable this step adds.
func TestResolveIdentityRefusesARotationOutOfRange(t *testing.T) {
	got, err := resolveIdentity(&config.Config{
		TelemetryIdentityRotation: telemetry.MaxKeyRotation + time.Hour,
	})
	if err == nil {
		t.Fatalf("resolveIdentity() = %+v, nil, want a refusal", got)
	}
	if !strings.Contains(err.Error(), config.EnvName("TELEMETRY_IDENTITY_ROTATION")) {
		t.Errorf("the refusal does not name the variable: %v", err)
	}
}

// TestResolveIdentityBuildsTheKeyringEvenUnderNone is the decision that makes
// the item digest work for a deployment that records nothing about its callers.
//
// Under `none` nothing is exported about who called, and the item redactor still
// needs a key — otherwise "this one book fails on every source" and "every
// download is failing" collapse into the same picture, and those two have
// opposite responses.
func TestResolveIdentityBuildsTheKeyringEvenUnderNone(t *testing.T) {
	got, err := resolveIdentity(&config.Config{})
	if err != nil {
		t.Fatalf("resolveIdentity() error = %v", err)
	}
	if got.policy != telemetry.IdentityNone {
		t.Errorf("policy = %q, want the default %q", got.policy, telemetry.IdentityNone)
	}
	if got.keys == nil {
		t.Fatal("no keyring was built under the none policy, so no item could be digested")
	}
	if got.caller == nil || got.item == nil {
		t.Fatal("a redactor is missing")
	}

	// The caller half records nothing, and the item half still digests.
	if attrs := got.caller.Attributes(telemetry.Caller{Address: "203.0.113.7"}); len(attrs) != 0 {
		t.Errorf("the none policy exported %v about a caller", attrs)
	}
	if attrs := got.item.ItemAttributes("9f2b7c1e4a5d6083f1c2b3a4d5e6f708"); len(attrs) != 1 {
		t.Errorf("ItemAttributes = %v, want one digest under the none policy", attrs)
	}
}

// TestResolveIdentityWarnsWhenBothTheKeyAndTheRotationAreSet covers the pairing
// where one of the two settings does nothing.
//
// Which one is not guessable from outside — both are accepted, the server starts,
// and the key simply never rotates — so the operator is told rather than left
// with a mental model that is half wrong.
func TestResolveIdentityWarnsWhenBothTheKeyAndTheRotationAreSet(t *testing.T) {
	logged := captureTelemetryLog(t)

	got, err := resolveIdentity(&config.Config{
		TelemetryIdentityKey:      "a-configured-secret-for-this-case",
		TelemetryIdentityRotation: time.Hour,
	})
	if err != nil {
		t.Fatalf("resolveIdentity() error = %v", err)
	}
	if !got.keys.Configured() {
		t.Fatal("a keyring built from a secret does not report itself configured")
	}
	if got.keys.Rotation() != 0 {
		t.Errorf("Rotation() = %s, want zero: a configured key does not rotate here", got.keys.Rotation())
	}

	out := messages(logged())
	if !strings.Contains(out, "rotation interval is ignored") {
		t.Errorf("nothing said that the rotation does nothing:\n%s", out)
	}
	if !strings.Contains(out, config.EnvName("TELEMETRY_IDENTITY_ROTATION")) {
		t.Errorf("the warning does not name the setting being ignored:\n%s", out)
	}
	// The secret is not in the line. It is the one value in this whole feature
	// that must never be logged, since it is what makes a digest reversible.
	if strings.Contains(out, "a-configured-secret-for-this-case") {
		t.Errorf("the pseudonymisation secret was written to the log:\n%s", out)
	}
}

// TestIdentityChoiceSaysWhereTheKeyCameFrom pins the startup line's most
// consequential field.
//
// Whether the key is configured decides whether one caller has one digest across
// a fleet or one per replica, and the two are indistinguishable from the outside:
// both produce sixteen hex characters. The line is the only place it is visible.
func TestIdentityChoiceSaysWhereTheKeyCameFrom(t *testing.T) {
	configured, err := resolveIdentity(&config.Config{TelemetryIdentityKey: "a-configured-secret"})
	if err != nil {
		t.Fatalf("resolveIdentity() error = %v", err)
	}
	if got := configured.keySource(); !strings.Contains(got, "configured") || !strings.Contains(got, "replicas") {
		t.Errorf("keySource() = %q, want it to say the key is configured and shared", got)
	}
	if got := configured.rotationDescription(); !strings.Contains(got, "process") {
		t.Errorf("rotationDescription() = %q for a configured key, want it to say the key does not rotate", got)
	}

	generated, err := resolveIdentity(&config.Config{TelemetryIdentityRotation: 2 * time.Hour})
	if err != nil {
		t.Fatalf("resolveIdentity() error = %v", err)
	}
	if got := generated.keySource(); !strings.Contains(got, "generated") {
		t.Errorf("keySource() = %q, want it to say the key was generated", got)
	}
	if got := generated.rotationDescription(); got != (2 * time.Hour).String() {
		t.Errorf("rotationDescription() = %q, want the interval", got)
	}
}

// TestStartTelemetryAnnouncesTheIdentityPolicy keeps the policy beside the
// destination in the startup log.
//
// A collector named without a policy leaves the reader to look up what
// "pseudonymous" exports; a policy named without a collector leaves them unable
// to tell where it is going.
func TestStartTelemetryAnnouncesTheIdentityPolicy(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	logged := captureTelemetryLog(t)

	_, stop, err := startTelemetry(t.Context(), &config.Config{
		Telemetry:         true,
		TelemetryIdentity: "pseudonymous",
	})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	t.Cleanup(func() { stop(context.WithoutCancel(t.Context())) })

	out := messages(logged())
	if !strings.Contains(out, "pseudonymous") {
		t.Errorf("the startup log does not name the identity policy:\n%s", out)
	}
	// In words, not only as a mode name.
	if !strings.Contains(out, telemetry.PolicyDescription(telemetry.IdentityPseudonymous)) {
		t.Errorf("the startup log does not say what the policy exports:\n%s", out)
	}
}
