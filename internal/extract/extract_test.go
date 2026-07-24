package extract

import (
	"context"
	"os"
	"path/filepath"
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

// TestExtract_SniffsAnExtensionlessFile verifies content decides when the name
// does not. A file fetched by content address — from IPFS, or from a CDN that
// announces no filename — arrives with no extension, and dispatching on the name
// alone made a perfectly readable PDF unreadable. A live evaluator run caught the
// consequence: the model was handed "unsupported file extension" for a real book
// and answered with a table of contents it had invented.
func TestExtract_SniffsAnExtensionlessFile(t *testing.T) {
	src, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	nameless := writeTemp(t, "d41d8cd98f00b204e9800998ecf8427e", src)
	c, err := Extract(context.Background(), nameless, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable {
		t.Fatalf("an extensionless PDF should still extract, got %+v", c)
	}
	if c.Format != "pdf" {
		t.Errorf("Format = %q, want pdf", c.Format)
	}
	if strings.TrimSpace(c.Text) == "" {
		t.Error("extractable but no text came back")
	}
}

// TestExtract_SniffingDoesNotRescueUnknownContent verifies a file that is neither
// named nor shaped like something extractable is still reported as such, so the
// caller is told plainly rather than handed empty text.
func TestExtract_SniffingDoesNotRescueUnknownContent(t *testing.T) {
	nameless := writeTemp(t, "abcdef", []byte("not a document at all"))
	c, err := Extract(context.Background(), nameless, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Extractable {
		t.Fatalf("unknown content should not be extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "unrecognized") {
		t.Errorf("reason should say the content was unrecognized, got %q", c.Reason)
	}
}

// writeTemp writes body to a file named name inside the test's own temp dir and
// returns its path. The name is a literal from the caller, never external input.
func writeTemp(t *testing.T, name string, body []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.Base(name))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		t.Fatalf("refusing to write outside the temp dir: %s", path)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil { //nolint:gosec // path is confined to the test's temp dir, checked above.
		t.Fatal(err)
	}
	return path
}

// TestOutlineAndSearchSniffToo verifies the other two entry points identify a file
// by content as well. They each dispatch on the extension independently, so fixing
// only Extract left outline and find still reporting a real book as unsupported —
// which is the exact path a live evaluator run caught a model inventing a table of
// contents for.
func TestOutlineAndSearchSniffToo(t *testing.T) {
	src, err := os.ReadFile("testdata/bookmarked.pdf")
	if err != nil {
		t.Fatal(err)
	}
	nameless := writeTemp(t, "0123456789abcdef0123456789abcdef", src)

	out, err := Outline(context.Background(), nameless)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Extractable {
		t.Errorf("Outline() on an extensionless PDF: %s", out.Reason)
	}
	if len(out.Entries) == 0 {
		t.Error("Outline() returned no entries for a bookmarked PDF")
	}

	res, err := Search(context.Background(), nameless, "the", SearchOpts{MaxMatches: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable {
		t.Errorf("Search() on an extensionless PDF: %s", res.Reason)
	}
}

// TestEntryPointsAgreeOnFormat verifies the three ways into this package identify
// a file the same way. Extract, Outline and Search each dispatch on their own, and
// a fix applied to one of them left the other two reporting a real book as
// unsupported — the path a live evaluator run caught a model inventing a table of
// contents for.
func TestEntryPointsAgreeOnFormat(t *testing.T) {
	pdf, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		path       string
		recognized bool
	}{
		"named pdf":         {path: "testdata/sample.pdf", recognized: true},
		"named txt":         {path: "testdata/sample.txt", recognized: true},
		"extensionless pdf": {path: writeTemp(t, "deadbeef", pdf), recognized: true},
		"unknown bytes":     {path: writeTemp(t, "nothing", []byte("not a document")), recognized: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			chunk, cerr := Extract(context.Background(), tc.path, Req{})
			if cerr != nil {
				t.Fatal(cerr)
			}
			outline, oerr := Outline(context.Background(), tc.path)
			if oerr != nil {
				t.Fatal(oerr)
			}
			search, serr := Search(context.Background(), tc.path, "the", SearchOpts{MaxMatches: 1})
			if serr != nil {
				t.Fatal(serr)
			}
			// "Recognized" is about dispatch, not about yielding text: a scanned PDF is
			// recognized and still has nothing to extract. What must never differ is
			// whether an entry point knows what the file is.
			for entry, reason := range map[string]string{
				"Extract": chunk.Reason, "Outline": outline.Reason, "Search": search.Reason,
			} {
				unrecognized := strings.Contains(reason, "unrecognized") ||
					strings.Contains(reason, "unsupported file extension")
				if unrecognized == tc.recognized {
					t.Errorf("%s disagrees: recognized=%v, reason=%q", entry, !unrecognized, reason)
				}
			}
		})
	}
}
