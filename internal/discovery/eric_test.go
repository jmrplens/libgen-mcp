package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// setERICBase points the package-level ericBase at the given test server URL and
// restores it when the test ends, so an httptest server stands in for the live ERIC
// API.
func setERICBase(t *testing.T, base string) {
	t.Helper()
	old := ericBase
	ericBase = base
	t.Cleanup(func() { ericBase = old })
}

// TestERIC_ParsesGreyLiterature verifies the happy path against a verbatim live ERIC
// response: eight docs parse into eight results stamped with origin "eric", and the
// ED report ERIC hosts a full text for carries the deterministic files.eric.ed.gov
// PDF URL and is marked open access.
func TestERIC_ParsesGreyLiterature(t *testing.T) {
	srv := serveFixture(t, "eric_search.json")
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "professional development", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 8 {
		t.Fatalf("Search() returned %d results, want 8", len(got))
	}

	byTitle := map[string]DiscoveryResult{}
	for _, r := range got {
		byTitle[r.Title] = r
	}

	report, ok := byTitle["Inquiry and Action: Implementation Guide for Professional Development Facilitators. Revised."]
	if !ok {
		t.Fatalf("Search() did not return the hosted ED report; got %+v", got)
	}
	if report.Origin != "eric" {
		t.Errorf("report.Origin = %q, want eric", report.Origin)
	}
	if report.Authors != "Drennon, Cassandra; Erno, Susan" {
		t.Errorf("report.Authors = %q, want the two joined author names", report.Authors)
	}
	if report.Year != "1998" {
		t.Errorf("report.Year = %q, want 1998", report.Year)
	}
	if report.PDFURL != "https://files.eric.ed.gov/fulltext/ED427241.pdf" {
		t.Errorf("report.PDFURL = %q, want the deterministic ED427241 full-text URL", report.PDFURL)
	}
	if !report.OpenAccess {
		t.Error("report.OpenAccess = false, want true: ERIC hosts this report's full text")
	}
	if report.Venue != "Virginia Commonwealth Univ., Richmond. Virginia Adult Education and Literacy Resource Center." {
		t.Errorf("report.Venue = %q, want the performing institution", report.Venue)
	}
	if report.DOI != "" {
		t.Errorf("report.DOI = %q, want empty: the record states no DOI", report.DOI)
	}
}

// TestERIC_MetadataOnlyRecordCarriesNoFullText verifies the ED record whose
// e_fulltextauth flag is 0 is surfaced as a bibliographic record only: no PDF URL is
// invented for it, and it is not claimed to be open access. files.eric.ed.gov answers
// 404 for exactly these ids, so guessing the URL would hand the caller a dead link.
func TestERIC_MetadataOnlyRecordCarriesNoFullText(t *testing.T) {
	srv := serveFixture(t, "eric_search.json")
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "professional development", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}

	var found bool
	for _, r := range got {
		if r.Title != "Missouri Professional Development Guidelines for Student Success." {
			continue
		}
		found = true
		if r.PDFURL != "" {
			t.Errorf("PDFURL = %q, want empty for a record ERIC does not host", r.PDFURL)
		}
		if r.OpenAccess {
			t.Error("OpenAccess = true, want false for a metadata-only record")
		}
	}
	if !found {
		t.Fatal("Search() did not return the metadata-only ED record")
	}
}

// TestERIC_JournalRecordMapsDOIAndVenue verifies a journal record contributes the DOI
// hidden in its url field — the one key download's existing chain can act on — plus a
// citation-shaped venue built from the journal name and its volume/issue string.
func TestERIC_JournalRecordMapsDOIAndVenue(t *testing.T) {
	srv := serveFixture(t, "eric_search.json")
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "professional development", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}

	var found bool
	for _, r := range got {
		if !strings.HasPrefix(r.Title, "Analyzing Principal Professional Development Practices") {
			continue
		}
		found = true
		if r.DOI != "10.1080/19415257.2013.821667" {
			t.Errorf("DOI = %q, want the doi extracted from the dx.doi.org url", r.DOI)
		}
		if r.Venue != "Professional Development in Education, v40 n2 p295-315 2014" {
			t.Errorf("Venue = %q, want journal name plus sourceid", r.Venue)
		}
	}
	if !found {
		t.Fatal("Search() did not return the journal record")
	}
}

// TestERIC_NonDOIURLIsNotReadAsADOI verifies a record whose url points at a
// dissertation gateway rather than doi.org contributes no DOI, and that its ISBN is
// surfaced with ERIC's "ISBN-" label stripped so it can be used as a lookup key.
func TestERIC_NonDOIURLIsNotReadAsADOI(t *testing.T) {
	srv := serveFixture(t, "eric_search.json")
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "professional development", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}

	var checkedGateway, checkedISBN bool
	for _, r := range got {
		if strings.HasPrefix(r.Title, "Gifted and Talented Professional Development") {
			checkedGateway = true
			if r.DOI != "" {
				t.Errorf("DOI = %q, want empty for a proquest gateway url", r.DOI)
			}
			if r.ISBN != "979-8-8193-7856-4" {
				t.Errorf("ISBN = %q, want the unprefixed isbn", r.ISBN)
			}
		}
		if strings.HasPrefix(r.Title, "Effective Professional Development") {
			checkedISBN = true
			if r.ISBN != "0-7785-4752-3" {
				t.Errorf("ISBN = %q, want the ISBN- label stripped", r.ISBN)
			}
		}
	}
	if !checkedGateway || !checkedISBN {
		t.Fatal("Search() did not return both records under test")
	}
}

// TestERIC_UnescapesEntities verifies XML entities are undone before a record reaches
// the caller. ERIC stores its text escaped and serves it to the JSON API that way, so
// a title with an apostrophe arrives as "Teachers&apos;" and would otherwise be shown
// as literal markup — a live search returned exactly that.
func TestERIC_UnescapesEntities(t *testing.T) {
	srv := serveFixture(t, "eric_entities.json")
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "future ready case study", 1)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search() returned %d results, want 1", len(got))
	}
	const wantTitle = "Future Ready Case Study #3: Supporting Teachers' Effective Use of Technology through Coaching"
	if got[0].Title != wantTitle {
		t.Errorf("Title = %q, want %q", got[0].Title, wantTitle)
	}
}

// TestERIC_ZeroHits verifies a query the index matches nothing for yields no results
// and no error, against a verbatim live empty response.
func TestERIC_ZeroHits(t *testing.T) {
	srv := serveFixture(t, "eric_zero_hits.json")
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "zzzqqqxxxyyynotathing", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() returned %d results, want 0", len(got))
	}
}

// TestERIC_SolrErrorEnvelope verifies the shape ERIC returns for a query its Solr
// parser rejects: HTTP 200 carrying an error object instead of a response. That must
// degrade to no results rather than being read as a successful empty page or an error.
func TestERIC_SolrErrorEnvelope(t *testing.T) {
	srv := serveFixture(t, "eric_solr_error.json")
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), `reading"`, 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() returned %d results, want 0", len(got))
	}
}

// TestERIC_MalformedJSON verifies a body that is not JSON at all degrades to an empty
// result with no error, keeping the provider best-effort.
func TestERIC_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	t.Cleanup(srv.Close)
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "reading", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() returned %d results, want 0", len(got))
	}
}

// TestERIC_NonOKStatus verifies a non-200 response degrades to an empty result with no
// error, so a failing ERIC never sinks a federated search.
func TestERIC_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "reading", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() returned %d results, want 0", len(got))
	}
}

// TestERIC_TransportFailure verifies an unreachable host degrades to an empty result
// with no error rather than propagating a transport failure.
func TestERIC_TransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()
	setERICBase(t, base)

	got, err := NewERIC().Search(context.Background(), "reading", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() returned %d results, want 0", len(got))
	}
}

// TestERIC_CanceledContext verifies a canceled context propagates as an error instead
// of being softened to an empty result, so federation can tell "caller went away" from
// "source degraded".
func TestERIC_CanceledContext(t *testing.T) {
	srv := serveFixture(t, "eric_search.json")
	setERICBase(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewERIC().Search(ctx, "reading", 8); err == nil {
		t.Fatal("Search() error = nil, want a context error")
	}
}

// TestERIC_SearchURL verifies the request the provider builds: the query escaped for
// Solr and joined with AND so a colon or stray quote in a title cannot turn into a
// syntax error, the JSON format, the clamped row count, and an explicit field list so
// the response never carries the multi-kilobyte abstracts the result shape has no room
// for.
func TestERIC_SearchURL(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write(readFixture(t, "eric_zero_hits.json"))
	}))
	t.Cleanup(srv.Close)
	setERICBase(t, srv.URL)

	if _, err := NewERIC().Search(context.Background(), `Reading: a "study"`, 500); err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if got := gotQuery.Get("search"); got != `Reading\: AND a AND \"study\"` {
		t.Errorf("search = %q, want the escaped AND-joined query", got)
	}
	if got := gotQuery.Get("format"); got != "json" {
		t.Errorf("format = %q, want json", got)
	}
	if got := gotQuery.Get("rows"); got != "50" {
		t.Errorf("rows = %q, want the clamped ceiling 50", got)
	}
	if got := gotQuery.Get("fields"); !strings.Contains(got, "e_fulltextauth") {
		t.Errorf("fields = %q, want it to request e_fulltextauth", got)
	}
}

// TestERIC_DefaultLimit verifies a non-positive limit falls back to the default row
// count rather than being sent to ERIC as-is.
func TestERIC_DefaultLimit(t *testing.T) {
	var gotRows string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRows = r.URL.Query().Get("rows")
		_, _ = w.Write(readFixture(t, "eric_zero_hits.json"))
	}))
	t.Cleanup(srv.Close)
	setERICBase(t, srv.URL)

	if _, err := NewERIC().Search(context.Background(), "reading", 0); err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if gotRows != "10" {
		t.Errorf("rows = %q, want the default 10", gotRows)
	}
}

// TestERIC_UnusableQuery verifies a query that escapes to nothing usable — only
// whitespace — never reaches ERIC, since an empty search parameter would either error
// or match the whole index.
func TestERIC_UnusableQuery(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write(readFixture(t, "eric_zero_hits.json"))
	}))
	t.Cleanup(srv.Close)
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "   \t  ", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() returned %d results, want 0", len(got))
	}
	if called {
		t.Error("Search() hit the API for a query with no usable terms")
	}
}

// TestERIC_SkipsRecordWithoutTitle verifies a doc ERIC returns with no title is
// dropped: it carries nothing a caller could read, cite or fetch.
func TestERIC_SkipsRecordWithoutTitle(t *testing.T) {
	const body = `{"response":{"numFound":2,"start":0,"docs":[{"id":"ED111111","e_fulltextauth":1},` +
		`{"id":"ED222222","title":"A Real Report","e_fulltextauth":1}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "reading", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Title != "A Real Report" {
		t.Fatalf("Search() = %+v, want only the titled record", got)
	}
}

// TestERIC_SkipsRecordWithoutID verifies a doc with no id yields no full-text URL even
// when its flag says ERIC hosts one, since the URL is derived from the id.
func TestERIC_SkipsRecordWithoutID(t *testing.T) {
	const body = `{"response":{"numFound":1,"start":0,"docs":[{"title":"Untraceable Report","e_fulltextauth":1}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "reading", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search() returned %d results, want 1", len(got))
	}
	if got[0].PDFURL != "" || got[0].OpenAccess {
		t.Errorf("got %+v, want no pdf_url and no open-access claim without an id", got[0])
	}
}

// TestERIC_MissingYear verifies a record ERIC files without a publication year yields
// an empty year rather than the string "0".
func TestERIC_MissingYear(t *testing.T) {
	const body = `{"response":{"numFound":1,"start":0,"docs":[{"id":"ED333333","title":"Undated Report"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	setERICBase(t, srv.URL)

	got, err := NewERIC().Search(context.Background(), "reading", 8)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Year != "" {
		t.Fatalf("got %+v, want a single result with an empty year", got)
	}
}

// TestEricVenue covers the placement fields ERIC records carry in every combination,
// including the sourceid-only and institution-only shapes a report can arrive in.
func TestEricVenue(t *testing.T) {
	cases := []struct {
		name string
		doc  ericDoc
		want string
	}{
		{"source and sourceid", ericDoc{Source: "Journal X", SourceID: "v1 n2 p3-4 2020"}, "Journal X, v1 n2 p3-4 2020"},
		{"source only", ericDoc{Source: "Online Submission"}, "Online Submission"},
		{"sourceid only", ericDoc{SourceID: "Ed.D. Dissertation, Baylor University"}, "Ed.D. Dissertation, Baylor University"},
		{"institution fallback", ericDoc{Institution: []string{"", "Missouri State Dept."}}, "Missouri State Dept."},
		{"nothing stated", ericDoc{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ericVenue(tc.doc); got != tc.want {
				t.Errorf("ericVenue() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEricDOIFromURL covers the link shapes ERIC files with its records, including the
// unparseable one that must not be mistaken for an identifier.
func TestEricDOIFromURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"dx.doi.org", "http://dx.doi.org/10.1080/19415257.2013.821667", "10.1080/19415257.2013.821667"},
		{"doi.org https", "https://doi.org/10.1007/s40593-020-00201-7", "10.1007/s40593-020-00201-7"},
		{"www prefix", "https://www.doi.org/10.1/abc", "10.1/abc"},
		{"publisher page", "http://www.rowmaneducation.com/Catalog/SingleBook.shtml", ""},
		{"empty", "", ""},
		{"unparseable", "http://[::1]bad", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ericDOIFromURL(tc.raw); got != tc.want {
				t.Errorf("ericDOIFromURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestERIC_Name verifies the origin label the provider stamps on its results.
func TestERIC_Name(t *testing.T) {
	if got := NewERIC().Name(); got != "eric" {
		t.Errorf("Name() = %q, want eric", got)
	}
}

// TestERIC_RateLimited verifies the provider paces itself: two back-to-back searches
// cannot both go through instantly, so ERIC is never hammered by a burst.
func TestERIC_RateLimited(t *testing.T) {
	srv := serveFixture(t, "eric_zero_hits.json")
	setERICBase(t, srv.URL)

	p := NewERIC()
	start := time.Now()
	for range 2 {
		if _, err := p.Search(context.Background(), "reading", 1); err != nil {
			t.Fatalf("Search() error = %v, want nil", err)
		}
	}
	if elapsed := time.Since(start); elapsed < ericRate/2 {
		t.Errorf("two searches took %v, want at least %v of limiter delay", elapsed, ericRate/2)
	}
}
