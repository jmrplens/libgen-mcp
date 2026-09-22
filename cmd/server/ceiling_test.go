package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/v2/internal/toolutil"
)

// heavyCall is a tools/call for a tool that moves a file's bytes.
func heavyCall(tool string) mcp.Request {
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: tool}}
}

// startCall drives one request through the ceiling middleware for address, with
// a handler that blocks until the returned function is called.
//
// The blocking is the point: a ceiling is about calls that are still running, so
// a test that let each one finish would never have two in flight at once.
func startCall(t *testing.T, records *clientRecords, ceiling heavyCeiling, address string, req mcp.Request) (result <-chan mcp.Result, finish func()) {
	t.Helper()

	release := make(chan struct{})
	done := make(chan mcp.Result, 1)
	entered := make(chan struct{})

	handler := records.limitHeavyCalls(ceiling)(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		close(entered)
		<-release
		return &mcp.CallToolResult{}, nil
	})

	rec, end := records.begin(t.Context(), address)
	ctx := context.WithValue(t.Context(), recordKey{}, rec)
	go func() {
		defer end()
		res, _ := handler(ctx, methodToolsCall, req)
		done <- res
	}()

	// Either the handler was reached, or the call was refused before it.
	select {
	case <-entered:
	case res := <-done:
		done <- res
	case <-time.After(5 * time.Second):
		t.Fatal("the call neither ran nor was refused")
	}
	// Idempotent, because a test that releases a call early also defers the
	// release, and closing a channel twice panics.
	var once sync.Once
	return done, func() { once.Do(func() { close(release) }) }
}

// refusalText returns the message of a refused call, or "" when the call was
// served.
func refusalText(t *testing.T, result mcp.Result) string {
	t.Helper()
	call, ok := result.(*mcp.CallToolResult)
	if !ok || !call.IsError {
		return ""
	}
	if len(call.Content) != 1 {
		t.Fatalf("the refusal carries %d content blocks, want 1", len(call.Content))
	}
	text, ok := call.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *mcp.TextContent", call.Content[0])
	}
	return text.Text
}

// TestTheCeilingIsPerCallerAndNotPerProcess is the whole claim.
//
// One address at its ceiling must not cost another address its first call —
// that is the difference between a per-caller bound and a second process-wide
// one, and it is the failure the download semaphore already has: four slots, and
// any caller may take all four.
func TestTheCeilingIsPerCallerAndNotPerProcess(t *testing.T) {
	records, _ := testRecords(t, 8)
	ceiling := heavyCeiling{perClient: 2, perProcess: maxHeavyPerProcess}

	firstDone, finishFirst := startCall(t, records, ceiling, "203.0.113.7", heavyCall("download"))
	defer finishFirst()
	secondDone, finishSecond := startCall(t, records, ceiling, "203.0.113.7", heavyCall("read"))
	defer finishSecond()

	// The third from the same caller, with two still running.
	thirdDone, finishThird := startCall(t, records, ceiling, "203.0.113.7", heavyCall("download"))
	defer finishThird()
	text := refusalText(t, <-thirdDone)
	if text == "" {
		t.Fatal("a third concurrent call from one caller was served; the ceiling is 2")
	}
	if !strings.Contains(text, "2") {
		t.Errorf("the refusal %q does not say what the limit is", text)
	}

	// A different caller, with the first one's two still running.
	otherDone, finishOther := startCall(t, records, ceiling, "198.51.100.23", heavyCall("download"))
	defer finishOther()
	select {
	case res := <-otherDone:
		t.Fatalf("the second caller's first call was refused: %q", refusalText(t, res))
	default:
	}

	// And once the first caller's calls end, their next one is admitted again.
	finishFirst()
	finishSecond()
	<-firstDone
	<-secondDone

	freedDone, finishFreed := startCall(t, records, ceiling, "203.0.113.7", heavyCall("download"))
	defer finishFreed()
	select {
	case res := <-freedDone:
		t.Fatalf("a call was refused although the caller's earlier ones had finished: %q", refusalText(t, res))
	default:
	}
}

// TestTheProcessCeilingRefusesEverybody is the bound the per-caller one cannot
// give.
//
// A per-caller ceiling multiplies by however many callers there are, so it says
// nothing at all about the process. This is the one that does, and it is
// deliberately not configurable.
func TestTheProcessCeilingRefusesEverybody(t *testing.T) {
	records, _ := testRecords(t, 64)
	ceiling := heavyCeiling{perClient: 4, perProcess: 3}

	// Each call must stay in flight while the next one starts, so the deferred
	// finish belongs to this function rather than to a per-case subtest.
	// sequential: the calls are held open together, not run one at a time
	for i, address := range []string{"a", "b", "c"} {
		_, finish := startCall(t, records, ceiling, address, heavyCall("download"))
		defer finish()
		if records.heavyTotal != i+1 {
			t.Fatalf("after %d calls the process count is %d", i+1, records.heavyTotal)
		}
	}

	// A fourth caller, well inside their own ceiling.
	done, finish := startCall(t, records, ceiling, "d", heavyCall("download"))
	defer finish()
	text := refusalText(t, <-done)
	if text == "" {
		t.Fatal("a fourth call was served although the process ceiling is 3")
	}
	if !strings.Contains(text, "3") {
		t.Errorf("the refusal %q does not say what the process limit is", text)
	}
}

// TestOnlyTheByteMovingToolsAreBounded keeps the ceiling off the calls it has
// nothing to say about: a search is one page fetch, and how fast those arrive is
// the rate bucket's job.
func TestOnlyTheByteMovingToolsAreBounded(t *testing.T) {
	records, _ := testRecords(t, 8)
	ceiling := heavyCeiling{perClient: 1, perProcess: maxHeavyPerProcess}

	_, finishHeavy := startCall(t, records, ceiling, "203.0.113.7", heavyCall("download"))
	defer finishHeavy()

	for _, tool := range []string{"search", "get_details"} {
		t.Run(tool, func(t *testing.T) {
			done, finish := startCall(t, records, ceiling, "203.0.113.7", heavyCall(tool))
			defer finish()
			select {
			case res := <-done:
				t.Fatalf("%s was refused by the in-flight ceiling: %q", tool, refusalText(t, res))
			default:
			}
		})
	}
}

// TestTheCountReturnsToZeroWhenACallEndsBadly is what keeps a caller from being
// locked out of their own ceiling by calls that are over.
//
// A handler that returns an error is every unhappy ending this server has: the
// client went away and the carrier canceled the call, the wall-clock cap fired,
// or the download chain ran out of sources. The count has to come back for all
// of them, and it does because the decrement is deferred rather than tied to a
// successful return.
func TestTheCountReturnsToZeroWhenACallEndsBadly(t *testing.T) {
	records, _ := testRecords(t, 8)
	ceiling := heavyCeiling{perClient: 1, perProcess: maxHeavyPerProcess}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "the caller went away", err: context.Canceled},
		{name: "the wall-clock cap fired", err: context.DeadlineExceeded},
		{name: "the handler failed", err: errors.New("every source is exhausted")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, end := records.begin(t.Context(), "203.0.113.7")
			ctx := context.WithValue(t.Context(), recordKey{}, rec)
			handler := records.limitHeavyCalls(ceiling)(func(context.Context, string, mcp.Request) (mcp.Result, error) {
				return nil, tc.err
			})
			_, err := handler(ctx, methodToolsCall, heavyCall("download"))
			end()

			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want the handler's own", err)
			}
			if rec.heavy != 0 {
				t.Errorf("the caller's count is %d after a call that ended with %v", rec.heavy, tc.err)
			}
			if records.heavyTotal != 0 {
				t.Errorf("the process count is %d after a call that ended with %v", records.heavyTotal, tc.err)
			}
		})
	}
}

// TestTheCountIsTakenWithNoCeilingConfigured is the trap the sibling project
// fell into with its own counter.
//
// Counting and capping are two jobs. Skipping the increment when no cap is set
// switches both off at once — and the count is also what tells the eviction that
// this caller is busy, so removing the ceiling would quietly make every
// downloading record look idle and evictable.
func TestTheCountIsTakenWithNoCeilingConfigured(t *testing.T) {
	records, _ := testRecords(t, 8)
	off := heavyCeiling{perClient: 0, perProcess: maxHeavyPerProcess}

	rec, end := records.begin(t.Context(), "203.0.113.7")
	defer end()
	ctx := context.WithValue(t.Context(), recordKey{}, rec)

	var seen int
	handler := records.limitHeavyCalls(off)(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		seen = rec.heavy
		if !rec.busy() {
			t.Error("a record with a download running reads as idle, so the eviction may take it")
		}
		return &mcp.CallToolResult{}, nil
	})
	if _, err := handler(ctx, methodToolsCall, heavyCall("download")); err != nil {
		t.Fatalf("the call failed: %v", err)
	}

	if seen != 1 {
		t.Errorf("the caller's count during the call was %d, want 1 even with no ceiling", seen)
	}
	if rec.heavy != 0 {
		t.Errorf("the count is %d after the call, want 0", rec.heavy)
	}
}

// TestACallWithNoRecordIsLeftAlone covers stdio and the in-memory session: one
// caller, nobody to be fair to, and the download semaphore already theirs.
func TestACallWithNoRecordIsLeftAlone(t *testing.T) {
	records, _ := testRecords(t, 8)
	ceiling := heavyCeiling{perClient: 1, perProcess: 1}

	handler := records.limitHeavyCalls(ceiling)(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{}, nil
	})
	for range 10 {
		result, err := handler(t.Context(), methodToolsCall, heavyCall("download"))
		if err != nil {
			t.Fatalf("the call failed: %v", err)
		}
		if text := refusalText(t, result); text != "" {
			t.Fatalf("a call carrying no caller record was refused: %q", text)
		}
	}
}

// TestResolveHeavyCeilingDerivesItsDefault is why the default is not a literal.
//
// What it bounds is the download semaphore, and that is 2 in code and 4 on the
// hosted deployment. A number written here would be right for one of them and
// silently wrong for the other.
func TestResolveHeavyCeilingDerivesItsDefault(t *testing.T) {
	cases := []struct {
		name                   string
		flagValue              int
		explicit               bool
		maxConcurrentDownloads int
		wantPerClient          int
	}{
		{name: "unset follows the code default", maxConcurrentDownloads: 2, wantPerClient: 2},
		{name: "unset follows the deployment's", maxConcurrentDownloads: 4, wantPerClient: 4},
		{name: "explicit wins", flagValue: 1, explicit: true, maxConcurrentDownloads: 4, wantPerClient: 1},
		{name: "explicit zero turns it off", flagValue: 0, explicit: true, maxConcurrentDownloads: 4, wantPerClient: 0},
		{name: "explicit negative turns it off", flagValue: -3, explicit: true, maxConcurrentDownloads: 4, wantPerClient: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ceiling := resolveHeavyCeiling(tc.flagValue, tc.explicit, tc.maxConcurrentDownloads)
			if ceiling.perClient != tc.wantPerClient {
				t.Errorf("perClient = %d, want %d", ceiling.perClient, tc.wantPerClient)
			}
			// The process-wide one is never configurable, whatever was passed.
			if ceiling.perProcess != maxHeavyPerProcess {
				t.Errorf("perProcess = %d, want the fixed %d", ceiling.perProcess, maxHeavyPerProcess)
			}
		})
	}
}

// TestCeilingDescribesItselfForTheStartupLine covers the only place an operator
// can read back what the deployment settled on.
func TestCeilingDescribesItselfForTheStartupLine(t *testing.T) {
	on := resolveHeavyCeiling(0, false, 4).describe()
	if !strings.Contains(on, "4 per charged address") {
		t.Errorf("describe() = %q, want it to name the per-caller bound", on)
	}
	off := resolveHeavyCeiling(0, true, 4).describe()
	if !strings.Contains(off, "off per caller") {
		t.Errorf("describe() = %q, want it to say the per-caller bound is off", off)
	}
	if !strings.Contains(off, "across the process") {
		t.Errorf("describe() = %q, want it to name the process bound, which is never off", off)
	}
}

// TestTheRefusalIsAShapeAModelCanAct keeps the ceiling from reading as a mirror
// failure, which a model answers by retrying into it forever.
func TestTheRefusalIsAShapeAModelCanAct(t *testing.T) {
	records, _ := testRecords(t, 8)
	ceiling := heavyCeiling{perClient: 1, perProcess: maxHeavyPerProcess}

	_, finish := startCall(t, records, ceiling, "203.0.113.7", heavyCall("download"))
	defer finish()

	done, finishSecond := startCall(t, records, ceiling, "203.0.113.7", heavyCall("download"))
	defer finishSecond()

	result := <-done
	call, ok := result.(*mcp.CallToolResult)
	if !ok {
		t.Fatalf("result is %T, want *mcp.CallToolResult", result)
	}
	if !call.IsError {
		t.Error("the refusal is not flagged IsError, so a model reads it as a successful call")
	}
	// The same shape the rate limiter and the wall-clock cap use, so a model
	// meets one answer for "this is your side to fix".
	if text := refusalText(t, result); text == "" || text != toolutil.RefusalResult(text).Content[0].(*mcp.TextContent).Text {
		t.Errorf("the refusal is not the shared refusal shape: %q", text)
	}
}
