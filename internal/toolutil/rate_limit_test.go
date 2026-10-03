package toolutil

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
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
//
// It runs in a synctest bubble, so the sleep moves a fake clock by exactly the
// amount asked rather than racing the real one.
func TestASecondWindowReportsAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := NewRateLimiter(frozenRPS, 1)
		limiter.throttleWindow = time.Millisecond

		lines := captureWarnings(t)
		_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search")) // allowed
		_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search")) // refused, reported
		time.Sleep(5 * time.Millisecond)
		_, _, _ = drive(t, limiter, methodToolsCall, callRequest("search")) // refused, new window

		if got := lines(); got != 2 {
			t.Errorf("%d refusal lines across two windows, want 2", got)
		}
	})
}

// TestRefusalReportsFollowTheDefaultWindow drives the report of a limiter that
// was given no window of its own, on a fake clock.
//
// Refusals inside the ten-second window are held back and counted, the line
// that opens the next window carries that count, a window that has fully
// elapsed opens the next one, and a refusal that names nothing is reported as
// the tools/call it is.
func TestRefusalReportsFollowTheDefaultWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := NewRateLimiter(frozenRPS, 1)
		recorder := captureRefusals(t)

		limiter.reportRefusal(t.Context(), "")
		time.Sleep(10*time.Second - time.Nanosecond)
		for range 3 {
			limiter.reportRefusal(t.Context(), "search")
		}
		time.Sleep(time.Nanosecond)
		limiter.reportRefusal(t.Context(), "search")

		want := []refusalLine{
			{what: methodToolsCall, alsoRefused: 0},
			{what: "search", alsoRefused: 3},
		}
		if got := recorder.lines(); !slices.Equal(got, want) {
			t.Errorf("refusal lines = %+v, want %+v", got, want)
		}
	})
}

// TestADisabledLimiterReportsNothing verifies the report on the two disabled
// shapes, a nil limiter and one with no bucket: neither has a rate to report,
// and neither may fail for being asked.
func TestADisabledLimiterReportsNothing(t *testing.T) {
	testCases := []struct {
		name    string
		limiter *RateLimiter
	}{
		{name: "nil limiter", limiter: nil},
		{name: "no bucket", limiter: &RateLimiter{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			lines := captureWarnings(t)
			tc.limiter.reportRefusal(t.Context(), methodToolsCall)
			if got := lines(); got != 0 {
				t.Errorf("a disabled limiter wrote %d refusal lines, want 0", got)
			}
		})
	}
}

// TestRateLimitedErrorCodeIsThePublishedNumber pins the code a refused
// flagless method carries. Clients match on the number, so it is a wire
// contract rather than a detail.
func TestRateLimitedErrorCodeIsThePublishedNumber(t *testing.T) {
	if RateLimitedErrorCode != -42900 {
		t.Errorf("RateLimitedErrorCode = %d, want -42900", RateLimitedErrorCode)
	}
}

// TestToolNameOfReadsBothParamShapes verifies the tool name is read from the
// raw params a receiving middleware sees and from the decoded ones a handler
// sees, trimmed, and that anything else, typed nils included, names no tool.
func TestToolNameOfReadsBothParamShapes(t *testing.T) {
	testCases := []struct {
		name string
		req  mcp.Request
		want string
	}{
		{name: "raw params", req: callRequest(" search "), want: "search"},
		{name: "decoded params", req: &mcp.ServerRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Name: " download "}}, want: "download"},
		{name: "typed nil raw params", req: &mcp.CallToolRequest{Params: (*mcp.CallToolParamsRaw)(nil)}, want: ""},
		{name: "typed nil decoded params", req: &mcp.ServerRequest[*mcp.CallToolParams]{}, want: ""},
		{name: "another method's params", req: &mcp.ListToolsRequest{Params: &mcp.ListToolsParams{}}, want: ""},
		{name: "no request", req: nil, want: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolNameOf(tc.req); got != tc.want {
				t.Errorf("ToolNameOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAttachRateLimitMetersAServer drives the registration end to end: a
// server given a limiter refuses the call its bucket cannot pay for, and a
// registration missing either half leaves the server unmetered rather than
// failing.
func TestAttachRateLimitMetersAServer(t *testing.T) {
	AttachRateLimit(nil, func(context.Context) *RateLimiter { return nil })

	limited := NewRateLimiter(frozenRPS, 1)
	testCases := []struct {
		name        string
		resolve     func(context.Context) *RateLimiter
		wantRefused bool
	}{
		{name: "a limiter refuses the second call", resolve: func(context.Context) *RateLimiter { return limited }, wantRefused: true},
		{name: "no resolver meters nothing", resolve: nil, wantRefused: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			session := servedSession(t, tc.resolve)

			first := callTool(t, session)
			if first.IsError {
				t.Fatalf("the first call was refused: %+v", first.Content)
			}
			second := callTool(t, session)
			if second.IsError != tc.wantRefused {
				t.Errorf("the second call's IsError = %v, want %v", second.IsError, tc.wantRefused)
			}
		})
	}
}

// servedSession starts a server with one tool, attaches the limiter resolve
// names, and returns a client session connected to it in memory.
func servedSession(t *testing.T, resolve func(context.Context) *RateLimiter) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "metered", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "served"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "served"}}}, nil, nil
	})
	AttachRateLimit(server, resolve)

	serverEnd, clientEnd := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverEnd, nil)
	if err != nil {
		t.Fatalf("connect the server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "caller", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), clientEnd, nil)
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callTool calls the one tool servedSession registers.
func callTool(t *testing.T, session *mcp.ClientSession) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "served"})
	if err != nil {
		t.Fatalf("call the tool: %v", err)
	}
	return result
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

// refusalLine is what one refusal line said: what was refused, and how many
// earlier refusals it stands for.
type refusalLine struct {
	what        string
	alsoRefused int64
}

// refusalRecorder is a slog.Handler that keeps what each refusal line said.
type refusalRecorder struct {
	mu   sync.Mutex
	seen []refusalLine
}

// captureRefusals installs a refusalRecorder for the test and puts the
// previous logger back afterwards.
func captureRefusals(t *testing.T) *refusalRecorder {
	t.Helper()

	recorder := &refusalRecorder{}
	previous := slog.Default()
	slog.SetDefault(slog.New(recorder))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return recorder
}

// Enabled accepts every level, so the recorder filters by message alone.
func (r *refusalRecorder) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps the two fields of a refusal line and ignores every other line.
func (r *refusalRecorder) Handle(_ context.Context, record slog.Record) error {
	if !strings.Contains(record.Message, "rate limit exceeded") {
		return nil
	}
	var line refusalLine
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "what":
			line.what = attr.Value.String()
		case "also_refused_since_last_report":
			line.alsoRefused = attr.Value.Int64()
		}
		return true
	})
	r.mu.Lock()
	r.seen = append(r.seen, line)
	r.mu.Unlock()
	return nil
}

// WithAttrs returns the recorder itself: the limiter adds no logger attributes.
func (r *refusalRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }

// WithGroup returns the recorder itself: the limiter opens no groups.
func (r *refusalRecorder) WithGroup(string) slog.Handler { return r }

// lines returns what every refusal line written so far said, in order.
func (r *refusalRecorder) lines() []refusalLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

// modernCallRequest is a tools/call as a client of revision version sends it,
// naming its revision in _meta the way every 2026-07-28 request does.
func modernCallRequest(tool, version string) mcp.Request {
	params := &mcp.CallToolParamsRaw{Name: tool}
	params.SetMeta(map[string]any{mcp.MetaKeyProtocolVersion: version})
	return &mcp.CallToolRequest{Params: params}
}

// decodedCallRequest is a tools/call whose params have already been decoded,
// the other shape [ToolNameOf] accepts, naming revision version in _meta.
func decodedCallRequest(version string) mcp.Request {
	params := &mcp.CallToolParams{Name: "download"}
	params.SetMeta(map[string]any{mcp.MetaKeyProtocolVersion: version})
	return &mcp.ServerRequest[*mcp.CallToolParams]{Params: params}
}

// wireFields writes a result the way the SDK sends it and returns its
// top-level fields, so an absent resultType can be told from an empty one.
func wireFields(t *testing.T, result mcp.Result) map[string]any {
	t.Helper()
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("the result does not marshal: %v", err)
	}
	var fields map[string]any
	if err = json.Unmarshal(wire, &fields); err != nil {
		t.Fatalf("the result is not a JSON object: %v (%s)", err, wire)
	}
	return fields
}

// TestRefusalResultCarriesResultTypeWhereTheRevisionRequiresIt covers the one
// field a refusal's revision decides.
//
// 2026-07-28 requires resultType on every result, and the SDK sets it only on
// the results its tool dispatcher makes, so a refusal written before the
// dispatcher runs has to set it itself. An earlier revision gets none, which is
// what the SDK sends such a client from the dispatcher too. Every row also
// checks that the message and the error flag survive the labeling, since the
// labeled refusal is rebuilt from its wire form.
func TestRefusalResultCarriesResultTypeWhereTheRevisionRequiresIt(t *testing.T) {
	cases := []struct {
		name    string
		req     mcp.Request
		labeled bool
	}{
		{name: "no request", req: nil, labeled: false},
		{name: "a request without params", req: &mcp.CallToolRequest{}, labeled: false},
		{name: "an initialize-era call", req: callRequest("download"), labeled: false},
		{name: "a call naming 2025-11-25", req: modernCallRequest("download", "2025-11-25"), labeled: false},
		{name: "a call naming 2026-07-28", req: modernCallRequest("download", "2026-07-28"), labeled: true},
		{name: "a call naming a later revision", req: modernCallRequest("download", "2027-03-01"), labeled: true},
		{name: "decoded params naming 2026-07-28", req: decodedCallRequest("2026-07-28"), labeled: true},
		{name: "nil decoded params", req: &mcp.ServerRequest[*mcp.CallToolParams]{}, labeled: false},
		{name: "a request of another method", req: &mcp.GetPromptRequest{Params: &mcp.GetPromptParams{}}, labeled: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := RefusalResult(tc.req, "slow down")
			if !result.IsError {
				t.Error("the refusal is not flagged IsError")
			}
			if text := resultText(t, result); text != "slow down" {
				t.Errorf("the refusal reads %q, want the message it was given", text)
			}
			got, present := wireFields(t, result)["resultType"]
			switch {
			case tc.labeled && got != "complete":
				t.Errorf("resultType = %v (present: %t), want \"complete\": revision 2026-07-28 requires it on every result", got, present)
			case !tc.labeled && present:
				t.Errorf("resultType = %v on a call of an earlier revision, which the SDK leaves unlabeled", got)
			}
		})
	}
}

// TestRateLimiterRefusalAtModernRevisionCarriesResultType is the rate
// limiter's refusal of a 2026-07-28 call, which is the path the wire-level
// test in test/e2e/http drives through the real binary.
func TestRateLimiterRefusalAtModernRevisionCarriesResultType(t *testing.T) {
	limiter := NewRateLimiter(frozenRPS, 1)
	req := modernCallRequest("search", "2026-07-28")
	if _, _, reached := drive(t, limiter, methodToolsCall, req); !reached {
		t.Fatal("the first call was refused although the bucket was full")
	}
	result, _, reached := drive(t, limiter, methodToolsCall, req)
	if reached {
		t.Fatal("the second call reached the handler although the bucket was empty")
	}
	if got := wireFields(t, result)["resultType"]; got != "complete" {
		t.Errorf("the refusal's resultType = %v, want \"complete\"", got)
	}
}

// sdkLabelFix is what a failure of the pin below asks the go-sdk bump to do.
const sdkLabelFix = "Delete labeledRefusal and declaresResultTypeRevision from internal/toolutil/rate_limit.go, " +
	"let RefusalResult return the plain result, delete this test, and mark the entry in " +
	"docs/development/upstream-bugs.md merged in the go-sdk release that carries it, in this same pull request."

// TestSDKLeavesAMiddlewareMadeToolResultUnlabeled pins the go-sdk behavior
// RefusalResult works around: a tools/call result a receiving middleware makes
// goes out at 2026-07-28 without resultType, while one the tool dispatcher
// makes carries "complete".
//
// It drives a bare SDK server over an in-memory pipe and reads the raw JSON-RPC
// response, because the SDK's own client decodes resultType into an unexported
// field and cannot say whether it arrived. go-sdk fixed this in e40f35d
// (modelcontextprotocol/go-sdk#1226), after v1.8.0. The bump that brings it
// fails here, which is the signal that the workaround has become dead code.
func TestSDKLeavesAMiddlewareMadeToolResultUnlabeled(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "pin", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "served"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "served"}}}, nil, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == methodToolsCall && ToolNameOf(req) == "refused" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "refused"}}}, nil
			}
			return next(ctx, method, req)
		}
	})

	serverEnd, clientEnd := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), serverEnd, nil)
	if err != nil {
		t.Fatalf("connect the server: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	conn, err := clientEnd.Connect(t.Context())
	if err != nil {
		t.Fatalf("connect the client end: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	served := rawModernToolCall(t, conn, 1, "served")
	if got := served["resultType"]; got != "complete" {
		t.Fatalf("a dispatcher-made result at 2026-07-28 carried resultType %v, want \"complete\": the control is broken, so this test says nothing about the middleware's result", got)
	}
	refused := rawModernToolCall(t, conn, 2, "refused")
	if got, present := refused["resultType"]; present {
		t.Errorf("a middleware-made result now carries resultType %v: the go-sdk this module builds against labels it itself, which is e40f35d (go-sdk#1226). %s", got, sdkLabelFix)
	}
}

// rawModernToolCall sends one tools/call at 2026-07-28 on conn and returns the
// result object of the response as it arrived.
func rawModernToolCall(t *testing.T, conn mcp.Connection, id int64, tool string) map[string]any {
	t.Helper()
	requestID, err := jsonrpc.MakeID(float64(id))
	if err != nil {
		t.Fatalf("make the request id: %v", err)
	}
	params, err := json.Marshal(map[string]any{
		"name":      tool,
		"arguments": map[string]any{},
		"_meta": map[string]any{
			mcp.MetaKeyProtocolVersion:    "2026-07-28",
			mcp.MetaKeyClientCapabilities: map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("marshal the params: %v", err)
	}
	if err = conn.Write(t.Context(), &jsonrpc.Request{ID: requestID, Method: methodToolsCall, Params: params}); err != nil {
		t.Fatalf("write the %s call: %v", tool, err)
	}
	msg, err := conn.Read(t.Context())
	if err != nil {
		t.Fatalf("read the answer to the %s call: %v", tool, err)
	}
	response, ok := msg.(*jsonrpc.Response)
	if !ok {
		t.Fatalf("the answer to the %s call is %T, want a response", tool, msg)
	}
	if response.Error != nil {
		t.Fatalf("the %s call was answered with an error: %v", tool, response.Error)
	}
	var result map[string]any
	if err = json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("the %s call's result is not a JSON object: %v (%s)", tool, err, response.Result)
	}
	return result
}
