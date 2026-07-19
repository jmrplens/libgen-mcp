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

// TestParseAllTopics parses every topic fixture and asserts that each non-empty
// page yields at least one well-formed result (valid 32-hex md5, non-empty title,
// at least one download option) and exposes a TotalFiles counter. The empty
// fixture must parse to zero results. The comics fixture is the regression guard:
// libgen renders its primary download link as get.php?md5= (not ads.php?md5=), so
// before the parser fix comics rows carried an empty md5 and this test failed.
func TestParseAllTopics(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		empty   bool
	}{
		{"nonfiction", "search_books.html", false},
		{"articles", "search_articles.html", false},
		{"fiction", "search_fiction.html", false},
		{"magazines", "search_magazines.html", false},
		{"comics", "search_comics.html", false},
		{"standards", "search_standards.html", false},
		{"fiction_rus", "search_fiction_rus.html", false},
		{"empty", "search_empty.html", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := parseFixture(t, tc.fixture)
			if tc.empty {
				if len(page.Results) != 0 {
					t.Fatalf("%s: results = %d, want 0", tc.fixture, len(page.Results))
				}
				return
			}
			assertTopicPage(t, tc.fixture, page)
		})
	}
}

// assertTopicPage checks a non-empty topic page: it must advertise a TotalFiles
// counter, hold at least one fully-formed result (valid 32-hex md5, non-empty
// title, at least one download option), and expose an md5 for the vast majority of
// its rows. A handful of libgen rows genuinely lack a recorded title, so full
// validity is counted rather than demanded of every row; for comics the get.php
// regression makes every md5 empty, so both the zero-valid and the majority checks
// fail exactly when the parser fix is missing.
func assertTopicPage(t *testing.T, fixture string, page *SearchPage) {
	t.Helper()
	if len(page.Results) == 0 {
		t.Fatalf("%s: 0 results, want >= 1", fixture)
	}
	if page.TotalFiles == "" {
		t.Errorf("%s: TotalFiles is empty", fixture)
	}
	var valid, withMD5 int
	for _, r := range page.Results {
		ok := md5Re.MatchString(r.MD5)
		if ok {
			withMD5++
		}
		if ok && r.Title != "" && len(r.Downloads) > 0 {
			valid++
		}
	}
	if valid == 0 {
		t.Errorf("%s: no fully-formed result (valid md5 + title + download)", fixture)
	}
	if withMD5*4 < len(page.Results)*3 {
		t.Errorf("%s: only %d/%d results have a valid md5", fixture, withMD5, len(page.Results))
	}
}

// TestSearchParamsAllColumns verifies that values() maps every search_in column to
// its single-letter libgen code, both individually and combined in one query.
func TestSearchParamsAllColumns(t *testing.T) {
	want := map[string]string{
		"title":     "t",
		"author":    "a",
		"series":    "s",
		"year":      "y",
		"publisher": "p",
		"isbn":      "i",
	}
	for col, letter := range want {
		t.Run(col, func(t *testing.T) {
			v := SearchParams{Query: "x", SearchIn: []string{col}}.values()
			if got := v["columns[]"]; len(got) != 1 || got[0] != letter {
				t.Errorf("search_in %q => columns[] = %v, want [%q]", col, got, letter)
			}
		})
	}
	all := []string{"title", "author", "series", "year", "publisher", "isbn"}
	v := SearchParams{Query: "x", SearchIn: all}.values()
	got := v["columns[]"]
	if len(got) != len(all) {
		t.Fatalf("combined columns[] = %v, want %d entries", got, len(all))
	}
	for i, col := range all {
		if got[i] != want[col] {
			t.Errorf("combined columns[][%d] = %q, want %q (%s)", i, got[i], want[col], col)
		}
	}
}

// TestSearchParamsOrderPagination verifies that every valid order maps to its
// libgen order code and that order_mode, res and page are emitted when set and
// omitted when left at their zero values.
func TestSearchParamsOrderPagination(t *testing.T) {
	orders := map[string]string{
		"id":         "f_id",
		"time_added": "time_added",
		"title":      "title",
		"author":     "author",
		"year":       "year",
		"size":       "filesize",
	}
	for order, code := range orders {
		t.Run("order_"+order, func(t *testing.T) {
			p := SearchParams{Query: "x", Order: order, OrderMode: "desc"}
			if err := p.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			v := p.values()
			if v.Get("order") != code {
				t.Errorf("order %q => %q, want %q", order, v.Get("order"), code)
			}
			if v.Get("ordermode") != "desc" {
				t.Errorf("ordermode = %q, want desc", v.Get("ordermode"))
			}
		})
	}

	// Full pagination: res and page emitted verbatim, ascending order mode.
	full := SearchParams{Query: "x", Order: "size", OrderMode: "asc", ResultsPerPage: 100, Page: 3}.values()
	if full.Get("res") != "100" || full.Get("page") != "3" || full.Get("ordermode") != "asc" || full.Get("order") != "filesize" {
		t.Errorf("full pagination values = %v", full)
	}

	// Page 1 is the default and must be omitted, unlike res.
	first := SearchParams{Query: "x", ResultsPerPage: 25, Page: 1}.values()
	if _, ok := first["page"]; ok {
		t.Errorf("page 1 should be omitted, values = %v", first)
	}
	if first.Get("res") != "25" {
		t.Errorf("res = %q, want 25", first.Get("res"))
	}

	// Nothing requested: order/ordermode/res/page all omitted.
	none := SearchParams{Query: "x"}.values()
	for _, k := range []string{"order", "ordermode", "res", "page"} {
		if _, ok := none[k]; ok {
			t.Errorf("values() includes %q that was not requested", k)
		}
	}
}

// TestParseDownloadOptions verifies the shape of the download options list. On the
// nonfiction fixture the first option is the "libgen" ads.php link (absolute) and
// the external mirrors (Anna's Archive, libgen.pw, Randombook) are captured with
// their labels. On the comics fixture the "libgen" option is the get.php link,
// which the parser must recognize and absolutize like ads.php.
func TestParseDownloadOptions(t *testing.T) {
	books := parseFixture(t, "search_books.html")
	if len(books.Results) == 0 {
		t.Fatal("0 results in the books fixture")
	}
	first := books.Results[0]
	if first.Downloads[0].Label != "libgen" ||
		!strings.HasPrefix(first.Downloads[0].URL, "https://libgen.li/ads.php?md5=") {
		t.Errorf("books first download = %+v, want libgen ads.php", first.Downloads[0])
	}
	labels := map[string]bool{}
	for _, d := range first.Downloads {
		labels[d.Label] = true
	}
	for _, want := range []string{"anna's archive", "libgen.pw", "Randombook"} {
		if !labels[want] {
			t.Errorf("books first result missing external mirror %q; labels = %v", want, labels)
		}
	}

	comics := parseFixture(t, "search_comics.html")
	if len(comics.Results) == 0 {
		t.Fatal("0 results in the comics fixture")
	}
	c := comics.Results[0]
	if c.Downloads[0].Label != "libgen" ||
		!strings.HasPrefix(c.Downloads[0].URL, "https://libgen.li/get.php?md5=") {
		t.Errorf("comics first download = %+v, want libgen get.php", c.Downloads[0])
	}
	if !md5Re.MatchString(c.MD5) {
		t.Errorf("comics first result md5 = %q, want valid 32-hex", c.MD5)
	}
}

// TestClientSearchValidationError verifies that Search rejects invalid parameters
// before issuing any HTTP request (the Validate() gate in Search).
func TestClientSearchValidationError(t *testing.T) {
	c := newTestClient(staticMirrors{"http://127.0.0.1:0"})
	if _, _, err := c.Search(context.Background(), SearchParams{Query: ""}); err == nil {
		t.Fatal("Search() with an empty query should fail without touching the network")
	}
}

// TestClientSearchAllMirrorsFailed verifies that when every mirror errors, Search
// propagates the transport failure instead of a parsed page.
func TestClientSearchAllMirrorsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, _, err := c.Search(context.Background(), SearchParams{Query: "golang"}); err == nil {
		t.Fatal("Search() should fail when every mirror is down")
	}
}

// TestPaginatorReachUnparsable verifies that a Paginator init script whose page
// count overflows an int yields a reach of 0 (unparsable → not truncated) rather
// than a panic or a bogus cap. The table is present with no rows, so the page
// parses to zero results.
func TestPaginatorReachUnparsable(t *testing.T) {
	const doc = `<html><body>` +
		`<script>new Paginator("paginator_example_top", 99999999999999999999, 25, 1)</script>` +
		`<table id="tablelibgen"></table></body></html>`
	page, err := ParseSearch(strings.NewReader(doc), "https://libgen.li")
	if err != nil {
		t.Fatalf("ParseSearch() error = %v", err)
	}
	if page.Reachable != 0 {
		t.Errorf("Reachable = %d, want 0 (unparsable paginator count)", page.Reachable)
	}
	if page.Truncated {
		t.Errorf("Truncated = true, want false (reach 0)")
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
