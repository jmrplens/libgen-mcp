package extract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shrinkReadBudget sets the guarded-read time budget to d for the duration of
// one test and restores it afterwards. The package's tests never run in
// parallel, so the swap is safe; the budget is only ever read on the calling
// goroutine, never by the guarded work itself.
func shrinkReadBudget(t *testing.T, d time.Duration) {
	t.Helper()
	previous := readBudget
	readBudget = d
	t.Cleanup(func() { readBudget = previous })
}

// awaitNoStuckReads waits for the stuck-read gauge to fall back to zero, so one
// test's abandoned goroutine cannot make the next one look saturated. It fails
// rather than blocking if the gauge stays up.
func awaitNoStuckReads(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for stuckReads.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("stuck-read gauge stayed at %d", stuckReads.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestGuardedRead_AnswersWhenTheWorkWillNot is the watchdog's reason for
// existing: work that ignores its context — which is every call into a parsing
// library, since none of them take one — must not be able to hold the caller.
// The read here sleeps far past the budget without ever looking at ctx, and the
// caller has to come back anyway, with the shared unreadable diagnosis.
func TestGuardedRead_AnswersWhenTheWorkWillNot(t *testing.T) {
	shrinkReadBudget(t, 50*time.Millisecond)
	awaitNoStuckReads(t)

	start := time.Now()
	value, reason, err := guardedRead(context.Background(), func(context.Context) (int, error) {
		time.Sleep(2 * time.Second)
		return 42, nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a read that ran out of budget is a soft failure, got error %v", err)
	}
	if reason != unresponsiveReadReason {
		t.Errorf("want %q, got %q", unresponsiveReadReason, reason)
	}
	if value != 0 {
		t.Errorf("no value should come back from work that never finished, got %d", value)
	}
	if elapsed > time.Second {
		t.Errorf("the caller waited %s on a 50ms budget", elapsed)
	}
	// The goroutine is still running, and the gauge must say so: that number is
	// the whole basis on which the package decides it has lost too much.
	if got := stuckReads.Load(); got != 1 {
		t.Errorf("want one outstanding stuck read, got %d", got)
	}
	// It was only slow, not stuck, so when it finally returns it gives the slot
	// back — the gauge counts outstanding abandoned work, not a lifetime total.
	awaitNoStuckReads(t)
}

// TestGuardedRead_RefusesOnceTooManyReadsAreStuck covers the damage limit. A
// goroutine abandoned inside an uninterruptible library call never dies, and one
// spinning on a malformed page tree allocates about a gigabyte a second, so a
// process that has already lost maxStuckReads of them has no business starting
// another read of an unknown file. Refusing is the honest degradation, and it
// caps at a constant what a stream of poisoned files can cost.
func TestGuardedRead_RefusesOnceTooManyReadsAreStuck(t *testing.T) {
	awaitNoStuckReads(t)
	stuckReads.Store(maxStuckReads)
	t.Cleanup(func() { stuckReads.Store(0) })

	ran := false
	_, reason, err := guardedRead(context.Background(), func(context.Context) (int, error) {
		ran = true
		return 1, nil
	})
	if err != nil {
		t.Fatalf("a refusal is a soft failure, got error %v", err)
	}
	if ran {
		t.Error("a saturated reader must not start the work at all")
	}
	if reason != saturatedReaderReason {
		t.Errorf("want %q, got %q", saturatedReaderReason, reason)
	}
}

// TestStuckWatch_LateFinishReleasesTheSlot pins the handoff between the two
// goroutines that can both touch the gauge. The dangerous ordering is the one
// where the work finishes in the instant between the budget expiring and the
// caller claiming a stuck slot for it: claim it anyway and the gauge is
// permanently one short of the truth, and a long-lived server eventually refuses
// every read on the strength of four goroutines that all returned. Calling
// finish before abandon reproduces exactly that interleaving, deterministically.
func TestStuckWatch_LateFinishReleasesTheSlot(t *testing.T) {
	awaitNoStuckReads(t)
	w := &stuckWatch{}
	w.finish()
	w.abandon()
	if got := stuckReads.Load(); got != 0 {
		t.Errorf("a goroutine that finished must not hold a stuck slot; gauge = %d", got)
	}
}

// TestGuardedRead_PassesThroughAResult verifies the ordinary path: work that
// finishes inside its budget hands back its own value and error untouched, with
// no reason of the watchdog's own.
func TestGuardedRead_PassesThroughAResult(t *testing.T) {
	awaitNoStuckReads(t)
	sentinel := errors.New("from the work")
	value, reason, err := guardedRead(context.Background(), func(context.Context) (string, error) {
		return "done", sentinel
	})
	if value != "done" || !errors.Is(err, sentinel) || reason != "" {
		t.Errorf("want (%q, %q, %v), got (%q, %q, %v)", "done", "", sentinel, value, reason, err)
	}
}

// TestGuardedRead_ReRaisesAPanic verifies the watchdog does not quietly swallow
// a panic. Moving the work onto a goroutine puts it out of reach of the
// caller's recover(), so the panic is carried back and raised again on the
// caller's own goroutine — otherwise the recover() guards inside this package,
// and the tool layer's own, would stop seeing malformed input and a crash would
// take the server down instead of becoming a tool error.
func TestGuardedRead_ReRaisesAPanic(t *testing.T) {
	awaitNoStuckReads(t)
	defer func() {
		rec := recover()
		if rec == nil {
			t.Error("the panic did not reach the caller")
		}
		if got, ok := rec.(string); !ok || got != "boom" {
			t.Errorf("want the original panic value, got %#v", rec)
		}
	}()
	_, _, _ = guardedRead(context.Background(), func(context.Context) (int, error) {
		panic("boom")
	})
}

// TestGuardedRead_AlreadyCanceledContext verifies a caller who has already given
// up gets the context error and no goroutine is started for it.
func TestGuardedRead_AlreadyCanceledContext(t *testing.T) {
	awaitNoStuckReads(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	_, reason, err := guardedRead(ctx, func(context.Context) (int, error) {
		ran = true
		return 1, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	if reason != "" || ran {
		t.Errorf("want no reason and no work; got reason %q, ran=%t", reason, ran)
	}
}

// TestGuardedRead_CallerCancelsMidRead verifies the difference between the two
// ways a guarded read can end early. When the caller cancels, that is the
// caller's own error and must propagate as one; only the budget expiring on a
// live caller produces the unreadable-file diagnosis, and only that case counts
// against the stuck-read gauge.
func TestGuardedRead_CallerCancelsMidRead(t *testing.T) {
	awaitNoStuckReads(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	_, reason, err := guardedRead(ctx, func(inner context.Context) (int, error) {
		<-inner.Done()
		return 0, inner.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	if reason != "" {
		t.Errorf("a caller's own cancellation is not a diagnosis about the file; got %q", reason)
	}
	awaitNoStuckReads(t)
}

// TestGuardedRead_CancelsTheWorkItAbandons verifies the property that keeps the
// leak rare rather than routine: the budget cancels the inner context, so work
// that does check for cancellation — every loop in this package, and the EPUB
// spine walk — unwinds and exits instead of being abandoned. Only genuinely
// uninterruptible code leaks a goroutine.
func TestGuardedRead_CancelsTheWorkItAbandons(t *testing.T) {
	shrinkReadBudget(t, 30*time.Millisecond)
	awaitNoStuckReads(t)

	noticed := make(chan error, 1)
	_, reason, err := guardedRead(context.Background(), func(inner context.Context) (int, error) {
		<-inner.Done()
		noticed <- inner.Err()
		return 0, inner.Err()
	})
	if err != nil || reason != unresponsiveReadReason {
		t.Fatalf("want the unresponsive diagnosis, got reason %q err %v", reason, err)
	}
	select {
	case got := <-noticed:
		if !errors.Is(got, context.DeadlineExceeded) {
			t.Errorf("want the work to see a deadline, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("the budget did not cancel the work it gave up on")
	}
	awaitNoStuckReads(t)
}

// TestReadModes_ReturnWithinTheBudget verifies all three entry points are
// actually wired to the watchdog and all three report the same thing when it
// fires. The budget is set below any achievable read time, so even the healthy
// sample document loses the race — which is the point: the guarantee under test
// is that a read always comes back, whatever the file turns out to be.
func TestReadModes_ReturnWithinTheBudget(t *testing.T) {
	shrinkReadBudget(t, time.Nanosecond)
	awaitNoStuckReads(t)

	chunk, err := Extract(context.Background(), "testdata/sample.pdf", Req{})
	if err != nil || chunk.Extractable || chunk.Reason != unresponsiveReadReason {
		t.Errorf("text mode: want %q, got %+v (err %v)", unresponsiveReadReason, chunk, err)
	}
	if chunk.Format != "pdf" {
		t.Errorf("a read that gave up should still say what it was reading, got %q", chunk.Format)
	}
	res, err := Search(context.Background(), "testdata/sample.pdf", "page", SearchOpts{})
	if err != nil || res.Extractable || res.Reason != unresponsiveReadReason {
		t.Errorf("find mode: want %q, got %+v (err %v)", unresponsiveReadReason, res, err)
	}
	toc, err := Outline(context.Background(), "testdata/sample.pdf")
	if err != nil || toc.Extractable || toc.Reason != unresponsiveReadReason {
		t.Errorf("outline mode: want %q, got %+v (err %v)", unresponsiveReadReason, toc, err)
	}
	awaitNoStuckReads(t)
}

// TestFormatHint verifies the fallback naming a read uses when it gave up before
// it could identify the document: the extension when there is a usable one, the
// file's own bytes when there is not, and nothing when neither answers.
func TestFormatHint(t *testing.T) {
	dir := t.TempDir()
	nameless := filepath.Join(dir, "d41d8cd98f00b204e9800998ecf8427e")
	if err := os.WriteFile(nameless, graphicsOnlyPDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"book.pdf":        "pdf",
		"book.EPUB":       "epub",
		"notes.txt":       "txt",
		"scan.djvu":       "djvu",
		nameless:          "pdf",
		"mystery.unknown": "",
	}
	for path, want := range cases {
		if got := formatHint(path); got != want {
			t.Errorf("formatHint(%q) = %q, want %q", path, got, want)
		}
	}
}
