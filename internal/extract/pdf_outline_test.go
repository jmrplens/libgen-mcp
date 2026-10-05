package extract

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// TestPdfOutline_ContextCancelledDirect verifies pdfOutline's own entry guard:
// called directly with an already-canceled context it returns the context
// error before opening the file, a checkpoint Outline normally short-circuits.
func TestPdfOutline_ContextCancelledDirect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pdfOutline(ctx, docFor(t, "testdata/bookmarked.pdf")); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestOutline_PDFUnreadableFile verifies pdfOutline's read-failure path: a .pdf
// that opens but cannot be read (a directory) is reported as not extractable,
// with the reason the text path gives for the same file, rather than
// propagating a hard error.
func TestOutline_PDFUnreadableFile(t *testing.T) {
	path := unreadableFixture(t, t.TempDir(), "unreadable.pdf")
	res, err := Outline(context.Background(), openFile(t, path))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	chunk, err := Extract(context.Background(), openFile(t, path), Req{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != chunk.Reason {
		t.Errorf("outline and text paths must give one diagnosis for one cause:\n outline: %q\n text:    %q",
			res.Reason, chunk.Reason)
	}
}

// TestOutline_PDFBookmarks verifies that a PDF carrying an embedded outline is
// read into ordered OutlineEntry values: three top-level chapters at
// Level 0 with their titles and 1-based page numbers, reported with Format
// "pdf" and Extractable true.
func TestOutline_PDFBookmarks(t *testing.T) {
	res, err := Outline(context.Background(), openFile(t, "testdata/bookmarked.pdf"))
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
		t.Run(w.Title, func(t *testing.T) {
			got := res.Entries[i]
			if got.Title != w.Title || got.Level != w.Level || got.Page != w.Page {
				t.Errorf("entry %d: want %+v, got %+v", i, w, got)
			}
		})
	}
}

// TestOutline_PDFNoBookmarks verifies the one case that really is "no table of
// contents": a PDF with a readable text layer and no bookmarks is extractable
// with no entries, and its reason says so without borrowing the scanned/no-text
// wording that belongs to a different failure.
func TestOutline_PDFNoBookmarks(t *testing.T) {
	res, err := Outline(context.Background(), openFile(t, "testdata/sample.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "pdf" {
		t.Fatalf("want extractable pdf, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries, got %+v", res.Entries)
	}
	// Compared against the constants, not fragments of them: the point of this
	// test is that one cause gets one wording, and a substring match would keep
	// passing after that wording drifted apart from the constant it quotes.
	if res.Reason != noPDFOutlineReason {
		t.Errorf("Reason = %q, want the no-table-of-contents diagnosis %q", res.Reason, noPDFOutlineReason)
	}
	if res.Reason == noTextLayerReason {
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
	res, err := Outline(context.Background(), openFile(t, "testdata/scanned.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable || res.Format != "pdf" {
		t.Fatalf("want a not-extractable pdf result, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries, got %+v", res.Entries)
	}
	chunk, err := Extract(context.Background(), openFile(t, "testdata/scanned.pdf"), Req{})
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
	res, err := Outline(context.Background(), openFile(t, path))
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
	res, err := Outline(context.Background(), openFile(t, path))
	if err != nil {
		t.Fatalf("expected nil error for a malformed PDF, got %v", err)
	}
	if res.Extractable || res.Format != "pdf" {
		t.Fatalf("want a not-extractable pdf result, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries for a malformed PDF, got %+v", res.Entries)
	}
	chunk, err := Extract(context.Background(), openFile(t, path), Req{})
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
	if _, err := pdfNoOutlineResult(passErr(0), docFor(t, "testdata/sample.pdf"), outlineWhole); err == nil {
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
	state, reason, err := probePDFTextLayer(context.Background(), docFor(t, path))
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
		"a stride left over":  {total: 10, budget: 3, want: []int{1, 4, 7}},
		"one page":            {total: 1, budget: 20, want: []int{1}},
		"a budget of one":     {total: 10, budget: 1, want: []int{1}},
		"no pages":            {total: 0, budget: 20, want: nil},
		"no budget":           {total: 10, budget: 0, want: nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := probePageNumbers(tc.total, tc.budget)
			if len(got) != len(tc.want) {
				t.Errorf("%s: probePageNumbers(%d, %d) = %v, want %v", name, tc.total, tc.budget, got, tc.want)
				return
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("%s: probePageNumbers(%d, %d) = %v, want %v", name, tc.total, tc.budget, got, tc.want)
					break
				}
			}
		})
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
	res, err := Outline(context.Background(), openFile(t, path))
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

// outlineTargetsPDF is a generated five-page PDF (testdata/outline-targets.ps
// through Ghostscript) whose outline reaches its pages through a named
// destination in a name tree, a direct page, a closed item, a GoTo action and a
// UTF-16 title, and carries a tab and a bell in two of its titles.
const outlineTargetsPDF = "testdata/outline-targets.pdf"

// outlineBody is the content stream every page of a built outline fixture
// shows, so the text-layer probe finds text on each of them.
const outlineBody = "BT /F1 12 Tf 10 100 Td (outline fixture body text) Tj ET"

// outlinePDF returns a three-page PDF with text on every page and an outline
// whose first item is object 9. Objects 1 to 8 are fixed: the catalog, with
// catalogExtra written into it, the page tree, pages 1 to 3 as objects 3 to 5,
// the /Outlines dictionary, the content stream the pages share and the font.
// objs are objects 9 onward, so a fixture writes its items, and anything they
// refer to, by those numbers.
func outlinePDF(catalogExtra string, objs ...string) []byte {
	page := "<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]/Contents 7 0 R/Resources<</Font<</F1 8 0 R>>>>>>"
	return buildPDF(append([]string{
		"<</Type/Catalog/Pages 2 0 R/Outlines 6 0 R" + catalogExtra + ">>",
		"<</Type/Pages/Kids[3 0 R 4 0 R 5 0 R]/Count 3>>",
		page, page, page,
		"<</Type/Outlines/First 9 0 R>>",
		streamObj(outlineBody),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	}, objs...))
}

// destinationFormsPDF returns an outline that names its pages every way the
// format allows, and several ways that lead to no page of the document: a name
// in the catalog's /Dests, a string in a name tree whose value is a dictionary,
// a GoTo action with an explicit page and with a name, a destination held in
// an object of its own, a link out, a name nobody defines, a destination at an
// object that is not a page, a page given as a number, as a link into another
// file gives it, and a dictionary that holds no destination. An item with a
// blank title is left out and its child is not.
func destinationFormsPDF() []byte {
	return outlinePDF("/Dests<</chap2[4 0 R/Fit]>>/Names<</Dests 20 0 R>>",
		"<</Title(Name in the catalog's Dests)/Dest/chap2/Next 10 0 R>>",
		"<</Title(String in the name tree)/Dest(chap3)/Next 11 0 R>>",
		"<</Title(GoTo with an explicit page)/A<</S/GoTo/D[3 0 R/Fit]>>/Next 12 0 R>>",
		"<</Title(A link out)/A<</S/URI/URI(https://example.org/)>>/Next 13 0 R>>",
		"<</Title(A name nobody defines)/Dest/nowhere/Next 14 0 R>>",
		"<</Title(Not a page)/Dest[6 0 R/Fit]/Next 15 0 R>>",
		"<</Title(A page number, as a remote link has)/Dest[0/Fit]/Next 16 0 R>>",
		"<</Title( )/Dest[3 0 R/Fit]/First 17 0 R/Next 18 0 R>>",
		"<</Title(Under an untitled item)/Dest[5 0 R/Fit]>>",
		"<</Title(GoTo through a name)/A<</S/GoTo/D/chap2>>/Next 19 0 R>>",
		"<</Title(Destination held elsewhere)/Dest 22 0 R/Next 24 0 R>>",
		"<</Kids[21 0 R]>>",
		"<</Limits[(chap3)(chap3)]/Names[(chap3) 23 0 R]>>",
		"[5 0 R/Fit]",
		"<</D[5 0 R/XYZ 0 0 0]>>",
		"<</Title(A dictionary with no destination)/Dest<</S/GoTo>>>>",
	)
}

// outlineCycle is an outline whose links lead back to an item already read,
// and the entries a read of it must stop with.
type outlineCycle struct {
	name string
	data []byte
	want string
}

// outlineCycles returns the cycles an outline can carry through the two links
// the walk follows: /Next back to an earlier sibling or to the item itself,
// and /First back to an ancestor or to the item itself.
func outlineCycles() []outlineCycle {
	return []outlineCycle{
		{
			name: "next leads back to an earlier sibling",
			data: outlinePDF("",
				"<</Title(A)/Next 10 0 R/Dest[3 0 R/Fit]>>",
				"<</Title(B)/Next 9 0 R/Dest[4 0 R/Fit]>>"),
			want: "0 1 A\n0 2 B\n",
		},
		{
			name: "next is the item itself",
			data: outlinePDF("", "<</Title(A)/Next 9 0 R>>"),
			want: "0 0 A\n",
		},
		{
			name: "first leads back to the parent",
			data: outlinePDF("",
				"<</Title(A)/First 10 0 R>>",
				"<</Title(B)/Parent 9 0 R/First 9 0 R>>"),
			want: "0 0 A\n1 0 B\n",
		},
		{
			name: "first is the item itself",
			data: outlinePDF("", "<</Title(A)/First 9 0 R>>"),
			want: "0 0 A\n",
		},
	}
}

// withObjectAt returns data with the cross-reference entry of object obj
// replaced by object at's, so obj resolves to the bytes of another object,
// which is what a file whose offsets went stale under an edit looks like to a
// reader. data is a buildPDF document, whose entries are twenty bytes each.
func withObjectAt(t *testing.T, data []byte, obj, at int) []byte {
	t.Helper()
	xref := bytes.LastIndex(data, []byte("\nxref\n"))
	if xref < 0 {
		t.Fatal("the fixture has no cross-reference table")
	}
	header := xref + len("\nxref\n")
	entries := header + bytes.IndexByte(data[header:], '\n') + 1
	out := bytes.Clone(data)
	copy(out[entries+20*obj:entries+20*obj+20], data[entries+20*at:entries+20*at+20])
	return out
}

// readerFor opens data with the PDF reader the outline walk uses.
func readerFor(t *testing.T, data []byte) *pdf.Reader {
	t.Helper()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("pdf.NewReader: %v", err)
	}
	return r
}

// outlineOf reads the outline of data, written to a file first because Outline
// takes one.
func outlineOf(t *testing.T, data []byte) OutlineResult {
	t.Helper()
	res, err := Outline(context.Background(), openFile(t, writeBytes(t, t.TempDir(), "outline.pdf", data)))
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}
	return res
}

// entryLines writes each entry as "level page title" on a line of its own, so
// a whole outline compares as one string and a mismatch prints both in full.
func entryLines(entries []OutlineEntry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%d %d %s\n", e.Level, e.Page, e.Title)
	}
	return b.String()
}

// TestOutline_PDFDestinationsAsAProducerWritesThem reads the Ghostscript
// fixture: each entry lands on the page its destination names, whether that
// is a name looked up in the name tree, a page given directly or a GoTo
// action, at the level its nesting gives it, with a closed item's children
// read like an open one's. The UTF-16 title is decoded, the tab between a
// number and a title becomes a space and the bell is removed.
func TestOutline_PDFDestinationsAsAProducerWritesThem(t *testing.T) {
	res := outlineOf(t, mustRead(t, outlineTargetsPDF))
	want := "0 1 Part I: named dest\n" +
		"1 2 1.1 Direct page\n" +
		"1 2 1.2 Named, closed\n" +
		"2 3 1.2.1 Deep\n" +
		"0 4 Part II: GoTo action\n" +
		"0 5 Español\n" +
		"0 4 3 Tabbed\n" +
		"0 5 Bell\n"
	if got := entryLines(res.Entries); got != want {
		t.Errorf("outline:\n%s\nwant:\n%s", got, want)
	}
	if !res.Extractable || res.Reason != "" {
		t.Errorf("want an extractable result with no reason, got %+v", res)
	}
}

// TestOutline_PDFTitlesInEveryEncoding reads the titles fixture, whose
// titles Ghostscript wrote byte for byte in each encoding a producer uses, and
// its AES-128 copy, in which the reader leaves AES padding on every title.
// Both must read as pdfcpu read them: the padding goes, a UTF-8 title is not
// taken for PDFDocEncoding, a UTF-8 BOM is not shown, and the destination
// named by a string still finds its page. The AES copy's encryption
// dictionary holds an empty /OE, as pikepdf writes it, and its pages must
// read as well.
func TestOutline_PDFTitlesInEveryEncoding(t *testing.T) {
	want := "0 1 • Café naïve — “quoted”\n" +
		"0 1 Part I — Basics\n" +
		"0 1 Accent ˇ caron\n" +
		"0 2 Méthodes & Ünicode ✓ \U0001F308\n" +
		"0 2 Introducción y métodos\n" +
		"0 3 Résumé\n" +
		"0 3 Sixteen bytes ok\n" +
		"0 3 Named by a string\n"
	for _, path := range []string{"testdata/outline-titles.pdf", "testdata/encrypted-aes128-titles.pdf"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			res := outlineOf(t, mustRead(t, path))
			if got := entryLines(res.Entries); got != want || !res.Extractable {
				t.Errorf("outline:\n%s\nwant:\n%s", got, want)
			}
			chunk, err := Extract(context.Background(), openFile(t, path), Req{})
			if err != nil {
				t.Fatal(err)
			}
			if !chunk.Extractable || !strings.Contains(chunk.Text, "Page 3") {
				t.Errorf("want the three pages' text, got %+v", chunk)
			}
		})
	}
}

// TestOutline_PDFEveryDestinationForm covers the destination forms the
// Ghostscript fixture does not write, and the ones that lead nowhere, which
// must give page 0 rather than a wrong page.
func TestOutline_PDFEveryDestinationForm(t *testing.T) {
	res := outlineOf(t, destinationFormsPDF())
	want := "0 2 Name in the catalog's Dests\n" +
		"0 3 String in the name tree\n" +
		"0 1 GoTo with an explicit page\n" +
		"0 0 A link out\n" +
		"0 0 A name nobody defines\n" +
		"0 0 Not a page\n" +
		"0 0 A page number, as a remote link has\n" +
		"1 3 Under an untitled item\n" +
		"0 2 GoTo through a name\n" +
		"0 3 Destination held elsewhere\n" +
		"0 0 A dictionary with no destination\n"
	if got := entryLines(res.Entries); got != want {
		t.Errorf("outline:\n%s\nwant:\n%s", got, want)
	}
}

// readsAt counts the reads of a file that start at one offset, which for the
// PDF reader is how many times it parsed the object there.
type readsAt struct {
	io.ReaderAt
	off   int64
	count int
}

// ReadAt reads from the file, counting a read that starts at r.off.
func (r *readsAt) ReadAt(p []byte, off int64) (int, error) {
	if off == r.off {
		r.count++
	}
	return r.ReaderAt.ReadAt(p, off)
}

// TestOutline_PDFLegacyDestsAreReadOnce names three pages through a PDF 1.1
// /Dests dictionary held in an object of its own, and counts the reads of
// that object: one, however many items name a destination in it. The reader
// keeps no object it has read, so looking each name up through the catalog
// parsed the dictionary once per item, which on 40,000 destinations and 3,000
// items ran past the read's time budget.
func TestOutline_PDFLegacyDestsAreReadOnce(t *testing.T) {
	data := outlinePDF("/Dests 12 0 R",
		"<</Title(One)/Dest/a/Next 10 0 R>>",
		"<</Title(Two)/Dest/b/Next 11 0 R>>",
		"<</Title(Three)/Dest/c>>",
		"<</a[3 0 R/Fit]/b[4 0 R/Fit]/c[5 0 R/Fit]>>")
	file := &readsAt{ReaderAt: bytes.NewReader(data), off: int64(bytes.Index(data, []byte("\n12 0 obj\n")) + 1)}
	entries, _, err := pdfBookmarkEntries(context.Background(), document{name: "dests.pdf", r: file, size: int64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryLines(entries), "0 1 One\n0 2 Two\n0 3 Three\n"; got != want {
		t.Errorf("outline:\n%s\nwant:\n%s", got, want)
	}
	if file.count != 1 {
		t.Errorf("the /Dests dictionary was read %d times, want once", file.count)
	}
}

// TestOutline_PDFCyclesEndTheWalk feeds the walk an outline whose links lead
// back to an item it has read. Each must return, promptly, with every item
// read once: a cycle is where the outline repeats, not a reason to drop the
// part before it.
func TestOutline_PDFCyclesEndTheWalk(t *testing.T) {
	for _, tc := range outlineCycles() {
		t.Run(tc.name, func(t *testing.T) {
			f := openFile(t, writeBytes(t, t.TempDir(), "cycle.pdf", tc.data))
			var (
				res OutlineResult
				err error
			)
			mustReturnWithin(t, 10*time.Second, "Outline", func() {
				res, err = Outline(context.Background(), f)
			})
			if err != nil {
				t.Fatalf("Outline: %v", err)
			}
			if got := entryLines(res.Entries); got != tc.want {
				t.Errorf("outline:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// TestOutline_PDFDamagedOutline covers the case the old reader could not tell
// apart from an absent outline: a document whose pages read and whose outline
// does not, because the cross-reference entry of its first item points at
// another object. Saying "no table of contents" there would be false, so the
// result says the outline is damaged, and stays extractable because the text
// is.
func TestOutline_PDFDamagedOutline(t *testing.T) {
	data := withObjectAt(t, outlinePDF("", "<</Title(A)/Dest[3 0 R/Fit]>>"), 9, 8)
	res := outlineOf(t, data)
	if len(res.Entries) != 0 {
		t.Errorf("a damaged outline yields no entries, got %+v", res.Entries)
	}
	if !res.Extractable || res.Reason != damagedPDFOutlineReason {
		t.Errorf("want extractable with the damaged-outline reason, got %+v", res)
	}
}

// TestOutline_PDFCyclicPageTreeWithAnOutline holds the outline read to the
// page-tree pre-flight even when the file has an outline to offer: a /Pages
// node that lists itself is refused with the shared diagnosis, as every mode
// refuses it, and no entry is returned from a document no mode can read.
func TestOutline_PDFCyclicPageTreeWithAnOutline(t *testing.T) {
	data := buildPDF([]string{
		"<</Type/Catalog/Pages 2 0 R/Outlines 3 0 R>>",
		"<</Type/Pages/Kids[2 0 R]/Count 1>>",
		"<</Type/Outlines/First 4 0 R>>",
		"<</Title(A chapter)/Dest[2 0 R/Fit]>>",
	})
	f := openFile(t, writeBytes(t, t.TempDir(), "cycle.pdf", data))
	var (
		res OutlineResult
		err error
	)
	mustReturnWithin(t, 10*time.Second, "Outline", func() {
		res, err = Outline(context.Background(), f)
	})
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}
	if len(res.Entries) != 0 || res.Extractable || res.Reason != cyclicPDFReason {
		t.Errorf("want no entries and the cyclic page-tree reason, got %+v", res)
	}
}

// TestOutline_PDFTruncatedFile covers the commonest damaged file, an
// interrupted download: the first 5,000 bytes of the sections fixture. It is
// reported as damaged, in the words the text path uses, and not as a document
// without a table of contents.
func TestOutline_PDFTruncatedFile(t *testing.T) {
	path := writeBytes(t, t.TempDir(), "truncated.pdf", mustRead(t, sectionsPDF)[:5000])
	res, err := Outline(context.Background(), openFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := Extract(context.Background(), openFile(t, path), Req{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable || !strings.HasPrefix(res.Reason, "cannot read PDF: the file is damaged (") {
		t.Errorf("want a not-extractable result with the damaged-file reason, got %+v", res)
	}
	if res.Reason != chunk.Reason {
		t.Errorf("outline and text paths must give one diagnosis for one cause:\n outline: %q\n text:    %q",
			res.Reason, chunk.Reason)
	}
}

// TestReadModes_DamageThatQuotesTheWordEncryption points a file's startxref at
// an ordinary object whose text says "encryption", which is what a stale
// startxref in a book about cryptography looks like. The reader quotes that
// object in its error, and every mode must still call the file damaged rather
// than encrypted, since another copy of a damaged file may read and no copy of
// an encryption the reader lacks will.
func TestReadModes_DamageThatQuotesTheWordEncryption(t *testing.T) {
	data := outlinePDF("", "<</Title(Public-key encryption)/Subject(A primer on public-key encryption)>>")
	at := bytes.Index(data, []byte("9 0 obj"))
	end := bytes.LastIndex(data, []byte("startxref\n")) + len("startxref\n")
	data = fmt.Appendf(slices.Clone(data[:end]), "%d\n%%%%EOF", at)
	path := writeBytes(t, t.TempDir(), "stale.pdf", data)

	res, err := Outline(context.Background(), openFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := Extract(context.Background(), openFile(t, path), Req{})
	if err != nil {
		t.Fatal(err)
	}
	found, err := Search(context.Background(), openFile(t, path), "primer", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for mode, reason := range map[string]string{"outline": res.Reason, "text": chunk.Reason, "find": found.Reason} {
		t.Run(mode, func(t *testing.T) {
			if !strings.HasPrefix(reason, "cannot read PDF: the file is damaged (") || !strings.Contains(reason, "encryption") {
				t.Errorf("want the damaged-file reason quoting the object, got %q", reason)
			}
		})
	}
}

// TestOutline_PDFEncrypted reads the sections and the titles fixtures
// encrypted with an empty user password, the shape of a PDF that only
// restricts printing or copying, every way a producer here writes one: by
// qpdf 12.2.0, by Ghostscript 10.00.0, and the AES-128 pdfcpu 0.16.0 writes,
// whose crypt filter gives the key length in bits. Every outline pdfcpu read
// must read the same, titles in every encoding and pages included.
//
// The reader decrypts RC4 (V=2) with a key of 88 bits or more and AES-128
// whose crypt filter gives the length in bytes, and those read in every mode,
// one with its titles in a compressed object stream too. pdfcpu's AES-128,
// which it refuses, reads in every mode once the filter's length is shown to
// it in bytes, object streams included. A shorter RC4 key, which the reader
// decrypts into other bytes, RC4 under a crypt filter (V=4 with V2), which it
// refuses, and AES-128 that leaves the metadata unencrypted, which it takes
// for a file that needs a password, have their outline read by the walk's own
// decryption, object streams included, and the text modes refuse them with a
// reason that points at outline mode. AES-256 (V=5) is refused by outline
// mode as well, and called encrypted rather than an invalid file, a scan, or
// one without a table of contents. A file that does need a password says so
// in every mode.
func TestOutline_PDFEncrypted(t *testing.T) {
	sections := entryLines(outlineOf(t, mustRead(t, sectionsPDF)).Entries)
	titles := entryLines(outlineOf(t, mustRead(t, "testdata/outline-titles.pdf")).Entries)
	for _, tc := range []struct {
		name    string
		outline string
		refusal string
		text    string
	}{
		{"encrypted-rc4.pdf", sections, "", ""},
		{"encrypted-rc4-88.pdf", sections, "", ""},
		{"encrypted-aes128.pdf", sections, "", ""},
		{"encrypted-rc4-titles.pdf", titles, "", ""},
		{"encrypted-aes128-titles.pdf", titles, "", ""},
		{"encrypted-aes128-objstm-titles.pdf", titles, "", ""},
		{"encrypted-rc4-80.pdf", sections, "", partlyEncryptedPDFReason},
		{"encrypted-rc4-40.pdf", sections, "", partlyEncryptedPDFReason},
		{"encrypted-rc4-v4.pdf", sections, "", partlyEncryptedPDFReason},
		{"encrypted-aes128-cf-bits.pdf", sections, "", ""},
		{"encrypted-aes128-cf-bits-titles.pdf", titles, "", ""},
		{"encrypted-aes128-cf-bits-objstm.pdf", sections, "", ""},
		{"encrypted-rc4-40-titles.pdf", titles, "", partlyEncryptedPDFReason},
		{"encrypted-rc4-v4-titles.pdf", titles, "", partlyEncryptedPDFReason},
		{"encrypted-aes128-clear-metadata-titles.pdf", titles, "", partlyEncryptedPDFReason},
		{"encrypted-rc4-40-objstm.pdf", sections, "", partlyEncryptedPDFReason},
		{"encrypted-rc4-v4-objstm-titles.pdf", titles, "", partlyEncryptedPDFReason},
		{"encrypted-aes128-clear-metadata-objstm-titles.pdf", titles, "", partlyEncryptedPDFReason},
		{"encrypted-aes256.pdf", "", encryptedPDFReason, encryptedPDFReason},
		{"encrypted-aes256-titles.pdf", "", encryptedPDFReason, encryptedPDFReason},
		{"encrypted-aes128-user-password.pdf", "", lockedPDFReason, lockedPDFReason},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("testdata", tc.name)
			res := outlineOf(t, mustRead(t, path))
			if got := entryLines(res.Entries); got != tc.outline || res.Extractable != (tc.outline != "") || res.Reason != tc.refusal {
				t.Errorf("outline:\n%s(%q)\nwant:\n%s(%q)", got, res.Reason, tc.outline, tc.refusal)
			}
			chunk, err := Extract(context.Background(), openFile(t, path), Req{})
			if err != nil {
				t.Fatal(err)
			}
			found, err := Search(context.Background(), openFile(t, path), "page", SearchOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if chunk.Reason != tc.text || found.Reason != tc.text || chunk.Extractable != (tc.text == "") {
				t.Errorf("one cause must get one answer, %q:\n text: %q\n find: %q", tc.text, chunk.Reason, found.Reason)
			}
		})
	}
}

// TestPdfOutline_ContextEndsDuringTheWalk ends the context after pdfOutline's
// own check, so the walk is the first to see it, and the walk's error is what
// comes back.
func TestPdfOutline_ContextEndsDuringTheWalk(t *testing.T) {
	path := writeBytes(t, t.TempDir(), "outline.pdf", outlinePDF("", "<</Title(A)>>"))
	if _, err := pdfOutline(passErr(1), docFor(t, path)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pdfOutline = %v, want the walk's context error", err)
	}
}

// TestValueString_PrintsAnUnresolvedReference pins the one ledongthuc/pdf
// behavior the outline walk depends on and the library does not document:
// Value.String prints a reference inside an array as "N G R" instead of
// resolving it. A destination is matched to its page through that text, so if
// a release of the library changed it, every page number would turn to 0 and
// this test, rather than a user, would say why.
func TestValueString_PrintsAnUnresolvedReference(t *testing.T) {
	root := readerFor(t, outlinePDF("", "<</Title(A)/Dest[4 0 R/XYZ 0 0 0]>>")).Trailer().Key("Root")
	if got := root.Key("Pages").Key("Kids").String(); got != "[3 0 R 4 0 R 5 0 R]" {
		t.Errorf("Kids.String() = %q, want %q", got, "[3 0 R 4 0 R 5 0 R]")
	}
	if got := root.Key("Outlines").Key("First").Key("Dest").String(); got != "[4 0 R /XYZ 0 0 0]" {
		t.Errorf("Dest.String() = %q, want %q", got, "[4 0 R /XYZ 0 0 0]")
	}
}

// TestOutlineTitle covers the title cleaning: whitespace controls become a
// space, other C0 and C1 controls and DEL go, the ends are trimmed, and bytes
// that are not UTF-8 become U+FFFD.
func TestOutlineTitle(t *testing.T) {
	for in, want := range map[string]string{
		"1\tPreface":       "1 Preface",
		"Bel\al":           "Bell",
		"line\nbreak":      "line break",
		"next\u0085line":   "next line",
		"csi\u009bremoved": "csiremoved",
		"del\x7fremoved":   "delremoved",
		"  padded\t":       "padded",
		"\x00":             "",
		"bad \xff byte":    "bad \ufffd byte",
		"Español":          "Español",
	} {
		t.Run(fmt.Sprintf("%q", in), func(t *testing.T) {
			if got := outlineTitle(in); got != want {
				t.Errorf("outlineTitle(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// TestWalkBudget covers the budget every walk spends: it allows exactly as
// many steps as it was given, none once its context has ended, keeping that
// error, and none after an error.
func TestWalkBudget(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	underContext := map[bool]context.Context{false: context.Background(), true: canceled}
	for _, tc := range []struct {
		name    string
		budget  walkBudget
		ended   bool
		spends  []bool
		wantErr error
	}{
		{"one step", walkBudget{left: 1}, false, []bool{true, false}, nil},
		{"two steps", walkBudget{left: 2}, false, []bool{true, true, false}, nil},
		{"no steps", walkBudget{left: 0}, false, []bool{false}, nil},
		{"context ended", walkBudget{left: 5}, true, []bool{false, false}, context.Canceled},
		{"error kept", walkBudget{left: 5, err: context.DeadlineExceeded}, false, []bool{false}, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.budget
			got := make([]bool, len(tc.spends))
			for i := range got {
				got[i] = b.spend(underContext[tc.ended])
			}
			if !slices.Equal(got, tc.spends) || !errors.Is(b.err, tc.wantErr) {
				t.Errorf("spends = %v err = %v, want %v err = %v", got, b.err, tc.spends, tc.wantErr)
			}
		})
	}
}

// TestOutlineWalk_Bounds drives the walk at the edges of its two bounds: an
// item on the last level it reads, a child one level past it and an item
// starting past it, and item budgets one short of the outline and exactly its
// size. Only an item left unread marks the outline too large.
func TestOutlineWalk_Bounds(t *testing.T) {
	leaf := outlinePDF("", "<</Title(Leaf)>>")
	nested := outlinePDF("", "<</Title(Parent)/First 10 0 R>>", "<</Title(Child)>>")
	chain := outlinePDF("",
		"<</Title(One)/Next 10 0 R>>", "<</Title(Two)/Next 11 0 R>>", "<</Title(Three)>>")
	last := maxOutlineDepth - 1
	for _, tc := range []struct {
		name      string
		data      []byte
		level     int
		items     int
		want      string
		wantState outlineState
	}{
		{"an item on the last level read", leaf, last, maxOutlineItems, fmt.Sprintf("%d 0 Leaf\n", last), outlineWhole},
		{"a child past the last level", nested, last, maxOutlineItems, fmt.Sprintf("%d 0 Parent\n", last), outlineTooLarge},
		{"an item past the last level", nested, maxOutlineDepth, maxOutlineItems, "", outlineTooLarge},
		{"a budget one item short", chain, 0, 2, "0 0 One\n0 0 Two\n", outlineTooLarge},
		{"a budget of every item", chain, 0, 3, "0 0 One\n0 0 Two\n0 0 Three\n", outlineWhole},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOutlineWalk(outlineSource{r: readerFor(t, tc.data), strs: readerStrings(false)})
			w.items.left = tc.items
			outlines := child(&w.root, "Outlines")
			w.walk(context.Background(), child(&outlines, "First"), tc.level)
			if got := entryLines(w.entries); got != tc.want || w.state != tc.wantState {
				t.Errorf("outline:\n%s(state %d)\nwant:\n%s(state %d)", got, w.state, tc.want, tc.wantState)
			}
		})
	}
}

// TestOutline_PDFAnAESFilterItsLengthDoesNotOpen reads pdfcpu's AES-128
// titles fixture with its crypt filter opened on another event than the
// document's, which the reader refuses whatever the length says. Its outline
// is read by the walk's own decryption, as the other refused filters' are,
// and the text modes point at outline mode.
func TestOutline_PDFAnAESFilterItsLengthDoesNotOpen(t *testing.T) {
	data := bytes.Replace(mustRead(t, "testdata/encrypted-aes128-cf-bits-titles.pdf"), []byte("/AuthEvent/DocOpen"), []byte("/AuthEvent/EFOpen "), 1)
	want := entryLines(outlineOf(t, mustRead(t, "testdata/outline-titles.pdf")).Entries)
	if got := entryLines(outlineOf(t, data).Entries); got != want {
		t.Errorf("outline:\n%s\nwant:\n%s", got, want)
	}
	chunk, err := Extract(context.Background(), openFile(t, writeBytes(t, t.TempDir(), "efopen.pdf", data)), Req{})
	if err != nil || chunk.Reason != partlyEncryptedPDFReason {
		t.Errorf("text: %q (%v), want %q", chunk.Reason, err, partlyEncryptedPDFReason)
	}
}

// TestOutline_PDFTitlesThatLookPadded reads an AES-128 file whose titles are
// in a compressed object stream, where they are not encrypted, and each ends
// in a byte that reads as AES padding: sixteen bytes of UTF-16 ending in a
// letter whose last byte is 0x01. The reader hands them over as they are,
// and taking that byte off as padding lost each title's last letter.
func TestOutline_PDFTitlesThatLookPadded(t *testing.T) {
	res := outlineOf(t, mustRead(t, "testdata/encrypted-aes128-objstm-padding.pdf"))
	if got, want := entryLines(res.Entries), "0 1 ภาคผนวก\n0 2 第一章甲乙丙丁\n0 3 Глава Ё\n"; got != want {
		t.Errorf("outline:\n%s\nwant:\n%s", got, want)
	}
}

// aesTitlesWithFirst returns the AES-128 titles fixture at path, which keeps
// no object stream, with the title of its first item, object 8, a string of
// 48 bytes written as hexadecimal, replaced by title and padded with spaces to
// the same length, so the cross-reference table still finds every object.
func aesTitlesWithFirst(t *testing.T, path, title string) []byte {
	t.Helper()
	data := mustRead(t, path)
	item := bytes.Index(data, []byte("\n8 0 obj\n"))
	at := item + bytes.Index(data[item:], []byte("/Title <")) + len("/Title ")
	if item < 0 || data[at+97] != '>' {
		t.Fatal("the fixture's first item is not where it was")
	}
	return slices.Concat(data[:at], []byte(fmt.Sprintf("%-98s", title)), data[at+98:])
}

// TestOutline_PDFStringsTheReaderCannotDecrypt writes the first title of the
// AES-128 titles fixture two ways each decryption reads differently. An empty
// string left unencrypted, which MuPDF writes when it adds a blank bookmark,
// made the reader panic, so the whole outline was called damaged; the walk's
// own decryption reads it as the empty string it is. The initialization
// vector alone is no string the walk's decryption accepts, and the reader
// takes it for an empty one, so that file is read as the reader decrypts it.
// Either way the blank first title is left out and the rest are listed.
func TestOutline_PDFStringsTheReaderCannotDecrypt(t *testing.T) {
	titles := entryLines(outlineOf(t, mustRead(t, "testdata/outline-titles.pdf")).Entries)
	rest := titles[strings.IndexByte(titles, '\n')+1:]
	for _, tc := range []struct {
		name, title string
	}{
		{"an empty string left unencrypted", "<>"},
		{"the initialization vector alone", "<" + strings.Repeat("05", aes.BlockSize) + ">"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := outlineOf(t, aesTitlesWithFirst(t, "testdata/encrypted-aes128-titles.pdf", tc.title))
			if got := entryLines(res.Entries); got != rest || res.Reason != "" {
				t.Errorf("outline:\n%s(%q)\nwant:\n%s", got, res.Reason, rest)
			}
		})
	}
}

// TestOutline_PDFAnAESTitleWithoutPadding writes the first title of the
// AES-128 titles fixtures as one block encrypted with no padding after it,
// which pdfcpu and qpdf read as the block's sixteen bytes. The walk's own
// decryption finds that damaged, and the file the reader decrypts is read as
// the reader decrypts it. The one that leaves its metadata unencrypted, which
// the reader refuses, was reported as damaged, and is read by the walk's own
// decryption once more, leniently.
func TestOutline_PDFAnAESTitleWithoutPadding(t *testing.T) {
	entries := outlineOf(t, mustRead(t, "testdata/outline-titles.pdf")).Entries
	entries[0].Title = "Sixteen bytes ok"
	want := entryLines(entries)
	for _, path := range []string{"testdata/encrypted-aes128-titles.pdf", "testdata/encrypted-aes128-clear-metadata-titles.pdf"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			trailer := hiddenTrailer(t, path)
			c, ok := standardCrypt(trailer.Key(hiddenEncryptKey), trailer.Key("ID").Index(0).RawString())
			if !ok {
				t.Fatal("standardCrypt refused the fixture")
			}
			block, err := aes.NewCipher(c.objectKey(objRef{id: 8}))
			if err != nil {
				t.Fatal(err)
			}
			iv, sealed := bytes.Repeat([]byte{5}, aes.BlockSize), []byte(entries[0].Title)
			cipher.NewCBCEncrypter(block, iv).CryptBlocks(sealed, sealed)
			res := outlineOf(t, aesTitlesWithFirst(t, path, fmt.Sprintf("<%x%x>", iv, sealed)))
			if got := entryLines(res.Entries); got != want || res.Reason != "" {
				t.Errorf("outline:\n%s(%q)\nwant:\n%s", got, res.Reason, want)
			}
		})
	}
}

// TestWalkEach walks a file as each opener opens it, until one reads the
// outline whole or finds it too large, and says what the walks found
// otherwise: damaged when an earlier walk or any of these found it so,
// whatever the walks after it found, and unread when none opened the file.
func TestWalkEach(t *testing.T) {
	whole := outlinePDF("", "<</Title(A)>>")
	damaged := bytes.Replace(whole, []byte("/Outlines/First 9 0 R"), []byte("/Outlines/First 7 0 R"), 1)
	items := make([]string, maxOutlineDepth+1)
	for i := range items {
		items[i] = fmt.Sprintf("<</Title(Level %d)/First %d 0 R>>", i, 10+i)
	}
	items[maxOutlineDepth] = "<</Title(Deepest)>>"
	tooDeep := outlinePDF("", items...)
	opener := func(data []byte) func() outlineSource {
		return func() outlineSource { return outlineSource{r: readerFor(t, data), strs: readerStrings(false)} }
	}
	unread := func() outlineSource { return outlineSource{} }
	for _, tc := range []struct {
		name    string
		openers []func() outlineSource
		found   outlineState
		want    outlineState
		entries int
	}{
		{"none", nil, outlineUnread, outlineUnread, 0},
		{"none after a damaged walk", nil, outlineDamaged, outlineDamaged, 0},
		{"openers that open nothing", []func() outlineSource{unread, unread}, outlineUnread, outlineUnread, 0},
		{"nothing after a damaged walk", []func() outlineSource{unread}, outlineDamaged, outlineDamaged, 0},
		{"damaged, then nothing", []func() outlineSource{opener(damaged), unread}, outlineUnread, outlineDamaged, 0},
		{"nothing, then damaged", []func() outlineSource{unread, opener(damaged)}, outlineUnread, outlineDamaged, 0},
		{"damaged, then whole", []func() outlineSource{opener(damaged), opener(whole)}, outlineUnread, outlineWhole, 1},
		{"whole after a damaged walk", []func() outlineSource{opener(whole)}, outlineDamaged, outlineWhole, 1},
		{"whole first", []func() outlineSource{opener(whole), opener(damaged)}, outlineUnread, outlineWhole, 1},
		{"too large, then whole", []func() outlineSource{opener(tooDeep), opener(whole)}, outlineUnread, outlineTooLarge, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, state, err := walkEach(context.Background(), tc.openers, tc.found)
			if state != tc.want || len(entries) != tc.entries || err != nil {
				t.Errorf("walkEach = %d entries, state %d, %v, want %d entries, state %d", len(entries), state, err, tc.entries, tc.want)
			}
		})
	}
}

// sourceKind names how src was opened: nothing, a file that is not
// encrypted, one whose encryption the walk decrypts itself, or one the reader
// decrypts.
func sourceKind(src outlineSource) string {
	switch {
	case src.r == nil:
		return "nothing"
	case src.view == nil:
		return "plain"
	case src.view.crypt != nil:
		return "self"
	}
	return "reader"
}

// TestOutlineOpeners names the ways the walk opens each kind of file, in the
// order it tries them, by what each opens: a plain file as the reader hands
// it over, one the reader decrypts correctly with the walk's own decryption
// first and the reader's next, one it decrypts wrongly with the walk's own
// alone, and an AES one last with the walk's own again, leniently, which for
// RC4 opens nothing. One that needs a password has openers that open
// nothing, and an AES-256 file or one that is not a PDF has none at all.
func TestOutlineOpeners(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    document
		want string
	}{
		{"plain", docFor(t, sectionsPDF), "plain"},
		{"AES-128", docFor(t, "testdata/encrypted-aes128.pdf"), "self reader self"},
		{"128-bit RC4", docFor(t, "testdata/encrypted-rc4.pdf"), "self reader nothing"},
		{"40-bit RC4", docFor(t, "testdata/encrypted-rc4-40.pdf"), "self nothing"},
		{"pdfcpu's AES-128", docFor(t, "testdata/encrypted-aes128-cf-bits.pdf"), "self reader self"},
		{"metadata left unencrypted", docFor(t, "testdata/encrypted-aes128-clear-metadata-titles.pdf"), "self self"},
		{"a password", docFor(t, "testdata/encrypted-aes128-user-password.pdf"), "nothing nothing"},
		{"AES-256", docFor(t, "testdata/encrypted-aes256.pdf"), ""},
		{"not a PDF", document{r: strings.NewReader("not a PDF"), size: 9}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var kinds []string
			for _, open := range outlineOpeners(tc.d) {
				kinds = append(kinds, sourceKind(open()))
			}
			if got := strings.Join(kinds, " "); got != tc.want {
				t.Errorf("openers open %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReaderDecrypting_NotAPDF opens nothing for a file the reader refuses,
// which an opener never asks it to open, rather than a source with no reader
// that the walk would read as a broken file.
func TestReaderDecrypting_NotAPDF(t *testing.T) {
	if src := readerDecrypting(document{r: strings.NewReader("not a PDF"), size: 9}); src.r != nil || src.view != nil {
		t.Errorf("readerDecrypting = %+v, want nothing", src)
	}
}

// TestWalkOutline_Unread walks a file its opener does not open, and one whose
// page tree is unsafe to walk: neither is walked, and each says so, so that
// the next opener is tried.
func TestWalkOutline_Unread(t *testing.T) {
	cyclic := buildPDF([]string{"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[2 0 R]/Count 1>>"})
	for _, tc := range []struct {
		name string
		open func() outlineSource
	}{
		{"nothing opened", func() outlineSource { return outlineSource{} }},
		{"a cyclic page tree", func() outlineSource { return outlineSource{r: readerFor(t, cyclic), strs: readerStrings(false)} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, outline, err := walkOutline(context.Background(), tc.open)
			if entries != nil || outline != outlineUnread || err != nil {
				t.Errorf("walkOutline = %v, %d, %v, want nothing and outlineUnread", entries, outline, err)
			}
		})
	}
}

// TestOutlineWalk_AStringThatDoesNotDecode walks an outline whose strings
// its decoder cannot decode, an AES string that does not decrypt to a padded
// whole. That is damage, and the title is not listed.
func TestOutlineWalk_AStringThatDoesNotDecode(t *testing.T) {
	fails := func(string, objRef) (string, bool) { return "", false }
	w := newOutlineWalk(outlineSource{r: readerFor(t, outlinePDF("", "<</Title(A)>>")), strs: fails})
	outlines := child(&w.root, "Outlines")
	w.walk(context.Background(), child(&outlines, "First"), 0)
	if len(w.entries) != 0 || w.state != outlineDamaged {
		t.Errorf("entries %v, state %d, want none and outlineDamaged", w.entries, w.state)
	}
}

// TestChildAndElement holds a value reached through a reference to the
// object it refers to, and a value written in place to the object of the
// value that holds it, in a dictionary and in an array, which is the object
// whose key encrypts its strings.
func TestChildAndElement(t *testing.T) {
	r := readerFor(t, buildPDF([]string{"<</Type/Catalog/T<</Ref 2 0 R/Here(s)/Arr[2 0 R(s)]>>>>", "(in two)"}))
	catalog := child(&node{v: r.Trailer()}, "Root")
	tree := child(&catalog, "T")
	arr := child(&tree, "Arr")
	refs := arrayRefs(arr.v)
	for _, tc := range []struct {
		name string
		got  node
		want objRef
	}{
		{"the catalog", catalog, objRef{id: 1}},
		{"a dictionary in place", tree, objRef{id: 1}},
		{"a reference", child(&tree, "Ref"), objRef{id: 2}},
		{"a string in place", child(&tree, "Here"), objRef{id: 1}},
		{"an element reference", element(arr, refs, 0), objRef{id: 2}},
		{"an element in place", element(arr, refs, 1), objRef{id: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.in != tc.want {
				t.Errorf("in = %+v, want %+v", tc.got.in, tc.want)
			}
		})
	}
	if s := child(&tree, "Ref").v.RawString(); s != "in two" {
		t.Errorf("the reference reads %q, want %q", s, "in two")
	}
}

// TestOutline_PDFLinksToNothing breaks an outline at a link the walk follows
// with a value that is not an item: an object of its own that is a stream,
// and a number written in place. Each is damage, reported as such with no
// entries, since what was read before the break is not the whole outline. A
// link to an object the file does not define is null, and ends its chain
// (TestOutline_PDFNullLinks).
func TestOutline_PDFLinksToNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		items    []string
		from, to string
	}{
		{"the first item not a dictionary", []string{"<</Title(A)>>"}, "/Outlines/First 9 0 R", "/Outlines/First 7 0 R"},
		{"a child written in place as a number", []string{"<</Title(A)/First 0/Next 10 0 R>>", "<</Title(B)>>"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each replacement keeps the object's length, so the
			// cross-reference table still finds every object.
			data := bytes.Replace(outlinePDF("", tc.items...), []byte(tc.from), []byte(tc.to), 1)
			res := outlineOf(t, data)
			if len(res.Entries) != 0 || !res.Extractable || res.Reason != damagedPDFOutlineReason {
				t.Errorf("want no entries and the damaged-outline reason, got %+v", res)
			}
		})
	}
}

// TestOutline_PDFNullLinks writes each link the walk follows as null, which
// ISO 32000-1 makes the same as leaving it out: the last item's /Next, a
// leaf's /First, the first item, and the catalog's /Outlines, which is how
// pdfcpu removes an outline. It also writes links to objects the file does
// not define, which the standard reads as null (7.3.10): one past the end of
// the cross-reference table, a free one, and one at a generation the table
// does not hold, which pdfcpu and qpdf read as the end of the chain. Each ends
// its chain, so the items read are the whole outline and a section of it
// reads, and a null /Outlines is a document with no outline rather than a
// damaged one. They were all reported as damage.
func TestOutline_PDFNullLinks(t *testing.T) {
	three := "0 1 A\n0 2 B\n0 3 C\n"
	threeThenNext := func(next string) []byte {
		return outlinePDF("",
			"<</Title(A)/Dest[3 0 R/Fit]/Next 10 0 R>>",
			"<</Title(B)/Dest[4 0 R/Fit]/Next 11 0 R>>",
			"<</Title(C)/Dest[5 0 R/Fit]/Next "+next+">>")
	}
	for _, tc := range []struct {
		name     string
		data     []byte
		want     string
		reason   string
		sections bool
	}{
		{"the last item's /Next", threeThenNext("null"), three, "", true},
		{"a /Next past the table", threeThenNext("99 0 R"), three, "", true},
		{"a /Next to a free object", threeThenNext("0 0 R"), three, "", true},
		{"a /Next at another generation", threeThenNext("10 1 R"), three, "", true},
		{"a leaf's /First", outlinePDF("",
			"<</Title(A)/Dest[3 0 R/Fit]/First null/Next 10 0 R>>",
			"<</Title(B)/Dest[4 0 R/Fit]/Next 11 0 R>>",
			"<</Title(C)/Dest[5 0 R/Fit]>>"), three, "", true},
		{"the first item", bytes.Replace(outlinePDF("", "<</Title(A)>>"), []byte("/First 9 0 R"), []byte("/First null "), 1), "", noPDFOutlineReason, false},
		{"the first item at another generation", bytes.Replace(outlinePDF("", "<</Title(A)>>"), []byte("/First 9 0 R"), []byte("/First 9 1 R"), 1), "", noPDFOutlineReason, false},
		{"the catalog's /Outlines", bytes.Replace(outlinePDF("", "<</Title(A)>>"), []byte("/Outlines 6 0 R"), []byte("/Outlines null "), 1), "", noPDFOutlineReason, false},
		{"the catalog's /Outlines at another generation", bytes.Replace(outlinePDF("", "<</Title(A)>>"), []byte("/Outlines 6 0 R"), []byte("/Outlines 6 1 R"), 1), "", noPDFOutlineReason, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := outlineOf(t, tc.data)
			if got := entryLines(res.Entries); got != tc.want || !res.Extractable || res.Reason != tc.reason {
				t.Errorf("outline:\n%s(%q)\nwant:\n%s(%q)", got, res.Reason, tc.want, tc.reason)
			}
			path := writeBytes(t, t.TempDir(), "null.pdf", tc.data)
			sc, err := Section(context.Background(), openFile(t, path), SectionRef{Index: 2}, Req{})
			if tc.sections && (err != nil || sc.PageStart != 2) {
				t.Errorf("section 2 = %+v (%v), want it to start on page 2", sc.Chunk, err)
			}
		})
	}
}

// TestOutline_PDFTooLarge reads an outline nested one level deeper than the
// walk reads. It is reported as too large to list, with no entries, rather
// than as the part above the bound, and a section read of it says the table
// of contents could not be read rather than that there is none.
func TestOutline_PDFTooLarge(t *testing.T) {
	items := make([]string, maxOutlineDepth+1)
	for i := range items {
		items[i] = fmt.Sprintf("<</Title(Level %d)/Dest[3 0 R/Fit]/First %d 0 R>>", i, 10+i)
	}
	items[maxOutlineDepth] = fmt.Sprintf("<</Title(Level %d)>>", maxOutlineDepth)
	path := writeBytes(t, t.TempDir(), "deep.pdf", outlinePDF("", items...))
	res, err := Outline(context.Background(), openFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 0 || !res.Extractable || res.Reason != largePDFOutlineReason {
		t.Errorf("want no entries and the too-large reason, got %+v", res)
	}
	if _, serr := Section(context.Background(), openFile(t, path), SectionRef{Index: 1}, Req{}); !errors.Is(serr, ErrOutlineUnreadable) {
		t.Errorf("Section = %v, want ErrOutlineUnreadable", serr)
	}
}

// TestLargePDFOutlineReason holds the numbers the too-large reasons name to
// the bounds the walk applies, since each reason is one literal.
func TestLargePDFOutlineReason(t *testing.T) {
	for _, reason := range []string{largePDFOutlineReason, partlyEncryptedLargeOutlineReason} {
		for _, want := range []string{fmt.Sprintf("over %d entries", maxOutlineItems), fmt.Sprintf("over %d levels", maxOutlineDepth)} {
			t.Run(want, func(t *testing.T) {
				if !strings.Contains(reason, want) {
					t.Errorf("reason = %q, want it to say %q", reason, want)
				}
			})
		}
	}
}

// TestPartlyEncryptedOutlineReason names what outline mode says of a file
// whose text the reader cannot decrypt and whose outline listed nothing, for
// each thing the walk can have found.
func TestPartlyEncryptedOutlineReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outline outlineState
		want    string
	}{
		{"none", outlineWhole, partlyEncryptedNoOutlineReason},
		{"damaged", outlineDamaged, partlyEncryptedDamagedOutlineReason},
		{"too large", outlineTooLarge, partlyEncryptedLargeOutlineReason},
		{"not opened", outlineUnread, encryptedPDFReason},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := partlyEncryptedOutlineReason(tc.outline); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOutline_PDFPartlyEncryptedWithNoOutline reads a two-page file
// Ghostscript encrypted with a 40-bit RC4 key and gave no outline: outline
// mode says it has none, where it used to say its table of contents could not
// be read, which the text modes had just said outline mode might list.
func TestOutline_PDFPartlyEncryptedWithNoOutline(t *testing.T) {
	res := outlineOf(t, mustRead(t, "testdata/encrypted-rc4-40-no-outline.pdf"))
	if len(res.Entries) != 0 || res.Extractable || res.Reason != partlyEncryptedNoOutlineReason {
		t.Errorf("want no entries and the no-outline reason, got %+v", res)
	}
}

// noIDPDF returns an outline PDF encrypted with a 40-bit RC4 key under the
// empty user password, with no /ID in its trailer, as a producer that leaves
// the ID out writes one: its key is derived from an empty ID. Each title is
// encrypted with the key of its own object, 9 onward, the item i leading to
// page i+1, and the encryption dictionary is the object after the last item.
func noIDPDF(titles ...string) []byte {
	o := strings.Repeat("\x5a", len(paddingString))
	c := stringCrypt{key: fileKey(o, -4, "", 2, minFileKeyBits/8, true)}
	objs := make([]string, 0, len(titles)+1)
	for i, title := range titles {
		next := ""
		if i+1 < len(titles) {
			next = fmt.Sprintf("/Next %d 0 R", 10+i)
		}
		sealed := rc4XOR(c.objectKey(objRef{id: uint32(9 + i)}), []byte(title))
		objs = append(objs, fmt.Sprintf("<</Title<%x>/Dest[%d 0 R/Fit]%s>>", sealed, 3+i, next))
	}
	objs = append(objs, fmt.Sprintf("<</Filter/Standard/V 1/R 2/Length 40/P -4/O<%x>/U<%x>>>", o, rc4XOR(c.key, []byte(paddingString))))
	return bytes.Replace(outlinePDF("", objs...), []byte("/Root 1 0 R>>"), fmt.Appendf(nil, "/Root 1 0 R/Encrypt %d 0 R>>", 9+len(titles)), 1)
}

// TestOutline_PDFEncryptedWithNoID reads an encrypted file whose trailer has
// no /ID, which the reader refuses as malformed: the walk derives its key from
// an empty ID, as pdfcpu did, and lists every title, and the text modes,
// which the reader cannot open it for, point at outline mode. Every mode
// called it damaged, and told the caller another copy might be intact.
func TestOutline_PDFEncryptedWithNoID(t *testing.T) {
	data := noIDPDF("A", "B", "C")
	if _, err := pdf.NewReader(bytes.NewReader(data), int64(len(data))); err == nil || err.Error() != missingIDRefusal {
		t.Fatalf("the reader opens the file with %v, want %q", err, missingIDRefusal)
	}
	res := outlineOf(t, data)
	if got := entryLines(res.Entries); got != "0 1 A\n0 2 B\n0 3 C\n" || res.Reason != "" {
		t.Errorf("outline:\n%s(%q)\nwant the three titles", got, res.Reason)
	}
	chunk, err := Extract(context.Background(), openFile(t, writeBytes(t, t.TempDir(), "noid.pdf", data)), Req{})
	if err != nil || chunk.Reason != partlyEncryptedPDFReason {
		t.Errorf("text = %q (%v), want %q", chunk.Reason, err, partlyEncryptedPDFReason)
	}
}

// nameTreePDF returns a two-page PDF whose catalog's /T is object 5, the first
// of objs, which are objects 5 onward. A destination "[3 0 R/Fit]" names page
// 1 and "[4 0 R/Fit]" page 2.
func nameTreePDF(objs ...string) []byte {
	return buildPDF(append([]string{
		"<</Type/Catalog/Pages 2 0 R/T 5 0 R>>",
		"<</Type/Pages/Kids[3 0 R 4 0 R]/Count 2>>",
		"<</Type/Page>>", "<</Type/Page>>",
	}, objs...))
}

// nameTreeRead reads the catalog's /T in data as the name tree, from depth, for
// an outline whose entries asked for the names asked, in that order, and
// returns each name with the page it was given, and the walk.
func nameTreeRead(t *testing.T, ctx context.Context, data []byte, aes bool, depth int, asked ...string) (string, *outlineWalk) {
	t.Helper()
	root := readerFor(t, data).Trailer().Key("Root")
	w := &outlineWalk{root: node{v: root}, strs: readerStrings(aes), named: map[string][]int{}, entries: make([]OutlineEntry, len(asked))}
	for i, name := range asked {
		w.named[name] = append(w.named[name], i)
	}
	w.readNameTree(ctx, node{v: root.Key("T"), in: objRef{id: 5}}, depth, map[objRef]bool{})
	lines := make([]string, len(asked))
	for i, name := range asked {
		lines[i] = fmt.Sprintf("%q=%d", name, w.entries[i].Page)
	}
	return strings.Join(lines, " "), w
}

// TestReadNameTree covers the name-tree read on trees built for each edge: a
// /Names array with a key and no value, a name the outline did not ask for, two
// entries asking for one name, a /Kids array that leads back to its own node,
// a later leaf replacing an earlier one, the last depth read and the first not
// read, a destination held in a dictionary, and a key that carries AES padding,
// which matches without it in an AES file and only as it is in any other.
func TestReadNameTree(t *testing.T) {
	padded := nameTreePDF("<</Names[(a" + strings.Repeat(`\017`, 15) + ") [4 0 R/Fit]]>>")
	for _, tc := range []struct {
		name  string
		data  []byte
		aes   bool
		depth int
		asked []string
		want  string
	}{
		{"a key with no value", nameTreePDF("<</Names[(a)[3 0 R/Fit] (b)[4 0 R/Fit] (c)]>>"), false, 0, []string{"a", "b", "c"}, `"a"=1 "b"=2 "c"=0`},
		{"a key with no value after one with a value", nameTreePDF("<</Kids[6 0 R 7 0 R]>>", "<</Names[(c)[4 0 R/Fit]]>>", "<</Names[(a)[3 0 R/Fit] (c)]>>"), false, 0, []string{"a", "c"}, `"a"=1 "c"=2`},
		{"an empty name, the last one", nameTreePDF("<</Names[()[4 0 R/Fit]]>>"), false, 0, []string{""}, `""=2`},
		{"a name nobody asked for", nameTreePDF("<</Names[(a)[3 0 R/Fit] (b)[4 0 R/Fit]]>>"), false, 0, []string{"b"}, `"b"=2`},
		{"two entries asking for one name", nameTreePDF("<</Names[(a)[4 0 R/Fit]]>>"), false, 0, []string{"a", "a"}, `"a"=2 "a"=2`},
		{"kids lead back to the node", nameTreePDF("<</Names[(a)[4 0 R/Fit]]/Kids[5 0 R]>>"), false, 0, []string{"a"}, `"a"=2`},
		{"a later leaf replaces an earlier one", nameTreePDF("<</Kids[6 0 R 7 0 R]>>", "<</Names[(a)[3 0 R/Fit]]>>", "<</Names[(a)[4 0 R/Fit]]>>"), false, 0, []string{"a"}, `"a"=2`},
		{"the last depth read", nameTreePDF("<</Names[(a)[3 0 R/Fit]]/Kids[6 0 R]>>", "<</Names[(b)[4 0 R/Fit]]>>"), false, maxNameTreeDepth - 1, []string{"a", "b"}, `"a"=1 "b"=0`},
		{"past the last depth", nameTreePDF("<</Names[(a)[3 0 R/Fit]]>>"), false, maxNameTreeDepth, []string{"a"}, `"a"=0`},
		{"a destination in a dictionary", nameTreePDF("<</Names[(a) 6 0 R]>>", "<</D[4 0 R/XYZ 0 0 0]>>"), false, 0, []string{"a"}, `"a"=2`},
		{"AES padding in an AES file", padded, true, 0, []string{"a"}, `"a"=2`},
		{"AES padding in another file", padded, false, 0, []string{"a", "a" + strings.Repeat("\x0f", 15)}, `"a"=0 "a\x0f\x0f\x0f\x0f\x0f\x0f\x0f\x0f\x0f\x0f\x0f\x0f\x0f\x0f\x0f"=2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := nameTreeRead(t, context.Background(), tc.data, tc.aes, tc.depth, tc.asked...); got != tc.want {
				t.Errorf("pages = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestReadNameTree_EachObjectOnce reads trees that refer to one object many
// times. One whose every node lists the same child twice, 31 levels deep, has
// two billion paths through it and 32 objects, and must read in a moment; a
// /Kids or a /Names array two nodes share is parsed once.
func TestReadNameTree_EachObjectOnce(t *testing.T) {
	t.Run("a node listed twice at every level", func(t *testing.T) {
		objs := make([]string, maxNameTreeDepth)
		for i := range objs[:len(objs)-1] {
			objs[i] = fmt.Sprintf("<</Kids[%d 0 R %[1]d 0 R]>>", 6+i)
		}
		objs[len(objs)-1] = "<</Names[(a)[4 0 R/Fit]]>>"
		var got string
		mustReturnWithin(t, 10*time.Second, "readNameTree", func() {
			got, _ = nameTreeRead(t, context.Background(), nameTreePDF(objs...), false, 0, "a")
		})
		if got != `"a"=2` {
			t.Errorf("pages = %s, want \"a\"=2", got)
		}
	})
	t.Run("a /Kids array two nodes share", func(t *testing.T) {
		data := nameTreePDF("<</Kids[6 0 R 7 0 R]>>", "<</Kids 8 0 R>>", "<</Kids 8 0 R>>", "[9 0 R]", "<</Names[(a)[4 0 R/Fit]]>>")
		file := &readsAt{ReaderAt: bytes.NewReader(data), off: int64(bytes.Index(data, []byte("\n8 0 obj\n")) + 1)}
		r, err := pdf.NewReader(file, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		root := r.Trailer().Key("Root")
		w := &outlineWalk{root: node{v: root}, strs: readerStrings(false), named: map[string][]int{"a": {0}}, entries: make([]OutlineEntry, 1)}
		w.readNameTree(context.Background(), node{v: root.Key("T")}, 0, map[objRef]bool{})
		if w.entries[0].Page != 2 || file.count != 1 {
			t.Errorf("page %d, /Kids read %d times, want page 2 read once", w.entries[0].Page, file.count)
		}
	})
	t.Run("a /Names array two nodes share", func(t *testing.T) {
		data := nameTreePDF("<</Kids[6 0 R 7 0 R]>>", "<</Names 8 0 R>>", "<</Names 8 0 R>>", "[(a)[4 0 R/Fit]]")
		file := &readsAt{ReaderAt: bytes.NewReader(data), off: int64(bytes.Index(data, []byte("\n8 0 obj\n")) + 1)}
		r, err := pdf.NewReader(file, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		root := r.Trailer().Key("Root")
		w := &outlineWalk{root: node{v: root}, strs: readerStrings(false), named: map[string][]int{"a": {0}}, entries: make([]OutlineEntry, 1)}
		w.readNameTree(context.Background(), node{v: root.Key("T")}, 0, map[objRef]bool{})
		if w.entries[0].Page != 2 || file.count != 1 {
			t.Errorf("page %d, /Names read %d times, want page 2 read once", w.entries[0].Page, file.count)
		}
	})
}

// TestReadNameTree_ContextEnds ends the context during the read: it stops,
// keeps the error for the walk to return, and gives no page it had not read.
func TestReadNameTree_ContextEnds(t *testing.T) {
	data := nameTreePDF("<</Kids[6 0 R]>>", "<</Names[(a)[4 0 R/Fit]]>>")
	for _, pass := range []int{0, 1} {
		t.Run(fmt.Sprintf("after %d checks", pass), func(t *testing.T) {
			got, w := nameTreeRead(t, passErr(pass), data, false, 0, "a")
			if got != `"a"=0` || !errors.Is(w.items.err, context.Canceled) {
				t.Errorf("pages = %s, err = %v, want \"a\"=0 and context.Canceled", got, w.items.err)
			}
		})
	}
}

// TestOutline_PDFMoreNamedDestinationsThanABudget reads an outline whose
// items name destinations at the far end of a name tree of 60,000 names, more
// than the 50,000 the read used to stop at. The Intel 64 and IA-32 manual
// holds 292,930, and 3,275 of its 4,106 entries came back on page 0 with
// nothing saying the outline was incomplete. Every entry finds its page.
func TestOutline_PDFMoreNamedDestinationsThanABudget(t *testing.T) {
	const leaves, perLeaf = 600, 100
	last := leaves*perLeaf - 1
	// Objects 9 to 11 are the items, 12 the tree's root and 13 onward its
	// leaves. Name k leads to page k%3+1, pages 1 to 3 being objects 3 to 5.
	objs := []string{
		"<</Title(First)/Dest(d000000)/Next 10 0 R>>",
		fmt.Sprintf("<</Title(Last but one)/Dest(d%06d)/Next 11 0 R>>", last-1),
		fmt.Sprintf("<</Title(Last)/Dest(d%06d)>>", last),
		"",
	}
	kids := make([]string, leaves)
	for l := range leaves {
		kids[l] = fmt.Sprintf("%d 0 R", 13+l)
		var leaf strings.Builder
		for k := l * perLeaf; k < (l+1)*perLeaf; k++ {
			fmt.Fprintf(&leaf, "(d%06d)[%d 0 R/Fit]", k, 3+k%3)
		}
		objs = append(objs, fmt.Sprintf("<</Limits[(d%06d)(d%06d)]/Names[%s]>>", l*perLeaf, (l+1)*perLeaf-1, leaf.String()))
	}
	objs[3] = "<</Kids[" + strings.Join(kids, " ") + "]>>"
	res := outlineOf(t, outlinePDF("/Names<</Dests 12 0 R>>", objs...))
	if got, want := entryLines(res.Entries), "0 1 First\n0 2 Last but one\n0 3 Last\n"; got != want || res.Reason != "" {
		t.Errorf("outline:\n%s(%q)\nwant:\n%s", got, res.Reason, want)
	}
}

// pageIndexLines writes a page index as "id gen=page", sorted.
func pageIndexLines(byRef map[string]int) string {
	var lines []string
	for ref, n := range byRef {
		lines = append(lines, fmt.Sprintf("%s=%d", ref, n))
	}
	slices.Sort(lines)
	return strings.Join(lines, " ")
}

// TestPageIndex covers the page numbering destinations are matched against:
// pages numbered in tree order across nested nodes, a /Kids array holding a
// page written in place (counted, and its node left unindexed because its
// references cannot be matched by position), the kid budget, and the last
// depth read and the first not read.
func TestPageIndex(t *testing.T) {
	page := "<</Type/Page>>"
	nested := buildPDF([]string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R 6 0 R]/Count 3>>",
		"<</Type/Pages/Kids[4 0 R 5 0 R]/Count 2>>",
		page, page, page,
	})
	flat := buildPDF([]string{"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[3 0 R 4 0 R]/Count 2>>", page, page})
	inPlace := buildPDF([]string{"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[<</Type/Page>> 3 0 R]/Count 2>>", page})
	for _, tc := range []struct {
		name  string
		data  []byte
		depth int
		kids  int
		want  string
	}{
		{"nested nodes", nested, 0, maxPageTreeKids, "4 0=1 5 0=2 6 0=3"},
		{"a page written in place", inPlace, 0, maxPageTreeKids, ""},
		{"a budget of one kid", flat, 0, 1, "3 0=1"},
		{"the last depth read", nested, maxPageTreeDepth - 1, maxPageTreeKids, "6 0=1"},
		{"past the last depth", nested, maxPageTreeDepth, maxPageTreeKids, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := pageIndex{byRef: map[string]int{}, kids: tc.kids}
			idx.add(readerFor(t, tc.data).Trailer().Key("Root").Key("Pages"), tc.depth)
			if got := pageIndexLines(idx.byRef); got != tc.want {
				t.Errorf("pages = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOpenPDFReason pins how a refusal to open becomes a diagnosis: the
// password sentinel, wrapped or not, each way the reader begins a refusal of an
// encryption, a file that is not a PDF at all, and a PDF that broke, including
// one whose error quotes file bytes that say "encryption" somewhere past its
// start, which is damage and not encryption.
func TestOpenPDFReason(t *testing.T) {
	damaged := func(detail string) string {
		return "cannot read PDF: the file is damaged (" + detail + "), and this reader does not repair one, " +
			"so neither its text nor its table of contents can be read; another copy of the file may be intact"
	}
	quoted := "malformed PDF: cross-reference table not found: {11 0 obj}<</Subject(A primer on public-key encryption)>>"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"password", pdf.ErrInvalidPassword, lockedPDFReason},
		{"password, wrapped", fmt.Errorf("opening: %w", pdf.ErrInvalidPassword), lockedPDFReason},
		{"AES-256", errors.New("malformed PDF: 256-bit encryption key"), encryptedPDFReason},
		{"RC4 V=4", errors.New("unsupported PDF: encryption version V=4; <<...>>"), encryptedPDFReason},
		{"a certificate handler", errors.New("unsupported PDF: encryption filter /Adobe.PubSec"), encryptedPDFReason},
		{"revision 6", errors.New("unsupported PDF: encryption revision R=6"), encryptedPDFReason},
		{"revision 1", errors.New("malformed PDF: encryption revision R=1"), encryptedPDFReason},
		{"no O or U", errors.New("malformed PDF: missing O= or U= encryption parameters"), encryptedPDFReason},
		{"the word in quoted bytes", errors.New(quoted), damaged(pdfErrorDetail(errors.New(quoted)))},
		{"a key length followed by more", errors.New("malformed PDF: 40-bit encryption key (0 0 obj)"), damaged("malformed PDF: 40-bit encryption key (0 0 obj)")},
		{"no O or U, followed by more", errors.New("malformed PDF: missing O= or U= encryption parameters here"), damaged("malformed PDF: missing O= or U= encryption parameters here")},
		{"not a PDF", errors.New("not a PDF file: invalid header"), "not a valid PDF: not a PDF file: invalid header"},
		{"truncated", errors.New("not a PDF file: missing %%EOF"), damaged("not a PDF file: missing %%EOF")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := openPDFReason(tc.err); got != tc.want {
				t.Errorf("openPDFReason(%v) =\n %q\nwant\n %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestPDFErrorDetail covers the quoted error's bounds: a message of exactly the
// limit is kept whole and one rune more is cut and marked, and control
// characters and bytes that are not UTF-8 collapse into single spaces.
func TestPDFErrorDetail(t *testing.T) {
	limit := strings.Repeat("x", maxPDFErrorDetail)
	for _, tc := range []struct {
		name, in, want string
	}{
		{"at the limit", limit, limit},
		{"one past the limit", limit + "y", limit + "..."},
		{"controls", "a\n\tb\x00c", "a b c"},
		{"not UTF-8", "bad \xff\xfe bytes", "bad bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pdfErrorDetail(errors.New(tc.in)); got != tc.want {
				t.Errorf("pdfErrorDetail(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// mustRead reads a fixture file.
func mustRead(tb testing.TB, path string) []byte {
	tb.Helper()
	data, err := os.ReadFile(path) //#nosec G304 -- a test fixture path.
	if err != nil {
		tb.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}

// outlineViolations lists what in entries no outline read may return: more
// entries than the budget, a level outside the depth bound, a negative page,
// and a title that is empty, padded, or holds a control character or
// invalid UTF-8.
func outlineViolations(entries []OutlineEntry) []string {
	var bad []string
	if len(entries) > maxOutlineItems {
		bad = append(bad, fmt.Sprintf("%d entries, past the budget of %d", len(entries), maxOutlineItems))
	}
	for _, e := range entries {
		clean := e.Title != "" && e.Title == strings.TrimSpace(e.Title) &&
			!strings.ContainsFunc(e.Title, unicode.IsControl) && strings.ToValidUTF8(e.Title, "") == e.Title
		if !clean || e.Level < 0 || e.Level >= maxOutlineDepth || e.Page < 0 {
			bad = append(bad, fmt.Sprintf("%+v", e))
		}
	}
	return bad
}

// FuzzPDFOutline feeds the outline walk arbitrary bytes, seeded with every
// PDF fixture the outline tests read and the built ones. The walk runs over a
// structure the file controls with a reader that panics on malformed input,
// so what it must hold to is what no input can break: it returns, it turns a
// panic into a verdict, it ends only with a context error, and what it returns
// stays inside the bounds and the title rules.
func FuzzPDFOutline(f *testing.F) {
	for _, path := range []string{
		outlineTargetsPDF, sectionsPDF, "testdata/bookmarked.pdf",
		"testdata/encrypted-rc4.pdf", "testdata/encrypted-aes128.pdf",
		"testdata/outline-titles.pdf", "testdata/encrypted-aes128-titles.pdf",
		"testdata/sections-pdf20.pdf", "testdata/encrypted-rc4-40-titles.pdf",
		"testdata/encrypted-aes128-cf-bits-titles.pdf", "testdata/encrypted-rc4-40-objstm.pdf",
		"testdata/encrypted-aes128-objstm-padding.pdf", "testdata/encrypted-aes128-clear-metadata-objstm-titles.pdf",
	} {
		f.Add(mustRead(f, path))
	}
	f.Add(destinationFormsPDF())
	f.Add(outlinePDF("", `<</Title(Results \& discussion)/Next 10 0 R>>`, `<</Title<414>/A<</S/GoToR/F(..\docs\a.pdf)/D[0/Fit]>>>>`))
	for _, tc := range outlineCycles() {
		f.Add(tc.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		d := document{name: "fuzz.pdf", r: bytes.NewReader(data), size: int64(len(data))}
		entries, _, err := pdfBookmarkEntries(context.Background(), d)
		if err != nil {
			t.Fatalf("pdfBookmarkEntries ended with %v on a live context", err)
		}
		if bad := outlineViolations(entries); len(bad) > 0 {
			t.Errorf("the walk returned entries no read may return: %v", bad)
		}
	})
}
