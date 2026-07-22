package extract

import (
	"context"
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
