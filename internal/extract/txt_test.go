package extract

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtract_TXT verifies that a plain-text file extracts its content and
// reports the txt format.
func TestExtract_TXT(t *testing.T) {
	c, err := Extract(context.Background(), "testdata/sample.txt", Req{})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable || c.Format != "txt" {
		t.Fatalf("expected txt, got %+v", c)
	}
	if !strings.Contains(c.Text, "quick brown fox") {
		t.Errorf("expected fox sentence, got %q", c.Text)
	}
}

// TestExtract_TXTCharPagination verifies char-based pagination on a text file:
// a small MaxChars truncates, sets HasMore/Truncated and the next cursor, and
// never splits a UTF-8 rune.
func TestExtract_TXTCharPagination(t *testing.T) {
	c, err := Extract(context.Background(), "testdata/sample.txt", Req{Offset: 0, MaxChars: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(c.Text)) != 10 {
		t.Errorf("want 10 runes, got %d", len([]rune(c.Text)))
	}
	if !c.HasMore || !c.Truncated || c.NextCursor.Char != 10 {
		t.Fatalf("pagination fields wrong, got %+v", c)
	}
	if c.CharStart != 0 || c.CharEnd != 10 {
		t.Errorf("want CharStart=0 CharEnd=10, got %d/%d", c.CharStart, c.CharEnd)
	}
}

// TestExtract_TXTOverCapMarksTruncated verifies that a plain-text file just
// over the maxTextFileBytes extraction cap is read up to the cap and the
// returned Chunk is flagged Truncated with an explanatory Reason, so the
// dropped tail is signaled honestly rather than silently lost. The oversized
// file is built in t.TempDir() to avoid committing a large fixture.
func TestExtract_TXTOverCapMarksTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.txt")
	// One byte past the cap is enough to saturate the LimitReader.
	data := bytes.Repeat([]byte("a"), maxTextFileBytes+1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable {
		t.Fatalf("expected extractable chunk, got %+v", c)
	}
	if !c.Truncated {
		t.Error("Truncated = false, want true for a file over the 8 MiB cap")
	}
	if !strings.Contains(c.Reason, "8 MiB extraction cap") {
		t.Errorf("Reason should note the extraction cap, got %q", c.Reason)
	}
}

// TestExtract_TXTOffsetPastEnd verifies that an Offset at or beyond the end of
// the file yields an empty chunk with no remaining content, rather than an
// error or an out-of-range slice.
func TestExtract_TXTOffsetPastEnd(t *testing.T) {
	c, err := Extract(context.Background(), "testdata/sample.txt", Req{Offset: 1_000_000, MaxChars: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable || c.Format != "txt" {
		t.Fatalf("expected extractable txt, got %+v", c)
	}
	if c.Text != "" {
		t.Errorf("want empty text past end of file, got %q", c.Text)
	}
	if c.HasMore {
		t.Errorf("want HasMore false past end of file, got %+v", c)
	}
}

// TestExtract_TXTMissingFile verifies that a non-existent .txt path is reported
// as not extractable with a reason (via the os.Open failure path) and a nil
// error, rather than propagating a hard error to the caller.
func TestExtract_TXTMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.txt")
	c, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatalf("expected nil error for a missing text file, got %v", err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "cannot open text file") {
		t.Errorf("reason should note the open failure, got %q", c.Reason)
	}
}
