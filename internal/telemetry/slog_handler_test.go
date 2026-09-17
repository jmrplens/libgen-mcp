package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// recordingExporter keeps every log record the SDK exports, so a test can read
// back what a collector would have received.
//
// Written here rather than pulled from a test module: one interface with three
// methods is cheaper than a dependency, and this way the assertions run against
// the same Exporter contract the real OTLP exporter implements.
type recordingExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *recordingExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.records = append(e.records, records...)
	return nil
}

func (e *recordingExporter) Shutdown(context.Context) error   { return nil }
func (e *recordingExporter) ForceFlush(context.Context) error { return nil }

func (e *recordingExporter) all() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdklog.Record(nil), e.records...)
}

// bridged installs a recording logger provider and returns a logger writing
// through the fan-out handler, the recorder, and the stderr leg's bytes.
func bridged(t *testing.T, level slog.Level) (*slog.Logger, *recordingExporter, *bytes.Buffer) {
	t.Helper()

	exporter := &recordingExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	previous := global.GetLoggerProvider()
	global.SetLoggerProvider(provider)
	t.Cleanup(func() { global.SetLoggerProvider(previous) })

	var stderr bytes.Buffer
	handler := NewSlogHandler(slog.NewJSONHandler(&stderr, &slog.HandlerOptions{Level: level}), DefaultLogSeverity)
	return slog.New(handler), exporter, &stderr
}

// rendered flattens one exported record into every byte a collector would hold,
// so a test can ask whether a planted value appears anywhere in it at all.
//
// Anywhere is the point: a value that must not leave the process must not leave
// it as a body, as an attribute, as a nested attribute, or as an error the
// bridge promoted into an exception field.
func rendered(record sdklog.Record) string {
	var out strings.Builder
	out.WriteString(record.Body().AsString())
	record.WalkAttributes(func(kv attribute.KeyValue) bool {
		// String, not AsString: AsString returns the string field alone and
		// renders nothing for a map or a slice, so a planted value that
		// survived one group deep would be absent from what this returns while
		// being present in what the collector holds — a leak assertion that
		// passes because it cannot see. String renders composite values
		// recursively.
		out.WriteString("\x00" + string(kv.Key) + "=" + kv.Value.String())
		return true
	})
	return out.String()
}

// exportedAttr returns one attribute of an exported record.
func exportedAttr(t *testing.T, record sdklog.Record, key string) (attribute.Value, bool) {
	t.Helper()

	var value attribute.Value
	var found bool
	record.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == key {
			value, found = kv.Value, true
			return false
		}
		return true
	})
	return value, found
}

// soleRecord fails unless exactly one record was exported, and returns it.
func soleRecord(t *testing.T, exporter *recordingExporter) sdklog.Record {
	t.Helper()

	records := exporter.all()
	if len(records) != 1 {
		t.Fatalf("exported %d records, want one", len(records))
	}
	return records[0]
}

// TestBothLegsGetTheRecord is the fan-out's whole premise: the collector leg is
// added to stderr rather than replacing it.
//
// The stderr JSON is what a container platform captures and what works with
// telemetry off, which is the default; a bridge that redirected would take that
// away from every deployment that turned telemetry on.
func TestBothLegsGetTheRecord(t *testing.T) {
	logger, exporter, stderr := bridged(t, slog.LevelInfo)

	logger.Info("source resolved", "source", "libgen")

	if !strings.Contains(stderr.String(), "source resolved") {
		t.Errorf("stderr = %q, want the record", stderr.String())
	}
	record := soleRecord(t, exporter)
	if body := record.Body().AsString(); body != "source resolved" {
		t.Errorf("exported body = %q, want the record", body)
	}
}

// TestTheExportFloorIsNotTheLogLevel pins the two dials apart in both
// directions.
//
// They are separate settings that a single delegation quietly fuses. Asking the
// stderr handler alone in Enabled is the shape that does it: at warn no INFO
// record ever reaches Handle, so the export floor this package documents becomes
// LIBGEN_MCP_LOG_LEVEL and the documentation is false in exactly the deployment
// that reads it — a quiet terminal shipping to a collector.
func TestTheExportFloorIsNotTheLogLevel(t *testing.T) {
	t.Run("an operator at warn still exports info", func(t *testing.T) {
		logger, exporter, stderr := bridged(t, slog.LevelWarn)

		logger.Info("source resolved")

		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing at warn", stderr.String())
		}
		if got := len(exporter.all()); got != 1 {
			t.Errorf("exported %d records, want the info record: the log level swallowed the export", got)
		}
	})

	t.Run("an operator at debug does not export debug", func(t *testing.T) {
		logger, exporter, stderr := bridged(t, slog.LevelDebug)

		logger.Debug("fetching")

		if !strings.Contains(stderr.String(), "fetching") {
			t.Errorf("stderr = %q, want the debug record", stderr.String())
		}
		if got := len(exporter.all()); got != 0 {
			t.Errorf("exported %d records, want none: debugging on a terminal must not ship a record per fetch", got)
		}
	})
}

// TestAStrippedFieldNeverLeavesTheProcess covers the list [ExportStrippedFields]
// exists for, on the leg it exists for.
//
// Each case is a field whose disclosure differs: what somebody searched for,
// what they were given, the credential they supplied for one call, and the
// address this deployment charged them to. The stderr copy keeps all of it — it
// is the operator's own terminal — and that half is asserted too, because a
// bridge that satisfied this by dropping the field everywhere would be a
// different, worse change.
func TestAStrippedFieldNeverLeavesTheProcess(t *testing.T) {
	for _, tc := range []struct {
		field string
		value string
	}{
		{LogFieldQuery, "the-planted-search-terms"},
		{LogFieldTitle, "the-planted-record-title"},
		{LogFieldAnnasKey, "the-planted-members-key"},
		{LogFieldUnpaywallEmail, "planted@example.org"},
		{LogFieldChargedAddress, "203.0.113.7"},
		{LogFieldPanic, "runtime error: the-planted-panic-value"},
		{LogFieldStack, "goroutine 1 [running]: /home/planted/path/main.go:42"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			logger, exporter, stderr := bridged(t, slog.LevelInfo)

			logger.Info("search served", tc.field, tc.value)
			// The same field one group deep, which is an ordinary value any
			// caller can pass and was enough to walk a governed field past a
			// flat name check.
			logger.Info("search served", slog.Group("request", tc.field, tc.value))

			for _, record := range exporter.all() {
				if strings.Contains(rendered(record), tc.value) {
					t.Errorf("%s reached the collector: %s", tc.field, rendered(record))
				}
			}
			if strings.Count(stderr.String(), tc.value) != 2 {
				t.Errorf("stderr lost the field; it is the operator's own terminal: %s", stderr.String())
			}
		})
	}
}

// TestAnErrorExportsItsTypeAndNotItsText is the rule that keeps a third party's
// bytes off the wire.
//
// The bridge promotes an error-valued attribute into an exception field equal to
// err.Error(), and on this server that string is net/http's "GET url: reason" —
// the mirror, and a path that for a download is the item identifier the item
// policy exists to digest. A type name is a compile-time constant and can carry
// no request data.
func TestAnErrorExportsItsTypeAndNotItsText(t *testing.T) {
	logger, exporter, stderr := bridged(t, slog.LevelInfo)

	const md5 = "87a4ebdaf21fa6cc70009a3dd63194ee"
	failure := &url.Error{
		Op:  "Get",
		URL: "https://mirror.example.org/get.php?md5=" + md5,
		Err: errors.New("connection refused"),
	}
	logger.Info("source failed, advancing", "source", "libgen", "error", fmt.Errorf("resolve: %w", failure))

	record := soleRecord(t, exporter)
	if strings.Contains(rendered(record), md5) {
		t.Errorf("the item identifier reached the collector inside the error: %s", rendered(record))
	}
	value, found := exportedAttr(t, record, "error")
	if !found {
		t.Fatalf("the error attribute vanished entirely: %s", rendered(record))
	}
	if value.AsString() != "*url.Error" {
		t.Errorf("exported error = %q, want the type: a generic wrapper classifies nothing", value.AsString())
	}
	if !strings.Contains(stderr.String(), md5) {
		t.Error("stderr lost the URL; the operator's own terminal keeps the whole error")
	}
}

// TestAnOversizedValueIsBoundedAndSaysSo keeps an attribute from being sized by
// whoever supplied its content.
func TestAnOversizedValueIsBoundedAndSaysSo(t *testing.T) {
	logger, exporter, _ := bridged(t, slog.LevelInfo)

	logger.Info("search refused", "reason", strings.Repeat("a", maxExportedAttrValue*3))

	value, found := exportedAttr(t, soleRecord(t, exporter), "reason")
	if !found {
		t.Fatal("the attribute vanished")
	}
	got := value.AsString()
	if len(got) > maxExportedAttrValue+len("[truncated]") {
		t.Errorf("exported %d bytes, want the bound of %d", len(got), maxExportedAttrValue)
	}
	if !strings.HasSuffix(got, "[truncated]") {
		t.Error("the value was shortened without saying so, which reads as the whole value")
	}
}

// TestACutValueIsStillValidUTF8 is the batch-dropping case.
//
// The bound is a byte count, so a cut lands inside a multi-byte character
// whenever one straddles it. proto.Marshal validates every string field, so one
// partial rune fails the upload for the whole batch — every record in it, from
// every caller, dropped for one truncation.
func TestACutValueIsStillValidUTF8(t *testing.T) {
	logger, exporter, _ := bridged(t, slog.LevelInfo)

	// Three-byte runes, so the cut at maxExportedAttrValue cannot fall on a
	// boundary: 1024 is not divisible by three.
	logger.Info("search refused", "reason", strings.Repeat("☃", maxExportedAttrValue))

	value, found := exportedAttr(t, soleRecord(t, exporter), "reason")
	if !found {
		t.Fatal("the attribute vanished")
	}
	if !utf8.ValidString(value.AsString()) {
		t.Error("the exported value is not valid UTF-8, so the batch it rides in is dropped whole")
	}
}

// TestAValueThatArrivedInvalidIsRepaired covers the other half: what arrives is
// not this server's to trust for encoding either.
//
// A title from a third-party catalog is bytes somebody else chose, and a short
// one is never truncated — so the repair cannot live only on the truncation
// path.
func TestAValueThatArrivedInvalidIsRepaired(t *testing.T) {
	logger, exporter, _ := bridged(t, slog.LevelInfo)

	logger.Info("record listed", "author", "Bell\xffLabs")

	value, found := exportedAttr(t, soleRecord(t, exporter), "author")
	if !found {
		t.Fatal("the attribute vanished")
	}
	if !utf8.ValidString(value.AsString()) {
		t.Errorf("exported %q, which is not valid UTF-8", value.AsString())
	}
}

// TestADerivedLoggerIsGovernedToo closes the bypass that WithAttrs is.
//
// Attributes attached with slog.With never pass through Handle's copy: they are
// attached to the handler. A component logger built the ordinary way would
// otherwise export whatever it attached, under every policy, for the life of the
// process.
func TestADerivedLoggerIsGovernedToo(t *testing.T) {
	logger, exporter, _ := bridged(t, slog.LevelInfo)

	component := logger.With(LogFieldQuery, "the-planted-search-terms")
	component.Info("search served")

	record := soleRecord(t, exporter)
	if strings.Contains(rendered(record), "the-planted-search-terms") {
		t.Errorf("an attached field bypassed the rules: %s", rendered(record))
	}
}

// TestARecordInsideASpanCarriesItsTrace is the whole reason for the OTLP leg.
//
// Correlating a record with the span it happened inside is what stderr cannot do
// at all; a bridge that exported records with no trace context would be a second
// copy of the log stream and nothing more.
func TestARecordInsideASpanCarriesItsTrace(t *testing.T) {
	logger, exporter, _ := bridged(t, slog.LevelInfo)

	tracer := sdktrace.NewTracerProvider().Tracer("test")
	ctx, span := tracer.Start(context.Background(), "download")
	defer span.End()

	logger.InfoContext(ctx, "source resolved")

	record := soleRecord(t, exporter)
	if !record.TraceID().IsValid() {
		t.Error("the exported record carries no trace, so it cannot be joined to the call it belongs to")
	}
	if record.TraceID() != span.SpanContext().TraceID() {
		t.Errorf("record trace = %s, span trace = %s", record.TraceID(), span.SpanContext().TraceID())
	}
}

// TestNewSlogHandlerDeclinesANilBase keeps a caller that has nothing to wrap
// from installing a handler with no stderr leg.
func TestNewSlogHandlerDeclinesANilBase(t *testing.T) {
	t.Parallel()

	if got := NewSlogHandler(nil, DefaultLogSeverity); got != nil {
		t.Errorf("NewSlogHandler(nil) = %v, want nil: there is no known-safe handler to fan out from", got)
	}
}

// TestAGroupedLoggerKeepsBothLegs covers WithGroup for the same reason as
// WithAttrs: a handler that forwarded one leg and not the other would work until
// somebody grouped, which is what the SDK logger wrapper does on every record.
func TestAGroupedLoggerKeepsBothLegs(t *testing.T) {
	logger, exporter, stderr := bridged(t, slog.LevelInfo)

	logger.WithGroup("sdk").Info("session connected", "session", "abc")

	if !strings.Contains(stderr.String(), `"sdk":{"session":"abc"}`) {
		t.Errorf("stderr = %q, want the grouped attribute", stderr.String())
	}
	if value, found := exportedAttr(t, soleRecord(t, exporter), "sdk"); !found {
		t.Errorf("the exported record lost the group: %v", value)
	}
}

// TestErrorTypeNameWalksPastTheGenericWrappers pins the classification itself.
//
// fmt.Errorf produces *fmt.wrapError for every wrapped error in this tree, so a
// walk that stopped at the first type would classify every failure in the server
// as the same thing, which is the same as classifying none.
func TestErrorTypeNameWalksPastTheGenericWrappers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a bare error", errors.New("plain"), "*errors.errorString"},
		{"a wrapped typed error", fmt.Errorf("a: %w", fmt.Errorf("b: %w", &url.Error{})), "*url.Error"},
		{"nothing but wrappers", fmt.Errorf("a: %w", errors.New("b")), "*errors.errorString"},
		{"a cycle", cyclicError{}, "telemetry.cyclicError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := errorTypeName(tc.err); got != tc.want {
				t.Errorf("errorTypeName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// cyclicError unwraps to itself, which is what bounds the walk: an error chain
// is built by whatever wrapped it, and a cycle is a hang rather than a panic.
type cyclicError struct{}

func (cyclicError) Error() string { return "cyclic" }
func (e cyclicError) Unwrap() error {
	return fmt.Errorf("wrapped: %w", e)
}

// TestTheStderrLegIsWrittenFirst pins the ordering the doc comment promises.
//
// If the bridge ever blocks or panics, the record has to already be somewhere a
// person can read it. A handler that exported first would lose exactly the
// record describing the failure that took it down.
func TestTheStderrLegIsWrittenFirst(t *testing.T) {
	var order []string
	stderr := orderingHandler{note: func() { order = append(order, "stderr") }}

	exporter := &recordingExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(
		notingExporter{inner: exporter, note: func() { order = append(order, "otlp") }},
	)))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	previous := global.GetLoggerProvider()
	global.SetLoggerProvider(provider)
	t.Cleanup(func() { global.SetLoggerProvider(previous) })

	slog.New(NewSlogHandler(stderr, DefaultLogSeverity)).Info("source resolved")

	if len(order) != 2 || order[0] != "stderr" {
		t.Errorf("legs ran %v, want stderr first", order)
	}
}

// orderingHandler is a stderr leg that only records that it ran.
type orderingHandler struct {
	slog.Handler
	note func()
}

func (h orderingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h orderingHandler) Handle(context.Context, slog.Record) error {
	h.note()
	return nil
}
func (h orderingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h orderingHandler) WithGroup(string) slog.Handler      { return h }

// notingExporter records that the collector leg ran.
type notingExporter struct {
	inner *recordingExporter
	note  func()
}

func (e notingExporter) Export(ctx context.Context, records []sdklog.Record) error {
	e.note()
	return e.inner.Export(ctx, records)
}
func (e notingExporter) Shutdown(context.Context) error   { return nil }
func (e notingExporter) ForceFlush(context.Context) error { return nil }

// Ensure the discard handler used above satisfies the interface the same way the
// real one does.
var _ io.Writer = io.Discard
