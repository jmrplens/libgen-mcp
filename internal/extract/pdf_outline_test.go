package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPdfOutline_ContextCancelledDirect verifies pdfOutline's own entry guard:
// called directly with an already-canceled context it returns the context
// error before opening the file, a checkpoint Outline normally short-circuits.
func TestPdfOutline_ContextCancelledDirect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pdfOutline(ctx, "testdata/bookmarked.pdf"); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestOutline_PDFMissingFile verifies pdfOutline's open-failure path: a
// non-existent .pdf path is reported as not extractable, with the reason the
// text path gives for the same path, rather than propagating a hard error.
func TestOutline_PDFMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.pdf")
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	chunk, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != chunk.Reason {
		t.Errorf("outline and text paths must give one diagnosis for one cause:\n outline: %q\n text:    %q",
			res.Reason, chunk.Reason)
	}
}

// TestOutline_PDFBookmarks verifies that a PDF carrying an embedded outline is
// read via pdfcpu into ordered OutlineEntry values: three top-level chapters at
// Level 0 with their titles and 1-based page numbers, reported with Format
// "pdf" and Extractable true.
func TestOutline_PDFBookmarks(t *testing.T) {
	res, err := Outline(context.Background(), "testdata/bookmarked.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "pdf" {
		t.Fatalf("want extractable pdf, got %+v", res)
	}
	if len(res.Entries) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(res.Entries), res.Entries)
	}
	want := []OutlineEntry{
		{Title: "Chapter 1: Intro", Level: 0, Page: 1},
		{Title: "Chapter 2: Methods", Level: 0, Page: 2},
		{Title: "Chapter 3: Results", Level: 0, Page: 2},
	}
	for i, w := range want {
		got := res.Entries[i]
		if got.Title != w.Title || got.Level != w.Level || got.Page != w.Page {
			t.Errorf("entry %d: want %+v, got %+v", i, w, got)
		}
	}
}

// TestOutline_PDFNoBookmarks verifies the one case that really is "no table of
// contents": a PDF with a readable text layer and no bookmarks is extractable
// with no entries, and its reason says so without borrowing the scanned/no-text
// wording that belongs to a different failure.
func TestOutline_PDFNoBookmarks(t *testing.T) {
	res, err := Outline(context.Background(), "testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "pdf" {
		t.Fatalf("want extractable pdf, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries, got %+v", res.Entries)
	}
	if !strings.Contains(res.Reason, "no embedded table of contents") {
		t.Errorf("want a reason mentioning the missing table of contents, got %q", res.Reason)
	}
	if strings.Contains(res.Reason, "text layer (likely") {
		t.Errorf("a readable PDF must not borrow the scanned diagnosis, got %q", res.Reason)
	}
}

// TestOutline_PDFScannedNoBookmarks pins the fix for the diagnosis that cost a
// whole extra round trip: outline mode over a scanned (no-text-layer) PDF used
// to answer "no table of contents found", which is both false and encouraging —
// it says the pages are readable and merely unindexed. The document has no text
// at all, so outline must report exactly what the text path reports, and report
// it as not extractable.
func TestOutline_PDFScannedNoBookmarks(t *testing.T) {
	res, err := Outline(context.Background(), "testdata/scanned.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable || res.Format != "pdf" {
		t.Fatalf("want a not-extractable pdf result, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries, got %+v", res.Entries)
	}
	chunk, err := Extract(context.Background(), "testdata/scanned.pdf", Req{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != chunk.Reason {
		t.Errorf("outline and text paths must give one diagnosis for one cause:\n outline: %q\n text:    %q",
			res.Reason, chunk.Reason)
	}
	if !strings.Contains(res.Reason, "OCR is not supported") {
		t.Errorf("the diagnosis must carry the OCR hint, got %q", res.Reason)
	}
}

// TestOutline_PDFGraphicsOnlyNoBookmarks covers the same verdict on a fixture
// built in this test rather than shipped: a structurally valid one-page PDF
// whose content stream draws a filled rectangle and emits no text operator is
// exactly what a page of a scan looks like to a text extractor.
func TestOutline_PDFGraphicsOnlyNoBookmarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graphics.pdf")
	if err := os.WriteFile(path, graphicsOnlyPDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable {
		t.Fatalf("a page with no text operators must not be extractable, got %+v", res)
	}
	if res.Reason != noTextLayerReason {
		t.Errorf("Reason = %q, want the shared no-text-layer diagnosis", res.Reason)
	}
}

// TestOutline_PDFMalformed verifies the third case: a file with a .pdf extension
// whose bytes are not a PDF is not "a document without a table of contents" but
// an unreadable file, reported not extractable with the same reason the text
// path gives, and without crashing on the recover-guarded bookmark read.
func TestOutline_PDFMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7 definitely not a pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatalf("expected nil error for a malformed PDF, got %v", err)
	}
	if res.Extractable || res.Format != "pdf" {
		t.Fatalf("want a not-extractable pdf result, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries for a malformed PDF, got %+v", res.Entries)
	}
	chunk, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != chunk.Reason {
		t.Errorf("outline and text paths must give one diagnosis for one cause:\n outline: %q\n text:    %q",
			res.Reason, chunk.Reason)
	}
}

// TestPdfNoOutlineResult_CtxCancelled verifies that a context canceled by the
// time the text-layer probe runs propagates out of pdfNoOutlineResult as an
// error rather than being reported as a document without a table of contents.
func TestPdfNoOutlineResult_CtxCancelled(t *testing.T) {
	if _, err := pdfNoOutlineResult(passErr(0), "testdata/sample.pdf"); err == nil {
		t.Fatal("expected the context error to propagate, got nil")
	}
}

// TestProbePDFTextLayer_Unreadable verifies the probe's open-failure branch: a
// file the PDF reader rejects is classified as unreadable and carries the same
// "not a valid PDF" wording the text path uses, so the outline path never has to
// invent a diagnosis of its own.
func TestProbePDFTextLayer_Unreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.pdf")
	if err := os.WriteFile(path, []byte("not a pdf at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, reason, err := probePDFTextLayer(context.Background(), path)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if state != pdfTextUnreadable {
		t.Fatalf("state = %v, want pdfTextUnreadable (reason %q)", state, reason)
	}
	if !strings.Contains(reason, "not a valid PDF") {
		t.Errorf("reason should name the unreadable PDF, got %q", reason)
	}
}

// TestProbePageNumbers verifies the sampling plan: a document shorter than the
// budget is read whole, a longer one is sampled at an even stride starting at
// page 1 (so a scanned cover cannot stand in for the whole book), and a document
// with no pages yields nothing to probe.
func TestProbePageNumbers(t *testing.T) {
	cases := map[string]struct {
		total, budget int
		want          []int
	}{
		"shorter than budget": {total: 3, budget: 20, want: []int{1, 2, 3}},
		"exactly the budget":  {total: 4, budget: 4, want: []int{1, 2, 3, 4}},
		"strided":             {total: 20, budget: 4, want: []int{1, 6, 11, 16}},
		"no pages":            {total: 0, budget: 20, want: nil},
		"no budget":           {total: 10, budget: 0, want: nil},
	}
	for name, tc := range cases {
		got := probePageNumbers(tc.total, tc.budget)
		if len(got) != len(tc.want) {
			t.Errorf("%s: probePageNumbers(%d, %d) = %v, want %v", name, tc.total, tc.budget, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: probePageNumbers(%d, %d) = %v, want %v", name, tc.total, tc.budget, got, tc.want)
				break
			}
		}
	}
}

// TestOutline_PDFTextAfterABlankFirstPage guards the probe against the obvious
// wrong shortcut — deciding on page 1 alone. A book that opens on a scanned cover
// or a plate is still a readable book, so the probe keeps looking: a PDF whose
// first page draws only graphics and whose second carries text must be reported
// as a document without a table of contents, not as a scan.
func TestOutline_PDFTextAfterABlankFirstPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverthentext.pdf")
	if err := os.WriteFile(path, blankThenTextPDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable {
		t.Fatalf("text on page 2 makes this a readable document, got %+v", res)
	}
	if res.Reason != noPDFOutlineReason {
		t.Errorf("Reason = %q, want the no-table-of-contents diagnosis", res.Reason)
	}
}
