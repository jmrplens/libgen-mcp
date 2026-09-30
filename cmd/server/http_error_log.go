// http_error_log.go gives net/http's error log a place in this server's stream
// that keeps the peer it names off the collector.

package main

import (
	"context"
	"log"
	"log/slog"

	"github.com/jmrplens/libgen-mcp/v2/internal/telemetry"
)

// httpServerErrorMessage is the message every net/http error-log record is
// written under. The line net/http composed is the value of
// [telemetry.LogFieldHTTPServerError].
const httpServerErrorMessage = "the HTTP server reported an error"

// httpServerErrorLevel is the severity those records carry.
//
// The log package's own route into slog writes at INFO, which reads a failed
// handshake and a handler panic as routine. net/http calls this its error log,
// so it is logged as a warning.
const httpServerErrorLevel = slog.LevelWarn

// httpServerErrorLog is the logger handed to http.Server.ErrorLog.
//
// # Why supply one at all
//
// With none, net/http writes through the log package, and once slog.SetDefault
// has run the log package writes through the default handler, telemetry bridge
// included, with net/http's line as the record's message. The export leg does
// not rewrite messages, and the lines a caller provokes name the caller: "http:
// TLS handshake error from 198.51.100.23:40211", or a handler panic with the
// peer's address, the panic value and the stack. Under the default identity
// policy nothing about who made a call leaves this process, and that line was
// the policy arriving by the back door.
//
// So the line moves to a field on the strip list and the message becomes a
// constant. stderr keeps the whole line, which is what an operator debugging a
// certificate needs to read, and the collector learns that the server logged an
// error and nothing about whom it was talking to.
func httpServerErrorLog() *log.Logger {
	return slog.NewLogLogger(newHTTPErrorLogHandler(), httpServerErrorLevel)
}

// httpErrorLogHandler rewrites net/http's records and writes them through the
// process logger current at the moment each one is written.
//
// Resolved per record, as the SDK's handler is (see [sdkLogHandler]): the
// telemetry bridge replaces the default logger at startup, and a captured
// handler would carry whichever logger was installed when the server happened
// to be built, which is an ordering nobody should have to keep true.
type httpErrorLogHandler struct {
	// derive turns the process's current handler into the one this logger
	// writes through, carrying whatever was attached with With or WithGroup.
	derive func(slog.Handler) slog.Handler
}

// newHTTPErrorLogHandler builds the handler behind [httpServerErrorLog].
func newHTTPErrorLogHandler() *httpErrorLogHandler {
	return &httpErrorLogHandler{derive: func(base slog.Handler) slog.Handler { return base }}
}

// current resolves the handler this record goes to.
func (h *httpErrorLogHandler) current() slog.Handler {
	return h.derive(slog.Default().Handler())
}

// Enabled implements [slog.Handler].
func (h *httpErrorLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.current().Enabled(ctx, level)
}

// Handle implements [slog.Handler]: the composed line becomes a field, and the
// message a constant.
func (h *httpErrorLogHandler) Handle(ctx context.Context, record slog.Record) error {
	rewritten := slog.NewRecord(record.Time, record.Level, httpServerErrorMessage, record.PC)
	rewritten.AddAttrs(slog.String(telemetry.LogFieldHTTPServerError, record.Message))
	record.Attrs(func(attr slog.Attr) bool {
		rewritten.AddAttrs(attr)
		return true
	})
	return h.current().Handle(ctx, rewritten)
}

// WithAttrs implements [slog.Handler].
func (h *httpErrorLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	derive := h.derive
	return &httpErrorLogHandler{
		derive: func(base slog.Handler) slog.Handler { return derive(base).WithAttrs(attrs) },
	}
}

// WithGroup implements [slog.Handler].
func (h *httpErrorLogHandler) WithGroup(name string) slog.Handler {
	derive := h.derive
	return &httpErrorLogHandler{
		derive: func(base slog.Handler) slog.Handler { return derive(base).WithGroup(name) },
	}
}
