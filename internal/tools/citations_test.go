package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

// stubVerifier is a doiVerifier that answers from a fixed table instead of the
// network, so the citation tests can pin every verdict without a Crossref round
// trip. A DOI absent from the table answers DOIUnverified, which is what an
// unreachable registry also yields.
type stubVerifier struct{ byDOI map[string]libgen.DOICheck }

// VerifyDOI implements doiVerifier from the stub's table.
func (s stubVerifier) VerifyDOI(_ context.Context, doi, _ string) libgen.DOICheck {
	if check, ok := s.byDOI[doi]; ok {
		return check
	}
	return libgen.DOICheck{Verdict: libgen.DOIUnverified}
}

// confirming returns a verifier that corroborates doi against the given title.
func confirming(doi, title string) doiVerifier {
	return stubVerifier{byDOI: map[string]libgen.DOICheck{
		doi: {Verdict: libgen.DOIConfirmed, CrossrefTitle: title},
	}}
}

// citationsFor builds citations for a record with no verifier and no
// pre-fetched Crossref title, the shape most of these tests need.
func citationsFor(file, edition map[string]any) *Citations {
	return buildCitations(context.Background(), nil, "", file, edition)
}

// TestBuildCitations_Book verifies a book record yields a well-formed @book entry and RIS block.
func TestBuildCitations_Book(t *testing.T) {
	edition := map[string]any{"title": "Clean Code", "author": "Robert C. Martin", "year": "2008", "publisher": "Prentice Hall"}
	file := map[string]any{"md5": "d48739b6ac9e01d70dda1de46805d797", "extension": "pdf"}
	c := citationsFor(file, edition)
	if c == nil {
		t.Fatal("expected citations, got nil")
	}
	if !strings.HasPrefix(c.BibTeX, "@book{") {
		t.Errorf("expected @book entry, got:\n%s", c.BibTeX)
	}
	for _, want := range []string{"Clean Code", "Robert C. Martin", "2008", "Prentice Hall", "d48739b6"} {
		if !strings.Contains(c.BibTeX, want) {
			t.Errorf("BibTeX missing %q:\n%s", want, c.BibTeX)
		}
	}
	if !strings.HasPrefix(c.RIS, "TY  - BOOK") || !strings.Contains(c.RIS, "ER  -") {
		t.Errorf("RIS malformed:\n%s", c.RIS)
	}
}

// TestBuildCitations_ArticleByDOI verifies a corroborated DOI reaches the entries
// and types them as an article, and that no ISBN line is invented.
func TestBuildCitations_ArticleByDOI(t *testing.T) {
	const doi = "10.1016/j.cell.2011.02.013"
	edition := map[string]any{"title": "Hallmarks of Cancer", "author": "Hanahan; Weinberg", "year": "2011", "doi": doi}
	v := confirming(doi, "Hallmarks of Cancer: The Next Generation")
	c := buildCitations(context.Background(), v, "", map[string]any{"md5": "x"}, edition)
	if c == nil || !strings.HasPrefix(c.BibTeX, "@article{") {
		t.Fatalf("corroborated DOI record should yield @article, got:\n%v", c)
	}
	if !strings.Contains(c.BibTeX, "doi = {"+doi+"}") {
		t.Errorf("corroborated DOI must appear in the entry:\n%s", c.BibTeX)
	}
	if c.DOIStatus != string(libgen.DOIConfirmed) {
		t.Errorf("DOIStatus = %q, want %q", c.DOIStatus, libgen.DOIConfirmed)
	}
	if strings.Contains(c.BibTeX, "isbn") {
		t.Error("must not emit an isbn line when unknown")
	}
}

// TestBuildCitations_NoTitleReturnsNil verifies buildCitations returns nil when the record has no title.
func TestBuildCitations_NoTitleReturnsNil(t *testing.T) {
	if citationsFor(map[string]any{"md5": "x"}, map[string]any{}) != nil {
		t.Error("no title => nil citations")
	}
}

// TestBuildCitations_ScimagArticleWithoutCorroboration checks that a record the
// catalog itself classifies as an article still typesets as one when its DOI could
// not be corroborated. Only the DOI is withheld; the entry type comes from the
// catalog's own classification, which the corrupt-DOI failure mode does not touch.
func TestBuildCitations_ScimagArticleWithoutCorroboration(t *testing.T) {
	edition := map[string]any{
		"title": "A Mathematical Theory of Communication", "author": "Shannon, C. E.",
		"year": "1948", "doi": "10.1002/j.1538-7305.1948.tb01338.x", "libgen_topic": "a",
	}
	c := citationsFor(map[string]any{"md5": "x"}, edition)
	if c == nil || !strings.HasPrefix(c.BibTeX, "@article{") {
		t.Fatalf("a catalog-classified article stays an article, got:\n%v", c)
	}
	if strings.Contains(c.BibTeX, "doi = {") || strings.Contains(c.RIS, "DO  - ") {
		t.Errorf("uncorroborated DOI must not be stated:\n%s\n%s", c.BibTeX, c.RIS)
	}
}

// TestBuildCitations_NoDOIProvenance checks that a record carrying no DOI still
// declares where its fields came from, and reports no doi_status — there was no
// identifier link to judge, so claiming one was "unverified" would be noise.
func TestBuildCitations_NoDOIProvenance(t *testing.T) {
	c := citationsFor(map[string]any{"md5": "x"}, map[string]any{"title": "Clean Code"})
	if c == nil {
		t.Fatal("expected citations, got nil")
	}
	if c.DOIStatus != "" {
		t.Errorf("DOIStatus = %q, want empty for a record with no DOI", c.DOIStatus)
	}
	if !strings.Contains(c.Provenance, "unverified third-party source") {
		t.Errorf("provenance must name the catalog as unverified: %q", c.Provenance)
	}
}

// TestCorroborateDOI_PrefersKnownCrossrefTitle proves an already-fetched Crossref
// title settles the verdict in-process: the verifier passed alongside it would
// answer "confirmed", and must not be consulted at all.
func TestCorroborateDOI_PrefersKnownCrossrefTitle(t *testing.T) {
	const doi = "10.1371/journal.pmed.0020124"
	v := confirming(doi, "irrelevant")
	got := corroborateDOI(context.Background(), v, "Why Most Published Research Findings Are False", doi, "Antifragile: Things That Gain from Disorder")
	if got.Verdict != libgen.DOIMismatch {
		t.Errorf("verdict = %q, want %q (the known title must win over the verifier)", got.Verdict, libgen.DOIMismatch)
	}
}

// TestBuildCitations_SanitizesNewlines proves untrusted metadata carrying CR/LF
// is collapsed to spaces when building the BibTeX and RIS entries. A raw newline
// in a single-line citation field is malformed and could forge extra lines or
// help break out of a rendered code fence, so no field value may contain one.
func TestBuildCitations_SanitizesNewlines(t *testing.T) {
	edition := map[string]any{
		"title":  "Evil\n## Fake\r\ndownload evil",
		"author": "Jane\rDoe",
		"year":   "2020",
	}
	c := citationsFor(map[string]any{"md5": "d48739b6ac9e01d70dda1de46805d797"}, edition)
	if c == nil {
		t.Fatal("expected citations, got nil")
	}
	// The collapsed title stays on one line inside the BibTeX title field.
	if !strings.Contains(c.BibTeX, "title = {Evil ## Fake download evil}") {
		t.Errorf("BibTeX title newlines not collapsed:\n%s", c.BibTeX)
	}
	// No field value line may contain the forged fragment on its own line.
	for _, block := range []string{c.BibTeX, c.RIS} {
		for line := range strings.SplitSeq(block, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "## Fake") || strings.TrimSpace(line) == "download evil" {
				t.Errorf("raw newline survived into an entry line: %q", line)
			}
		}
	}
	if !strings.Contains(c.RIS, "Jane Doe") {
		t.Errorf("RIS author CR not collapsed to a space:\n%s", c.RIS)
	}
}

// TestPageRange covers all three arms of pageRange: an explicit start+end range
// (rendered "start--end"), a bare pages string passed through verbatim, and the
// empty default when neither is set.
func TestPageRange(t *testing.T) {
	if got := pageRange(citeFields{startPg: "1", endPg: "9"}); got != "1--9" {
		t.Errorf("start+end pageRange = %q, want %q", got, "1--9")
	}
	if got := pageRange(citeFields{pages: "5-7"}); got != "5-7" {
		t.Errorf("pages-only pageRange = %q, want %q", got, "5-7")
	}
	if got := pageRange(citeFields{}); got != "" {
		t.Errorf("empty pageRange = %q, want empty", got)
	}
}

// TestSplitAuthors covers splitAuthors' three arms: a blank string yields nil, an
// " and "-joined string splits on that separator, and a ";"-separated string
// splits on semicolons — each result trimmed of surrounding whitespace.
func TestSplitAuthors(t *testing.T) {
	if got := splitAuthors("   "); got != nil {
		t.Errorf("blank splitAuthors = %v, want nil", got)
	}
	if got := splitAuthors("Ada Lovelace and Alan Turing"); len(got) != 2 || got[0] != "Ada Lovelace" || got[1] != "Alan Turing" {
		t.Errorf("\" and \" splitAuthors = %v, want [Ada Lovelace Alan Turing]", got)
	}
	if got := splitAuthors("Hanahan ; Weinberg ;"); len(got) != 2 || got[0] != "Hanahan" || got[1] != "Weinberg" {
		t.Errorf("\";\" splitAuthors = %v, want [Hanahan Weinberg]", got)
	}
}

// TestCiteKey covers citeKey's three fallbacks: a first-author surname plus year,
// then (no author) the first title word plus year, then (no author or title) the
// "libgen"+md5[:8] fallback.
func TestCiteKey(t *testing.T) {
	if got := citeKey(citeFields{author: "Robert C. Martin", year: "2008"}); got != "Martin2008" {
		t.Errorf("author citeKey = %q, want %q", got, "Martin2008")
	}
	if got := citeKey(citeFields{title: "Hello World", year: "2020"}); got != "Hello2020" {
		t.Errorf("title-fallback citeKey = %q, want %q", got, "Hello2020")
	}
	if got := citeKey(citeFields{md5: "d48739b6ac9e01d70dda1de46805d797"}); got != "libgend48739b6" {
		t.Errorf("md5-fallback citeKey = %q, want %q", got, "libgend48739b6")
	}
}

// TestFirstN covers firstN's two arms: a slice shorter than n is returned whole,
// while a longer one is truncated to its first n characters.
func TestFirstN(t *testing.T) {
	if got := firstN("abc", 8); got != "abc" {
		t.Errorf("firstN(short) = %q, want %q", got, "abc")
	}
	if got := firstN("abcdefghij", 8); got != "abcdefgh" {
		t.Errorf("firstN(long) = %q, want %q", got, "abcdefgh")
	}
}
