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
