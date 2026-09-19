package toolutil

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// frozenRPS is a refill rate slow enough that no bucket in this file refills
// while a test is running — one token every sixteen minutes.
//
// The burst is what each test varies, so what it measures is "how many at once"
// with the refill held still. A brisk rate would make these tests race the clock
// and pass or fail on how busy the machine is.
const frozenRPS = 0.001

// callRequest is a tools/call as the SDK hands it to a receiving middleware:
// the raw params, before the handler decodes the arguments.
func callRequest(tool string) mcp.Request {
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: tool}}
}

// drive runs one request through the middleware the limiter attaches, and
// reports what came back.
//
// It calls the middleware directly rather than through a server, because what is
// under test is the decision and its shape — a real server would add a session,
// a transport and a handler, none of which changes the answer.
func drive(t *testing.T, limiter *RateLimiter, method string, req mcp.Request) (mcp.Result, error, bool) {
	t.Helper()

	var reached bool
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		reached = true
		return &mcp.CallToolResult{}, nil
	}
	middleware := rateLimitMiddleware(func(context.Context) *RateLimiter { return limiter })
	result, err := middleware(next)(t.Context(), method, req)
	return result, err, reached
}

// TestRateLimiterRefusesACallInAShapeAModelCanAct is the first of the two
// refusal shapes, and the reason there are two.
//
// A refused tools/call comes back as a *successful* JSON-RPC result flagged
// IsError, so the model receives a structured diagnostic and the agent loop can
// back off. A JSON-RPC error would reach the client's transport layer instead
// and never be seen by the thing that has to decide to wait.
func TestRateLimiterRefusesACallInAShapeAModelCanAct(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 1)

	if _, _, reached := drive(t, limiter, methodToolsCall, callRequest("search")); !reached {
		t.Fatal("the first call was refused although the bucket was full")
	}

	result, err, reached := drive(t, limiter, methodToolsCall, callRequest("search"))
	if reached {
		t.Fatal("the second call reached the handler although the bucket was empty")
	}
	if err != nil {
		t.Fatalf("a refused tools/call returned a Go error (%v); the model never sees one", err)
	}
	call, ok := result.(*mcp.CallToolResult)
	if !ok {
		t.Fatalf("result is %T, want *mcp.CallToolResult", result)
	}
	if !call.IsError {
		t.Error("the refusal is not flagged IsError, so a model reads it as a successful call")
	}
	text := resultText(t, call)
	if !strings.HasPrefix(text, RateLimitRefusalPrefix) {
		t.Errorf("message %q does not start with the exported prefix anything matching on it looks for", text)
	}
	// Naming the tool is what makes the message useful in an agent trace, where
	// the call that was refused is otherwise indistinguishable from its
	// neighbors.
	if !strings.Contains(text, "search") {
		t.Errorf("message %q does not name the tool that was refused", text)
	}
}

// resultText returns the text of a tool result's single content block.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("the refusal carries %d content blocks, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *mcp.TextContent", result.Content[0])
	}
	return text.Text
}

// TestRateLimiterRefusesTheFlaglessMethodsAsJSONRPCErrors is the other shape:
// their results carry no error flag at all, so the refusal has to be a JSON-RPC
// error with the code that mirrors HTTP 429.
func TestRateLimiterRefusesTheFlaglessMethodsAsJSONRPCErrors(t *testing.T) {
	for _, method := range []string{methodPromptsGet, methodSubscriptionsListen} {
		t.Run(method, func(t *testing.T) {
			limiter := NewRateLimiter(frozenRPS, 1)

			if _, _, reached := drive(t, limiter, method, nil); !reached {
				t.Fatal("the first request was refused although the bucket was full")
			}

			_, err, reached := drive(t, limiter, method, nil)
			if reached {
				t.Fatal("the second request reached the handler although the bucket was empty")
			}
			var rpcErr *jsonrpc.Error
			if !errors.As(err, &rpcErr) {
				t.Fatalf("error is %T (%v), want a *jsonrpc.Error", err, err)
			}
			if rpcErr.Code != RateLimitedErrorCode {
				t.Errorf("code = %d, want %d", rpcErr.Code, RateLimitedErrorCode)
			}
			if !strings.Contains(rpcErr.Message, method) {
				t.Errorf("message %q does not name what was refused", rpcErr.Message)
			}
		})
	}
}

// TestCatalogListingSurvivesADrainedCallBucket is the property the second bucket
// exists for, and the one a single bucket would break.
//
// A refused listing is worse than a refused call: no model is in the loop to
// read the message and back off, so a client that cannot list cannot discover
// the surface at all. Draining the call bucket must therefore never cost a
// caller their discovery.
func TestCatalogListingSurvivesADrainedCallBucket(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 2)

	for range 10 {
		_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search"))
	}

	if _, err, reached := drive(t, limiter, methodToolsList, nil); err != nil || !reached {
		t.Fatalf("tools/list was refused (%v) although only the call bucket was drained", err)
	}
}

// TestCatalogListingIsMeteredOnItsOwnBucket is the other half: a separate bucket
// is not an exemption. A client listing in a loop spends the processor every
// caller of this process is waiting for.
func TestCatalogListingIsMeteredOnItsOwnBucket(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 1)

	if _, err, reached := drive(t, limiter, methodToolsList, nil); err != nil || !reached {
		t.Fatalf("the first listing was refused (%v) although the bucket was full", err)
	}

	_, err, reached := drive(t, limiter, methodToolsList, nil)
	if reached {
		t.Fatal("the second listing reached the handler although its bucket was empty")
	}
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != RateLimitedErrorCode {
		t.Errorf("error = %v, want a *jsonrpc.Error with code %d", err, RateLimitedErrorCode)
	}
}

// TestCatalogBucketIsKeptRatherThanRebuilt pins the memoization, which is the
// difference between a second limit and no limit at all: a bucket derived per
// request arrives full every time and meters nothing.
func TestCatalogBucketIsKeptRatherThanRebuilt(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 1)
	if first, second := limiter.forCatalog(), limiter.forCatalog(); first != second {
		t.Fatal("forCatalog built a second bucket; a bucket rebuilt per request is not a limit")
	}
}

// TestUnmeteredMethodsPassThrough keeps the limiter to the methods that cost
// something. The resource methods and completion/complete are already -32601
// here, and metering a method nothing can reach is a bucket with no door.
func TestUnmeteredMethodsPassThrough(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 1)
	// Drain everything it could possibly draw on.
	for range 10 {
		_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search"))
		_, _, _ = drive(t, limiter, methodToolsList, nil)
	}

	for _, method := range []string{
		"initialize", "prompts/list", "notifications/initialized",
		"resources/list", "resources/read", "completion/complete",
	} {
		t.Run(method, func(t *testing.T) {
			if IsMetered(method) {
				t.Fatalf("%s is metered; this test asserts the opposite", method)
			}
			if _, err, reached := drive(t, limiter, method, nil); err != nil || !reached {
				t.Errorf("%s was refused (%v) although it is not metered", method, err)
			}
		})
	}
}

// TestIsMeteredNamesEveryMeteredMethod is the list a second layer reads to
// decide whether a caller's bucket is worth resolving, so it has to agree with
// the middleware's own switch.
func TestIsMeteredNamesEveryMeteredMethod(t *testing.T) {
	for _, method := range []string{methodToolsCall, methodPromptsGet, methodSubscriptionsListen, methodToolsList} {
		t.Run(method, func(t *testing.T) {
			if !IsMetered(method) {
				t.Errorf("IsMetered(%q) = false, but the middleware charges it", method)
			}
		})
	}
	if IsMetered("prompts/list") {
		t.Error("IsMetered claims prompts/list is charged, and it is not")
	}
}

// TestADisabledLimiterAllowsEverything covers the deployment that turned it off
// and the transports that have no caller to be fair to.
func TestADisabledLimiterAllowsEverything(t *testing.T) {
	if NewRateLimiter(0, 40) != nil {
		t.Error("a zero rate produced a limiter; the caller treats nil as disabled")
	}
	if NewRateLimiter(-1, 40) != nil {
		t.Error("a negative rate produced a limiter")
	}
	for _, method := range []string{methodToolsCall, methodPromptsGet, methodToolsList} {
		t.Run(method, func(t *testing.T) {
			for range 100 {
				if _, err, reached := drive(t, nil, method, callRequest("search")); err != nil || !reached {
					t.Fatalf("a nil limiter refused %s (%v)", method, err)
				}
			}
		})
	}
}

// TestAZeroBurstIsRaisedToOne keeps a misconfiguration from producing a bucket
// that refuses everything forever, which reads exactly like an outage.
func TestAZeroBurstIsRaisedToOne(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 0)
	if _, err, reached := drive(t, limiter, methodToolsCall, callRequest("search")); err != nil || !reached {
		t.Fatalf("a zero-burst limiter refused its very first call (%v)", err)
	}
}

// TestRefusalsAreReportedOncePerWindow pins the self-suppression.
//
// Refusals are unbounded — their rate is the arrival rate minus the limit — so a
// line per refusal would replace a silent limiter with a log flood, which is the
// same problem pointing the other way. One line per window carries the count of
// everything it stands for.
func TestRefusalsAreReportedOncePerWindow(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 1)
	limiter.throttleWindow = time.Hour

	lines := captureWarnings(t)
	for range 20 {
		_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search"))
	}

	if got := lines(); got != 1 {
		t.Errorf("%d refusal lines were written for 19 refusals inside one window, want 1", got)
	}
}

// TestASecondWindowReportsAgain is the other half: the suppression is a window,
// not a one-shot, or a flood that outlasts the first line would go unreported
// for the life of the process.
func TestASecondWindowReportsAgain(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 1)
	limiter.throttleWindow = time.Millisecond

	lines := captureWarnings(t)
	_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search")) // allowed
	_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search")) // refused, reported
	time.Sleep(5 * time.Millisecond)
	_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search")) // refused, new window

	if got := lines(); got < 2 {
		t.Errorf("%d refusal lines across two windows, want at least 2", got)
	}
}

// TestDescribeReadsBackWhatWasConfigured covers the startup line, which is the
// only place an operator can see what the deployment settled on.
func TestDescribeReadsBackWhatWasConfigured(t *testing.T) {
	if got := NewRateLimiter(10, 40).Describe(); got != "10 rps, burst 40" {
		t.Errorf("Describe() = %q", got)
	}
	if got := (*RateLimiter)(nil).Describe(); got != "off" {
		t.Errorf("a nil limiter describes itself as %q, want %q", got, "off")
	}
}

// captureWarnings installs a slog handler that counts the refusal lines written
// while the test runs, and puts the previous logger back afterwards.
//
// It counts by message rather than by level so an unrelated warning from another
// part of the process cannot make a suppression test pass or fail.
func captureWarnings(t *testing.T) func() int {
	t.Helper()

	counter := &warningCounter{}
	previous := slog.Default()
	slog.SetDefault(slog.New(counter))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return counter.count
}

// warningCounter is a slog.Handler that counts the limiter's own refusal lines.
type warningCounter struct {
	mu sync.Mutex
	n  int
}

func (c *warningCounter) Enabled(context.Context, slog.Level) bool { return true }

func (c *warningCounter) Handle(_ context.Context, record slog.Record) error {
	if strings.Contains(record.Message, "rate limit exceeded") {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
	return nil
}

func (c *warningCounter) WithAttrs([]slog.Attr) slog.Handler { return c }

func (c *warningCounter) WithGroup(string) slog.Handler { return c }

func (c *warningCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
