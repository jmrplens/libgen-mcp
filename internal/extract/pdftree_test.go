package extract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ledongthuc/pdf"
)

// cyclicPageTreePDF returns the bytes of the smallest file that made
// ledongthuc/pdf's Reader.Page never return: a /Pages node that lists itself in
// its own /Kids. Everything else about it is valid — the cross-reference table
// is correct, pdf.Open succeeds and NumPage answers 1 — so it is not caught by
// any "is this a PDF" check; only the descent loop inside Page(1) notices, by
// never leaving it. Built here in code, at about 220 bytes, so the repository
// carries no third-party binary to reproduce a denial of service.
func cyclicPageTreePDF() []byte {
	return buildPDF([]string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[2 0 R]/Count 1>>",
	})
}

// nestedPageTreePDF returns a PDF whose page tree is a chain of depth /Pages
// nodes ending in one page of real text. It is the acyclic counterpart of the
// fixture above: nesting alone, with no cycle, so the depth bound can be shown
// to accept a deep-but-finite tree and reject a deeper one on the same shape.
func nestedPageTreePDF(depth int) []byte {
	objs := []string{"<</Type/Catalog/Pages 2 0 R>>"}
	for i := range depth {
		objs = append(objs, fmt.Sprintf("<</Type/Pages/Kids[%d 0 R]/Count 1>>", i+3))
	}
	page := depth + 2
	objs = append(objs,
		fmt.Sprintf("<</Type/Page/Parent %d 0 R/MediaBox[0 0 200 200]/Contents %d 0 R/Resources<</Font<</F1 %d 0 R>>>>>>",
			page-1, page+1, page+2),
		streamObj("BT /F1 12 Tf 10 100 Td (deeply nested body text) Tj ET"),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	)
	return buildPDF(objs)
}

// mustReturnWithin runs work on its own goroutine and fails the test if it has
// not returned within limit. Every test that feeds this package a file designed
// not to terminate goes through it, because the alternative — calling the read
// directly and trusting it to come back — turns a regression into a suite that
// hangs until CI kills it, with no failing test name to show for it.
//
// On failure the goroutine is abandoned, exactly as the production watchdog
// abandons one, so a broken build leaks a spinning goroutine for the remainder
// of the test binary's life. That is the deliberate trade: a named failure now
// beats a silent hang.
func mustReturnWithin(t *testing.T, limit time.Duration, what string, work func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		work()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s did not return within %s — the read is hung", what, limit)
	}
}

// TestReadModes_CyclicPageTreeCannotHang is the regression test for the denial
// of service this whole file exists to prevent: before the page-tree check, a
// ~220-byte PDF whose /Pages node cites itself sent Reader.Page into a loop that
// never ended, and every read mode inherited it. Each mode is given a hard
// deadline, so a regression fails here by name instead of hanging the suite, and
// all three must name the same cause in the same words.
func TestReadModes_CyclicPageTreeCannotHang(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cycle.pdf")
	if err := os.WriteFile(path, cyclicPageTreePDF(), 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		chunk   Chunk
		matches SearchResult
		toc     OutlineResult
		errs    [3]error
	)
	mustReturnWithin(t, 10*time.Second, "Extract", func() {
		chunk, errs[0] = Extract(context.Background(), path, Req{})
	})
	mustReturnWithin(t, 10*time.Second, "Search", func() {
		matches, errs[1] = Search(context.Background(), path, "anything", SearchOpts{})
	})
	mustReturnWithin(t, 10*time.Second, "Outline", func() {
		toc, errs[2] = Outline(context.Background(), path)
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("mode %d: expected a soft not-extractable result, got error %v", i, err)
		}
	}

	if chunk.Extractable || matches.Extractable || toc.Extractable {
		t.Fatalf("a file no mode can read must not be extractable: text=%t find=%t outline=%t",
			chunk.Extractable, matches.Extractable, toc.Extractable)
	}
	if chunk.Reason != cyclicPDFReason || matches.Reason != cyclicPDFReason || toc.Reason != cyclicPDFReason {
		t.Errorf("one cause must get one answer:\n text:    %q\n find:    %q\n outline: %q",
			chunk.Reason, matches.Reason, toc.Reason)
	}
	// The wording has to place the file in the "cannot read" family, not the
	// "scanned, so try OCR" one: the pages may be full of text that no parser can
	// reach, and sending the caller after a text layer would be a wrong steer.
	if strings.Contains(chunk.Reason, "text layer") || strings.Contains(chunk.Reason, "scanned") {
		t.Errorf("a poisoned file is unreadable, not text-free; got %q", chunk.Reason)
	}
}

// TestReadModes_LegitimatePDFUnaffected pins the other half of the bargain: the
// page-tree check runs before every PDF read, so a real document must come back
// exactly as it did before it existed, and quickly. The budget here is loose
// enough not to flake on a loaded CI box and tight enough that a check which
// walked the tree pathologically — say, once per page — would blow it.
func TestReadModes_LegitimatePDFUnaffected(t *testing.T) {
	start := time.Now()
	chunk, err := Extract(context.Background(), "testdata/sample.pdf", Req{StartPage: 1, MaxPages: 5, MaxChars: 100000})
	if err != nil || !chunk.Extractable || !strings.Contains(chunk.Text, "Hands-On Software Architecture") {
		t.Fatalf("text mode regressed on a valid PDF: err=%v chunk=%+v", err, chunk)
	}
	res, err := Search(context.Background(), "testdata/sample.pdf", "Second page", SearchOpts{})
	if err != nil || !res.Extractable || res.TotalMatches == 0 {
		t.Fatalf("find mode regressed on a valid PDF: err=%v res=%+v", err, res)
	}
	toc, err := Outline(context.Background(), "testdata/bookmarked.pdf")
	if err != nil || !toc.Extractable || len(toc.Entries) == 0 {
		t.Fatalf("outline mode regressed on a bookmarked PDF: err=%v toc=%+v", err, toc)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("three reads of a small PDF took %s; the page-tree check should be negligible", elapsed)
	}
}

// TestPageTree_AcceptsDeepButFiniteNesting verifies the depth bound does not
// reject a legitimately nested page tree. A chain far deeper than any real
// producer emits still has an end, so the walk reaches it and the document reads
// normally — the bound is there to catch descent that never ends, not descent
// that is merely long.
func TestPageTree_AcceptsDeepButFiniteNesting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep.pdf")
	if err := os.WriteFile(path, nestedPageTreePDF(maxPageTreeDepth-2), 0o600); err != nil {
		t.Fatal(err)
	}
	chunk, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if !chunk.Extractable {
		t.Fatalf("a deep but finite page tree must still read; got %+v", chunk)
	}
	if !strings.Contains(chunk.Text, "deeply nested body text") {
		t.Errorf("expected the nested page's text, got %q", chunk.Text)
	}
}

// TestPageTree_RejectsNestingPastTheBound verifies the same shape one level too
// deep is refused, and refused with the shared diagnosis rather than a
// mode-specific one.
func TestPageTree_RejectsNestingPastTheBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deeper.pdf")
	if err := os.WriteFile(path, nestedPageTreePDF(maxPageTreeDepth+4), 0o600); err != nil {
		t.Fatal(err)
	}
	chunk, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Extractable || chunk.Reason != cyclicPDFReason {
		t.Fatalf("want %q, got %+v", cyclicPDFReason, chunk)
	}
}

// TestPageTreeReason_NoPageTree verifies a file whose catalog names no page tree
// is passed through rather than condemned: Reader.Page's loop only runs while it
// is standing on a /Pages node, so with none to stand on there is nothing to
// protect against, and the later stages get to give their own diagnosis.
func TestPageTreeReason_NoPageTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nopages.pdf")
	if err := os.WriteFile(path, buildPDF([]string{"<</Type/Catalog>>"}), 0o600); err != nil {
		t.Fatal(err)
	}
	f, r, err := pdf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if got := pageTreeReason(r); got != "" {
		t.Errorf("a catalog with no page tree is safe to read; got %q", got)
	}
}

// TestWalkPageTree_KidBudget verifies the second bound: a tree with no cycle and
// no excessive depth is still refused once it has cost more kid inspections than
// the budget allows, so a file that is pathological only in its width cannot
// make the safety check itself the expensive part. The budget is passed in, so
// the test exercises the real branch without fabricating a 200,000-object file.
func TestWalkPageTree_KidBudget(t *testing.T) {
	f, r, err := pdf.Open("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	root := r.Trailer().Key("Root").Key("Pages")

	spent := 0
	if walkPageTree(root, 0, &spent) {
		t.Error("a walk with no budget left must refuse the tree")
	}
	generous := maxPageTreeKids
	if !walkPageTree(root, 0, &generous) {
		t.Error("the same tree must be accepted with the real budget")
	}
	if generous >= maxPageTreeKids {
		t.Error("the walk should have spent budget on the document's pages")
	}
}
