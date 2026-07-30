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

// TestSearch_IgnoresWhitespaceDifferences verifies that a multi-word query
// matches text whose whitespace does not line up with it. PDF text layers
// routinely drop the space between two words or split one across a line break,
// which made a phrase search silently return zero matches while each word on its
// own returned many. The reported offset and snippet still point into the
// original text.
func TestSearch_IgnoresWhitespaceDifferences(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // the run of original text the match must cover
	}{
		{"space dropped by the extractor", "the OverseasHarriette Kane papers", "OverseasHarriette"},
		{"split across a line break", "the Overseas\nHarriette Kane papers", "Overseas\nHarriette"},
		{"padded with extra spaces", "the Overseas   Harriette Kane papers", "Overseas   Harriette"},
		{"spaces inside a word", "the Over seas Harriette Kane papers", "Over seas Harriette"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "doc.txt")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := Search(context.Background(), path, "Overseas Harriette", SearchOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if res.TotalMatches != 1 {
				t.Fatalf("TotalMatches = %d, want 1 (%+v)", res.TotalMatches, res)
			}
			got := res.Matches[0]
			wantOffset := len([]rune(tc.body[:strings.Index(tc.body, tc.want)]))
			if got.CharOffset != wantOffset {
				t.Errorf("CharOffset = %d, want %d (the offset of %q)", got.CharOffset, wantOffset, tc.want)
			}
			if !strings.Contains(got.Snippet, "Kane") {
				t.Errorf("snippet should show the surrounding text, got %q", got.Snippet)
			}
		})
	}
}

// TestSearch_WhitespaceInsensitiveMatchIsNotSubstringNoise verifies the flexible
// matching stays anchored to the query's non-whitespace runes: a phrase whose
// letters are not all present still finds nothing.
func TestSearch_WhitespaceInsensitiveMatchIsNotSubstringNoise(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(path, []byte("the OverseasHarriette Kane papers"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), path, "Overseas Harold", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 0 {
		t.Errorf("TotalMatches = %d, want 0 (%+v)", res.TotalMatches, res)
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
	// A conditional check ("if there were matches, there must be some") would pass
	// even if a negative StartMatch were clamped to the LAST match instead of the
	// first. Compare against the explicit first window instead, which is the only
	// thing "normalized to zero" can mean.
	base, err := Search(context.Background(), "testdata/sample.txt", "the", SearchOpts{StartMatch: 0})
	if err != nil {
		t.Fatal(err)
	}
	if base.TotalMatches == 0 {
		t.Fatal("test setup: the fixture yields no matches, so nothing is being compared")
	}
	if res.TotalMatches != base.TotalMatches {
		t.Errorf("TotalMatches = %d, want %d (windowing must not change the total)", res.TotalMatches, base.TotalMatches)
	}
	if len(res.Matches) != len(base.Matches) {
		t.Fatalf("got %d matches, want the %d of the first window", len(res.Matches), len(base.Matches))
	}
	for i := range base.Matches {
		if res.Matches[i].CharOffset != base.Matches[i].CharOffset {
			t.Errorf("match %d at offset %d, want %d: a negative StartMatch did not clamp to the first window",
				i, res.Matches[i].CharOffset, base.Matches[i].CharOffset)
		}
	}
}

// TestFindMatches_Boundaries covers the edges of the matcher directly, where an
// off-by-one is a panic or a silently wrong offset rather than a wrong-looking
// result. Every existing offset assertion in this file is relative ("later than the
// previous one") or on pure-ASCII text, so none of these cases were constrained.
func TestFindMatches_Boundaries(t *testing.T) {
	const snippet = 40

	t.Run("a match at offset zero is found and reported at zero", func(t *testing.T) {
		got := findMatches("The quick brown fox", "The", false, 1, snippet)
		if len(got) != 1 {
			t.Fatalf("got %d matches, want 1", len(got))
		}
		if got[0].CharOffset != 0 {
			t.Errorf("CharOffset = %d, want 0", got[0].CharOffset)
		}
		if !strings.Contains(got[0].Snippet, "The") {
			t.Errorf("snippet %q does not contain the match", got[0].Snippet)
		}
	})

	t.Run("a match at the very end is found", func(t *testing.T) {
		const body = "the quick brown fox"
		got := findMatches(body, "fox", false, 1, snippet)
		if len(got) != 1 {
			t.Fatalf("got %d matches, want 1", len(got))
		}
		if want := len([]rune(body)) - 3; got[0].CharOffset != want {
			t.Errorf("CharOffset = %d, want %d", got[0].CharOffset, want)
		}
		if !strings.Contains(got[0].Snippet, "fox") {
			t.Errorf("snippet %q does not contain the trailing match", got[0].Snippet)
		}
	})

	t.Run("a query longer than the document matches nothing", func(t *testing.T) {
		// This is the i+m <= len(hay) guard. Getting it wrong is a slice panic, not a
		// wrong answer, so it must be exercised rather than reasoned about.
		if got := findMatches("short", "a considerably longer query than the text", false, 1, snippet); got != nil {
			t.Errorf("got %+v, want no matches", got)
		}
	})

	t.Run("an empty document matches nothing", func(t *testing.T) {
		if got := findMatches("", "anything", false, 1, snippet); got != nil {
			t.Errorf("got %+v, want no matches", got)
		}
	})

	t.Run("an empty query matches nothing", func(t *testing.T) {
		// The existing test uses "   "; the literal empty string reaches the same
		// guard but is the value a caller is most likely to send by accident.
		if got := findMatches("some text", "", false, 1, snippet); got != nil {
			t.Errorf("got %+v, want no matches", got)
		}
	})

	t.Run("the whole document as the query matches once at zero", func(t *testing.T) {
		const body = "exactly this"
		got := findMatches(body, body, false, 1, snippet)
		if len(got) != 1 {
			t.Fatalf("got %d matches, want 1", len(got))
		}
		if got[0].CharOffset != 0 {
			t.Errorf("CharOffset = %d, want 0", got[0].CharOffset)
		}
	})
}

// TestFindMatches_QueryIsLiteralNotARegex pins that the matcher compares runes and
// never interprets the query.
//
// Nothing asserted this, so swapping the hand-rolled matcher for regexp — a
// plausible "simplification" — would change the meaning of every user query
// containing a dot, a bracket or a backslash, and no test would object. A search for
// "a.b" must not match "axb".
func TestFindMatches_QueryIsLiteralNotARegex(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		query      string
		wantMatch  bool
		wantOffset int
	}{
		{name: "dot does not match any rune", text: "axb", query: "a.b"},
		{name: "dot matches a literal dot", text: "a.b", query: "a.b", wantMatch: true},
		{name: "star is not a quantifier", text: "aaab", query: "a*b"},
		{name: "star matches a literal star", text: "a*b", query: "a*b", wantMatch: true},
		{name: "character class is literal", text: "abc", query: "[abc]"},
		{name: "backslash is literal", text: "a1b", query: `a\db`},
		{name: "parentheses are literal", text: "ab", query: "(a)b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findMatches(tc.text, tc.query, false, 1, 40)
			if tc.wantMatch {
				if len(got) != 1 {
					t.Fatalf("got %d matches for %q in %q, want 1", len(got), tc.query, tc.text)
				}
				if got[0].CharOffset != tc.wantOffset {
					t.Errorf("CharOffset = %d, want %d", got[0].CharOffset, tc.wantOffset)
				}
				return
			}
			if len(got) != 0 {
				t.Errorf("query %q matched %q as a pattern (%+v); matching must be literal", tc.query, tc.text, got)
			}
		})
	}
}

// TestFindMatches_UnicodeOffsetsAreRuneIndices verifies CharOffset counts runes, not
// bytes, in text that actually distinguishes the two.
//
// Every other offset assertion in this file runs on pure ASCII, where rune index and
// byte index are equal — so the rune/byte mapping through compactRunes and its `at`
// table is effectively unasserted. A confusion there would place every reported
// offset in a non-English document past its true position, silently, and the read
// tool would page the user to the wrong place.
func TestFindMatches_UnicodeOffsetsAreRuneIndices(t *testing.T) {
	// Each of these leading characters is multiple bytes but one rune.
	const body = "日本語のテキスト berlin"
	if len(body) == len([]rune(body)) {
		t.Fatal("test setup: the body has no multi-byte runes, so it cannot distinguish the two")
	}

	got := findMatches(body, "berlin", false, 1, 60)
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1", len(got))
	}
	wantRune := len([]rune("日本語のテキスト "))
	if got[0].CharOffset != wantRune {
		t.Errorf("CharOffset = %d, want the rune index %d (byte index would be %d)",
			got[0].CharOffset, wantRune, strings.Index(body, "berlin"))
	}
}

// TestFindMatches_UnicodeCaseFolding verifies the default case-insensitive match
// folds non-ASCII letters, and that CaseSensitive turns that off.
//
// The package folds with unicode.ToLower, which handles far more than ASCII, but no
// test used a non-ASCII letter — so a regression to a byte-wise ASCII fold would
// pass everything while quietly failing every accented or Cyrillic query.
func TestFindMatches_UnicodeCaseFolding(t *testing.T) {
	cases := []struct {
		name          string
		text, query   string
		caseSensitive bool
		want          int
	}{
		{name: "accented latin folds", text: "Ärger und Öl", query: "ärger", want: 1},
		{name: "cyrillic folds", text: "Москва зимой", query: "москва", want: 1},
		{name: "greek folds", text: "Λόγος", query: "λόγος", want: 1},
		{name: "case sensitive rejects the folded form", text: "Ärger", query: "ärger", caseSensitive: true},
		{name: "case sensitive accepts the exact form", text: "Ärger", query: "Ärger", caseSensitive: true, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findMatches(tc.text, tc.query, tc.caseSensitive, 1, 40)
			if len(got) != tc.want {
				t.Errorf("got %d matches for %q in %q, want %d", len(got), tc.query, tc.text, tc.want)
			}
		})
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
