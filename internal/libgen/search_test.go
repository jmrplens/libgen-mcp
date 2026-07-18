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

// TestParsePaginatorReach verifies that paginatorReach derives the reachable
// result cap from the Paginator init script and that Truncated flags a search
// whose advertised total exceeds that cap.
func TestParsePaginatorReach(t *testing.T) {
	// The "golang" fixture paginates 6 pages of 25 => 150 reachable, and the
	// Files tab advertises 135, so the search is not truncated.
	page := parseFixture(t, "search_books.html")
	if page.Reachable != 150 {
		t.Errorf("Reachable = %d, want 150", page.Reachable)
	}
	if page.Truncated {
		t.Errorf("Truncated = true, want false (135 <= 150)")
	}

	// The "physics" fixture paginates 20 pages of 100 => 2000 reachable, while
	// the Files tab advertises 38514, so the search is truncated.
	trunc := parseFixture(t, "search_truncated.html")
	if trunc.Reachable != 2000 {
		t.Errorf("Reachable = %d, want 2000", trunc.Reachable)
	}
	if !trunc.Truncated {
		t.Errorf("Truncated = false, want true (38514 > 2000)")
	}
}

// TestIsTruncated covers the truncation decision, including libgen's capped
// "1000+" display: a non-numeric-but-large total must still flag the search as
// truncated (the previous strconv.Atoi silently failed on "1000+" and left it
// false exactly when the result set was largest).
func TestIsTruncated(t *testing.T) {
	cases := []struct {
		name       string
		totalFiles string
		reachable  int
		want       bool
	}{
		{"capped 1000+ over reach", "1000+", 100, true},
		{"capped 1000+ equal-ish reach", "1000+", 100, true},
		{"plain number over reach", "38514", 2000, true},
		{"plain number under reach", "135", 150, false},
		{"plain number equals reach", "150", 150, false},
		{"non-numeric indicator", "many", 100, true},
		{"empty total", "", 100, false},
		{"zero total", "0", 100, false},
		{"reach zero", "1000+", 0, false},
		{"trailing plus with spaces", " 1200+ ", 500, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTruncated(tc.totalFiles, tc.reachable); got != tc.want {
				t.Errorf("isTruncated(%q, %d) = %v, want %v", tc.totalFiles, tc.reachable, got, tc.want)
			}
		})
	}
}

// TestParseSearchArticles verifies ParseSearchArticles.
func TestParseSearchArticles(t *testing.T) {
	page := parseFixture(t, "search_articles.html")
	if len(page.Results) == 0 {
		t.Fatal("0 results in the articles fixture")
	}
}

// TestParseSearchArticlesDOI verifies the DOI printed in an article row is parsed
// into Result.DOI, so the model can pass it to the download tool (articles are
// fetched by DOI). The first fixture row advertises DOI 10.14311/nnw.2016.26.006.
func TestParseSearchArticlesDOI(t *testing.T) {
	page := parseFixture(t, "search_articles.html")
	if len(page.Results) == 0 {
		t.Fatal("0 results in the articles fixture")
	}
	const wantDOI = "10.14311/nnw.2016.26.006"
	if got := page.Results[0].DOI; got != wantDOI {
		t.Errorf("Results[0].DOI = %q, want %q", got, wantDOI)
	}
	var withDOI int
	for _, r := range page.Results {
		if r.DOI != "" {
			withDOI++
		}
	}
	if withDOI == 0 {
		t.Error("no article result carried a DOI, want several")
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
