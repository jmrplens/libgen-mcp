package extract

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// sectionsPDF is a generated eight-page PDF (testdata/sections.ps through
// Ghostscript) whose outline nests two levels and puts two entries on one page
// twice: the shapes a section's end has to be computed over.
const sectionsPDF = "testdata/sections.pdf"

// sectionsEPUB is a hand-written EPUB3 with both a nav document and an NCX. Its
// nav links into three content documents with and without fragments, through
// a percent-escaped name and a subdirectory, and once to the nav document
// itself, which is not in the reading order.
const sectionsEPUB = "testdata/sections.epub"

// wholeSection is a Req large enough that a whole fixture section fits in one
// chunk.
var wholeSection = Req{MaxPages: 100, MaxChars: 1 << 20}

// TestOutline_NumbersEveryEntry verifies Outline numbers its entries 1..n in
// document order, which is the number Section takes.
func TestOutline_NumbersEveryEntry(t *testing.T) {
	res, err := Outline(context.Background(), openFile(t, sectionsPDF))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 10 {
		t.Fatalf("want 10 entries, got %d: %+v", len(res.Entries), res.Entries)
	}
	for i, e := range res.Entries {
		if e.Index != i+1 {
			t.Errorf("entry %d %q: Index = %d, want %d", i, e.Title, e.Index, i+1)
		}
	}
}

// TestSection_PDFRanges verifies each PDF section runs from its entry's page to
// the page the next entry at the same or a higher level starts on, inclusive:
// a chapter spans its subsections, a subsection stops at its sibling, two
// entries on one page give a one-page section, and the last entry runs to the
// end of the document.
func TestSection_PDFRanges(t *testing.T) {
	testCases := []struct {
		name       string
		index      int
		start, end int
		first      string
		last       string
	}{
		{name: "top level stops at the next top level", index: 1, start: 1, end: 2, first: "Page one", last: "Page two"},
		{name: "chapter spans its subsections", index: 2, start: 2, end: 5, first: "Page two", last: "Page five"},
		{name: "subsection stops at its sibling", index: 3, start: 3, end: 4, first: "Page three", last: "Page four"},
		{name: "last subsection stops at the next chapter", index: 4, start: 4, end: 5, first: "Page four", last: "Page five"},
		{name: "subsection on its chapter's page", index: 6, start: 5, end: 6, first: "Page five", last: "Page six"},
		{name: "two entries on one page", index: 7, start: 6, end: 6, first: "Page six", last: "Page six"},
		{name: "last entry runs to the end", index: 10, start: 8, end: 8, first: "Page eight", last: "Page eight"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := Section(context.Background(), openFile(t, sectionsPDF), SectionRef{Index: tc.index}, wholeSection)
			if err != nil {
				t.Fatal(err)
			}
			if !sc.Extractable || sc.HasMore {
				t.Fatalf("want one whole extractable chunk, got extractable=%v has_more=%v reason=%q", sc.Extractable, sc.HasMore, sc.Reason)
			}
			if sc.Span.Index != tc.index || sc.Span.PageStart != tc.start || sc.Span.PageEnd != tc.end {
				t.Errorf("span = %+v, want index %d pages %d-%d", sc.Span, tc.index, tc.start, tc.end)
			}
			if sc.PageStart != tc.start || sc.PageEnd != tc.end || sc.TotalPages != 8 {
				t.Errorf("chunk pages %d-%d of %d, want %d-%d of 8", sc.PageStart, sc.PageEnd, sc.TotalPages, tc.start, tc.end)
			}
			if !strings.HasPrefix(strings.TrimSpace(sc.Text), tc.first) || !strings.Contains(sc.Text, tc.last) {
				t.Errorf("text = %q, want it to open with %q and reach %q", sc.Text, tc.first, tc.last)
			}
		})
	}
}

// TestSection_PDFContinuesToTheSectionEnd verifies a section longer than one
// chunk pages on through StartPage and stops at the section's last page with
// HasMore false, although the document goes on.
func TestSection_PDFContinuesToTheSectionEnd(t *testing.T) {
	f := openFile(t, sectionsPDF)
	req := Req{MaxPages: 1, MaxChars: 1 << 20}
	var pages []int
	// sequential: each read resumes where the previous one stopped.
	for range 5 {
		sc, err := Section(context.Background(), f, SectionRef{Index: 5}, req)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, sc.PageStart)
		if !sc.HasMore {
			break
		}
		req.StartPage = sc.NextCursor.Page
	}
	if want := []int{5, 6, 7}; len(pages) != len(want) || pages[0] != 5 || pages[1] != 6 || pages[2] != 7 {
		t.Errorf("pages read = %v, want %v", pages, want)
	}
}

// TestSection_PDFMaxCharsStopsInsideTheSection verifies max_chars cuts a section
// chunk the way it cuts a sequential one: truncated, with more of the section
// left.
func TestSection_PDFMaxCharsStopsInsideTheSection(t *testing.T) {
	sc, err := Section(context.Background(), openFile(t, sectionsPDF), SectionRef{Index: 2}, Req{MaxPages: 10, MaxChars: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !sc.HasMore || !sc.Truncated || sc.PageEnd != 2 || sc.NextCursor.Page != 3 {
		t.Errorf("got pages %d-%d has_more=%v truncated=%v next=%d, want page 2 then 3 with more", sc.PageStart, sc.PageEnd, sc.HasMore, sc.Truncated, sc.NextCursor.Page)
	}
}

// TestSection_PDFResumeOutsideTheSection verifies a resume page before the
// section or after its end is refused rather than read.
func TestSection_PDFResumeOutsideTheSection(t *testing.T) {
	for _, page := range []int{1, 6} {
		t.Run(strconv.Itoa(page), func(t *testing.T) {
			_, err := Section(context.Background(), openFile(t, sectionsPDF), SectionRef{Index: 3}, Req{StartPage: page})
			var se *SectionError
			if !errors.As(err, &se) || !strings.Contains(se.Error(), "outside section 3") {
				t.Errorf("err = %v, want a SectionError naming section 3", err)
			}
		})
	}
}

// TestSection_PDFResumeAtTheSectionEdges is the other side of the refusal: a
// resume page on the section's first page or on its last is inside it, and so
// is any page of the last section, which runs to the end of the document.
func TestSection_PDFResumeAtTheSectionEdges(t *testing.T) {
	for _, tc := range []struct {
		name         string
		index, start int
		wantEnd      int
	}{
		{name: "the first page", index: 3, start: 3, wantEnd: 4},
		{name: "the last page", index: 3, start: 4, wantEnd: 4},
		{name: "a page of the last section", index: 10, start: 8, wantEnd: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := Section(context.Background(), openFile(t, sectionsPDF), SectionRef{Index: tc.index}, Req{StartPage: tc.start, MaxPages: 10})
			if err != nil {
				t.Fatal(err)
			}
			if !sc.Extractable || sc.PageStart != tc.start || sc.PageEnd != tc.wantEnd {
				t.Errorf("got pages %d-%d extractable=%v reason=%q, want %d-%d", sc.PageStart, sc.PageEnd, sc.Extractable, sc.Reason, tc.start, tc.wantEnd)
			}
		})
	}
}

// TestPDFSection_ReadsThatAreNotTheEntrysFault verifies that a section the
// reader could not give text for is answered with the reader's own diagnosis,
// never with the error that blames the entry for pointing past the last page:
// a page with no text layer on the document's last page, and a file the reader
// will not open, which reports no page count at all. The span keeps the
// outline's boundary when the read learned no page count to clamp it to.
func TestPDFSection_ReadsThatAreNotTheEntrysFault(t *testing.T) {
	entries := []OutlineEntry{{Index: 1, Title: "One", Page: 1}, {Index: 2, Title: "Two", Page: 4}}
	for _, tc := range []struct {
		name       string
		doc        document
		wantReason string
		wantEnd    int
	}{
		{name: "a page with no text layer", doc: docFor(t, writeBytes(t, t.TempDir(), "cover.pdf", graphicsOnlyPDF())), wantReason: noTextLayerReason, wantEnd: 1},
		{name: "a file the reader will not open", doc: docFor(t, "testdata/encrypted-aes256.pdf"), wantReason: encryptedPDFReason, wantEnd: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := pdfSection(context.Background(), tc.doc, entries, 0, Req{})
			if err != nil {
				t.Fatalf("err = %v, want the reader's diagnosis and no error", err)
			}
			if sc.Extractable || sc.Reason != tc.wantReason {
				t.Errorf("extractable=%v reason=%q, want %q", sc.Extractable, sc.Reason, tc.wantReason)
			}
			if sc.Span.PageStart != 1 || sc.Span.PageEnd != tc.wantEnd {
				t.Errorf("span pages %d-%d, want 1-%d", sc.Span.PageStart, sc.Span.PageEnd, tc.wantEnd)
			}
		})
	}
}

// TestPDFSection_ABoundaryPastTheLastPage verifies a section whose next entry
// points past the end of the document is read to the last page, and its span
// ends there rather than at a page the document does not have.
func TestPDFSection_ABoundaryPastTheLastPage(t *testing.T) {
	entries := []OutlineEntry{{Index: 1, Title: "Seven", Page: 7}, {Index: 2, Title: "Phantom", Page: 99}}
	sc, err := pdfSection(context.Background(), docFor(t, sectionsPDF), entries, 0, wholeSection)
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Extractable || sc.Span.PageEnd != 8 || sc.PageEnd != 8 {
		t.Errorf("span ends on %d, chunk on %d, extractable=%v, want both on page 8", sc.Span.PageEnd, sc.PageEnd, sc.Extractable)
	}
}

// TestSection_PDFEntryWithoutAPage verifies an entry whose bookmark resolved to
// no page is refused by name instead of read from page one.
func TestSection_PDFEntryWithoutAPage(t *testing.T) {
	entries := []OutlineEntry{{Index: 1, Title: "Lost", Page: 0}}
	_, err := pdfSection(context.Background(), docFor(t, sectionsPDF), entries, 0, Req{})
	var se *SectionError
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "points to no page") {
		t.Errorf("err = %v, want a SectionError saying the entry points to no page", err)
	}
}

// TestPDFSectionEnd verifies the boundary rules: an entry with no page is passed
// over, a deeper entry does not end the section, and a boundary pointing back
// before the entry is clamped to the entry's own page.
func TestPDFSectionEnd(t *testing.T) {
	testCases := []struct {
		name    string
		entries []OutlineEntry
		want    int
	}{
		{name: "deeper entries do not bound", entries: []OutlineEntry{{Page: 2}, {Level: 1, Page: 3}, {Page: 6}}, want: 6},
		{name: "a pageless boundary is skipped", entries: []OutlineEntry{{Page: 2}, {Page: 0}, {Page: 4}}, want: 4},
		{name: "a backward boundary is clamped", entries: []OutlineEntry{{Page: 5}, {Page: 3}}, want: 5},
		{name: "nothing follows", entries: []OutlineEntry{{Page: 5}, {Level: 1, Page: 6}}, want: 0},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pdfSectionEnd(tc.entries, 0); got != tc.want {
				t.Errorf("pdfSectionEnd = %d, want %d", got, tc.want)
			}
		})
	}
}

// epubFullText is the whole reading-order text of path as Extract returns it,
// which every EPUB section's span indexes into.
func epubFullText(t *testing.T, path string) []rune {
	t.Helper()
	c, err := Extract(context.Background(), openFile(t, path), Req{MaxChars: 1 << 20})
	if err != nil || !c.Extractable {
		t.Fatalf("extract %s: %v %q", path, err, c.Reason)
	}
	return []rune(c.Text)
}

// assertWholeEPUBSection checks a section read in one chunk: extractable, with
// nothing more, its chunk range equal to its span, and its text exactly the
// slice of the whole reading-order text the span names.
func assertWholeEPUBSection(t *testing.T, sc SectionChunk, full []rune) {
	t.Helper()
	if !sc.Extractable || sc.HasMore || sc.Format != "epub" {
		t.Fatalf("want one whole epub chunk, got %+v", sc.Chunk)
	}
	span := sc.Span
	if span.CharStart != sc.CharStart || span.CharEnd != sc.CharEnd {
		t.Errorf("chunk chars %d-%d, want the span's %d-%d", sc.CharStart, sc.CharEnd, span.CharStart, span.CharEnd)
	}
	if got, want := sc.Text, string(full[span.CharStart:span.CharEnd]); got != want {
		t.Errorf("text = %q, want the slice of the whole text %q", got, want)
	}
}

// TestSection_EPUBRanges verifies each EPUB section is the run of the
// reading-order text from its entry's anchor to the next entry at the same or a
// higher level: a chapter in the middle of a file stops at the next chapter's
// heading, a part spans its chapters, a link with no fragment starts at its
// file, and the last entry runs to the end. Each section's text must be exactly
// the slice of Extract's text its span names.
func TestSection_EPUBRanges(t *testing.T) {
	full := epubFullText(t, sectionsEPUB)
	testCases := []struct {
		name   string
		index  int
		opens  string
		holds  string
		lacks  string
		toLast bool
	}{
		{name: "a file with no fragment", index: 2, opens: "Front Matter", holds: "before every part", lacks: "Part One"},
		{name: "a part spans its chapters", index: 3, opens: "Part One", holds: "Middles belong", lacks: "Part Two"},
		{name: "a chapter stops at the next chapter", index: 4, opens: "Chapter 1 Beginnings", holds: "café included", lacks: "Middles"},
		{name: "a last chapter stops at the next part", index: 5, opens: "Chapter 2 Middles", holds: "second chapter", lacks: "Part Two"},
		{name: "a part linked without a fragment", index: 6, opens: "Part Two", holds: "restates everything", lacks: "Afterword"},
		{name: "the last entry runs to the end", index: 9, opens: "Afterword", holds: "final word", toLast: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := Section(context.Background(), openFile(t, sectionsEPUB), SectionRef{Index: tc.index}, wholeSection)
			if err != nil {
				t.Fatal(err)
			}
			assertWholeEPUBSection(t, sc, full)
			span := sc.Span
			text := strings.TrimSpace(sc.Text)
			if !strings.HasPrefix(text, tc.opens) || !strings.Contains(text, tc.holds) {
				t.Errorf("text = %q, want it to open with %q and hold %q", text, tc.opens, tc.holds)
			}
			if tc.lacks != "" && strings.Contains(text, tc.lacks) {
				t.Errorf("text = %q, must not reach %q", text, tc.lacks)
			}
			if tc.toLast && span.CharEnd != len(full) {
				t.Errorf("span ends at %d, want the end of the text, %d", span.CharEnd, len(full))
			}
		})
	}
}

// TestSection_EPUBLinkOutsideTheReadingOrder verifies an entry linking to a
// document the spine does not list (here the nav document itself) is refused,
// since no part of the text is the entry's.
func TestSection_EPUBLinkOutsideTheReadingOrder(t *testing.T) {
	_, err := Section(context.Background(), openFile(t, sectionsEPUB), SectionRef{Index: 1}, wholeSection)
	var se *SectionError
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "links to nothing in the reading order") {
		t.Errorf("err = %v, want a SectionError about the reading order", err)
	}
}

// TestSection_EPUBContinuesToTheSectionEnd verifies a section read in small
// chunks through Offset reassembles to exactly the section, and the last chunk
// reports no more although the book continues.
func TestSection_EPUBContinuesToTheSectionEnd(t *testing.T) {
	f := openFile(t, sectionsEPUB)
	whole, err := Section(context.Background(), f, SectionRef{Index: 3}, wholeSection)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	req := Req{MaxChars: 40}
	// sequential: each read resumes where the previous one stopped.
	for range 50 {
		sc, rerr := Section(context.Background(), f, SectionRef{Index: 3}, req)
		if rerr != nil {
			t.Fatal(rerr)
		}
		b.WriteString(sc.Text)
		if !sc.HasMore {
			if sc.CharEnd != whole.Span.CharEnd {
				t.Errorf("last chunk ends at %d, want the section end %d", sc.CharEnd, whole.Span.CharEnd)
			}
			break
		}
		req.Offset = sc.NextCursor.Char
	}
	if b.String() != whole.Text {
		t.Errorf("chunks reassemble to %q, want %q", b.String(), whole.Text)
	}
}

// TestSection_EPUBOffsetOutsideTheSection verifies a resume offset before the
// section or at its end is refused rather than read.
func TestSection_EPUBOffsetOutsideTheSection(t *testing.T) {
	f := openFile(t, sectionsEPUB)
	whole, err := Section(context.Background(), f, SectionRef{Index: 4}, wholeSection)
	if err != nil {
		t.Fatal(err)
	}
	for name, offset := range map[string]int{"before": whole.Span.CharStart - 1, "at the end": whole.Span.CharEnd} {
		t.Run(name, func(t *testing.T) {
			_, rerr := Section(context.Background(), f, SectionRef{Index: 4}, Req{Offset: offset})
			var se *SectionError
			if !errors.As(rerr, &se) || !strings.Contains(rerr.Error(), "outside section 4") {
				t.Errorf("err = %v, want a SectionError naming section 4", rerr)
			}
		})
	}
}

// TestSection_EPUBOffsetAtTheSectionStart is the other side of the refusal: a
// resume offset on the section's first character is inside it, and reads from
// there.
func TestSection_EPUBOffsetAtTheSectionStart(t *testing.T) {
	f := openFile(t, sectionsEPUB)
	whole, err := Section(context.Background(), f, SectionRef{Index: 4}, wholeSection)
	if err != nil {
		t.Fatal(err)
	}
	if whole.Span.CharStart == 0 {
		t.Fatal("section 4 starts the text, so an offset on its start is no resume at all")
	}
	sc, err := Section(context.Background(), f, SectionRef{Index: 4}, Req{Offset: whole.Span.CharStart, MaxChars: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if sc.CharStart != whole.Span.CharStart || sc.Text != whole.Text {
		t.Errorf("resumed at %d with %q, want the whole section from %d", sc.CharStart, sc.Text, whole.Span.CharStart)
	}
}

// TestSection_EPUB2NCXFragments verifies an EPUB with no nav document resolves
// its NCX content links, fragments included, the same way.
func TestSection_EPUB2NCXFragments(t *testing.T) {
	ncx := `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><navMap>
<navPoint id="a"><navLabel><text>First</text></navLabel><content src="chapter1.xhtml#first"/></navPoint>
<navPoint id="b"><navLabel><text>Second</text></navLabel><content src="chapter1.xhtml#second"/></navPoint>
</navMap></ncx>`
	files := epub2Files(ncxOPF, ncx)
	files["OEBPS/chapter1.xhtml"] = `<html><head><meta name="first" content="x"/></head><body>` +
		`<h1 id="first">First</h1><p>alpha</p><h1><a name="second"></a>Second</h1><p>beta</p></body></html>`
	path := writeEPUB(t, t.TempDir(), "ncx-sections.epub", files)

	sc, err := Section(context.Background(), openFile(t, path), SectionRef{Title: "first"}, wholeSection)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(sc.Text); got != "Firstalpha" {
		t.Errorf("first section = %q, want %q", got, "Firstalpha")
	}
	sc, err = Section(context.Background(), openFile(t, path), SectionRef{Index: 2}, wholeSection)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(sc.Text); got != "Secondbeta" {
		t.Errorf("second section = %q, want %q", got, "Secondbeta")
	}
}

// TestSection_NoOutline verifies a readable document with no table of contents
// yields ErrNoOutline, for a PDF without bookmarks and for plain text.
func TestSection_NoOutline(t *testing.T) {
	for _, path := range []string{"testdata/sample.pdf", "testdata/sample.txt"} {
		t.Run(path, func(t *testing.T) {
			_, err := Section(context.Background(), openFile(t, path), SectionRef{Index: 1}, Req{})
			if !errors.Is(err, ErrNoOutline) {
				t.Errorf("err = %v, want ErrNoOutline", err)
			}
		})
	}
}

// TestSection_DamagedOutline verifies a readable PDF whose outline is damaged
// yields ErrOutlineUnreadable, which outline mode's diagnosis agrees with,
// and not ErrNoOutline, which would say the document has none.
func TestSection_DamagedOutline(t *testing.T) {
	data := withObjectAt(t, outlinePDF("", "<</Title(A)/Dest[3 0 R/Fit]>>"), 9, 8)
	path := writeBytes(t, t.TempDir(), "damaged.pdf", data)
	_, err := Section(context.Background(), openFile(t, path), SectionRef{Index: 1}, Req{})
	if !errors.Is(err, ErrOutlineUnreadable) || errors.Is(err, ErrNoOutline) {
		t.Errorf("err = %v, want ErrOutlineUnreadable", err)
	}
}

// TestSection_UnreadableFile verifies a file nothing can be read from comes
// back as a not-extractable chunk with the reason the other modes give, not as
// an error.
func TestSection_UnreadableFile(t *testing.T) {
	sc, err := Section(context.Background(), openFile(t, "testdata/scanned.pdf"), SectionRef{Index: 1}, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if sc.Extractable || sc.Reason != noTextLayerReason {
		t.Errorf("got extractable=%v reason=%q, want the no-text-layer reason", sc.Extractable, sc.Reason)
	}
}

// TestSection_ContextCanceled verifies a canceled context is returned as the
// error.
func TestSection_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Section(ctx, openFile(t, sectionsEPUB), SectionRef{Index: 2}, Req{}); err == nil {
		t.Fatal("want a context error, got nil")
	}
}

// TestSection_CanceledInsideTheRead verifies a context canceled once the read
// is under way is returned from each stage that checks it: the outline, the
// PDF page scan and the EPUB spine walk.
func TestSection_CanceledInsideTheRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sectionChecked(ctx, docFor(t, sectionsPDF), SectionRef{Index: 1}, Req{}); err == nil {
		t.Error("outline stage: want a context error, got nil")
	}
	entries := []OutlineEntry{{Index: 1, Title: "Preface", Page: 1}}
	if _, err := pdfSection(ctx, docFor(t, sectionsPDF), entries, 0, Req{}); err == nil {
		t.Error("page stage: want a context error, got nil")
	}
}

// TestSection_EPUBOverTheCap verifies a section of an EPUB whose chapter was
// clipped at the extraction cap says so, as a sequential read does.
func TestSection_EPUBOverTheCap(t *testing.T) {
	ncx := `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><navMap>
<navPoint id="a"><navLabel><text>Long</text></navLabel><content src="chapter1.xhtml"/></navPoint>
</navMap></ncx>`
	files := epub2Files(ncxOPF, ncx)
	files["OEBPS/chapter1.xhtml"] = "<html><body><p>" + strings.Repeat("a", maxTextFileBytes+1) + "</p></body></html>"
	path := writeEPUB(t, t.TempDir(), "oversized-section.epub", files)
	sc, err := Section(context.Background(), openFile(t, path), SectionRef{Index: 1}, Req{MaxChars: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Truncated || !strings.Contains(sc.Reason, "8 MiB extraction cap") {
		t.Errorf("got truncated=%v reason=%q, want the cap noted", sc.Truncated, sc.Reason)
	}
}

// TestSection_ClosedFile verifies a file that cannot be stat'ed is an error.
func TestSection_ClosedFile(t *testing.T) {
	f := openFile(t, sectionsPDF)
	_ = f.Close()
	if _, err := Section(context.Background(), f, SectionRef{Index: 1}, Req{}); err == nil {
		t.Fatal("want an error for a closed file, got nil")
	}
}

// TestResolveSection verifies how a reference picks an entry: a number in
// range, a title matched exactly ignoring case and spacing, a title matched by
// containment when nothing is exact, and the refusals for everything else.
func TestResolveSection(t *testing.T) {
	entries := []OutlineEntry{
		{Index: 1, Title: "Chapter 1 Foundations", Page: 2},
		{Index: 2, Title: "Summary", Level: 1, Page: 4},
		{Index: 3, Title: "Chapter 2  Methods", Page: 5},
		{Index: 4, Title: "Summary", Level: 1},
		{Index: 5, Title: "Results summary"},
	}
	testCases := []struct {
		name    string
		ref     SectionRef
		want    int
		errHint string
	}{
		{name: "number", ref: SectionRef{Index: 3}, want: 2},
		{name: "number past the end", ref: SectionRef{Index: 6}, errHint: "outline has 5 entries"},
		{name: "exact title ignoring case and spacing", ref: SectionRef{Title: " chapter 2 methods "}, want: 2},
		{name: "contained title", ref: SectionRef{Title: "foundations"}, want: 0},
		{name: "exact title wins over containment", ref: SectionRef{Title: "Results summary"}, want: 4},
		{name: "ambiguous exact title", ref: SectionRef{Title: "summary"}, errHint: `mean: 2 "Summary" (p.4), 4 "Summary".`},
		{name: "ambiguous contained title", ref: SectionRef{Title: "chapter"}, errHint: "matches 2 entries"},
		{name: "unknown title", ref: SectionRef{Title: "epilogue"}, errHint: "no outline entry matches"},
		{name: "empty title", ref: SectionRef{Title: "  "}, errHint: "section is empty"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSection(entries, tc.ref)
			if tc.errHint == "" {
				if err != nil || got != tc.want {
					t.Errorf("resolveSection = %d, %v, want %d", got, err, tc.want)
				}
				return
			}
			var se *SectionError
			if !errors.As(err, &se) || !strings.Contains(err.Error(), tc.errHint) {
				t.Errorf("err = %v, want a SectionError containing %q", err, tc.errHint)
			}
		})
	}
}

// TestResolveSection_CapsTheCandidates verifies an ambiguous title lists at
// most maxSectionCandidates entries and counts the rest.
func TestResolveSection_CapsTheCandidates(t *testing.T) {
	entries := make([]OutlineEntry, maxSectionCandidates+2)
	for i := range entries {
		entries[i] = OutlineEntry{Index: i + 1, Title: "Exercises"}
	}
	_, err := resolveSection(entries, SectionRef{Title: "exercises"})
	if err == nil || !strings.Contains(err.Error(), `10 "Exercises" and 2 more.`) {
		t.Errorf("err = %v, want ten candidates and the count of the rest", err)
	}
	if !strings.HasSuffix(err.Error(), untrustedTitles) {
		t.Errorf("err = %v, want it to frame the quoted titles as untrusted", err)
	}
}

// TestSection_PDFEntryPastTheLastPage verifies a bookmark pointing past the end
// of the document is refused by name as a broken entry, not reported as an
// unreadable file.
func TestSection_PDFEntryPastTheLastPage(t *testing.T) {
	entries := []OutlineEntry{{Index: 1, Title: "Phantom", Page: 99}}
	_, err := pdfSection(context.Background(), docFor(t, sectionsPDF), entries, 0, Req{})
	var se *SectionError
	if !errors.As(err, &se) || !strings.Contains(err.Error(), `entry 1 "Phantom" points to page 99, past the document's last page (8)`) {
		t.Errorf("err = %v, want a SectionError naming the entry and the last page", err)
	}
	if err != nil && !strings.HasSuffix(err.Error(), untrustedTitles) {
		t.Errorf("err = %v, want it to frame the quoted title as untrusted", err)
	}
}

// TestSection_EPUBEscapedFragment verifies a link whose fragment is
// percent-encoded reaches the element whose id is the decoded text, so the
// entry before it stops at that heading instead of both sections reading the
// same text from the document start.
func TestSection_EPUBEscapedFragment(t *testing.T) {
	ncx := `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><navMap>
<navPoint id="a"><navLabel><text>Intro</text></navLabel><content src="chapter1.xhtml"/></navPoint>
<navPoint id="b"><navLabel><text>Cafe</text></navLabel><content src="chapter1.xhtml#caf%C3%A9"/></navPoint>
<navPoint id="c"><navLabel><text>Percent</text></navLabel><content src="chapter1.xhtml#50%25"/></navPoint>
</navMap></ncx>`
	files := epub2Files(ncxOPF, ncx)
	files["OEBPS/chapter1.xhtml"] = `<html><body><p>intro</p><h1 id="café">Cafe</h1><p>coffee</p>` +
		`<h1 id="50%">Percent</h1><p>half</p></body></html>`
	path := writeEPUB(t, t.TempDir(), "escaped-fragment.epub", files)

	testCases := []struct{ name, title, want string }{
		{name: "the entry before stops at the heading", title: "intro", want: "intro"},
		{name: "the escaped fragment is found", title: "cafe", want: "Cafecoffee"},
		{name: "an id holding a percent sign is found", title: "percent", want: "Percenthalf"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := Section(context.Background(), openFile(t, path), SectionRef{Title: tc.title}, wholeSection)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(sc.Text); got != tc.want {
				t.Errorf("section %q = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

// TestSection_EPUBArchiveNameWithLiteralEscapes verifies a content document the
// archive stores under its escaped spelling, which Extract reads, is reachable
// from an outline link too, since both resolve to the decoded name.
func TestSection_EPUBArchiveNameWithLiteralEscapes(t *testing.T) {
	opf := `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0"><manifest>
<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
<item id="r" href="raw%20name.xhtml" media-type="application/xhtml+xml"/>
</manifest><spine toc="ncx"><itemref idref="r"/></spine></package>`
	ncx := `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><navMap>
<navPoint id="a"><navLabel><text>Raw</text></navLabel><content src="raw%20name.xhtml#top"/></navPoint>
</navMap></ncx>`
	path := writeEPUB(t, t.TempDir(), "literal-escapes.epub", map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf":      opf,
		"OEBPS/toc.ncx":          ncx,
		"OEBPS/raw%20name.xhtml": `<html><body><h1 id="top">Raw</h1><p>stored escaped</p></body></html>`,
	})
	sc, err := Section(context.Background(), openFile(t, path), SectionRef{Index: 1}, wholeSection)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(sc.Text); got != "Rawstored escaped" {
		t.Errorf("section = %q, want the document stored under its escaped name", got)
	}
}

// TestSpineTextLocate verifies how an outline target becomes an offset: a known
// anchor is exact, an unknown fragment falls back to its document's start, and
// an empty target or a document outside the reading order has no offset.
func TestSpineTextLocate(t *testing.T) {
	st := spineText{anchors: map[string]int{"a.xhtml": 10, "a.xhtml#x": 25}}
	testCases := []struct {
		target string
		want   int
		ok     bool
	}{
		{target: "a.xhtml#x", want: 25, ok: true},
		{target: "a.xhtml#missing", want: 10, ok: true},
		{target: "a.xhtml", want: 10, ok: true},
		{target: "nav.xhtml#toc"},
		{target: ""},
	}
	for _, tc := range testCases {
		t.Run(tc.target, func(t *testing.T) {
			got, ok := st.locate(tc.target)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Errorf("locate(%q) = %d, %v, want %d, %v", tc.target, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestSpineTextSectionEnd verifies an EPUB section does not end at a following
// entry that resolves to its own start or before it, and does not end at a
// deeper entry.
func TestSpineTextSectionEnd(t *testing.T) {
	st := spineText{anchors: map[string]int{"a": 0, "a#deep": 5, "b": 40}}
	entries := []OutlineEntry{
		{target: "a"},
		{Level: 1, target: "a#deep"},
		{target: "a"},
		{target: "nowhere"},
		{target: "b"},
	}
	if got := st.sectionEnd(entries, 0, 0, 100); got != 40 {
		t.Errorf("sectionEnd = %d, want 40", got)
	}
	if got := st.sectionEnd(entries, 4, 40, 100); got != 100 {
		t.Errorf("sectionEnd of the last entry = %d, want 100", got)
	}
}

// TestEPUBSection_BrokenArchives verifies epubSection reports an archive it
// cannot open, or whose structure is broken, as not extractable rather than as
// an error.
func TestEPUBSection_BrokenArchives(t *testing.T) {
	entries := []OutlineEntry{{Index: 1, Title: "One", target: "OEBPS/chapter1.xhtml"}}
	t.Run("not a zip", func(t *testing.T) {
		sc, err := epubSection(context.Background(), docFor(t, "testdata/sample.txt"), entries, 0, Req{})
		if err != nil || sc.Extractable || !strings.HasPrefix(sc.Reason, "cannot open EPUB archive") {
			t.Errorf("got %+v, %v, want a cannot-open reason", sc.Chunk, err)
		}
	})
	t.Run("no container", func(t *testing.T) {
		path := writeEPUB(t, t.TempDir(), "nocontainer.epub", map[string]string{"OEBPS/chapter1.xhtml": "<p>x</p>"})
		sc, err := epubSection(context.Background(), docFor(t, path), entries, 0, Req{})
		if err != nil || sc.Extractable || !strings.HasPrefix(sc.Reason, "not a readable EPUB") {
			t.Errorf("got %+v, %v, want a not-readable reason", sc.Chunk, err)
		}
	})
	t.Run("no text", func(t *testing.T) {
		files := epub2Files(ncxOPF, "")
		files["OEBPS/chapter1.xhtml"] = "<html><body> </body></html>"
		path := writeEPUB(t, t.TempDir(), "notext.epub", files)
		sc, err := epubSection(context.Background(), docFor(t, path), entries, 0, Req{})
		if err != nil || sc.Extractable || sc.Reason != noEPUBTextReason {
			t.Errorf("got %+v, %v, want the no-text reason", sc.Chunk, err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := epubSection(ctx, docFor(t, sectionsEPUB), entries, 0, Req{}); err == nil {
			t.Error("want a context error, got nil")
		}
	})
}

// TestLinkTarget verifies how an outline href becomes an archive target: joined
// to the linking document's directory, percent-escapes decoded, the fragment
// kept, an undecodable escape left as written, and an empty href no target.
func TestLinkTarget(t *testing.T) {
	testCases := []struct{ name, dir, href, want string }{
		{name: "plain", dir: "OEBPS", href: "a.xhtml", want: "OEBPS/a.xhtml"},
		{name: "fragment", dir: "OEBPS", href: "a.xhtml#c1", want: "OEBPS/a.xhtml#c1"},
		{name: "escaped name", dir: "OEBPS", href: "part%20one.xhtml#p1", want: "OEBPS/part one.xhtml#p1"},
		{name: "subdirectory", dir: "OEBPS", href: "text/b.xhtml", want: "OEBPS/text/b.xhtml"},
		{name: "parent directory", dir: "OEBPS/nav", href: "../c.xhtml", want: "OEBPS/c.xhtml"},
		{name: "bad escape", dir: "OEBPS", href: "a%zz.xhtml", want: "OEBPS/a%zz.xhtml"},
		{name: "empty fragment", dir: "OEBPS", href: "a.xhtml#", want: "OEBPS/a.xhtml"},
		{name: "empty", dir: "OEBPS", href: " ", want: ""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := linkTarget(tc.dir, tc.href); got != tc.want {
				t.Errorf("linkTarget(%q, %q) = %q, want %q", tc.dir, tc.href, got, tc.want)
			}
		})
	}
}

// TestReadSpine_EscapedManifestHref verifies a spine document whose manifest
// href is percent-escaped is read under its decoded archive name, and one
// stored under the escaped spelling is still read under that.
func TestReadSpine_EscapedManifestHref(t *testing.T) {
	opf := `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><manifest>
<item id="a" href="part%20one.xhtml" media-type="application/xhtml+xml"/>
<item id="b" href="raw%20name.xhtml" media-type="application/xhtml+xml"/>
</manifest><spine><itemref idref="a"/><itemref idref="b"/></spine></package>`
	path := writeEPUB(t, t.TempDir(), "escaped.epub", map[string]string{
		"META-INF/container.xml":     outlineContainerXML,
		"OEBPS/content.opf":          opf,
		"OEBPS/part one.xhtml":       "<p>decoded</p>",
		"OEBPS/raw%20name.xhtml":     "<p>as written</p>",
		"OEBPS/unrelated.xhtml":      "<p>never</p>",
		"OEBPS/raw name.xhtml.extra": "<p>never</p>",
	})
	c, err := Extract(context.Background(), openFile(t, path), Req{MaxChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Text, "decoded") || !strings.Contains(c.Text, "as written") || strings.Contains(c.Text, "never") {
		t.Errorf("text = %q, want both spine documents and nothing else", c.Text)
	}
}

// TestHTMLText_SkipsTheTitleAndRecordsAnchors verifies the text of a content
// document leaves out its head <title>, and that ids, and names on links only,
// are reported at the offset where their element's text starts.
func TestHTMLText_SkipsTheTitleAndRecordsAnchors(t *testing.T) {
	doc := `<html><head><title>Book Title</title><meta name="viewport"/></head><body>` +
		`<p>ab</p><h1 id="h">cd</h1><p><a name="n"/>ef</p><input name="q"/></body></html>`
	got := map[string]int{}
	text := htmlText(strings.NewReader(doc), func(id string, at int) { got[id] = at })
	if text != "abcdef" {
		t.Errorf("text = %q, want %q", text, "abcdef")
	}
	want := map[string]int{"h": 2, "n": 4}
	if len(got) != len(want) || got["h"] != 2 || got["n"] != 4 {
		t.Errorf("anchors = %v, want %v", got, want)
	}
}

// TestHTMLText_AStrayCloseAndAnEmptyId covers two malformed inputs: a closing
// </title> with no opening one before it must not make the next <title> count
// as text, and an element whose id is empty names no anchor.
func TestHTMLText_AStrayCloseAndAnEmptyId(t *testing.T) {
	doc := `<html><body></title>stray<title>Head</title><p id="">ab</p><p id="c">cd</p></body></html>`
	got := map[string]int{}
	text := htmlText(strings.NewReader(doc), func(id string, at int) { got[id] = at })
	if text != "strayabcd" {
		t.Errorf("text = %q, want %q", text, "strayabcd")
	}
	if len(got) != 1 || got["c"] != 7 {
		t.Errorf("anchors = %v, want only c at 7", got)
	}
}
