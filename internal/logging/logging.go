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
