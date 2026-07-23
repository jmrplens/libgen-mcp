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
