// Package logging configures the server's structured logger over stderr.
//
// The stdout channel is reserved for the MCP JSON-RPC transport, so all logs are
// emitted in JSON format to os.Stderr via log/slog.
package logging

import (
	"errors"
	"log/slog"
	"os"
	"strings"
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
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
