package extract

import (
	"context"
	"strings"
	"testing"
)

// TestExtract_UnsupportedDJVU verifies that a .djvu file is reported as not
// extractable with a non-empty reason.
func TestExtract_UnsupportedDJVU(t *testing.T) {
	c, err := Extract(context.Background(), "testdata/unsupported.djvu", Req{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Extractable || c.Reason == "" {
		t.Fatalf("djvu must be not-extractable with a reason, got %+v", c)
	}
}

// TestExtract_UnsupportedExtension verifies that a file with an unrecognized
// extension (neither a supported nor a known-unsupported container) is reported
// as not extractable with a reason naming the extension, exercising the default
// dispatch branch.
func TestExtract_UnsupportedExtension(t *testing.T) {
	c, err := Extract(context.Background(), "testdata/whatever.xyz", Req{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "unsupported file extension") {
		t.Errorf("reason should name the unsupported extension, got %q", c.Reason)
	}
}

// TestExtract_ContextCancelled verifies that a canceled context causes Extract
// to return the context error.
func TestExtract_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Extract(ctx, "testdata/sample.pdf", Req{})
	if err == nil {
		t.Fatal("expected a context error, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context error, got %v", err)
	}
}

// countingCtx is a context.Context whose Err reports cancellation only after
// its Err method has been called more than `pass` times. It lets a test drive
// cancellation to a specific inner ctx.Err() checkpoint: the outer guards see a
// live context and pass, while a chosen nested guard observes cancellation.
// This exercises the "propagate a context error from a deeper call" branches
// that a fully canceled context (rejected by the very first guard) cannot reach.
type countingCtx struct {
	context.Context
	calls *int
	pass  int
}

// Err returns nil for the first `pass` calls, then context.Canceled.
func (c countingCtx) Err() error {
	*c.calls++
	if *c.calls > c.pass {
		return context.Canceled
	}
	return nil
}

// passErr returns a context that survives its first `pass` Err() checks and
// reports context.Canceled on every check thereafter.
func passErr(pass int) context.Context {
	n := 0
	return countingCtx{Context: context.Background(), calls: &n, pass: pass}
}

// TestAppendNote verifies both branches of appendNote: an empty reason yields
// the note alone, while a non-empty reason is joined to the note with "; ".
func TestAppendNote(t *testing.T) {
	if got := appendNote("", "note"); got != "note" {
		t.Errorf("empty reason: want %q, got %q", "note", got)
	}
	if got := appendNote("reason", "note"); got != "reason; note" {
		t.Errorf("both notes: want %q, got %q", "reason; note", got)
	}
}
