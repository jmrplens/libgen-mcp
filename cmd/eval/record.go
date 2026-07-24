//go:build eval

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// A run record is the complete account of one scenario: what was asked, what the
// model did with the tools, what the server did internally, what came back, and
// what the model finally told the user. It exists so a failure — or a suspicion
// about a passing run — can be investigated after the fact without paying for
// another live run.
//
// Everything is recorded, not just what the assertions grade. An assertion can
// only check what someone thought to check; the record is what makes the
// unthought-of visible.

// scenarioRecord is one scenario's complete account, written as a single JSON
// object so a run is a JSONL file that jq and grep can both work with.
type scenarioRecord struct {
	ID         string            `json:"id"`
	Mode       string            `json:"mode"`
	Model      string            `json:"model"`
	Prompt     string            `json:"prompt"`
	SetupEnv   map[string]string `json:"setup_env,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	DurationMS int64             `json:"duration_ms"`

	// ToolsOffered is the tool surface the model was shown for this scenario —
	// names, descriptions and input schemas. It is recorded per scenario rather
	// than once per run because it genuinely differs: a remote deployment describes
	// download differently, and a description is what a model has to work from when
	// it discovers (or fails to discover) an argument.
	ToolsOffered []toolDef `json:"tools_offered,omitempty"`

	// Turns is the conversation as it happened: each model reply, the prose it
	// wrote alongside its tool calls, and what that reply cost.
	Turns []turnRecord `json:"turns,omitempty"`
	// Calls is every tool invocation, with the arguments the model chose, what the
	// server logged while serving it, and everything it returned.
	Calls []callRecord `json:"calls,omitempty"`
	// Elicitations is every prompt the server raised back at the host.
	Elicitations []elicitRecord `json:"elicitations,omitempty"`
	// Fetched is what the harness pulled from resolve-only links, standing in for
	// the agent's own fetch tool.
	Fetched []fetchedFile `json:"fetched,omitempty"`
	// Progress is the notification stream the client received.
	Progress []progressRecord `json:"progress,omitempty"`

	FinalAnswer string `json:"final_answer"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	Error       string `json:"error,omitempty"`
}

// turnRecord is one model reply: the prose it produced, the calls it asked for,
// and the tokens and latency it cost.
type turnRecord struct {
	N            int    `json:"n"`
	LatencyMS    int64  `json:"latency_ms"`
	StopReason   string `json:"stop_reason,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	// Text is the prose the model wrote in this turn. Intermediate turns carry the
	// model's own narration of what it is about to do, which is often where a wrong
	// turn is first visible.
	Text     string          `json:"text,omitempty"`
	ToolUses []toolUseRecord `json:"tool_uses,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// toolUseRecord is one tool the model asked for, with the arguments it chose.
type toolUseRecord struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input,omitempty"`
}

// callRecord is one executed tool call, from the arguments in to the payload out,
// including what the server logged while serving it.
type callRecord struct {
	Name       string         `json:"name"`
	Input      map[string]any `json:"input,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	IsError    bool           `json:"is_error,omitempty"`
	// Text is the human-readable content the tool returned — the Markdown the model
	// actually reads. Structured is the typed output beside it.
	Text       string `json:"text,omitempty"`
	Structured any    `json:"structured,omitempty"`
	// ServerLogs is what the MCP server logged internally while serving this call:
	// mirror failover, retries, source-chain decisions. It is the only view of what
	// happened between the request and the response.
	ServerLogs []string `json:"server_logs,omitempty"`
}

// elicitRecord is one prompt the server raised back at the host, and the answer
// the host gave it.
type elicitRecord struct {
	Field   string `json:"field"`
	Message string `json:"message,omitempty"`
	Action  string `json:"action"`
}

// progressRecord is one progress notification, flattened for the record. The
// token is kept because it is what ties a notification to the call that emitted
// it — without it a re-grade cannot tell a download's progress from anything
// else's, and reports a working stream as missing.
type progressRecord struct {
	Token    string  `json:"token,omitempty"`
	Progress float64 `json:"progress"`
	Total    float64 `json:"total,omitempty"`
	Message  string  `json:"message,omitempty"`
}

// recorder writes scenario records to a JSONL file, one object per line. A nil
// recorder is valid and does nothing, so recording stays optional without the
// callers branching on it.
type recorder struct {
	mu sync.Mutex
	f  *os.File
}

// newRecorder opens path for writing. An empty path disables recording.
func newRecorder(path string) (*recorder, error) {
	if path == "" {
		return nil, nil //nolint:nilnil // a nil recorder is the documented "off" state.
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open record file: %w", err)
	}
	return &recorder{f: f}, nil
}

// write appends one scenario record.
func (r *recorder) write(rec scenarioRecord) {
	if r == nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "record marshal:", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.f.Write(append(data, '\n'))
}

// close releases the file.
func (r *recorder) close() {
	if r == nil {
		return
	}
	_ = r.f.Close()
}

// logCapture tees the server's structured log to a buffer while leaving it on
// stderr, so a run stays watchable and still ends up fully recorded.
type logCapture struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	prev *slog.Logger
}

// captureServerLogs installs a capturing logger and returns it with a restore
// func. Every log line the MCP server emits from here on is both printed and kept.
func captureServerLogs() (*logCapture, func()) {
	c := &logCapture{prev: slog.Default()}
	handler := slog.NewJSONHandler(io.MultiWriter(os.Stderr, &syncWriter{c: c}), &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	return c, func() { slog.SetDefault(c.prev) }
}

// syncWriter serializes writes into the capture buffer, since the server may log
// from more than one goroutine.
type syncWriter struct{ c *logCapture }

func (w *syncWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.buf.Write(p)
}

// mark returns the current length of the capture, so a caller can slice out just
// the lines produced between two marks — which is how a call's own server logs
// are separated from its neighbours'.
func (c *logCapture) mark() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Len()
}

// since returns the log lines written after the given mark.
func (c *logCapture) since(mark int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mark > c.buf.Len() {
		return nil
	}
	chunk := strings.TrimSpace(c.buf.String()[mark:])
	if chunk == "" {
		return nil
	}
	return strings.Split(chunk, "\n")
}
