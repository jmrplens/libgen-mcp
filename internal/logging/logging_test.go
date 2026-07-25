package logging_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/logging"
)

// TestParseLevel covers ParseLevel with table-driven subtests.
func TestParseLevel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    slog.Level
		wantErr bool
	}{
		{name: "empty defaults to info", input: "", want: slog.LevelInfo},
		{name: "debug", input: "debug", want: slog.LevelDebug},
		{name: "info", input: "info", want: slog.LevelInfo},
		{name: "warn", input: "WARN", want: slog.LevelWarn},
		{name: "warning alias", input: "warning", want: slog.LevelWarn},
		{name: "error", input: "error", want: slog.LevelError},
		{name: "trimmed and mixed case", input: "  Debug  ", want: slog.LevelDebug},
		{name: "unknown value", input: "banana", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := logging.ParseLevel(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestToolCallError verifies ToolCallError.
func TestToolCallError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	logging.ToolCall("search", time.Now(), errors.New("boom"))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to unmarshal log line %q: %v", buf.String(), err)
	}
	if entry["level"] != "ERROR" {
		t.Fatalf("expected level ERROR, got %v", entry["level"])
	}
	if entry["tool"] != "search" {
		t.Fatalf("expected tool key %q, got %v", "search", entry["tool"])
	}
	if entry["error"] != "boom" {
		t.Fatalf("expected error key %q, got %v", "boom", entry["error"])
	}
}

// TestToolCallSuccess verifies ToolCallSuccess.
func TestToolCallSuccess(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	logging.ToolCall("details", time.Now(), nil)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to unmarshal log line %q: %v", buf.String(), err)
	}
	if entry["level"] != "INFO" {
		t.Fatalf("expected level INFO, got %v", entry["level"])
	}
	if entry["tool"] != "details" {
		t.Fatalf("expected tool key %q, got %v", "details", entry["tool"])
	}
}

// TestSourceSkipped verifies a source passed over for being in cooldown is
// reported with its name and the instant it becomes eligible again, so a shortened
// chain is never a silent one.
func TestSourceSkipped(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	logging.SourceSkipped("fatcat", time.Now().Add(time.Minute))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to unmarshal log line %q: %v", buf.String(), err)
	}
	if entry["level"] != "INFO" {
		t.Errorf("expected level INFO, got %v", entry["level"])
	}
	if entry["source"] != "fatcat" {
		t.Errorf("expected source %q, got %v", "fatcat", entry["source"])
	}
	if entry["cooldown_until"] == nil {
		t.Error("expected the log line to report when the cooldown expires")
	}
}

// TestSourceCooldownBypassed verifies the all-cooled-down bypass is reported at
// Warn with the sources it tried anyway.
func TestSourceCooldownBypassed(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	logging.SourceCooldownBypassed([]string{"fatcat", "scidb"})

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to unmarshal log line %q: %v", buf.String(), err)
	}
	if entry["level"] != "WARN" {
		t.Errorf("expected level WARN, got %v", entry["level"])
	}
	if got, ok := entry["sources"].([]any); !ok || len(got) != 2 {
		t.Errorf("expected the two bypassed sources, got %v", entry["sources"])
	}
}

// TestSetupWritesStderr verifies SetupWritesStderr.
func TestSetupWritesStderr(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Setup must not panic for any valid level.
	logging.Setup(slog.LevelInfo)
	logging.Setup(slog.LevelDebug)
}
