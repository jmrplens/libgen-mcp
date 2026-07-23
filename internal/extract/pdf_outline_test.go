package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestOutline_PDFNoBookmarks verifies that a normal text PDF with no embedded
// outline is reported as extractable with no entries and a reason noting the
// absence of an outline: a PDF without bookmarks is valid, not an error or a
// crash.
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
	if !strings.Contains(res.Reason, "outline") {
		t.Errorf("want a reason mentioning no outline, got %q", res.Reason)
	}
}

// TestOutline_PDFScannedNoBookmarks verifies that a scanned (no-text-layer) PDF
// with no embedded outline is reported as extractable with no entries and does
// not panic.
func TestOutline_PDFScannedNoBookmarks(t *testing.T) {
	res, err := Outline(context.Background(), "testdata/scanned.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "pdf" {
		t.Fatalf("want extractable pdf, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries, got %+v", res.Entries)
	}
}

// TestOutline_PDFMalformed verifies that a file with a .pdf extension whose
// bytes are not a valid PDF is handled by the recover-guarded bookmark reader:
// the outline read fails softly to an extractable result with no entries and a
// reason, rather than crashing.
func TestOutline_PDFMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7 definitely not a pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatalf("expected nil error for a malformed PDF, got %v", err)
	}
	if !res.Extractable || res.Format != "pdf" {
		t.Fatalf("want extractable pdf with no entries, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries for a malformed PDF, got %+v", res.Entries)
	}
	if res.Reason == "" {
		t.Error("want a non-empty reason for a malformed PDF")
	}
}
