// action_timeout.go bounds how long one tool call may run.
//
// Every one of the four tool handlers passes through withRecovery, which makes
// it the one place a deadline reaches all of them.
//
// Both transports already end a call whose client went away: an HTTP POST's
// lifetime cancels the calls it carries, and a stdio client's cancellation
// notification reaches the handler. What neither bounds is a handler whose
// client is still waiting. This server has a resolve budget per source and a
// stall guard on a transfer, and neither is a wall clock: internal/libgen's
// download path says so in as many words. A mirror that trickles a byte every
// fifty-nine seconds defeats the stall guard forever while holding a
// concurrent-download slot, a temp-cache slot and a share of the one outbound
// rate limiter. The deadline is what ends that.

package toolutil

import (
	"context"
	"sync/atomic"
	"time"
)

// actionTimeoutNanos is the per-call deadline in nanoseconds; 0 means none.
//
// Process-wide rather than carried per call: it is a property of the
// deployment, set once at startup from the configuration and read on every
// call. Keeping it here rather than reading internal/config is what leaves this
// package free of that dependency, which internal/tools already has.
var actionTimeoutNanos atomic.Int64

// SetActionTimeout sets the deadline every tool call runs under. Zero or a
// negative value disables it. Call once at startup, before anything serves.
func SetActionTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	actionTimeoutNanos.Store(int64(d))
}

// ActionTimeout reports the deadline every tool call runs under, 0 for none.
func ActionTimeout() time.Duration {
	return time.Duration(actionTimeoutNanos.Load())
}

// WithActionDeadline derives the context a tool call runs under: the caller's,
// bounded by the configured deadline when there is one. The cancel function is
// always safe to call.
//
// It derives rather than replaces, so a caller's own shorter deadline still
// wins — context.WithTimeout keeps the earlier of the two. A deployment's cap
// is a ceiling on what it will spend, not a floor on what a client may ask for.
func WithActionDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	d := ActionTimeout()
	if d <= 0 {
		return ctx, func() { /* no deadline was added, so there is nothing to cancel */ }
	}
	return context.WithTimeout(ctx, d)
}
