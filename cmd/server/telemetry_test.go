package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/logging"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/netguard"
	"github.com/jmrplens/libgen-mcp/internal/telemetry"
	"github.com/jmrplens/libgen-mcp/internal/transport"
)

// collectorCredentialFixture stands in for the collector credential the
// plaintext warning is about.
//
// Worded as a fixture rather than as a plausible token: a string shaped like a
// real credential in a test file is what a secret scanner reports, and a
// repository whose scanner cries wolf over its own fixtures is one where the
// next real finding is dismissed.
const collectorCredentialFixture = "not-a-credential-only-a-test-fixture-9f3a"

// captureTelemetryLog redirects the server's own log stream to a buffer and
// returns what has been written to it.
//
// The stream rather than the default logger, and that is forced rather than
// preferred: installing the telemetry bridge rebuilds the stderr handler, so a
// substituted slog.Default() is replaced at the exact moment the wiring under
// test does its job. A test built that way would assert on a logger the server
// had stopped using, and would keep passing while the bridge sent everything
// somewhere else.
func captureTelemetryLog(t *testing.T) func() string {
	t.Helper()

	stream := &syncBuffer{}
	t.Cleanup(logging.SetDestination(stream))
	previous := slog.Default()
	// Debug, so a case about a level is decided by the assertion rather than by
	// the capture.
	logging.Setup(slog.LevelDebug)
	t.Cleanup(func() { slog.SetDefault(previous) })
	return stream.String
}

// syncBuffer is a buffer safe to write from the goroutines a shutdown flush
// runs on.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write appends to the buffer.
func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns everything written so far.
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// findLogRecord returns the first JSON record in the stream with this message.
//
// Parsed rather than matched as a substring, because two of the assertions here
// are about a record's severity and its fields rather than about the words in
// it, and "WARN" appears in a line that merely mentions one.
func findLogRecord(t *testing.T, stream, message string) (map[string]any, bool) {
	t.Helper()

	for line := range strings.SplitSeq(strings.TrimSpace(stream), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("the log stream is not JSON, which every client depends on: %q: %v", line, err)
		}
		if record["msg"] == message {
			return record, true
		}
	}
	return nil, false
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

	if got := logged(); strings.Contains(got, "telemetry") {
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

	announcement, found := findLogRecord(t, logged(), "telemetry enabled")
	if !found {
		t.Fatalf("nothing announced that telemetry was on:\n%s", logged())
	}
	if announcement["level"] != "WARN" {
		t.Errorf("the announcement is at %v, want WARN: a deployment running at warn would export with nothing on its own stderr naming the collector", announcement["level"])
	}
	if endpoint, _ := announcement["endpoint"].(string); endpoint != "http://127.0.0.1:4318" {
		t.Errorf("the announcement does not name the endpoint: %v", announcement)
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

	got := logged()
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

	if got := logged(); strings.Contains(got, "crosses the network in the clear") {
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

	if got := logged(); strings.Contains(got, "telemetry enabled") {
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

	got := logged()
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

	out := logged()
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

	out := logged()
	if !strings.Contains(out, "pseudonymous") {
		t.Errorf("the startup log does not name the identity policy:\n%s", out)
	}
	// In words, not only as a mode name.
	if !strings.Contains(out, telemetry.PolicyDescription(telemetry.IdentityPseudonymous)) {
		t.Errorf("the startup log does not say what the policy exports:\n%s", out)
	}
}

// recordingCollector is an OTLP/HTTP endpoint that keeps every payload byte for
// byte.
//
// It decodes nothing. The question these tests ask is whether a given string
// left the process at all, and a decoder that understood the payload could only
// answer it for the fields it knew about — which is the wrong shape for a check
// about what must never be there.
func recordingCollector(t *testing.T) (url string, payloads func() string) {
	t.Helper()

	var received syncBuffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			unzipped, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Errorf("the collector could not read a gzipped payload: %v", err)
				w.WriteHeader(http.StatusOK)
				return
			}
			defer func() { _ = unzipped.Close() }()
			reader = unzipped
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Errorf("reading the payload: %v", err)
		}
		_, _ = received.Write(body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server.URL, received.String
}

// TestALogRecordReachesTheCollectorOnlyAfterTheBridgeIsInstalled is the wire
// order, driven rather than read.
//
// Two things about this wiring are invisible from inside the process, and both
// are the kind that survive review. The first is that the logs signal is only
// real once something writes into it: the provider can be installed, "logs" can
// be announced at startup and published on the server card, and no record ever
// exported. The second is that the startup announcement — the one line saying
// what this deployment exports about its callers — is written before the bridge
// unless somebody put it after, in which case it reaches stderr alone, which is
// the one place an operator running several replicas is not looking.
//
// So the assertions are a real OTLP endpoint and three strings: a record from
// before, a record from after, and the announcement itself.
func TestALogRecordReachesTheCollectorOnlyAfterTheBridgeIsInstalled(t *testing.T) {
	endpoint, payloads := recordingCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
	captureTelemetryLog(t)

	slog.Warn("a-record-from-before-the-bridge")

	_, stop, err := startTelemetry(t.Context(), &config.Config{
		Telemetry:        true,
		TelemetrySignals: "logs",
	})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	slog.Warn("a-record-from-after-the-bridge")

	// The flush: the batch processor exports on shutdown, so this is what puts
	// the records on the wire.
	stop(context.WithoutCancel(t.Context()))

	exported := payloads()
	if !strings.Contains(exported, "a-record-from-after-the-bridge") {
		t.Errorf("no record reached the collector, so the logs signal exports nothing:\n%s", exported)
	}
	if strings.Contains(exported, "a-record-from-before-the-bridge") {
		t.Error("a record written before the bridge was exported, which cannot happen and means this test proves nothing about order")
	}
	if !strings.Contains(exported, "telemetry enabled") {
		t.Error("the startup announcement did not reach the collector, so it was written before the bridge")
	}
}

// TestTheBridgeIsNotInstalledForASignalNobodyAskedFor keeps the wrapper off the
// log path of a deployment that exports traces alone.
//
// With the logs signal off, the global logger provider is still the no-op one,
// so bridging would cost every record in the process a trip through a handler
// that discards it — on the hot path of a server whose logs are its only local
// evidence.
func TestTheBridgeIsNotInstalledForASignalNobodyAskedFor(t *testing.T) {
	endpoint, payloads := recordingCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
	captureTelemetryLog(t)

	_, stop, err := startTelemetry(t.Context(), &config.Config{
		Telemetry:        true,
		TelemetrySignals: "traces",
	})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	slog.Warn("a-record-under-traces-only")
	stop(context.WithoutCancel(t.Context()))

	if strings.Contains(payloads(), "a-record-under-traces-only") {
		t.Error("a log record was exported although the logs signal was not selected")
	}
}

// TestStoppingTelemetryTakesTheBridgeBackOut is what keeps a stopped exporter
// from outliving the provider it belongs to.
//
// The default logger is a process global. Leaving the bridge installed after
// shutdown routes every later record at a logger provider that has been shut
// down — in a test binary, one test's collector receiving the rest of the suite.
func TestStoppingTelemetryTakesTheBridgeBackOut(t *testing.T) {
	endpoint, _ := recordingCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
	captureTelemetryLog(t)

	before := slog.Default()
	_, stop, err := startTelemetry(t.Context(), &config.Config{
		Telemetry:        true,
		TelemetrySignals: "logs",
	})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	if slog.Default() == before {
		t.Fatal("the default logger was not replaced, so nothing bridges the log records")
	}
	stop(context.WithoutCancel(t.Context()))

	if slog.Default() != before {
		t.Error("the bridge outlived the provider it writes into")
	}
}

// TestAnUnusableIdentityPolicyStopsStartup is the rule this surface applies to
// every variable it defines, reaching the one place it can be observed.
//
// A value that is set and cannot be parsed is an error rather than a warning,
// because falling back to the default in silence is how a deployment that does
// not match its own configuration survives to production — and the identity
// policy is the setting where that is worst: the operator believes they turned
// caller identity off.
//
// It refuses whether or not telemetry is on. The variable was set by somebody
// who meant something by it.
func TestAnUnusableIdentityPolicyStopsStartup(t *testing.T) {
	t.Setenv(config.EnvName("TELEMETRY_IDENTITY"), "anonymous")
	stubStdinEOF(t)

	var err error
	awaitReturn(t, func() {
		err = run(canceledContext(), listenSpec{}, transport.DefaultOptions(), transportDecision{})
	})

	if err == nil {
		t.Fatal("run() served a deployment whose identity policy nobody could parse")
	}
	if !strings.Contains(err.Error(), config.EnvName("TELEMETRY_IDENTITY")) {
		t.Errorf("the refusal does not name the variable an operator would fix: %v", err)
	}
}

// TestTheOutboundMetricNamesTheHostsThisDeploymentReaches keeps the closed set
// from closing on nothing.
//
// The dimension is bounded so a caller cannot mint a time series by causing a
// fetch, and everything outside the set is recorded as one bucket. Leaving the
// built-in mirror families out of it does not make that safer: it puts every
// catalog request and every download this server makes on its ordinary path into
// the bucket reserved for hosts nobody configured, which is the same as not
// having the metric.
func TestTheOutboundMetricNamesTheHostsThisDeploymentReaches(t *testing.T) {
	got := outboundMetricHosts(&config.Config{
		Mirror:      "https://libgen.example/index.php",
		ScihubHosts: []string{"sci-hub.example"},
	})

	for _, want := range []string{"libgen.example", "sci-hub.example"} {
		t.Run(want, func(t *testing.T) {
			if !slices.Contains(got, want) {
				t.Errorf("hosts = %v, want the configured %q", got, want)
			}
		})
	}
	// A URL never reaches the comparison, which is against a hostname: an entry
	// with a scheme in it is one that can never match, so a set full of them is
	// a set of nothing.
	for _, host := range got {
		if strings.Contains(host, "/") {
			t.Errorf("hosts = %v, want hostnames: %q can never match a request's host", got, host)
		}
	}
	// The families this server ships with, which are what an unconfigured
	// deployment actually reaches.
	families := outboundMetricHosts(&config.Config{})
	if len(families) == 0 {
		t.Fatal("an unconfigured deployment names no hosts, so every request it makes is recorded as _OTHER")
	}
	// One from each built-in family's own definition: the page a mirror list is
	// discovered from, and a mirror the chain reaches on the ordinary path.
	for _, want := range []string{"shadowlibraries.github.io", mirrors.LibgenFamily.Preferred} {
		t.Run(want, func(t *testing.T) {
			host := want
			if parsed, err := url.Parse(want); err == nil && parsed.Hostname() != "" {
				host = parsed.Hostname()
			}
			if !slices.Contains(families, host) {
				t.Errorf("hosts = %v, want the built-in mirror family host %q", families, host)
			}
		})
	}
}

// TestAFailedStartStillTakesTheObserverBackOut covers the exit nobody looks at.
//
// The outbound observer is installed before the providers, deliberately, because
// it is also what strips trace context from every outbound request. That makes
// the failure path the one that leaks it: the process-global wrapper stays
// installed for the life of the process, which in a test binary is one test's
// telemetry reaching every later test's clients.
func TestAFailedStartStillTakesTheObserverBackOut(t *testing.T) {
	// A wrapper of the test's own, so what is asserted is which observer is
	// installed rather than whether one is.
	var observed atomic.Bool
	t.Cleanup(netguard.SetOutboundObserver(func(base http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			observed.Store(true)
			return base.RoundTrip(r)
		})
	}))

	// A protocol nothing implements is the startup failure that needs no
	// network: it is refused where the exporters are built.
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "not-a-protocol")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	logged := captureTelemetryLog(t)

	_, stop, err := startTelemetry(t.Context(), &config.Config{Telemetry: true})
	if err != nil {
		t.Fatalf("startTelemetry() error = %v", err)
	}
	stop(context.WithoutCancel(t.Context()))

	// Asserted, because this whole case is about the failure path: if the start
	// succeeded, the restore under test is the one on the ordinary path and the
	// rest of this proves nothing.
	if _, failed := findLogRecord(t, logged(), "telemetry disabled: it could not be started"); !failed {
		t.Fatalf("telemetry started, so this test did not drive the failure path:\n%s", logged())
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	client := netguard.ClientFor(time.Second, netguard.NewPolicy(nil, true))
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the request failed: %v", err)
	}
	_ = response.Body.Close()

	if !observed.Load() {
		t.Error("the test's own observer was not restored, so the failed start left its wrapper installed for the rest of the process")
	}
}

// roundTripperFunc adapts a function to [http.RoundTripper].
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements [http.RoundTripper].
func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
