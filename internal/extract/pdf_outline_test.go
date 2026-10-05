package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	if _, err := pdfNoOutlineResult(passErr(0), docFor(t, "testdata/sample.pdf"), false); err == nil {
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
// object that is not a page, and a page given as a number, as a link into
// another file gives it. An item with a blank title is left out and its child
// is not.
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
		"<</Title(Destination held elsewhere)/Dest 22 0 R>>",
		"<</Kids[21 0 R]>>",
		"<</Limits[(chap3)(chap3)]/Names[(chap3) 23 0 R]>>",
		"[5 0 R/Fit]",
		"<</D[5 0 R/XYZ 0 0 0]>>",
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
		"0 3 Destination held elsewhere\n"
	if got := entryLines(res.Entries); got != want {
		t.Errorf("outline:\n%s\nwant:\n%s", got, want)
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

// TestOutline_PDFEncrypted reads the sections fixture encrypted four ways by
// qpdf 12.2.0 with an empty user password, the shape of a PDF that only
// restricts printing or copying. The reader decrypts RC4 (V=2) and AES-128
// (V=4 with AESV2), and those read exactly as the plain file does. It does not
// decrypt AES-256 (V=5) or RC4 under crypt filters (V=4 with V2), and those are
// reported as encrypted, in the same words by every mode, rather than as an
// invalid file or as one without a table of contents.
func TestOutline_PDFEncrypted(t *testing.T) {
	plain := entryLines(outlineOf(t, mustRead(t, sectionsPDF)).Entries)
	for _, tc := range []struct {
		name     string
		readable bool
	}{
		{"encrypted-rc4.pdf", true},
		{"encrypted-aes128.pdf", true},
		{"encrypted-aes256.pdf", false},
		{"encrypted-rc4-v4.pdf", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("testdata", tc.name)
			res := outlineOf(t, mustRead(t, path))
			if tc.readable {
				if got := entryLines(res.Entries); got != plain || !res.Extractable {
					t.Errorf("outline:\n%s\nwant the plain file's:\n%s", got, plain)
				}
				return
			}
			chunk, err := Extract(context.Background(), openFile(t, path), Req{})
			if err != nil {
				t.Fatal(err)
			}
			found, err := Search(context.Background(), openFile(t, path), "page", SearchOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Extractable || res.Reason != encryptedPDFReason {
				t.Errorf("want a not-extractable result with the encrypted reason, got %+v", res)
			}
			if chunk.Reason != res.Reason || found.Reason != res.Reason {
				t.Errorf("one cause must get one answer:\n outline: %q\n text:    %q\n find:    %q",
					res.Reason, chunk.Reason, found.Reason)
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

// TestOutlineWalk_Bounds drives the walk at the edges of its two bounds: the
// last level it reads and the first it does not, and an item budget smaller
// than the outline.
func TestOutlineWalk_Bounds(t *testing.T) {
	nested := outlinePDF("", "<</Title(Parent)/First 10 0 R>>", "<</Title(Child)>>")
	chain := outlinePDF("",
		"<</Title(One)/Next 10 0 R>>", "<</Title(Two)/Next 11 0 R>>", "<</Title(Three)>>")
	for _, tc := range []struct {
		name  string
		data  []byte
		level int
		items int
		want  string
	}{
		{"the last level read", nested, maxOutlineDepth - 1, maxOutlineItems, fmt.Sprintf("%d 0 Parent\n", maxOutlineDepth-1)},
		{"past the last level", nested, maxOutlineDepth, maxOutlineItems, ""},
		{"a budget of two items", chain, 0, 2, "0 0 One\n0 0 Two\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := readerFor(t, tc.data).Trailer().Key("Root")
			w := newOutlineWalk(root)
			w.items.left = tc.items
			w.walk(context.Background(), root.Key("Outlines").Key("First"), tc.level)
			if got := entryLines(w.entries); got != tc.want {
				t.Errorf("outline:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// nameTreeLines writes every name collectNameTree recorded as "name=value",
// sorted, with each value read back as an integer.
func nameTreeLines(into map[string]nameEntry) string {
	var lines []string
	for name, e := range into {
		lines = append(lines, fmt.Sprintf("%s=%d", name, e.names.Index(e.at).Int64()))
	}
	slices.Sort(lines)
	return strings.Join(lines, " ")
}

// TestCollectNameTree covers the name-tree read on trees built for each edge:
// a /Names array with a key and no value, a /Kids array that leads back to its
// own node, the last depth read and the first not read, and a visit budget
// spent on names and on kids. The node under test is the catalog's /T.
func TestCollectNameTree(t *testing.T) {
	tree := func(objs ...string) []byte {
		return buildPDF(append([]string{"<</Type/Catalog/T 2 0 R>>"}, objs...))
	}
	for _, tc := range []struct {
		name   string
		data   []byte
		depth  int
		visits int
		want   string
	}{
		{"a key with no value", tree("<</Names[(a) 1 (b) 2 (c)]>>"), 0, maxNameTreeVisits, "a=1 b=2"},
		{"kids lead back to the node", tree("<</Names[(a) 1]/Kids[2 0 R]>>"), 0, maxNameTreeVisits, "a=1"},
		{"a later leaf replaces an earlier one", tree("<</Kids[3 0 R 4 0 R]>>", "<</Names[(a) 1]>>", "<</Names[(a) 2]>>"), 0, maxNameTreeVisits, "a=2"},
		{"the last depth read", tree("<</Names[(a) 1]/Kids[3 0 R]>>", "<</Names[(b) 2]>>"), maxNameTreeDepth - 1, maxNameTreeVisits, "a=1"},
		{"past the last depth", tree("<</Names[(a) 1]>>"), maxNameTreeDepth, maxNameTreeVisits, ""},
		{"a budget of one name", tree("<</Names[(a) 1 (b) 2]>>"), 0, 1, "a=1"},
		{"a budget spent on a kid", tree("<</Names[(a) 1]/Kids[3 0 R 3 0 R]>>", "<</Names[(b) 2]>>"), 0, 2, "a=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := readerFor(t, tc.data).Trailer().Key("Root").Key("T")
			into := map[string]nameEntry{}
			visits := walkBudget{left: tc.visits}
			collectNameTree(context.Background(), node, tc.depth, &visits, into)
			if got := nameTreeLines(into); got != tc.want {
				t.Errorf("names = %q, want %q", got, tc.want)
			}
		})
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
// password sentinel, wrapped or not, an encryption the reader names, a file
// that is not a PDF at all, and a PDF that broke.
func TestOpenPDFReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"password", pdf.ErrInvalidPassword, lockedPDFReason},
		{"password, wrapped", fmt.Errorf("opening: %w", pdf.ErrInvalidPassword), lockedPDFReason},
		{"AES-256", errors.New("malformed PDF: 256-bit encryption key"), encryptedPDFReason},
		{"RC4 V=4", errors.New("unsupported PDF: encryption version V=4; <<...>>"), encryptedPDFReason},
		{"not a PDF", errors.New("not a PDF file: invalid header"), "not a valid PDF: not a PDF file: invalid header"},
		{
			"truncated", errors.New("not a PDF file: missing %%EOF"),
			"cannot read PDF: the file is damaged (not a PDF file: missing %%EOF), and this reader does not repair one, " +
				"so neither its text nor its table of contents can be read; another copy of the file may be intact",
		},
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
	} {
		f.Add(mustRead(f, path))
	}
	f.Add(destinationFormsPDF())
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
