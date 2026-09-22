// slog_handler.go sends every log record to stderr and, above a floor, to a
// collector.

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/log/global"
)

// scopeName names this bridge as the instrumentation scope on every exported log
// record.
//
// Its own package rather than internal/mcpotel's, because a scope says which
// instrumentation produced a signal: these records come from this server's own
// logger, not from the MCP middleware, and a reader filtering by scope wants to
// be able to tell them apart.
const scopeName = "github.com/jmrplens/libgen-mcp/v2/internal/telemetry"

// DefaultLogSeverity is the floor for records that reach a collector.
//
// Info rather than debug, and for bounded resource use rather than taste: the
// specification's own warning is that "Logging could consume much memory by
// default if the end user application emits too many logs". A debug run of this
// server writes a record per outbound fetch, and exporting those would duplicate
// on the wire what the outbound spans already describe, on top of the spans.
const DefaultLogSeverity = slog.LevelInfo

// NewSlogHandler wraps an existing handler so records go to both stderr and the
// collector.
//
// # Why both, rather than one or the other
//
// The stderr JSON is what an operator reads over somebody's shoulder, what a
// container platform captures, and what works when telemetry is off — which is
// the default and will stay the common case. It cannot be replaced. The OTLP leg
// is what correlates a record with the span it happened inside, which stderr
// cannot do at all.
//
// So this is a fan-out rather than a redirect, and the stderr leg is
// deliberately first: if the bridge ever blocks or panics, the record has
// already been written where somebody can see it.
//
// # The severity floor
//
// The collector leg is filtered and the stderr leg is not.
// LIBGEN_MCP_LOG_LEVEL still governs stderr; this floor governs only what is
// exported, so an operator debugging on their own terminal does not thereby
// start shipping a record per mirror request to a collector.
//
// # What it adds
//
// Nothing. The identity policy, the item policy and every decision about what a
// record may carry live where the record is written, so a field that must not be
// exported must not be logged either. What this does do is subtract: the
// exported copy loses the fields in [ExportStrippedFields], every error's text,
// and anything longer than one attribute's budget. See [exportRecord].
func NewSlogHandler(base slog.Handler, floor slog.Level) slog.Handler {
	if base == nil {
		return nil
	}
	return &fanOutHandler{
		stderr:  base,
		otlp:    otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(global.GetLoggerProvider())),
		otlpMin: floor,
	}
}

// fanOutHandler writes each record to stderr and, above a floor, to OTLP.
type fanOutHandler struct {
	stderr  slog.Handler
	otlp    slog.Handler
	otlpMin slog.Level
}

// Enabled asks each leg on its own.
//
// Delegating to stderr alone would make LIBGEN_MCP_LOG_LEVEL govern the export
// too: at warn, no INFO record would ever reach Handle, and the export floor
// this package documents as separate would silently be the log level. Handle
// then gates each leg again, so neither widens the other.
func (h *fanOutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.stderr.Enabled(ctx, level) {
		return true
	}
	return level >= h.otlpMin
}

// Handle writes to stderr first, then to the collector.
//
// The stderr error is the one returned. An OTLP failure is deliberately
// swallowed: a collector that is down must not turn every log call in this
// server into an error path, and the SDK reports its own export failures through
// the handler installed in diagnostics.go, which is where an operator should
// learn about it.
func (h *fanOutHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
	if h.stderr.Enabled(ctx, record.Level) {
		err = h.stderr.Handle(ctx, record)
	}
	if record.Level >= h.otlpMin {
		_ = h.otlp.Handle(ctx, exportRecord(record))
	}
	return err
}

// exportRecord returns the record as it may leave the process.
//
// Three subtractions, and the copy is made on the exported leg alone so the
// operator's own terminal keeps the whole record:
//
//   - the fields in [ExportStrippedFields], at any depth;
//   - every error's text, replaced by its type (see [errorTypeName]);
//   - every value's length, bounded and made valid UTF-8.
//
// The message is not rewritten. Every call site in this tree passes a compile-
// time constant, the go-sdk's own records do too, and slog offers no other way
// to build one — so nothing a caller chose can be in it, and the values are
// where untrusted content arrives.
func exportRecord(record slog.Record) slog.Record {
	exported := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)

	attrs := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})
	exported.AddAttrs(exportAttrs(attrs)...)
	return exported
}

// exportAttrs returns what the exported copy of an attribute set may carry.
//
// It is the one place that decides, and both suppliers of exported attributes go
// through it: a record's own attributes in [exportRecord], and the attributes a
// derived logger attaches in WithAttrs. Having two paths was the sibling
// project's hole — the attached ones went to the OTLP handler untransformed, so
// any component logger built with slog.With bypassed the rules for whatever it
// attached.
func exportAttrs(attrs []slog.Attr) []slog.Attr {
	kept := StripExported(attrs)
	out := make([]slog.Attr, 0, len(kept))
	for _, attr := range kept {
		out = append(out, redactAttr(attr))
	}
	return out
}

// redactAttr rewrites one attribute for the exported leg.
//
// Groups are descended, because slog.Group is an ordinary value a caller can
// pass and a flat scan reads one as a single opaque value.
func redactAttr(attr slog.Attr) slog.Attr {
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		rewritten := make([]slog.Attr, 0, len(group))
		for _, inner := range group {
			rewritten = append(rewritten, redactAttr(inner))
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(rewritten...)}
	}

	if attr.Value.Kind() == slog.KindAny {
		if err, ok := attr.Value.Any().(error); ok {
			return slog.String(attr.Key, errorTypeName(err))
		}
	}

	text := attr.Value.String()
	if len(text) <= maxExportedAttrValue && utf8.ValidString(text) {
		// Nothing to change: hand back the original so a typed value keeps its
		// type on the wire rather than becoming a string.
		return attr
	}
	return slog.String(attr.Key, truncateForExport(text))
}

// maxExportedAttrValue bounds one exported attribute value.
//
// Nothing in the logs SDK applies a length limit, so without this an attribute
// built from a caller-controlled string is relayed to the collector byte for
// byte: a search this server refused can be a megabyte of query, and the refusal
// paths log what they refused. The number is generous enough that no legitimate
// field this server writes is near it, and small enough that a flood of them is
// a rounding error on the operator's bill.
const maxExportedAttrValue = 1024

// truncateForExport bounds a value, saying so where it was cut, and hands back
// something a proto3 string field will accept.
//
// The marker matters as much as the bound: a value that was silently shortened
// reads as the whole value, and somebody eventually debugs the difference
// between a truncated host name and a wrong one.
//
// The bound is a byte count, so a cut can land inside a multi-byte character,
// and that costs more than the character. The OTLP log exporters serialize with
// proto.Marshal, which validates every string field, so one partial rune fails
// the upload for the entire batch: every record in it, from every caller, is
// dropped and an SDK error line is all that is left. The repair covers what
// arrives as well as what is cut here, because a title from a third-party
// catalog is not this server's to trust for encoding either.
func truncateForExport(text string) string {
	if len(text) <= maxExportedAttrValue {
		return strings.ToValidUTF8(text, string(utf8.RuneError))
	}
	return strings.ToValidUTF8(text[:maxExportedAttrValue], string(utf8.RuneError)) + "[truncated]"
}

// errorTypeName reports the type an error should be classified as, and is the
// whole of what the exported copy says about it.
//
// The otelslog bridge promotes any error-valued attribute into an
// exception.message equal to err.Error(). On this server that string is a
// third party's: net/http renders a failed request as "GET
// scheme://host/path: reason", so a failed download would export the mirror, the
// path — which for `get.php?md5=…` is the item identifier the item policy
// exists to digest — and, on other paths, a mirror's own response text.
// Replacing the value with a string both removes the text and defeats the
// promotion at its source, since the bridge only promotes a value that still is
// an error.
//
// This is the same disclosure netguard.RedactTransportError closes where the
// error is made. Both are wanted: that one keeps a credential out of the
// operator's own stderr, and this one keeps a third party's text off the wire
// without depending on every future call site having remembered the first.
//
// A type name is a compile-time constant, so it can carry no request data, and
// it answers the question an operator has at this level — whether the call failed
// at the transport or was refused by the far end. The status code and the timing
// are on the outbound span, which records neither URL nor body.
//
// The chain is walked past the generic wrappers, because fmt.Errorf produces
// *fmt.wrapError for every wrapped error in this tree and classifying every
// failure as that would be the same as classifying none.
func errorTypeName(err error) string {
	for range maxUnwrapDepth {
		name := fmt.Sprintf("%T", err)
		if !genericErrorTypes[name] {
			return name
		}
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return name
		}
		err = unwrapped
	}
	return "error"
}

// maxUnwrapDepth bounds the walk, because an error chain is built by whatever
// wrapped it and a cyclic Unwrap is a hang rather than a panic.
const maxUnwrapDepth = 16

// genericErrorTypes are the wrapper types that say nothing about what failed.
var genericErrorTypes = map[string]bool{
	"*fmt.wrapError":      true,
	"*fmt.wrapErrors":     true,
	"*errors.errorString": true,
	"*errors.joinError":   true,
}

// WithAttrs applies to both legs, so a logger derived from this one keeps
// exporting. Forgetting either would produce a handler that works until somebody
// calls slog.With, which is the ordinary way to build a component logger.
func (h *fanOutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fanOutHandler{
		stderr: h.stderr.WithAttrs(attrs),
		// The exported copy of attached attributes goes through the same
		// transform as a record's own. Handing them over raw is a bypass: a
		// component logger built with slog.With would export whatever it
		// attached, and exportRecord never sees it, because attachment happens
		// here.
		otlp:    h.otlp.WithAttrs(exportAttrs(attrs)),
		otlpMin: h.otlpMin,
	}
}

// WithGroup applies to both legs, for the same reason as WithAttrs.
func (h *fanOutHandler) WithGroup(name string) slog.Handler {
	return &fanOutHandler{
		stderr:  h.stderr.WithGroup(name),
		otlp:    h.otlp.WithGroup(name),
		otlpMin: h.otlpMin,
	}
}
