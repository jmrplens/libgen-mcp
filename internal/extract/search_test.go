package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSearch_PDFFindsSecondPage verifies that searching the sample PDF for a
// word that only appears on page 2 returns at least one match anchored to that
// page, with a snippet containing the term and the pdf format reported.
func TestSearch_PDFFindsSecondPage(t *testing.T) {
	res, err := Search(context.Background(), "testdata/sample.pdf", "Second", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "pdf" {
		t.Fatalf("expected extractable pdf, got %+v", res)
	}
	if res.TotalMatches < 1 || len(res.Matches) < 1 {
		t.Fatalf("expected at least one match, got %+v", res)
	}
	m := res.Matches[0]
	if m.Page != 2 {
		t.Errorf("want Page==2, got %d", m.Page)
	}
	if !strings.Contains(m.Snippet, "Second") {
		t.Errorf("snippet should contain the match term, got %q", m.Snippet)
	}
}

// TestSearch_CaseInsensitiveDefault verifies that, by default, matching is
// case-insensitive: searching "hands-on" finds the "Hands-On" heading in the
// sample PDF.
func TestSearch_CaseInsensitiveDefault(t *testing.T) {
	res, err := Search(context.Background(), "testdata/sample.pdf", "hands-on", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches < 1 {
		t.Fatalf("expected a case-insensitive match, got %+v", res)
	}
	if !strings.Contains(strings.ToLower(res.Matches[0].Snippet), "hands-on") {
		t.Errorf("snippet should contain the matched heading, got %q", res.Matches[0].Snippet)
	}
}

// TestSearch_Pagination verifies match windowing: a query with several hits and
// MaxMatches==1 returns one match with HasMore and NextMatch==1, and resuming
// at StartMatch==1 returns the following match at a later offset.
func TestSearch_Pagination(t *testing.T) {
	first, err := Search(context.Background(), "testdata/sample.txt", "the", SearchOpts{MaxMatches: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Matches) != 1 {
		t.Fatalf("want exactly 1 match returned, got %d", len(first.Matches))
	}
	if first.TotalMatches < 2 {
		t.Fatalf("want TotalMatches>=2 for pagination, got %d", first.TotalMatches)
	}
	if !first.HasMore || first.NextMatch != 1 {
		t.Fatalf("want HasMore and NextMatch==1, got %+v", first)
	}
	second, err := Search(context.Background(), "testdata/sample.txt", "the", SearchOpts{MaxMatches: 1, StartMatch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Matches) != 1 {
		t.Fatalf("want 1 match on resume, got %d", len(second.Matches))
	}
	if second.Matches[0].CharOffset <= first.Matches[0].CharOffset {
		t.Errorf("resumed match should be at a later offset, got %d then %d",
			first.Matches[0].CharOffset, second.Matches[0].CharOffset)
	}
}

// TestSearch_NoMatches verifies that an absent term yields zero matches,
// HasMore false and no error, while still reporting the format as extractable.
func TestSearch_NoMatches(t *testing.T) {
	res, err := Search(context.Background(), "testdata/sample.txt", "zzzznotpresent", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable {
		t.Fatalf("expected extractable, got %+v", res)
	}
	if res.TotalMatches != 0 || len(res.Matches) != 0 || res.HasMore {
		t.Fatalf("expected no matches, got %+v", res)
	}
}

// TestSearch_EmptyQuery verifies that a whitespace-only query returns zero
// matches without an error and reports the format as extractable.
func TestSearch_EmptyQuery(t *testing.T) {
	res, err := Search(context.Background(), "testdata/sample.txt", "   ", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.TotalMatches != 0 {
		t.Fatalf("expected extractable with zero matches, got %+v", res)
	}
}

// TestSearch_EPUB verifies that searching a temporary EPUB for a known chapter
// word returns a match with a character offset set and the epub format.
func TestSearch_EPUB(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	res, err := Search(context.Background(), path, "beta", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("expected extractable epub, got %+v", res)
	}
	if res.TotalMatches < 1 {
		t.Fatalf("expected a match, got %+v", res)
	}
	if res.Matches[0].CharOffset <= 0 {
		t.Errorf("want a positive char offset for a second-chapter term, got %d", res.Matches[0].CharOffset)
	}
	if !strings.Contains(res.Matches[0].Snippet, "beta") {
		t.Errorf("snippet should contain the term, got %q", res.Matches[0].Snippet)
	}
}

// TestSearch_TXT verifies that searching the sample text file for "brown"
// returns a match whose snippet contains the term.
func TestSearch_TXT(t *testing.T) {
	res, err := Search(context.Background(), "testdata/sample.txt", "brown", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "txt" {
		t.Fatalf("expected extractable txt, got %+v", res)
	}
	if res.TotalMatches != 1 {
		t.Fatalf("want exactly one match for 'brown', got %+v", res)
	}
	if !strings.Contains(res.Matches[0].Snippet, "brown") {
		t.Errorf("snippet should contain 'brown', got %q", res.Matches[0].Snippet)
	}
}

// TestSearch_ScannedPDFNoText verifies that a PDF with no text layer is reported
// as not extractable with a reason mentioning the missing text layer, and never
// panics.
func TestSearch_ScannedPDFNoText(t *testing.T) {
	res, err := Search(context.Background(), "testdata/scanned.pdf", "anything", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "text layer") && !strings.Contains(res.Reason, "scanned") {
		t.Errorf("reason should mention text layer/scanned, got %q", res.Reason)
	}
}

// TestSearch_UnsupportedFormat verifies that an unsupported container format is
// reported as not extractable with a non-empty reason.
func TestSearch_UnsupportedFormat(t *testing.T) {
	res, err := Search(context.Background(), "testdata/unsupported.djvu", "anything", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable || res.Reason == "" {
		t.Fatalf("djvu must be not-extractable with a reason, got %+v", res)
	}
}

// TestSearch_UnsupportedExtension verifies Search's default dispatch branch: an
// unrecognized extension (neither supported nor a known container) is reported
// as not extractable with a reason naming the extension.
func TestSearch_UnsupportedExtension(t *testing.T) {
	res, err := Search(context.Background(), "testdata/whatever.xyz", "q", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "unsupported file extension") {
		t.Errorf("reason should name the unsupported extension, got %q", res.Reason)
	}
}

// TestSearch_NegativeStartMatch verifies that a negative StartMatch is
// normalized to zero, so the first window of matches is returned rather than
// an out-of-range slice.
func TestSearch_NegativeStartMatch(t *testing.T) {
	res, err := Search(context.Background(), "testdata/sample.txt", "the", SearchOpts{StartMatch: -5})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable {
		t.Fatalf("expected extractable, got %+v", res)
	}
	if res.TotalMatches > 0 && len(res.Matches) == 0 {
		t.Errorf("negative StartMatch should return the first window, got %+v", res)
	}
}

// TestSearchPDF_MalformedOpen verifies scanPDFMatches' pdf.Open failure branch: a
// .pdf whose bytes are not a valid PDF is reported as not extractable with a
// "not a valid PDF" reason rather than crashing.
func TestSearchPDF_MalformedOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7 not really a pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), path, "anything", SearchOpts{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "not a valid PDF") {
		t.Errorf("reason should note the invalid PDF, got %q", res.Reason)
	}
}

// TestScanPDFMatches_ContextCancelled verifies that a context canceled by the
// time the page loop runs is propagated out of scanPDFMatches: the per-page
// guard fires on the first page and the function returns the context error.
func TestScanPDFMatches_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanPDFMatches(ctx, "testdata/sample.pdf", "the", SearchOpts{SnippetChars: 160}); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestSearchPDF_NullPage verifies the null-page skip branch in the PDF search
// scanner: a PDF advertising one page whose only Kid is a dangling reference
// yields a null page, which is skipped, leaving no text and the scanned/no-text-
// layer reason rather than a crash.
func TestSearchPDF_NullPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nullpage.pdf")
	if err := os.WriteFile(path, nullPagePDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), path, "anything", SearchOpts{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "text layer") {
		t.Errorf("reason should note the missing text layer, got %q", res.Reason)
	}
}

// TestSearchTXT_ContextCancelledDirect verifies searchTXT's own entry guard:
// called directly with an already-canceled context it returns the context error
// before opening the file.
func TestSearchTXT_ContextCancelledDirect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searchTXT(ctx, "testdata/sample.txt", "the", SearchOpts{SnippetChars: 160}); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestSearchTXT_MissingFile verifies searchTXT's os.Open failure branch: a
// non-existent .txt path is reported as not extractable with a reason noting the
// file could not be opened.
func TestSearchTXT_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.txt")
	res, err := Search(context.Background(), path, "the", SearchOpts{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "cannot open text file") {
		t.Errorf("reason should note the open failure, got %q", res.Reason)
	}
}

// TestSearchTXT_ReadError verifies searchTXT's read-failure branch: a path that
// opens but cannot be read to completion (a directory) is reported as not
// extractable with a reason noting the read failure.
func TestSearchTXT_ReadError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "adir.txt")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), dir, "the", SearchOpts{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "cannot read text file") {
		t.Errorf("reason should note the read failure, got %q", res.Reason)
	}
}

// TestSearchEPUB_ContextCancelledDirect verifies searchEPUB's own entry guard:
// called directly with an already-canceled context it returns the context error
// before opening the archive.
func TestSearchEPUB_ContextCancelledDirect(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searchEPUB(ctx, path, "beta", SearchOpts{SnippetChars: 160}); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestSearchEPUB_NotAZip verifies searchEPUB's zip.OpenReader failure branch: a
// .epub whose bytes are not a valid ZIP archive is reported as not extractable
// with a reason noting the archive could not be opened.
func TestSearchEPUB_NotAZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notazip.epub")
	if err := os.WriteFile(path, []byte("this is not a zip archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), path, "beta", SearchOpts{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "cannot open EPUB archive") {
		t.Errorf("reason should note the archive open failure, got %q", res.Reason)
	}
}

// TestSearchEPUB_StructuralError verifies searchEPUB's non-context error branch:
// a valid ZIP that is not a structurally valid EPUB (no container.xml) makes the
// search report not extractable with a "not a readable EPUB" reason.
func TestSearchEPUB_StructuralError(t *testing.T) {
	path := writeEPUB(t, t.TempDir(), "no-container.epub", map[string]string{
		"README.txt": "not an epub",
	})
	res, err := Search(context.Background(), path, "beta", SearchOpts{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "not a readable EPUB") {
		t.Errorf("reason should note the unreadable EPUB, got %q", res.Reason)
	}
}

// TestSearchEPUB_ContextCancelledMidSpine verifies that a context live at
// searchEPUB's entry but canceled by the time the spine walk runs is propagated
// as the context error rather than a result.
func TestSearchEPUB_ContextCancelledMidSpine(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	if _, err := searchEPUB(passErr(1), path, "beta", SearchOpts{SnippetChars: 160}); err == nil {
		t.Fatal("expected a context error propagated from the spine walk, got nil")
	}
}

// TestSearch_ContextCancelled verifies that a canceled context causes Search to
// return the context error.
func TestSearch_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Search(ctx, "testdata/sample.pdf", "Second", SearchOpts{})
	if err == nil {
		t.Fatal("expected a context error, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context error, got %v", err)
	}
}
