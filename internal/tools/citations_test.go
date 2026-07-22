package tools

import (
	"strings"
	"testing"
)

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

func TestBuildCitations_NoTitleReturnsNil(t *testing.T) {
	if buildCitations(map[string]any{"md5": "x"}, map[string]any{}) != nil {
		t.Error("no title => nil citations")
	}
}
