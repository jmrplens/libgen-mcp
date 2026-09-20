package logging

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// ParseLevel converts a string into a slog.Level.
//
// It accepts "debug", "info", "warn", "warning" and "error", case-insensitively
// and trimming whitespace. An empty string returns slog.LevelInfo. Any other
// value produces an error.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errors.New("unknown log level: " + s)
	}
}

// Setup installs a slog.JSONHandler over os.Stderr filtered to the given level
// as the default logger.
func Setup(level slog.Level) {
	slog.SetDefault(slog.New(newStderrHandler(level)))
}

// SetupWrapped installs the same stderr handler with wrap applied to it, and
// returns the function that puts the previous default logger back.
//
// It exists so the telemetry bridge wraps a handler whose behavior is known —
// the one this package builds — rather than whatever the global default happened
// to hold when it ran. Reading slog.Default() and wrapping that would work every
// time it was tried and fail the once it mattered: a test, a library, or a later
// startup step that replaced the default would silently become the stderr leg,
// and with it the thing an operator's whole log stream depends on.
//
// The wrapper may decline by returning nil, in which case the plain stderr
// handler is installed; a caller that cannot build its wrapper does not thereby
// take the logs down.
//
// The restore is not tidiness either. The default logger is a process global, so
// a bridge left installed after its provider shut down routes every later record
// at a stopped exporter — in a test binary, one test's collector receiving the
// rest of the suite.
func SetupWrapped(level slog.Level, wrap func(slog.Handler) slog.Handler) (restore func()) {
	previous := slog.Default()
	handler := newStderrHandler(level)
	if wrap != nil {
		if wrapped := wrap(handler); wrapped != nil {
			handler = wrapped
		}
	}
	slog.SetDefault(slog.New(handler))
	return func() { slog.SetDefault(previous) }
}

// newStderrHandler builds the JSON handler every logger in this process writes
// through.
func newStderrHandler(level slog.Level) slog.Handler {
	return slog.NewJSONHandler(destination(), &slog.HandlerOptions{Level: level})
}

// stream is where records are written.
//
// os.Stderr in every deployment, and not configurable: stdout is reserved for
// the stdio MCP transport, so there is exactly one place a log record may go.
var stream struct {
	mu sync.Mutex
	to io.Writer
}

// destination reports where records are written.
func destination() io.Writer {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.to == nil {
		return os.Stderr
	}
	return stream.to
}

// SetDestination redirects the log stream and returns the function that puts it
// back.
//
// It is here for tests, and for one specific thing they cannot otherwise do:
// [SetupWrapped] builds its own stderr handler rather than wrapping whatever the
// default logger happens to hold, so a test that substitutes slog.Default() has
// its substitute replaced the moment the telemetry bridge is installed. Reading
// the stream is then the only way to observe what the server actually wrote —
// which is also closer to what such a test claims to assert.
//
// Nothing in the server calls it. The rebuild is the property worth keeping: a
// bridge that wrapped the ambient default would work every time it was tried and
// fail the once it mattered, when something else had replaced it.
func SetDestination(w io.Writer) (restore func()) {
	stream.mu.Lock()
	previous := stream.to
	stream.to = w
	stream.mu.Unlock()
	return func() {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		stream.to = previous
	}
}

// SourceAttempt records one download source's outcome inside the chain.
//
// It is Info rather than Debug because it answers the question asked whenever a
// download misbehaves — which source was tried, why it failed, which one served
// the file — and that question was previously unanswerable: the log held the
// mirror requests and the tool call's total duration, and nothing about the
// decision between them. It is one line per source tried.
//
// mirror is the scheme://host that served the bytes, empty when the attempt
// failed or the caller has none to report. It is logged here because the download
// result no longer carries it: provenance is the operator's business, and this
// line is now the only place the pair (source, mirror) is written down.
func SourceAttempt(source, mirror string, start time.Time, err error) {
	duration := time.Since(start)
	if err != nil {
		slog.Info("source failed, advancing", "source", source, "duration", duration, "error", err)
		return
	}
	if mirror == "" {
		slog.Info("source resolved", "source", source, "duration", duration)
		return
	}
	slog.Info("source resolved", "source", source, "mirror", mirror, "duration", duration)
}

// SourceSkipped records that the chain passed over a download source because an
// earlier failure showed it to be unavailable and its cooldown has not expired.
//
// It is Info, at the same level as SourceAttempt, because a source that simply
// vanishes from the chain log is the harder debugging problem: the reader can see
// which sources were tried but not why one they expected is missing. until is the
// instant the source becomes eligible again.
func SourceSkipped(source string, until time.Time) {
	slog.Info("source in cooldown, skipping", "source", source, "cooldown_until", until)
}

// SourceCooldownBypassed records that every source able to serve the item was in
// cooldown, so the chain tried them regardless. A cooldown only deprioritizes a
// source, so this is the expected outcome rather than an error — but it is worth a
// Warn, since it means every provider for this kind of item recently failed.
func SourceCooldownBypassed(sources []string) {
	slog.Warn("every capable source is in cooldown, trying them anyway", "sources", sources)
}

// ToolCall records the outcome of an MCP tool execution.
//
// It emits an Info-level log when err is nil and an Error-level log otherwise,
// always including the tool name and the duration elapsed since start.
func ToolCall(tool string, start time.Time, err error) {
	duration := time.Since(start)
	if err != nil {
		slog.Error("tool call failed", "tool", tool, "duration", duration, "error", err)
		return
	}
	slog.Info("tool call completed", "tool", tool, "duration", duration)
}
