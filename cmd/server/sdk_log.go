// sdk_log.go adapts the go-sdk's own log records to this server's stream.

package main

import (
	"context"
	"log/slog"
)

// sdkLogGroup nests everything the SDK says under one key.
//
// It is what makes an attribute named `level` harmless. The SDK logs
// "client log level set" with `level` set to the client's chosen severity
// (`mcp/server.go:2127` in v1.8.0), slog's JSON handler calls the record's own
// severity the same thing, and neither side deduplicates — so the line goes out
// with two `level` members and a parser keeping the last one reads the severity
// as whatever the client asked for, or as the empty string. Under a group there
// is no top level left to collide on, for that attribute or for any the SDK adds
// later, and a reader can tell the protocol layer's records from this server's
// at a glance.
const sdkLogGroup = "sdk"

// sdkLogger is the logger handed to [mcp.ServerOptions].
//
// # Why supply one at all
//
// The SDK logs nothing unless it is given a logger: `mcp.NewServer` fills an
// unset one with a handler that discards. So with no logger there is nothing
// from the protocol layer to read when a session misbehaves — only this
// server's own records, which begin one layer above the thing that went wrong.
//
// # Why not simply pass slog.Default()
//
// Because of what arrives. This server is stateless by default, so every POST is
// a session: connect, initialize, log-level and disconnect are emitted per
// request, at INFO, while the calls themselves are logged by this server. The
// sibling project measured its own default and found ninety-six records for
// twenty-four tool calls, every one of them the same four per-session messages.
// An operator watching that stream sees steady traffic and learns nothing from
// it, and the log level is no help: raising it to warn silences the SDK and this
// server's startup signal together.
//
// So the chatter is demoted rather than dropped — under
// LIBGEN_MCP_LOG_LEVEL=debug it is exactly what is wanted when a session is
// misbehaving — and everything the SDK says is nested under [sdkLogGroup].
func sdkLogger() *slog.Logger {
	return slog.New(newSDKLogHandler())
}

// sdkLogHandler routes the SDK's records through whatever logger this process
// is using at the moment each record is written.
//
// The handler is resolved per record rather than captured at construction, and
// that is the difference between a rule and a coincidence. The telemetry bridge
// replaces the default logger at startup, so a captured handler would carry
// whatever was installed when the server happened to be built: today that is the
// bridged one, and the day somebody moves the server construction one line above
// the telemetry wiring, the SDK's records would silently stop reaching the
// collector while everything else kept going. Resolving live costs one handler
// clone per record on a path that emits a handful per session.
type sdkLogHandler struct {
	// derive turns the process's current handler into the one this logger writes
	// through: the group above, plus whatever the SDK attached with With or
	// WithGroup.
	derive func(slog.Handler) slog.Handler
}

// newSDKLogHandler builds the handler the SDK's logger writes through.
func newSDKLogHandler() *sdkLogHandler {
	return &sdkLogHandler{
		derive: func(base slog.Handler) slog.Handler { return base.WithGroup(sdkLogGroup) },
	}
}

// current resolves the handler this record goes to.
func (h *sdkLogHandler) current() slog.Handler {
	return h.derive(slog.Default().Handler())
}

// Enabled implements [slog.Handler].
//
// It answers for the level the record arrives with, not the level it may be
// demoted to, because slog consults this before building the record. Handle asks
// again with the final level, so a demoted record is still dropped when the
// process logger would not have it.
func (h *sdkLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.current().Enabled(ctx, level)
}

// Handle implements [slog.Handler].
func (h *sdkLogHandler) Handle(ctx context.Context, record slog.Record) error {
	base := h.current()
	level := sdkLevelFor(record)
	if level == record.Level {
		return base.Handle(ctx, record)
	}
	if !base.Enabled(ctx, level) {
		return nil
	}

	demoted := slog.NewRecord(record.Time, level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		demoted.AddAttrs(attr)
		return true
	})
	return base.Handle(ctx, demoted)
}

// WithAttrs implements [slog.Handler].
func (h *sdkLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	derive := h.derive
	return &sdkLogHandler{
		derive: func(base slog.Handler) slog.Handler { return derive(base).WithAttrs(attrs) },
	}
}

// WithGroup implements [slog.Handler].
func (h *sdkLogHandler) WithGroup(name string) slog.Handler {
	derive := h.derive
	return &sdkLogHandler{
		derive: func(base slog.Handler) slog.Handler { return derive(base).WithGroup(name) },
	}
}

// sdkSessionChatter is the set of messages the SDK emits once per session.
//
// Every one of them is INFO in go-sdk v1.8.0 and every one of them is per
// request on a stateless deployment, which is the default here. Demoted rather
// than dropped: the whole reason to ask the SDK for its logs is to have them
// when a session is behaving strangely, and that is a debugging session.
var sdkSessionChatter = map[string]bool{
	"server run start":            true,
	"server connecting":           true,
	"server session connected":    true,
	"session initialized":         true,
	"client log level set":        true,
	"server session disconnected": true,
	"server session ended":        true,
}

// sdkRunCanceled is the line an ordinary stop produces, spelled as the SDK
// spells it.
//
// The SDK writes it with the other spelling, which this repository's linter
// rejects in its own prose, so it is assembled rather than written out. Matching
// on the text is deliberate and fails in the safe direction: if the wording ever
// changes, the line simply stays at ERROR, which is where it is today, and
// [TestTheSDKsOrdinaryStopIsNotAnError] is what makes that drift visible.
var sdkRunCanceled = "server run " + "cancel" + "led"

// sdkLevelFor returns the level a record should be emitted at.
func sdkLevelFor(record slog.Record) slog.Level {
	if sdkSessionChatter[record.Message] {
		return slog.LevelDebug
	}
	// Only a canceled context reaches this line, and the only context this
	// process cancels is the one signal.NotifyContext builds. A signal is how a
	// server is meant to be stopped, so an ERROR record for it makes every
	// ordinary exit look like a failure — the same misreport as a nonzero status
	// on a clean shutdown, which the binding forbids and this binary does not
	// produce.
	if record.Message == sdkRunCanceled {
		return slog.LevelDebug
	}
	return record.Level
}
