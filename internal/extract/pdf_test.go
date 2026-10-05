package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// buildPDF assembles a minimal but structurally valid PDF from the given object
// bodies (object N is objs[N-1]), writing a correct cross-reference table and
// trailer so pdf.Open succeeds. It is shared with the search tests to build
// fixtures that open cleanly yet exercise page-level edge cases without shipping
// binary files.
func buildPDF(objs []string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xrefStart := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objs)+1)
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF", len(objs)+1, xrefStart)
	return b.Bytes()
}

// streamObj wraps content as a PDF stream object with the /Length its bytes
// actually have, so a fixture stays valid when its content is edited.
func streamObj(content string) string {
	return fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", len(content), content)
}

// graphicsOnlyPDF returns the bytes of a structurally valid one-page PDF whose
// content stream paints a filled rectangle and contains no text-showing
// operator. That is what a scanned page looks like to a text extractor — pixels
// and no characters — so it stands in for a scan without shipping one: no
// third-party file enters the repository, and the fixture is a few hundred bytes
// of readable PDF syntax rather than an opaque blob.
func graphicsOnlyPDF() []byte {
	return buildPDF([]string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]/Contents 4 0 R/Resources<</ProcSet[/PDF]>>>>",
		streamObj("0 0 0 rg 10 10 100 100 re f"),
	})
}

// blankThenTextPDF returns the bytes of a two-page PDF whose first page is
// graphics-only (as above) and whose second shows text, modeling a book that
// opens on a scanned cover before its text layer begins.
func blankThenTextPDF() []byte {
	return buildPDF([]string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R 4 0 R]/Count 2>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]/Contents 5 0 R/Resources<</ProcSet[/PDF]>>>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]/Contents 6 0 R/Resources<</Font<</F1 7 0 R>>>>>>",
		streamObj("0 0 0 rg 10 10 100 100 re f"),
		streamObj("BT /F1 12 Tf 10 100 Td (readable body text) Tj ET"),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	})
}

// nullPagePDF returns the bytes of a PDF whose Pages tree declares Count 1 but
// whose single Kid reference points at a missing object, so pdf.NumPage reports
// one page while pdf.Page(1) resolves to a null page. It drives the
// "skip a null page" branch of the scanners.
func nullPagePDF() []byte {
	return buildPDF([]string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[99 0 R]/Count 1>>",
	})
}

// TestPDFReasonHelpers verifies the two diagnoses every PDF mode shares are
// produced in one place and word the failure identically. They exist to stop the
// text, find and outline paths from drifting into three phrasings of one
// problem; the recover-guarded malformed case in particular is reachable only
// from a panicking reader, so this is where its wording is pinned.
func TestPDFReasonHelpers(t *testing.T) {
	if got := invalidPDFReason(os.ErrNotExist); got != "not a valid PDF: file does not exist" {
		t.Errorf("invalidPDFReason = %q", got)
	}
	if got := malformedPDFReason("index out of range"); got != "cannot read PDF (malformed or encrypted): index out of range" {
		t.Errorf("malformedPDFReason = %q", got)
	}
}

// TestExtract_PDF verifies that a text-layer PDF extracts its first page,
// reports the correct format and total page count, and signals HasMore when
// further pages remain.
func TestExtract_PDF(t *testing.T) {
	c, err := Extract(context.Background(), openFile(t, "testdata/sample.pdf"), Req{StartPage: 1, MaxPages: 1, MaxChars: 10000})
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
	c, err := Extract(context.Background(), openFile(t, "testdata/sample.pdf"), Req{StartPage: 2, MaxPages: 1})
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
	c, err := Extract(context.Background(), openFile(t, "testdata/scanned.pdf"), Req{})
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

// TestExtract_PDFStartPageBeyondEnd verifies that requesting a StartPage past
// the document's last page is reported as not extractable with a reason that
// mentions the out-of-range condition, rather than the misleading "scanned/no
// text layer" reason used for genuinely empty in-range pages.
func TestExtract_PDFStartPageBeyondEnd(t *testing.T) {
	c, err := Extract(context.Background(), openFile(t, "testdata/sample.pdf"), Req{StartPage: 99})
	if err != nil {
		t.Fatal(err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "beyond") {
		t.Errorf("reason should mention the page being beyond the document, got %q", c.Reason)
	}
	if strings.Contains(c.Reason, "scanned") || strings.Contains(c.Reason, "text layer") {
		t.Errorf("reason must not reuse the scanned/text-layer wording, got %q", c.Reason)
	}
}

// TestExtract_PDFMultiPage verifies that a page range spanning the whole
// document (MaxPages larger than the page count) reads every page, ends the
// scan on the natural loop boundary, and reports HasMore false.
func TestExtract_PDFMultiPage(t *testing.T) {
	c, err := Extract(context.Background(), openFile(t, "testdata/sample.pdf"), Req{StartPage: 1, MaxPages: 5, MaxChars: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable || c.Format != "pdf" {
		t.Fatalf("expected extractable pdf, got %+v", c)
	}
	if c.PageEnd != c.TotalPages {
		t.Errorf("want PageEnd==TotalPages(%d), got %d", c.TotalPages, c.PageEnd)
	}
	if c.HasMore {
		t.Errorf("want HasMore false when all pages read, got %+v", c)
	}
	if !strings.Contains(c.Text, "Second page") {
		t.Errorf("expected page-2 text in the multi-page range, got %q", c.Text)
	}
}

// TestExtract_PDFMaxCharsStop verifies that a small MaxChars stops the scan
// before a subsequent page once the accumulated character budget is reached,
// marking the chunk Truncated with HasMore and a next-page cursor.
func TestExtract_PDFMaxCharsStop(t *testing.T) {
	c, err := Extract(context.Background(), openFile(t, "testdata/sample.pdf"), Req{StartPage: 1, MaxPages: 5, MaxChars: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable {
		t.Fatalf("expected extractable, got %+v", c)
	}
	if !c.Truncated || !c.HasMore {
		t.Errorf("want Truncated and HasMore after hitting MaxChars, got %+v", c)
	}
	if c.NextCursor.Page < 2 {
		t.Errorf("want next-page cursor >= 2, got %d", c.NextCursor.Page)
	}
}

// TestExtract_PDFMaxCharsReachedAtAPageEnd pins the character budget at its
// edge: a budget the first page fills exactly is spent, so the read stops
// before the second page instead of starting it.
func TestExtract_PDFMaxCharsReachedAtAPageEnd(t *testing.T) {
	f := openFile(t, sectionsPDF)
	first, err := Extract(context.Background(), f, Req{MaxPages: 1, MaxChars: 1 << 20})
	if err != nil || !first.Extractable {
		t.Fatalf("page 1: %v %q", err, first.Reason)
	}
	c, err := Extract(context.Background(), f, Req{MaxPages: 5, MaxChars: utf8.RuneCountInString(first.Text)})
	if err != nil {
		t.Fatal(err)
	}
	if c.PageEnd != 1 || !c.Truncated || !c.HasMore || c.NextCursor.Page != 2 {
		t.Errorf("got pages %d-%d truncated=%v has_more=%v next=%d, want page 1 alone and page 2 next",
			c.PageStart, c.PageEnd, c.Truncated, c.HasMore, c.NextCursor.Page)
	}
}

// TestPDFRange_WithDefaults verifies a window left at zero takes the package's
// defaults, starting on page 1, and a window the caller set is kept as given.
func TestPDFRange_WithDefaults(t *testing.T) {
	if got, want := (pdfRange{last: 3}).withDefaults(), (pdfRange{start: 1, last: 3, maxPages: defaultMaxPages, maxChars: defaultMaxChars}); got != want {
		t.Errorf("zero window = %+v, want %+v", got, want)
	}
	if got, want := (pdfRange{start: 2, last: 3, maxPages: 4, maxChars: 5}).withDefaults(), (pdfRange{start: 2, last: 3, maxPages: 4, maxChars: 5}); got != want {
		t.Errorf("set window = %+v, want it kept as %+v", got, want)
	}
}

// TestExtract_PDFWithNoPages verifies a PDF whose page tree holds no page is
// reported the way the outline reports it, as having no text layer, and not
// as a start page past the end of a document the caller asked nothing of.
func TestExtract_PDFWithNoPages(t *testing.T) {
	path := writeBytes(t, t.TempDir(), "empty.pdf", buildPDF([]string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[]/Count 0>>",
	}))
	c, err := Extract(context.Background(), openFile(t, path), Req{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Extractable || c.Reason != noTextLayerReason || c.TotalPages != 0 {
		t.Errorf("got extractable=%v total=%d reason=%q, want no text layer and no pages", c.Extractable, c.TotalPages, c.Reason)
	}
	res, err := Outline(context.Background(), openFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != c.Reason {
		t.Errorf("outline reason %q, want the one Extract gives, %q", res.Reason, c.Reason)
	}
}

// TestExtractPDF_ContextCancelledDirect verifies extractPDF's own entry guard:
// called directly with an already-canceled context it returns the context error
// before reading, a checkpoint Extract's top-level guard normally short-circuits.
func TestExtractPDF_ContextCancelledDirect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractPDF(ctx, docFor(t, "testdata/sample.pdf"), Req{}); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestReadPDFPages_ContextCancelled verifies that a context canceled by the time
// the page scan runs is propagated out of readPDFPages: the per-page guard in
// scanPDFPages fires on the first page and readPDFPages returns the context
// error rather than a chunk.
func TestReadPDFPages_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readPDFPages(ctx, docFor(t, "testdata/sample.pdf"), pdfRange{start: 1, maxPages: 5, maxChars: defaultMaxChars}); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestExtractPDF_NullPage verifies the null-page skip branch: a PDF whose page
// tree advertises one page but whose only Kid is a dangling reference yields a
// null page, which the scanner skips, leaving no text and the scanned/no-text-
// layer reason rather than crashing.
func TestExtractPDF_NullPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nullpage.pdf")
	if err := os.WriteFile(path, nullPagePDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Extract(context.Background(), openFile(t, path), Req{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "text layer") {
		t.Errorf("reason should note the missing text layer, got %q", c.Reason)
	}
}

// TestExtract_PDFMalformed verifies that a file with a .pdf extension whose
// bytes are not a valid PDF is reported as not extractable with a reason (via
// the pdf.NewReader failure path) and a nil error, rather than crashing the caller.
func TestExtract_PDFMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7 not really a pdf at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Extract(context.Background(), openFile(t, path), Req{})
	if err != nil {
		t.Fatalf("expected nil error for malformed PDF, got %v", err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if c.Reason == "" {
		t.Fatal("expected a non-empty reason for a malformed PDF")
	}
}

// TestEncryptionOf reads the encryption dictionary of a plain file, an RC4
// file at two revisions and an AES-128 file, and of the AES-128 file pikepdf
// writes with an empty /OE and /UE, which the reader panics on while it
// decrypts them and which is read as V=4 with nothing else known.
func TestEncryptionOf(t *testing.T) {
	for _, tc := range []struct {
		path string
		want encryption
	}{
		{sectionsPDF, encryption{}},
		{"testdata/encrypted-rc4-40.pdf", encryption{version: 1, revision: 2, bits: 40}},
		{"testdata/encrypted-rc4-88.pdf", encryption{version: 2, revision: 3, bits: 88}},
		{"testdata/encrypted-aes128.pdf", encryption{version: 4, revision: 4, bits: 128}},
		{"testdata/encrypted-aes128-titles.pdf", encryption{version: 4}},
	} {
		t.Run(filepath.Base(tc.path), func(t *testing.T) {
			if got := encryptionOf(readerFor(t, mustRead(t, tc.path))); got != tc.want {
				t.Errorf("encryptionOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestOverwritten reads every window of a PDF 2.0 file's first bytes through
// the view pdfBytes gives it, and each must read as the same window of the
// file with a 1.7 header: the version is replaced whichever part of it a read
// covers, and nothing around it moves.
func TestOverwritten(t *testing.T) {
	const rest = "\n%\xe2\xe3\n1 0 obj"
	file := []byte("%PDF-2.0" + rest)
	shown := "%PDF-1.7" + rest
	v := overwritten{ReaderAt: bytes.NewReader(file), at: versionAt, with: shownVersion}
	for off := range file {
		for size := 1; off+size <= len(file); size++ {
			t.Run(fmt.Sprintf("%d+%d", off, size), func(t *testing.T) {
				p := make([]byte, size)
				n, err := v.ReadAt(p, int64(off))
				if n != size || err != nil || string(p) != shown[off:off+size] {
					t.Errorf("ReadAt = %d %v %q, want %d nil %q", n, err, p, size, shown[off:off+size])
				}
			})
		}
	}
}

// TestOverwritten_AShortRead passes on what the file returns past its end,
// and replaces only the bytes that were read.
func TestOverwritten_AShortRead(t *testing.T) {
	p := []byte("xxxxxxxxxx")
	n, err := overwritten{ReaderAt: strings.NewReader("%PDF-2"), at: versionAt, with: shownVersion}.ReadAt(p, 0)
	if n != 6 || !errors.Is(err, io.EOF) || string(p) != "%PDF-1xxxx" {
		t.Errorf("ReadAt = %d %v %q, want 6 EOF %q", n, err, p, "%PDF-1xxxx")
	}
}

// TestPDFBytes gives the reader a 2.0 file through the view and any other
// file as it is, including one too short to hold a header.
func TestPDFBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		head string
		view bool
	}{
		{"PDF 2.0", "%PDF-2.0\n", true},
		{"PDF 1.7", "%PDF-1.7\n", false},
		{"shorter than a header", "%PDF-2", false},
		{"not a PDF", "PK\x03\x04", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.NewReader(tc.head)
			_, view := pdfBytes(document{r: src, size: int64(len(tc.head))}).(overwritten)
			if view != tc.view {
				t.Errorf("read through the view = %v, want %v", view, tc.view)
			}
		})
	}
}

// TestReadModes_PDF20 reads the sections fixture as Ghostscript writes it at
// PDF 2.0, which the reader refuses by its header alone. Every mode must read
// it as it reads the 1.7 file: the same outline, the same text, the same
// matches.
func TestReadModes_PDF20(t *testing.T) {
	const pdf20 = "testdata/sections-pdf20.pdf"
	ctx := context.Background()
	for _, mode := range []struct {
		name string
		read func(path string) (any, error)
	}{
		{"outline", func(path string) (any, error) { return Outline(ctx, openFile(t, path)) }},
		{"text", func(path string) (any, error) { return Extract(ctx, openFile(t, path), Req{MaxPages: 100}) }},
		{"find", func(path string) (any, error) { return Search(ctx, openFile(t, path), "summary", SearchOpts{}) }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			want, err := mode.read(sectionsPDF)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mode.read(pdf20)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
				t.Errorf("PDF 2.0:\n%+v\nwant the 1.7 file's:\n%+v", got, want)
			}
		})
	}
}
