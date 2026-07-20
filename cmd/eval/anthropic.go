//go:build eval

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// anthropicEndpoint is the Anthropic Messages API endpoint.
	anthropicEndpoint = "https://api.anthropic.com/v1/messages"
	// anthropicVersion is the Anthropic API version header value.
	anthropicVersion = "2023-06-01"
	// evalModel is the small, cheap model the harness drives.
	evalModel = "claude-haiku-4-5-20251001"
	// maxTokens caps each model response.
	maxTokens = 1024
	// maxResponseBytes caps the API response body kept in memory.
	maxResponseBytes = 4 << 20
)

// anthropicRequest is the JSON body of a Messages API call.
type anthropicRequest struct {
	Model       string         `json:"model"`
	MaxTokens   int            `json:"max_tokens"`
	Temperature float64        `json:"temperature"`
	Tools       []toolDef      `json:"tools"`
	ToolChoice  map[string]any `json:"tool_choice"`
	Messages    []message      `json:"messages"`
}

// toolDef is a tool definition as the Messages API expects it, built from the
// MCP server's advertised tools.
type toolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

// message is one conversation turn (user or assistant) of content blocks.
type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// contentBlock is a single content block. It covers the block kinds the harness
// exchanges with the API: text, tool_use (assistant), and tool_result (user).
type contentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

// MarshalJSON emits the block in the shape the Messages API requires. In
// particular a tool_use block always carries a non-null input object, which the
// API rejects when omitted.
func (b contentBlock) MarshalJSON() ([]byte, error) {
	payload := map[string]any{"type": b.Type}
	switch b.Type {
	case "text":
		payload["text"] = b.Text
	case "tool_use":
		payload["id"] = b.ID
		payload["name"] = b.Name
		if b.Input == nil {
			payload["input"] = map[string]any{}
		} else {
			payload["input"] = b.Input
		}
	case "tool_result":
		payload["tool_use_id"] = b.ToolUseID
		payload["content"] = b.Content
		if b.IsError {
			payload["is_error"] = true
		}
	}
	return json.Marshal(payload)
}

// anthropicResponse is the subset of the Messages API response the harness reads.
type anthropicResponse struct {
	Content    []contentBlock  `json:"content"`
	StopReason string          `json:"stop_reason"`
	Error      *anthropicError `json:"error,omitempty"`
}

// anthropicError is the API-level error object.
type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// anthropicClient makes raw net/http Messages API calls with an API key.
type anthropicClient struct {
	apiKey string
	http   *http.Client
}

// newAnthropicClient builds a client bound to the given API key.
func newAnthropicClient(apiKey string) *anthropicClient {
	return &anthropicClient{apiKey: apiKey, http: &http.Client{Timeout: 90 * time.Second}}
}

// call sends one Messages request and decodes the response, surfacing HTTP and
// API-level errors.
func (c *anthropicClient) call(ctx context.Context, req anthropicRequest) (anthropicResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return anthropicResponse{}, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint, bytes.NewReader(body))
	if err != nil {
		return anthropicResponse{}, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return anthropicResponse{}, fmt.Errorf("anthropic request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return anthropicResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return anthropicResponse{}, fmt.Errorf("anthropic status %d: %s", resp.StatusCode, string(data))
	}
	var out anthropicResponse
	if err = json.Unmarshal(data, &out); err != nil {
		return anthropicResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil {
		return anthropicResponse{}, fmt.Errorf("anthropic error %s: %s", out.Error.Type, out.Error.Message)
	}
	return out, nil
}
