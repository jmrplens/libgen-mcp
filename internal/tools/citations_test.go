package tools

import (
	"strings"
	"testing"
)

// TestBuildCitations_Book verifies a book record yields a well-formed @book entry and RIS block.
func TestBuildCitations_Book(t *testing.T) {
	edition := map[string]any{"title": "Clean Code", "author": "Robert C. Martin", "year": "2008", "publisher": "Prentice Hall"}
	file := map[string]any{"md5": "d48739b6ac9e01d70dda1de46805d797", "extension": "pdf"}
	c := buildCitations(file, edition)
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

// TestBuildCitations_ArticleByDOI verifies a DOI-bearing record yields an @article/JOUR citation and never emits an ISBN line.
func TestBuildCitations_ArticleByDOI(t *testing.T) {
	edition := map[string]any{"title": "Hallmarks of Cancer", "author": "Hanahan; Weinberg", "year": "2011", "doi": "10.1016/j.cell.2011.02.013"}
	c := buildCitations(map[string]any{"md5": "x"}, edition)
	if c == nil || !strings.HasPrefix(c.BibTeX, "@article{") {
		t.Fatalf("DOI record should yield @article, got:\n%v", c)
	}
	if strings.Contains(c.BibTeX, "isbn") {
		t.Error("must not emit an isbn line when unknown")
	}
}

// TestBuildCitations_NoTitleReturnsNil verifies buildCitations returns nil when the record has no title.
func TestBuildCitations_NoTitleReturnsNil(t *testing.T) {
	if buildCitations(map[string]any{"md5": "x"}, map[string]any{}) != nil {
		t.Error("no title => nil citations")
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
	c := buildCitations(map[string]any{"md5": "d48739b6ac9e01d70dda1de46805d797"}, edition)
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
