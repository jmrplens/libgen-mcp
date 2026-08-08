package extract

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// The page-tree check in pdftree.go closes the one non-terminating input this
// package has actually been shown. It cannot close the ones it has not: every
// read here runs third-party parsing code over bytes a stranger produced, and a
// loop inside a library call is invisible to a context — the call makes no
// system call to interrupt and never reaches a cancellation check.
//
// So each public entry point runs its work on its own goroutine behind a time
// budget and answers the caller when the budget runs out. That is a real trade,
// not a free one, and it is worth stating plainly:
//
//   - The blocked goroutine does not die. Go has no way to kill one. A read
//     that never returns leaks a goroutine for the life of the process, and if
//     it is spinning rather than blocking it keeps consuming a core too.
//   - What the budget buys is the caller's thread and the caller's answer: the
//     request completes, the connection is freed, and the model gets a
//     diagnosis instead of silence. One stuck file no longer costs a client.
//   - The inner context is canceled when the budget expires, so every read that
//     *does* honor cancellation — the per-page loops here, the EPUB spine walk,
//     any future ctx-aware library — unwinds normally and leaks nothing. Only
//     genuinely uninterruptible code is abandoned.
//   - Abandoned goroutines are counted, and once maxStuckReads of them are
//     outstanding the package stops starting new work. A process that has
//     already lost four threads to unkillable parsing is not one to hand a
//     fifth unknown file to; refusing is the honest degradation, and it caps
//     the damage a stream of poisoned files can do at a constant.
//
// The counter is a gauge of *outstanding* abandoned work, not a lifetime total:
// a read that was merely slow, and returns after the caller gave up on it, gives
// its slot back.

// readBudget is how long one read of one file may run before the caller is
// answered without it. It is deliberately generous — a large scanned PDF can
// spend tens of seconds having twenty pages probed for a text layer, and cutting
// a legitimate document short is a worse failure than answering a damaged one
// slowly. It is a variable so tests can shrink it; nothing else writes it.
var readBudget = 90 * time.Second

// maxStuckReads is how many abandoned reads may be outstanding before the
// package refuses to start another.
const maxStuckReads = 4

// stuckReads counts guarded reads whose goroutine was abandoned and has not
// since returned.
var stuckReads atomic.Int64

// unresponsiveReadReason is the diagnosis for a read that ran out of budget. It
// says the file could not be read, in the same "cannot read" register as the
// other unreadable-file diagnoses, and it is format-agnostic because a read that
// never came back never established what it was reading.
const unresponsiveReadReason = "cannot read the file: the reader did not finish within the time limit " +
	"(the document is damaged or pathologically structured)"

// saturatedReaderReason is the diagnosis for a read that was never started
// because too many earlier ones are still stuck. It names the server's state
// rather than the file's, because this file has not been looked at.
const saturatedReaderReason = "cannot read the file: too many earlier reads are still stuck on damaged documents, " +
	"so no new read was started"

// guardOutcome carries one guarded read's result back to its caller, including a
// panic so that recovering stays the caller's business.
type guardOutcome[T any] struct {
	value    T
	err      error
	panicked bool
	recovery any
}

// stuckWatch tracks whether a guarded read's goroutine finished and whether its
// caller gave up on it, so that exactly one of the two adjusts stuckReads no
// matter which happens first.
type stuckWatch struct {
	finished atomic.Bool
	counted  atomic.Bool
}

// finish records that the goroutine returned, releasing the stuck slot if the
// caller had already given up and claimed one.
func (w *stuckWatch) finish() {
	w.finished.Store(true)
	if w.counted.Swap(false) {
		stuckReads.Add(-1)
	}
}

// abandon records that the caller gave up on the goroutine and claims a stuck
// slot for it. If the goroutine turns out to have finished in the meantime, the
// slot is released again immediately; the Swap makes that safe against finish
// running concurrently, so the slot is released exactly once.
func (w *stuckWatch) abandon() {
	w.counted.Store(true)
	stuckReads.Add(1)
	if w.finished.Load() && w.counted.Swap(false) {
		stuckReads.Add(-1)
	}
}

// guardedRead runs fn under readBudget on its own goroutine and returns as soon
// as fn is done, ctx is canceled, or the budget expires — whichever comes first.
// A non-empty reason means fn did not answer and the caller should report that
// reason instead of the (zero) value. A panic inside fn is carried back and
// re-raised on the caller's goroutine, so the recover() guards further in, and
// the tool layer's own, still see it.
func guardedRead[T any](ctx context.Context, fn func(context.Context) (T, error)) (value T, reason string, err error) {
	var zero T
	if e := ctx.Err(); e != nil {
		return zero, "", e
	}
	if stuckReads.Load() >= maxStuckReads {
		return zero, saturatedReaderReason, nil
	}
	inner, cancel := context.WithTimeout(ctx, readBudget)
	defer cancel()

	w := &stuckWatch{}
	// Buffered, so a goroutine the caller has already abandoned can still send its
	// result and exit instead of blocking here forever.
	done := make(chan guardOutcome[T], 1)
	go func() {
		out := runGuarded(inner, fn)
		w.finish()
		done <- out
	}()

	select {
	case out := <-done:
		if out.panicked {
			panic(out.recovery)
		}
		return out.value, "", out.err
	case <-inner.Done():
		if e := ctx.Err(); e != nil {
			return zero, "", e
		}
		w.abandon()
		return zero, unresponsiveReadReason, nil
	}
}

// runGuarded calls fn and captures whatever it produced, turning a panic into a
// value so the goroutine unwinds cleanly and the caller can re-raise it.
func runGuarded[T any](ctx context.Context, fn func(context.Context) (T, error)) (out guardOutcome[T]) {
	defer func() {
		if rec := recover(); rec != nil {
			out = guardOutcome[T]{panicked: true, recovery: rec}
		}
	}()
	out.value, out.err = fn(ctx)
	return out
}

// formatHint names the format of the file at path from its extension, falling
// back to its bytes, so a read that gave up before it could identify the
// document still reports what the document looked like. It returns "" when
// nothing matches, which is what the read modes report for an unrecognized file
// anyway.
func formatHint(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf", ".epub", ".txt", ".djvu", ".cbr", ".cbz", ".mobi", ".azw", ".azw3":
		return strings.TrimPrefix(ext, ".")
	}
	return sniffFormat(path)
}
