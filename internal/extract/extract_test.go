package extract

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildEPUB writes a minimal valid EPUB (mimetype + container.xml + OPF with a
// 2-item spine + 2 XHTML chapters) into dir and returns the file path. The two
// chapters contain the sentences "Chapter one alpha sentence." and
// "Chapter two beta sentence." so tests can assert on extracted content.
func buildEPUB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sample.epub")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)

	// mimetype must be first and stored (uncompressed) per the EPUB spec.
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err = mw.Write([]byte("application/epub+zip")); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}

	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="chapter2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
    <itemref idref="c2"/>
  </spine>
</package>`,
		"OEBPS/chapter1.xhtml": `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>C1</title>
<style>.x{color:red}</style></head>
<body><h1>Chapter One</h1><p>Chapter one alpha sentence.</p>
<script>var ignore = 1;</script></body></html>`,
		"OEBPS/chapter2.xhtml": `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>C2</title></head>
<body><p>Chapter two beta sentence.</p></body></html>`,
	}
	for name, content := range files {
		w, cerr := zw.Create(name)
		if cerr != nil {
			t.Fatalf("create %s: %v", name, cerr)
		}
		if _, werr := w.Write([]byte(content)); werr != nil {
			t.Fatalf("write %s: %v", name, werr)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return path
}

// TestExtract_PDF verifies that a text-layer PDF extracts its first page,
// reports the correct format and total page count, and signals HasMore when
// further pages remain.
func TestExtract_PDF(t *testing.T) {
	c, err := Extract(context.Background(), "testdata/sample.pdf", Req{StartPage: 1, MaxPages: 1, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable || c.Format != "pdf" || strings.TrimSpace(c.Text) == "" {
		t.Fatalf("expected extractable pdf text, got %+v", c)
	}
	if c.TotalPages < 2 {
		t.Errorf("want TotalPages>=2, got %d", c.TotalPages)
	}
	if !strings.Contains(c.Text, "Hands-On Software Architecture") {
		t.Errorf("expected page-1 text, got %q", c.Text)
	}
	if c.PageEnd != 1 {
		t.Errorf("want PageEnd==1, got %d", c.PageEnd)
	}
	if !c.HasMore {
		t.Errorf("want HasMore true (page 2 remains), got %+v", c)
	}
}

// TestExtract_PDFSecondPage verifies that StartPage=2 extracts the second page
// of the sample PDF.
func TestExtract_PDFSecondPage(t *testing.T) {
	c, err := Extract(context.Background(), "testdata/sample.pdf", Req{StartPage: 2, MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable {
		t.Fatalf("expected extractable, got %+v", c)
	}
	if !strings.Contains(c.Text, "Second page") {
		t.Errorf("expected page-2 text, got %q", c.Text)
	}
}

// TestExtract_ScannedPDFNoTextLayer verifies that a PDF with no text layer is
// reported as not extractable with a reason mentioning the missing text layer.
func TestExtract_ScannedPDFNoTextLayer(t *testing.T) {
	c, err := Extract(context.Background(), "testdata/scanned.pdf", Req{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "text layer") && !strings.Contains(c.Reason, "scanned") {
		t.Errorf("reason should mention text layer/scanned, got %q", c.Reason)
	}
}

// TestExtract_EPUB verifies that a minimal EPUB extracts XHTML chapter text in
// spine order, reporting the epub format.
func TestExtract_EPUB(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	c, err := Extract(context.Background(), path, Req{Offset: 0, MaxChars: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable || c.Format != "epub" || strings.TrimSpace(c.Text) == "" {
		t.Fatalf("expected epub text, got %+v", c)
	}
	if !strings.Contains(c.Text, "Chapter one alpha sentence") {
		t.Errorf("expected chapter-1 sentence, got %q", c.Text)
	}
	if strings.Contains(c.Text, "var ignore") {
		t.Errorf("script content must be skipped, got %q", c.Text)
	}
}

// TestExtract_EPUBPagination verifies char-based pagination on an EPUB: a small
// MaxChars truncates output, sets HasMore and NextCursor.Char, and a follow-up
// call at that offset continues the document.
func TestExtract_EPUBPagination(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	c, err := Extract(context.Background(), path, Req{Offset: 0, MaxChars: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(c.Text)) > 20 {
		t.Errorf("text longer than MaxChars: %d runes", len([]rune(c.Text)))
	}
	if !c.HasMore || c.NextCursor.Char != 20 {
		t.Fatalf("want HasMore and NextCursor.Char==20, got %+v", c)
	}
	c2, err := Extract(context.Background(), path, Req{Offset: c.NextCursor.Char, MaxChars: 20})
	if err != nil {
		t.Fatal(err)
	}
	if c2.CharStart != 20 || strings.TrimSpace(c2.Text) == "" {
		t.Errorf("continuation wrong, got %+v", c2)
	}
}

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
