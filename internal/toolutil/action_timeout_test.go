package toolutil

import (
	"context"
	"testing"
	"time"
)

// withActionTimeout sets the process-wide cap for one test and puts it back.
//
// Process-wide state is what this is, and a test that left it set would change
// what every later test in the binary measures — which is the failure mode the
// cap itself would be blamed for.
func withActionTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	previous := ActionTimeout()
	t.Cleanup(func() { SetActionTimeout(previous) })
	SetActionTimeout(d)
}

// TestWithActionDeadlineBoundsACall is the plain case.
func TestWithActionDeadlineBoundsACall(t *testing.T) {
	withActionTimeout(t, time.Hour)

	ctx, cancel := WithActionDeadline(t.Context())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("no deadline was set, so nothing bounds the call")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Hour {
		t.Errorf("deadline is %v away, want within the configured hour", remaining)
	}
}

// TestZeroDisablesTheCap covers the deployment that would rather hold a slot
// than refuse a download.
//
// Zero is a value here rather than an omission, which is why Validate accepts it
// where it refuses zero for every other bound.
func TestZeroDisablesTheCap(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		withActionTimeout(t, d)

		ctx, cancel := WithActionDeadline(t.Context())
		defer cancel()

		if _, ok := ctx.Deadline(); ok {
			t.Errorf("SetActionTimeout(%v) still bounded the call", d)
		}
		if ActionTimeout() != 0 {
			t.Errorf("ActionTimeout() = %v after SetActionTimeout(%v), want 0", ActionTimeout(), d)
		}
	}
}

// TestACallersOwnShorterDeadlineWins pins that this is a ceiling on what the
// deployment will spend, not a floor on what a client may ask for.
//
// context.WithTimeout keeps the earlier of the two, so a client that gave itself
// ten milliseconds does not get an hour because the deployment configured one.
// Deriving rather than replacing is what gives that for free, and replacing
// would look identical at the call site.
func TestACallersOwnShorterDeadlineWins(t *testing.T) {
	withActionTimeout(t, time.Hour)

	caller, cancelCaller := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancelCaller()

	ctx, cancel := WithActionDeadline(caller)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the derived context has no deadline at all")
	}
	if remaining := time.Until(deadline); remaining > time.Second {
		t.Errorf("deadline is %v away; the deployment's hour overrode the caller's own", remaining)
	}
}

// TestTheDerivedContextIsCancelledWhenTheDeadlinePasses is the behavior the
// whole thing exists for: a handler still running past the cap is told to stop.
func TestTheDerivedContextIsCancelledWhenTheDeadlinePasses(t *testing.T) {
	withActionTimeout(t, 20*time.Millisecond)

	ctx, cancel := WithActionDeadline(t.Context())
	defer cancel()

	select {
	case <-ctx.Done():
		if !isDeadlineExceeded(ctx) {
			t.Errorf("ctx.Err() = %v, want the deadline, so a handler can tell this from a client hanging up", ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the deadline never fired")
	}
}

// isDeadlineExceeded reports whether the context ended at its deadline rather
// than by cancellation.
func isDeadlineExceeded(ctx context.Context) bool {
	return ctx.Err() == context.DeadlineExceeded
}

// TestTheCancelFunctionIsAlwaysSafeToCall covers the disabled path, where there
// is no timer to stop.
//
// Callers defer cancel unconditionally rather than testing whether a deadline
// was added, so a nil or panicking function on the disabled path would be a
// crash in every call of a deployment that turned the cap off.
func TestTheCancelFunctionIsAlwaysSafeToCall(t *testing.T) {
	withActionTimeout(t, 0)

	ctx, cancel := WithActionDeadline(t.Context())
	if cancel == nil {
		t.Fatal("cancel is nil; every caller defers it")
	}
	cancel()
	cancel() // twice, which context.CancelFunc also tolerates
	if ctx.Err() != nil {
		t.Errorf("ctx.Err() = %v; canceling a context that got no deadline must not end the caller's", ctx.Err())
	}
}
