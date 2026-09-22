// rpc.go is the client side of the measurement: it speaks MCP to a target the
// way a real client does, and times what comes back.
//
// It is hand-written rather than built on the SDK's client on purpose. What is
// being timed is the server, and an SDK client would put its own session
// bookkeeping, its own retries and its own buffering between the stopwatch and
// the wire. A JSON-RPC frame and a stopwatch measure the server; anything
// larger measures the pair.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The headers a plain client sends. The protocol version is the legacy-era one
// for the reason the HTTP end-to-end module gives: it is what a plain POST
// negotiates without the per-request _meta the later era requires, so a driver
// that is not about version negotiation does not carry that ceremony.
const (
	protocolVersion = "2025-11-25"
	acceptHeader    = "application/json, text/event-stream"
)

// The MCP methods this command measures.
//
// They are the three a client actually spends time in: the catalog it reads on
// every reconnect, the prompts beside it, and one real tool call that reaches
// the catalog, parses a results page and renders it. Nothing here calls
// download or read: both write files, and a benchmark that filled a disk would
// measure the disk.
const (
	methodToolsList   = "tools/list"
	methodPromptsList = "prompts/list"
	methodToolsCall   = "tools/call"
)

// searchArguments is the one tool call the matrix makes.
//
// extra_sources is never rather than auto because auto escalates to the
// open-access providers when the catalog comes up empty, and those are on the
// internet. never keeps the call inside the stand-in catalog, which is what
// makes the number reproducible on a second machine.
var searchArguments = map[string]any{
	"query":            "algorithms",
	"topics":           []string{"nonfiction"},
	"results_per_page": 25,
	"extra_sources":    "never",
}

// rpcRequest is one JSON-RPC call.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcError is the error member of a JSON-RPC response.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcResponse is what comes back, decoded no further than this command needs.
type rpcResponse struct {
	Error  *rpcError       `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// caller is one client of a target: an address on HTTP, a process on stdio.
type caller interface {
	// call sends one method and returns how long it took and how many bytes
	// came back. An error means the call did not complete; a protocol-level
	// error comes back as a non-nil rpcError inside the response.
	call(ctx context.Context, method string, params any) (callResult, error)
	// close releases whatever the caller holds.
	close()
}

// callResult is one timed call.
type callResult struct {
	Duration time.Duration
	Bytes    int
	// Throttled is set when the server refused the call for rate rather than
	// serving it, which a sizing number must not count as service.
	Throttled bool
	Err       *rpcError
	// ToolError is set when the frame came back fine and the tool inside it
	// reported a failure, and ToolErrText is what it said.
	//
	// It is a separate field because it is a separate thing, and conflating
	// them is how a benchmark measures nothing: a search that cannot reach its
	// catalog answers in under a millisecond with isError true, and a driver
	// that only looked at the JSON-RPC envelope would publish that as the cost
	// of a search.
	ToolError   bool
	ToolErrText string
}

// toolFailed reports whether a tools/call result carries isError, and what it
// said. The text matters: a benchmark that stops because its calls failed is
// only useful if it names the failure, and the tool's own words are the whole
// diagnosis.
func toolFailed(result json.RawMessage) (failed bool, said string) {
	if len(result) == 0 {
		return false, ""
	}
	var doc struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		return false, ""
	}
	if !doc.IsError {
		return false, ""
	}
	var blocks []string
	for _, block := range doc.Content {
		if block.Text != "" {
			blocks = append(blocks, block.Text)
		}
	}
	return true, truncate(strings.Join(blocks, " "), 300)
}

// httpCaller is one client address talking to an HTTP target.
//
// Each carries its own http.Client so connections are not shared between
// clients: two callers on one pool would be one connection to the server, and
// the thing being measured is what N separate callers cost.
type httpCaller struct {
	url     string
	address string
	client  *http.Client
	// nextID is atomic because one caller serves several calls at once: a
	// scenario with Parallel above one hands this same caller to that many
	// goroutines, and an unsynchronized counter is both a data race and a
	// source of duplicate JSON-RPC ids.
	nextID atomic.Int64
}

// newHTTPCaller builds a caller that presents the given address to the server.
func newHTTPCaller(baseURL, address string) *httpCaller {
	return &httpCaller{
		url:     baseURL + "/",
		address: address,
		client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        2,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// call posts one JSON-RPC frame and times the round trip.
func (c *httpCaller) call(ctx context.Context, method string, params any) (callResult, error) {
	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0", ID: int(c.nextID.Add(1)), Method: method, Params: params,
	})
	if err != nil {
		return callResult{}, fmt.Errorf("encode %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return callResult{}, fmt.Errorf("build %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	req.Header.Set("X-Real-IP", c.address)

	started := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		return callResult{}, fmt.Errorf("%s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(resp.Body)
	elapsed := time.Since(started)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return callResult{}, fmt.Errorf("read %s: %w", method, readErr)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return callResult{Duration: elapsed, Bytes: len(raw), Throttled: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return callResult{}, fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, truncate(string(raw), 200))
	}
	decoded, err := decodeFrame(raw)
	if err != nil {
		return callResult{}, fmt.Errorf("%s: %w", method, err)
	}
	failed, said := toolFailed(decoded.Result)
	return callResult{
		Duration:    elapsed,
		Bytes:       len(raw),
		Err:         decoded.Error,
		ToolError:   failed,
		ToolErrText: said,
	}, nil
}

// close releases the caller's idle connections, so a finished step's sockets do
// not sit in the next step's measurement.
func (c *httpCaller) close() { c.client.CloseIdleConnections() }

// decodeFrame reads a response body that may be a bare JSON document or a
// single server-sent event carrying one.
//
// Both are correct: this server answers a plain POST with an event stream
// unless it was started with --json-response, and measuring only the flag's
// shape would measure the path a deployment does not take by default.
func decodeFrame(raw []byte) (rpcResponse, error) {
	body := bytes.TrimSpace(raw)
	if bytes.HasPrefix(body, []byte("{")) {
		return decodeJSON(body)
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		if payload, found := strings.CutPrefix(strings.TrimSpace(line), "data:"); found {
			return decodeJSON([]byte(strings.TrimSpace(payload)))
		}
	}
	return rpcResponse{}, fmt.Errorf("no JSON document in %q", truncate(string(raw), 200))
}

// decodeJSON decodes one JSON-RPC response document.
func decodeJSON(body []byte) (rpcResponse, error) {
	var resp rpcResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return rpcResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}

// truncate shortens a quoted fragment so an error names what came back without
// reproducing a whole results page in the terminal.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// stdioCaller drives one server process over its own pipes.
//
// Unlike HTTP there is one caller per process, because that is what a client is
// on this transport: a client that wants its own stdio server starts one.
type stdioCaller struct {
	t      *target
	mu     sync.Mutex
	nextID int
}

// newStdioCaller completes the handshake the stdio binding requires and returns
// a caller ready to be timed.
//
// The handshake is done here rather than being measured because it is paid
// once per process and the scenario reports it separately as startup: folding
// it into the first tools/list would put a one-off cost into a per-call figure.
func newStdioCaller(ctx context.Context, t *target) (*stdioCaller, error) {
	c := &stdioCaller{t: t}
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "bench_resources", "version": "1"},
	}); err != nil {
		return nil, err
	}
	if err := c.notify("notifications/initialized"); err != nil {
		return nil, err
	}
	return c, nil
}

// call writes one frame and reads the reply.
func (c *stdioCaller) call(ctx context.Context, method string, params any) (callResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params})
	if err != nil {
		return callResult{}, fmt.Errorf("encode %s: %w", method, err)
	}
	started := time.Now()
	if _, wErr := c.t.stdin.Write(append(body, '\n')); wErr != nil {
		return callResult{}, fmt.Errorf("write %s: %w", method, wErr)
	}
	line, rErr := readReply(ctx, c.t.stdout)
	elapsed := time.Since(started)
	if rErr != nil {
		return callResult{}, fmt.Errorf("%s: %w\n%s", method, rErr, c.t.stderrText())
	}
	decoded, err := decodeJSON(line)
	if err != nil {
		return callResult{}, fmt.Errorf("%s: %w", method, err)
	}
	failed, said := toolFailed(decoded.Result)
	return callResult{
		Duration:    elapsed,
		Bytes:       len(line),
		Err:         decoded.Error,
		ToolError:   failed,
		ToolErrText: said,
	}, nil
}

// notify writes a frame with no id, which expects no reply.
func (c *stdioCaller) notify(method string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return fmt.Errorf("encode %s: %w", method, err)
	}
	if _, wErr := c.t.stdin.Write(append(body, '\n')); wErr != nil {
		return fmt.Errorf("write %s: %w", method, wErr)
	}
	return nil
}

// close stops the process this caller owns.
func (c *stdioCaller) close() { c.t.stop() }

// readReply reads one line of JSON-RPC from a server's stdout, skipping
// anything that is not a response to the call in flight.
//
// A notification can arrive between the request and its reply — a progress
// update, a log record a client subscribed to — and a driver that took the
// first line as the answer would time the notification instead. Only a frame
// carrying a result or an error is one.
func readReply(ctx context.Context, r *bufio.Reader) ([]byte, error) {
	type framed struct {
		line []byte
		err  error
	}
	for {
		ch := make(chan framed, 1)
		go func() {
			line, err := r.ReadBytes('\n')
			ch <- framed{line: line, err: err}
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case got := <-ch:
			if got.err != nil {
				return nil, got.err
			}
			trimmed := bytes.TrimSpace(got.line)
			if len(trimmed) == 0 {
				continue
			}
			if bytes.Contains(trimmed, []byte(`"result"`)) || bytes.Contains(trimmed, []byte(`"error"`)) {
				return trimmed, nil
			}
		}
	}
}
