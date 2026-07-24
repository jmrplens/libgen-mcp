//go:build eval

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fetchedFile records the harness fetching a resolve-only download URL to local
// disk, simulating an agent's own fetch tool in the remote block: it proves the
// file ends up local even when the (remote) server only returned a link.
type fetchedFile struct {
	URL  string
	Path string
	Size int64
	Err  string
}

// maybeFetchResolved fetches the resolved download URL from a download tool call
// (if any) to the sandbox download dir, returning nil when the call is not a
// resolve-only download. It sets up no state on the server — it acts purely as
// the caller's own fetch capability would.
func maybeFetchResolved(ctx context.Context, call toolCall) *fetchedFile {
	if call.Name != "download" || call.Result == nil || call.Result.IsError {
		return nil
	}
	var out struct {
		Resolved *struct {
			URL      string            `json:"url"`
			Filename string            `json:"filename"`
			Headers  map[string]string `json:"headers"`
		} `json:"resolved"`
	}
	if decodeStructured(call.Structured, &out) != nil || out.Resolved == nil || out.Resolved.URL == "" {
		return nil
	}
	name := out.Resolved.Filename
	if name == "" {
		name = "resolved.bin"
	}
	// filepath.Base strips any path components, and the dir is the eval's own
	// sandbox, so the resolved filename cannot escape it.
	path := filepath.Join(os.Getenv("LIBGEN_MCP_DOWNLOAD_DIR"), filepath.Base(name))
	fetched := &fetchedFile{URL: out.Resolved.URL, Path: path}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, out.Resolved.URL, http.NoBody)
	if err != nil {
		fetched.Err = err.Error()
		return fetched
	}
	for k, v := range out.Resolved.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "libgen-mcp-eval-fetch")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fetched.Err = err.Error()
		return fetched
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		fetched.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return fetched
	}
	f, err := os.Create(path) //nolint:gosec // path is sandbox dir + filepath.Base(name); cannot escape
	if err != nil {
		fetched.Err = err.Error()
		return fetched
	}
	n, cerr := io.Copy(f, io.LimitReader(resp.Body, maxDownloadBytes))
	_ = f.Close()
	fetched.Size = n
	if cerr != nil {
		fetched.Err = cerr.Error()
	}
	return fetched
}

// maxTurns bounds how many model calls one scenario conversation may make.
const maxTurns = 6

// scenarioBudget bounds the wall time of one scenario. Turns are bounded, but a
// single tool call was not: a download or read that sits on an established but
// silent connection blocks the scenario, and with it the whole run, forever —
// three full runs stalled on the last scenario before this existed, each with
// established IPFS gateway sockets and no bytes moving.
//
// It is generous on purpose. The slowest honest scenarios take a little over two
// minutes, so anything past this is not slow, it is stuck, and reporting it as
// stuck is more useful than waiting.
var scenarioBudget = 6 * time.Minute

// maxToolResultLen caps the size of a tool result fed back to the model.
const maxToolResultLen = 20_000

// downloadProgressToken is attached to every download tool call so the server
// emits progress notifications the eval client can capture and assert on.
const downloadProgressToken = "eval-progress"

// progressCapture accumulates the progress notifications the client receives,
// guarded for the SDK's notification goroutine.
type progressCapture struct {
	mu     sync.Mutex
	events []mcp.ProgressNotificationParams
}

// add records one received progress notification.
func (p *progressCapture) add(e *mcp.ProgressNotificationParams) {
	if e == nil {
		return
	}
	p.mu.Lock()
	p.events = append(p.events, *e)
	p.mu.Unlock()
}

// snapshot returns a copy of the notifications received so far.
func (p *progressCapture) snapshot() []mcp.ProgressNotificationParams {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]mcp.ProgressNotificationParams, len(p.events))
	copy(out, p.events)
	return out
}

// toolCall records one executed model tool call and the real MCP response.
type toolCall struct {
	Name       string
	Input      map[string]any
	Result     *mcp.CallToolResult
	Structured any
	// Duration is how long the tool took to answer, and ServerLogs is what the MCP
	// server logged internally while serving it. Neither is graded; both are
	// recorded, because they are the only view of what happened between the request
	// and the response.
	Duration   time.Duration
	ServerLogs []string
}

// transcript captures everything a scenario's assertions grade against: every
// executed tool call, the model's final prose (when it stopped without a tool
// call), and the progress notifications the client received during the run.
type transcript struct {
	Calls     []toolCall
	FinalText string
	Progress  []mcp.ProgressNotificationParams
	// Fetched records files the harness pulled from resolve-only download links
	// (remote block), acting as the agent's own fetch tool.
	Fetched []fetchedFile
	// ConfirmElicits is how many download-save confirmation prompts the host's
	// elicitation handler answered during this scenario (see confirmElicitations);
	// the confirmation scenario hard-asserts it fired.
	ConfirmElicits int
	// Turns is the conversation as it happened, recorded rather than graded: the
	// prose the model wrote alongside each set of tool calls, what the reply cost,
	// and how long it took. An intermediate turn is often where a wrong decision is
	// first visible, and none of it survived anywhere before.
	Turns []turnRecord
	// Elicitations is every prompt the server raised back at the host.
	Elicitations []elicitRecord
	// Tools is the surface the model was shown: what it had to work from when
	// choosing a tool and its arguments.
	Tools []toolDef
}

// runScenario drives one scenario to completion: it applies any per-scenario
// environment, builds a fresh in-process libgen-mcp host, then runs the
// tool-use loop (send prompt + tools; execute each tool_use against the real
// MCP session; feed tool_result blocks back) until the model answers without a
// tool call or the turn budget is exhausted.
func runScenario(ctx context.Context, ac *anthropicClient, sc scenario) (tr transcript, err error) {
	ctx, cancel := context.WithTimeout(ctx, scenarioBudget)
	defer cancel()

	restore := applyEnv(sc.SetupEnv)
	defer restore()

	// Everything the MCP server logs from here on is both printed and kept, so each
	// call's internal decisions can be attached to the request that caused them.
	logs, restoreLog := captureServerLogs()
	defer restoreLog()

	session, progress, cleanup, err := newHostSession(ctx, sc.Remote)
	if err != nil {
		return transcript{}, err
	}
	defer cleanup()

	defs, err := toolDefs(ctx, session)
	if err != nil {
		return transcript{}, err
	}
	tr.Tools = defs

	toolChoice := sc.ToolChoice
	if toolChoice == "" {
		toolChoice = "auto"
	}

	// Snapshot the progress notifications and the download-confirmation count at
	// every exit (named return tr), including the early return when the model
	// answers without a tool call.
	defer func() {
		tr.Progress = progress.snapshot()
		tr.ConfirmElicits = confirmElicitationCount()
		tr.Elicitations = elicitationsSnapshot()
	}()
	messages := []message{{Role: "user", Content: []contentBlock{{Type: "text", Text: sc.Prompt}}}}
	for turn := 1; turn <= maxTurns; turn++ {
		started := time.Now()
		resp, callErr := ac.call(ctx, anthropicRequest{
			Model:       evalModel,
			MaxTokens:   maxTokens,
			Temperature: 0,
			Tools:       defs,
			ToolChoice:  map[string]any{"type": toolChoice},
			Messages:    messages,
		})
		if callErr != nil {
			// A blown budget shows up here, on whichever call happened to be next.
			// Say so plainly: the time went to the scenario, not to this request, and
			// the record's per-call durations show where.
			if ctx.Err() != nil {
				callErr = fmt.Errorf("scenario exceeded its %s budget (see the record's per-call durations for where the time went)", scenarioBudget)
			}
			tr.Turns = append(tr.Turns, turnRecord{N: turn, LatencyMS: time.Since(started).Milliseconds(), Error: callErr.Error()})
			return tr, callErr
		}
		uses := toolUseBlocks(resp.Content)
		tr.Turns = append(tr.Turns, newTurnRecord(turn, time.Since(started), resp, uses))
		messages = append(messages, message{Role: "assistant", Content: resp.Content})

		if len(uses) == 0 {
			tr.FinalText = textOf(resp.Content)
			return tr, nil
		}
		results := make([]contentBlock, 0, len(uses))
		for _, use := range uses {
			call, block := executeTool(ctx, session, use, logs)
			tr.Calls = append(tr.Calls, call)
			results = append(results, block)
			// Simulate the agent's own fetch tool: pull any resolve-only download
			// link to local disk (remote block).
			if f := maybeFetchResolved(ctx, call); f != nil {
				tr.Fetched = append(tr.Fetched, *f)
			}
		}
		messages = append(messages, message{Role: "user", Content: results})
	}
	return tr, nil
}

// newTurnRecord flattens one model reply into its record.
func newTurnRecord(n int, latency time.Duration, resp anthropicResponse, uses []contentBlock) turnRecord {
	rec := turnRecord{
		N:          n,
		LatencyMS:  latency.Milliseconds(),
		StopReason: resp.StopReason,
		Text:       textOf(resp.Content),
	}
	if resp.Usage != nil {
		rec.InputTokens, rec.OutputTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens
	}
	for _, use := range uses {
		rec.ToolUses = append(rec.ToolUses, toolUseRecord{Name: use.Name, Input: use.Input})
	}
	return rec
}

// executeTool runs one model tool_use against the live MCP session and returns
// both the recorded toolCall and the tool_result block to feed back to the model.
func executeTool(ctx context.Context, session *mcp.ClientSession, use contentBlock, logs *logCapture) (toolCall, contentBlock) {
	call := toolCall{Name: use.Name, Input: use.Input}
	params := &mcp.CallToolParams{Name: use.Name, Arguments: use.Input}
	// Attach a progress token to download calls so the server emits progress
	// notifications the client captures (see progressCapture).
	if use.Name == "download" {
		params.SetProgressToken(downloadProgressToken)
	}
	// Calls run one after another, so the log written between these two marks is
	// this call's own — which is what attributes the server's internal decisions to
	// the request that caused them.
	mark, started := logs.mark(), time.Now()
	res, err := session.CallTool(ctx, params)
	call.Duration = time.Since(started)
	call.ServerLogs = logs.since(mark)
	if err != nil {
		return call, contentBlock{
			Type:      "tool_result",
			ToolUseID: use.ID,
			Content:   "tool call failed: " + err.Error(),
			IsError:   true,
		}
	}
	call.Result = res
	if res != nil {
		call.Structured = res.StructuredContent
	}
	return call, contentBlock{
		Type:      "tool_result",
		ToolUseID: use.ID,
		Content:   resultText(res),
		IsError:   res != nil && res.IsError,
	}
}

// toolUseBlocks returns the tool_use blocks from a model response, in order.
func toolUseBlocks(blocks []contentBlock) []contentBlock {
	var uses []contentBlock
	for _, b := range blocks {
		if b.Type == "tool_use" {
			uses = append(uses, b)
		}
	}
	return uses
}

// textOf joins the text blocks of a model response.
func textOf(blocks []contentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// resultText renders an MCP tool result as text for the model, preferring the
// structured content JSON and falling back to text content.
func resultText(res *mcp.CallToolResult) string {
	if res == nil {
		return "empty result"
	}
	if res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			return truncate(string(data), maxToolResultLen)
		}
	}
	var parts []string
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok && strings.TrimSpace(text.Text) != "" {
			parts = append(parts, text.Text)
		}
	}
	if len(parts) == 0 {
		return "ok"
	}
	return truncate(strings.Join(parts, "\n"), maxToolResultLen)
}

// truncate clips s to at most n bytes, appending an ellipsis marker when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// applyEnv sets the given environment variables and returns a restore function
// that puts the previous values back. A nil or empty map is a no-op.
func applyEnv(env map[string]string) func() {
	if len(env) == 0 {
		return func() { /* no variables changed, nothing to restore */ }
	}
	saved := make(map[string]*string, len(env))
	for key, value := range env {
		if old, ok := os.LookupEnv(key); ok {
			restore := old
			saved[key] = &restore
		} else {
			saved[key] = nil
		}
		_ = os.Setenv(key, value)
	}
	return func() {
		for key, old := range saved {
			if old == nil {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, *old)
			}
		}
	}
}
