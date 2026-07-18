package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

var md5Re = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TestSearchParamsValues verifies SearchParamsValues.
func TestSearchParamsValues(t *testing.T) {
	p := SearchParams{
		Query:          "golang",
		Topics:         []string{"nonfiction", "articles"},
		SearchIn:       []string{"title", "isbn"},
		ResultsPerPage: 50,
		Page:           2,
		Order:          "year",
		OrderMode:      "desc",
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	v := p.values()
	if v.Get("req") != "golang" {
		t.Errorf("req = %q", v.Get("req"))
	}
	if got := v["topics[]"]; len(got) != 2 || got[0] != "l" || got[1] != "a" {
		t.Errorf("topics[] = %v", got)
	}
	if got := v["columns[]"]; len(got) != 2 || got[0] != "t" || got[1] != "i" {
		t.Errorf("columns[] = %v", got)
	}
	if v.Get("res") != "50" || v.Get("page") != "2" || v.Get("order") != "year" || v.Get("ordermode") != "desc" {
		t.Errorf("values = %v", v)
	}
}

// TestSearchParamsMinimalOmitsDefaults verifies SearchParamsMinimalOmitsDefaults.
func TestSearchParamsMinimalOmitsDefaults(t *testing.T) {
	v := SearchParams{Query: "golang"}.values()
	for _, k := range []string{"topics[]", "columns[]", "res", "page", "order", "ordermode"} {
		if _, ok := v[k]; ok {
			t.Errorf("values() includes %q that was not requested", k)
		}
	}
}

// TestSearchParamsValidate verifies SearchParamsValidate.
func TestSearchParamsValidate(t *testing.T) {
	cases := []SearchParams{
		{Query: ""},
		{Query: "x", Topics: []string{"cooking"}},
		{Query: "x", SearchIn: []string{"body"}},
		{Query: "x", ResultsPerPage: 30},
		{Query: "x", Order: "pages"},
		{Query: "x", OrderMode: "up"},
	}
	for i, p := range cases {
		if err := p.Validate(); err == nil {
			t.Errorf("case %d: Validate() = nil, want error", i)
		}
	}
}

func parseFixture(t *testing.T, name string) *SearchPage {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	page, err := ParseSearch(f, "https://libgen.li")
	if err != nil {
		t.Fatalf("ParseSearch(%s) error = %v", name, err)
	}
	return page
}

// TestParseSearchBooks verifies ParseSearchBooks.
func TestParseSearchBooks(t *testing.T) {
	page := parseFixture(t, "search_books.html")
	if len(page.Results) == 0 {
		t.Fatal("0 resultados en fixture de libros")
	}
	if page.TotalFiles == "" {
		t.Error("TotalFiles is empty")
	}
	for i, r := range page.Results {
		if !md5Re.MatchString(r.MD5) {
			t.Errorf("result %d: invalid md5 %q", i, r.MD5)
		}
		if r.Title == "" {
			t.Errorf("result %d: empty title", i)
		}
		if len(r.Downloads) == 0 {
			t.Errorf("result %d: no download options", i)
		}
		if r.Downloads[0].Label != "libgen" || !strings.HasPrefix(r.Downloads[0].URL, "https://libgen.li/ads.php?md5=") {
			t.Errorf("result %d: first download = %+v", i, r.Downloads[0])
		}
	}
	// Known row from the 2026-07-17 capture (adjust to the committed fixture):
	const wantMD5 = "87a4ebdaf21fa6cc70009a3dd63194ee"
	var found *Result
	for i := range page.Results {
		if page.Results[i].MD5 == wantMD5 {
			found = &page.Results[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("known md5 %s not present", wantMD5)
	}
	if !strings.Contains(found.Title, "Golang") {
		t.Errorf("Title = %q", found.Title)
	}
	if found.EditionID != "138281637" || found.FileID != "93485370" {
		t.Errorf("EditionID/FileID = %s/%s", found.EditionID, found.FileID)
	}
	if found.Extension != "pdf" || found.Year != "2018" || found.Language != "English" {
		t.Errorf("ext/year/language = %s/%s/%s", found.Extension, found.Year, found.Language)
	}
	if len(found.ISBNs) == 0 {
		t.Error("no ISBNs")
	}
}

// TestParseSearchArticles verifies ParseSearchArticles.
func TestParseSearchArticles(t *testing.T) {
	page := parseFixture(t, "search_articles.html")
	if len(page.Results) == 0 {
		t.Fatal("0 results in the articles fixture")
	}
}

// TestParseSearchEmpty verifies ParseSearchEmpty.
func TestParseSearchEmpty(t *testing.T) {
	page := parseFixture(t, "search_empty.html")
	if len(page.Results) != 0 {
		t.Errorf("results = %d, want 0", len(page.Results))
	}
	if page.TotalFiles != "0" {
		t.Errorf("TotalFiles = %q, want \"0\"", page.TotalFiles)
	}
}

// TestParseSearchLayoutChanged verifies ParseSearchLayoutChanged.
func TestParseSearchLayoutChanged(t *testing.T) {
	_, err := ParseSearch(strings.NewReader("<html><body><p>hola</p></body></html>"), "https://libgen.li")
	if err == nil || !strings.Contains(err.Error(), "layout") {
		t.Fatalf("err = %v, want ErrLayoutChanged", err)
	}
}

// TestClientSearch verifies ClientSearch.
func TestClientSearch(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/search_books.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.php" || r.URL.Query().Get("req") != "golang" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		w.Write(fixture)
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	page, mirror, err := c.Search(context.Background(), SearchParams{Query: "golang"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if mirror != srv.URL || len(page.Results) == 0 {
		t.Errorf("Search() mirror=%q results=%d", mirror, len(page.Results))
	}
}
