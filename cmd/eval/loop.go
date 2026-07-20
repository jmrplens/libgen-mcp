//go:build eval

package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxTurns bounds how many model calls one scenario conversation may make.
const maxTurns = 4

// maxToolResultLen caps the size of a tool result fed back to the model.
const maxToolResultLen = 20_000

// toolCall records one executed model tool call and the real MCP response.
type toolCall struct {
	Name       string
	Input      map[string]any
	Result     *mcp.CallToolResult
	Structured any
}

// transcript captures everything a scenario's assertions grade against: every
// executed tool call and the model's final prose (when it stopped without a
// tool call).
type transcript struct {
	Calls     []toolCall
	FinalText string
}

// runScenario drives one scenario to completion: it applies any per-scenario
// environment, builds a fresh in-process libgen-mcp host, then runs the
// tool-use loop (send prompt + tools; execute each tool_use against the real
// MCP session; feed tool_result blocks back) until the model answers without a
// tool call or the turn budget is exhausted.
func runScenario(ctx context.Context, ac *anthropicClient, sc scenario) (transcript, error) {
	restore := applyEnv(sc.SetupEnv)
	defer restore()

	session, cleanup, err := newHostSession(ctx)
	if err != nil {
		return transcript{}, err
	}
	defer cleanup()

	defs, err := toolDefs(ctx, session)
	if err != nil {
		return transcript{}, err
	}

	toolChoice := sc.ToolChoice
	if toolChoice == "" {
		toolChoice = "auto"
	}

	var tr transcript
	messages := []message{{Role: "user", Content: []contentBlock{{Type: "text", Text: sc.Prompt}}}}
	for range maxTurns {
		resp, callErr := ac.call(ctx, anthropicRequest{
			Model:       evalModel,
			MaxTokens:   maxTokens,
			Temperature: 0,
			Tools:       defs,
			ToolChoice:  map[string]any{"type": toolChoice},
			Messages:    messages,
		})
		if callErr != nil {
			return tr, callErr
		}
		messages = append(messages, message{Role: "assistant", Content: resp.Content})

		uses := toolUseBlocks(resp.Content)
		if len(uses) == 0 {
			tr.FinalText = textOf(resp.Content)
			return tr, nil
		}
		results := make([]contentBlock, 0, len(uses))
		for _, use := range uses {
			call, block := executeTool(ctx, session, use)
			tr.Calls = append(tr.Calls, call)
			results = append(results, block)
		}
		messages = append(messages, message{Role: "user", Content: results})
	}
	return tr, nil
}

// executeTool runs one model tool_use against the live MCP session and returns
// both the recorded toolCall and the tool_result block to feed back to the model.
func executeTool(ctx context.Context, session *mcp.ClientSession, use contentBlock) (toolCall, contentBlock) {
	call := toolCall{Name: use.Name, Input: use.Input}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: use.Name, Arguments: use.Input})
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
		return func() {}
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
