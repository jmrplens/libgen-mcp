package tools

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/toolutil"
)

// withActionTimeout sets the process-wide cap for one test and puts it back.
func withActionTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	previous := toolutil.ActionTimeout()
	t.Cleanup(func() { toolutil.SetActionTimeout(previous) })
	toolutil.SetActionTimeout(d)
}

// timeoutIn and timeoutOut are the empty argument and result types the probe
// handlers below use; the cap has nothing to do with what a tool carries.
type (
	timeoutIn  struct{}
	timeoutOut struct{}
)

// TestWithRecoveryHandsTheHandlerTheBoundedContext is the assertion the wiring
// can fail silently without.
//
// withRecovery derives a bounded context and must pass THAT one on. Passing the
// original compiles, runs, meters the call and bounds nothing at all — there is
// no error, no warning and no test failure anywhere else, because every other
// assertion about this wrapper is about panics and metrics. So the probe handler
// reads its own ctx.Deadline() and reports what it was given.
func TestWithRecoveryHandsTheHandlerTheBoundedContext(t *testing.T) {
	withActionTimeout(t, time.Hour)

	var (
		sawDeadline bool
		remaining   time.Duration
	)
	handler := mcp.ToolHandlerFor[timeoutIn, timeoutOut](
		func(ctx context.Context, _ *mcp.CallToolRequest, _ timeoutIn) (*mcp.CallToolResult, timeoutOut, error) {
			deadline, ok := ctx.Deadline()
			sawDeadline = ok
			if ok {
				remaining = time.Until(deadline)
			}
			return &mcp.CallToolResult{}, timeoutOut{}, nil
		},
	)

	if _, _, err := withRecovery("probe", handler)(t.Context(), nil, timeoutIn{}); err != nil {
		t.Fatalf("the probe handler failed: %v", err)
	}
	if !sawDeadline {
		t.Fatal("the handler received a context with no deadline; withRecovery derived one and passed the original on")
	}
	if remaining <= 0 || remaining > time.Hour {
		t.Errorf("the handler's deadline is %v away, want within the configured hour", remaining)
	}
}

// TestWithRecoveryLeavesTheContextAloneWhenTheCapIsOff covers the deployment
// that turned it off: the handler must get the caller's context unchanged, not
// one with an invented bound.
func TestWithRecoveryLeavesTheContextAloneWhenTheCapIsOff(t *testing.T) {
	withActionTimeout(t, 0)

	var sawDeadline bool
	handler := mcp.ToolHandlerFor[timeoutIn, timeoutOut](
		func(ctx context.Context, _ *mcp.CallToolRequest, _ timeoutIn) (*mcp.CallToolResult, timeoutOut, error) {
			_, sawDeadline = ctx.Deadline()
			return &mcp.CallToolResult{}, timeoutOut{}, nil
		},
	)

	if _, _, err := withRecovery("probe", handler)(t.Context(), nil, timeoutIn{}); err != nil {
		t.Fatalf("the probe handler failed: %v", err)
	}
	if sawDeadline {
		t.Error("a deadline was imposed although the cap is disabled")
	}
}

// TestASlowHandlerIsCancelledAndTheCallReturns is the behavior a held slot is
// released by.
//
// The handler waits on its own context rather than sleeping a fixed time, so
// what is measured is the cancellation reaching it — a sleep would pass on a
// wrapper that bounded nothing, as long as the sleep was shorter than the test.
func TestASlowHandlerIsCancelledAndTheCallReturns(t *testing.T) {
	withActionTimeout(t, 20*time.Millisecond)

	var cause error
	handler := mcp.ToolHandlerFor[timeoutIn, timeoutOut](
		func(ctx context.Context, _ *mcp.CallToolRequest, _ timeoutIn) (*mcp.CallToolResult, timeoutOut, error) {
			select {
			case <-ctx.Done():
				cause = ctx.Err()
				return nil, timeoutOut{}, ctx.Err()
			case <-time.After(30 * time.Second):
				return &mcp.CallToolResult{}, timeoutOut{}, nil
			}
		},
	)

	start := time.Now()
	_, _, err := withRecovery("probe", handler)(t.Context(), nil, timeoutIn{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("the call returned no error, so the handler ran to completion")
	}
	if cause != context.DeadlineExceeded {
		t.Errorf("the handler saw %v, want the deadline so it can tell this from a client hanging up", cause)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the call took %v; the cap did not end it", elapsed)
	}
}

// TestAFastHandlerIsUntouched is the negative that keeps the cap from being a
// cure worse than the disease.
func TestAFastHandlerIsUntouched(t *testing.T) {
	withActionTimeout(t, time.Hour)

	handler := mcp.ToolHandlerFor[timeoutIn, timeoutOut](
		func(context.Context, *mcp.CallToolRequest, timeoutIn) (*mcp.CallToolResult, timeoutOut, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, timeoutOut{}, nil
		},
	)

	result, _, err := withRecovery("probe", handler)(t.Context(), nil, timeoutIn{})
	if err != nil {
		t.Fatalf("a call well inside the cap failed: %v", err)
	}
	if result == nil || len(result.Content) == 0 {
		t.Error("the result was lost on the way back through the wrapper")
	}
}
