package discovery

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// readFixture reads a captured API response from testdata, failing the test when it
// cannot be read. The dblp and PubMed fixtures are verbatim live responses, so the
// parsers are exercised against the shapes the real services emit.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// setDblpBase points the package-level dblpBase at the given test server URL and
// restores it when the test ends, so an httptest server stands in for the live dblp
// SPARQL service.
func setDblpBase(t *testing.T, base string) {
	t.Helper()
	old := dblpBase
	dblpBase = base
	t.Cleanup(func() { dblpBase = old })
}

// serveFixture starts an httptest server that answers every request with the named
// testdata fixture, closing it when the test ends.
func serveFixture(t *testing.T, name string) *httptest.Server {
	t.Helper()
	body := readFixture(t, name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newUnpacedDBLP builds a DBLPProvider that never waits for the outbound bucket,
// for a test that asks one server twice and is about something other than pacing:
// dblp's real pace is one request per ten seconds, longer than a search's budget.
func newUnpacedDBLP() *DBLPProvider {
	return &DBLPProvider{client: newDiscoveryClient(), pace: pace{limit: rate.Inf, burst: 1}}
}

// dblpRequest is what a test server saw of one request.
type dblpRequest struct {
	method, path string
	header       http.Header
	query        url.Values
}

// recordDblpRequest starts a server that answers with the named fixture and keeps
// the last request it received, so a test can read what was asked.
func recordDblpRequest(t *testing.T, fixture string) *atomic.Pointer[dblpRequest] {
	t.Helper()
	body := readFixture(t, fixture)
	var last atomic.Pointer[dblpRequest]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last.Store(&dblpRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), query: r.URL.Query()})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	setDblpBase(t, srv.URL)
	return &last
}

// TestDblp_ParsesBindings verifies the happy path against a verbatim live SPARQL
// response for "attention is all you need": the records parse in the order the
// service ranked them, carrying the title, the authors in signature order, the
// year, the venue and the DOI, stamped with origin "dblp". Neither OpenAccess nor
// PDFURL may be set — dblp states nothing about free availability.
func TestDblp_ParsesBindings(t *testing.T) {
	srv := serveFixture(t, "dblp_sparql_search.json")
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "attention is all you need", 3)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search() returned %d results, want 3", len(got))
	}

	nips, corr := got[0], got[1]
	if nips.Origin != "dblp" {
		t.Errorf("Origin = %q, want dblp", nips.Origin)
	}
	if nips.Title != "Attention is All you Need." {
		t.Errorf("Title = %q, want the NeurIPS record first", nips.Title)
	}
	const wantAuthors = "Ashish Vaswani; Noam Shazeer; Niki Parmar; Jakob Uszkoreit; Llion Jones; Aidan N. Gomez; Lukasz Kaiser; Illia Polosukhin"
	if nips.Authors != wantAuthors {
		t.Errorf("Authors = %q, want them in signature order: %q", nips.Authors, wantAuthors)
	}
	if nips.Year != "2017" || nips.Venue != "NIPS" {
		t.Errorf("Year, Venue = %q, %q, want 2017, NIPS", nips.Year, nips.Venue)
	}
	if nips.DOI != "" {
		t.Errorf("DOI = %q, want empty: dblp records none for this paper", nips.DOI)
	}
	if corr.DOI != "10.48550/ARXIV.1706.03762" || corr.Venue != "CoRR" {
		t.Errorf("second record DOI, Venue = %q, %q, want the arXiv DOI under CoRR", corr.DOI, corr.Venue)
	}
	for i, r := range got {
		if r.OpenAccess {
			t.Errorf("result %d OpenAccess = true, want false (dblp states no availability)", i)
		}
		if r.PDFURL != "" {
			t.Errorf("result %d PDFURL = %q, want empty", i, r.PDFURL)
		}
	}
}

// TestDblp_DropsTheHomonymNumber verifies, against a live response, that the
// number dblp appends to tell namesakes apart ("Xiangyu Zhang 0005") is not shown
// as part of the name.
func TestDblp_DropsTheHomonymNumber(t *testing.T) {
	srv := serveFixture(t, "dblp_sparql_residual.json")
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "residual learning image recognition", 5)
	if err != nil || len(got) == 0 {
		t.Fatalf("Search() = %d results, %v, want results", len(got), err)
	}
	if got[0].Title != "Deep Residual Learning for Image Recognition." {
		t.Fatalf("first Title = %q, want the CVPR paper", got[0].Title)
	}
	if want := "Kaiming He; Xiangyu Zhang; Shaoqing Ren; Jian Sun"; got[0].Authors != want {
		t.Errorf("Authors = %q, want %q", got[0].Authors, want)
	}
	if got[0].DOI != "10.1109/CVPR.2016.90" {
		t.Errorf("DOI = %q, want 10.1109/CVPR.2016.90", got[0].DOI)
	}
}

// TestDblp_ZeroHits verifies a live "no matches" response yields no results and no
// error.
func TestDblp_ZeroHits(t *testing.T) {
	srv := serveFixture(t, "dblp_sparql_zero.json")
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "zzzqqqxxnonexistentquery12345", 10)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Search() returned %d results, want 0", len(got))
	}
}

// TestDblp_IsARangeSearcher verifies the year range is pushed into the query
// rather than applied to a page of results afterwards: the provider is a
// RangeSearcher, and a bounded search sends a FILTER on dblp:yearOfPublication
// with both bounds, requiring a year rather than leaving it optional. The live
// response for 2017 alone is parsed whole.
func TestDblp_IsARangeSearcher(t *testing.T) {
	var _ RangeSearcher = NewDBLP()
	last := recordDblpRequest(t, "dblp_sparql_years.json")

	got, err := searchProvider(context.Background(), NewDBLP(), "attention is all you need", 5, YearRange{From: 2017, To: 2017})
	if err != nil {
		t.Fatalf("searchProvider() error = %v", err)
	}
	if len(got) == 0 {
		t.Fatal("searchProvider() returned nothing from the year-bounded fixture")
	}
	for _, r := range got {
		if r.Year != "2017" {
			t.Errorf("result %q has year %q, want 2017", r.Title, r.Year)
		}
	}
	q := last.Load().query.Get("query")
	for _, want := range []string{
		`FILTER(?year >= "2017"^^xsd:gYear && ?year <= "2017"^^xsd:gYear)`,
		"?publ dblp:yearOfPublication ?year .",
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(q, want) {
				t.Errorf("query lacks %q:\n%s", want, q)
			}
		})
	}
	if strings.Contains(q, "OPTIONAL { ?publ dblp:yearOfPublication") {
		t.Errorf("a bounded query left the year optional:\n%s", q)
	}
}

// TestDblp_AnOpenSideIsFilledFromTheBounds verifies a range open on one side is
// sent as a closed interval, the open side taken from MinYear or MaxYear.
func TestDblp_AnOpenSideIsFilledFromTheBounds(t *testing.T) {
	q := dblpSPARQL([]string{"x"}, 5, YearRange{From: 2020})
	if want := `FILTER(?year >= "2020"^^xsd:gYear && ?year <= "2100"^^xsd:gYear)`; !strings.Contains(q, want) {
		t.Errorf("query lacks %q:\n%s", want, q)
	}
	q = dblpSPARQL([]string{"x"}, 5, YearRange{To: 1999})
	if want := `FILTER(?year >= "1000"^^xsd:gYear && ?year <= "1999"^^xsd:gYear)`; !strings.Contains(q, want) {
		t.Errorf("query lacks %q:\n%s", want, q)
	}
}

// TestDblp_RequestShape verifies the request the SPARQL service receives: a GET on
// /sparql asking for SPARQL JSON results, the service-side timeout, and a query
// that searches the words, keeps the year optional and asks for the limit.
func TestDblp_RequestShape(t *testing.T) {
	last := recordDblpRequest(t, "dblp_sparql_zero.json")

	if _, err := NewDBLP().Search(context.Background(), "Neural  Networks", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	r := last.Load()
	if r.method != http.MethodGet || r.path != "/sparql" {
		t.Errorf("request = %s %s, want GET /sparql", r.method, r.path)
	}
	if got := r.header.Get("Accept"); got != "application/sparql-results+json" {
		t.Errorf("Accept = %q, want application/sparql-results+json", got)
	}
	if got := r.header.Get("User-Agent"); got != discoveryUserAgent() {
		t.Errorf("User-Agent = %q, want the discovery agent", got)
	}
	if got := r.query.Get("timeout"); got != "5s" {
		t.Errorf("timeout = %q, want 5s", got)
	}
	q := r.query.Get("query")
	for _, want := range []string{
		`ql:contains-word "neural networks"`,
		"OPTIONAL { ?publ dblp:yearOfPublication ?year }",
		"ORDER BY STRLEN(?title) DESC(?year) LIMIT 5",
		// Editors are not authors: an editor-only record is not chosen, and only
		// author signatures are read.
		"FILTER EXISTS { ?publ dblp:authoredBy ?anyAuthor }\n    } ORDER BY",
		"?signature a dblp:AuthorSignature .",
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(q, want) {
				t.Errorf("query lacks %q:\n%s", want, q)
			}
		})
	}
	if strings.Contains(q, "FILTER(") {
		t.Errorf("an unbounded query carries a year FILTER:\n%s", q)
	}
}

// TestDblp_LimitClamped verifies a non-positive limit falls back to the default and
// an over-large limit is clamped to the maximum, both observed via the LIMIT.
func TestDblp_LimitClamped(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  string
	}{
		{name: "zero takes the default", limit: 0, want: "LIMIT 10\n"},
		{name: "negative takes the default", limit: -3, want: "LIMIT 10\n"},
		{name: "the maximum is kept", limit: 50, want: "LIMIT 50\n"},
		{name: "past the maximum is clamped", limit: 9999, want: "LIMIT 50\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(strings.SplitN(dblpQueryURL("https://x.invalid", []string{"q"}, tc.limit, YearRange{}), "?", 2)[1])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(q.Get("query"), tc.want) {
				t.Errorf("limit %d: query lacks %q", tc.limit, tc.want)
			}
		})
	}
}

// TestDblpWords pins how the caller's text becomes the searched words: letters and
// digits only, lowercased, de-duplicated and capped, so nothing the caller types
// can close the SPARQL literal, open a group, or reach QLever's prefix operator.
func TestDblpWords(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "plain title", query: "Attention Is All You Need", want: "attention is all you need"},
		{name: "a hyphen splits as the index does", query: "BERT: Pre-training", want: "bert pre training"},
		{name: "a quote and a brace cannot close anything", query: `foo" } ?x ?y ?z . { "`, want: "foo x y z"},
		{name: "a backslash escape is dropped", query: `a\" b\\`, want: "a b"},
		{name: "a newline and a tab are separators", query: "deep\nresidual\tlearning", want: "deep residual learning"},
		{name: "the prefix star is dropped", query: "atten* need*", want: "atten need"},
		{name: "a comment marker is dropped", query: "x # } LIMIT 1", want: "x limit 1"},
		{name: "letters beyond ascii are kept", query: "René Vidal Schrödinger", want: "rené vidal schrödinger"},
		{name: "a repeated word is asked once", query: "data data DATA banks", want: "data banks"},
		{name: "punctuation alone is no word", query: ` "{}\*?.`, want: ""},
		{name: "the count is capped", query: "a b c d e f g h i j k l m n", want: "a b c d e f g h i j k l"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(dblpWords(tc.query), " "); got != tc.want {
				t.Errorf("dblpWords(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

// dblpWordLiteral matches the text-search literal in a built query.
var dblpWordLiteral = regexp.MustCompile(`ql:contains-word "([^"]*)" \.`)

// TestDblpSPARQL_HostileQueryStaysOneLiteral builds the query for text crafted to
// break out of the literal and checks the result: one text-search literal, holding
// nothing but letters, digits and spaces, and no line of the template changed.
func TestDblpSPARQL_HostileQueryStaysOneLiteral(t *testing.T) {
	hostile := "x\" . ?publ ?p ?o } UNION { ?s ?p ?o \n} #\\u0022 *"
	q := dblpSPARQL(dblpWords(hostile), 5, YearRange{})
	m := dblpWordLiteral.FindAllStringSubmatch(q, -1)
	if len(m) != 1 {
		t.Fatalf("found %d text-search literals, want 1:\n%s", len(m), q)
	}
	if !regexp.MustCompile(`^[\p{L}\p{N} ]+$`).MatchString(m[0][1]) {
		t.Errorf("literal %q holds a character other than letters, digits and spaces", m[0][1])
	}
	if strings.Count(q, "UNION") != 0 || strings.Count(q, "\n") != strings.Count(dblpSPARQL([]string{"x"}, 5, YearRange{}), "\n") {
		t.Errorf("the hostile text changed the query's shape:\n%s", q)
	}
}

// TestDblp_NoWordAsksNothing verifies a query with nothing to search for returns
// at once, without spending the bucket or asking the service.
func TestDblp_NoWordAsksNothing(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked.Add(1) }))
	t.Cleanup(srv.Close)
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), ` "{}*.`, 5)
	if err != nil || got != nil {
		t.Errorf("Search() = %v, %v, want nil, nil", got, err)
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("the service was asked %d times, want 0", n)
	}
}

// TestDblpRate pins the pace to the Crawl-delay sparql.dblp.org's robots.txt names.
func TestDblpRate(t *testing.T) {
	if got := dblpRate(); got != 10*time.Second {
		t.Errorf("dblpRate() = %v, want 10s", got)
	}
	if got := NewDBLP().pace; got.limit != rate.Every(10*time.Second) || got.burst != 1 {
		t.Errorf("NewDBLP().pace = %+v, want one request per 10s with a burst of 1", got)
	}
}

// TestDblp_ASecondSearchInsideThePaceGoesWithoutDblp verifies the consequence of
// the pace: the next search's provider shares the bucket, cannot get a token
// within its budget, and returns empty without asking rather than waiting.
func TestDblp_ASecondSearchInsideThePaceGoesWithoutDblp(t *testing.T) {
	var asked atomic.Int32
	body := readFixture(t, "dblp_sparql_search.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	setDblpBase(t, srv.URL)

	if got, err := NewDBLP().Search(context.Background(), "attention", 3); err != nil || len(got) == 0 {
		t.Fatalf("first search = %d results, %v, want results", len(got), err)
	}
	start := time.Now()
	got, err := NewDBLP().Search(context.Background(), "attention", 3)
	if err != nil || got != nil {
		t.Errorf("second search = %v, %v, want nil, nil", got, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("second search took %v, want an immediate return", elapsed)
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("the service was asked %d times, want 1", n)
	}
}

// TestDblpDOI pins which IRIs carry a DOI.
func TestDblpDOI(t *testing.T) {
	cases := []struct {
		name string
		iri  string
		want string
	}{
		{name: "https resolver", iri: "https://doi.org/10.1109/CVPR.2016.90", want: "10.1109/CVPR.2016.90"},
		{name: "http resolver", iri: "http://doi.org/10.1/x", want: "10.1/x"},
		{name: "old dx resolver", iri: "https://dx.doi.org/10.1/x", want: "10.1/x"},
		{name: "old dx resolver over http", iri: "http://dx.doi.org/10.1/x", want: "10.1/x"},
		{name: "bare doi", iri: " 10.1/x ", want: "10.1/x"},
		{name: "absent", iri: "", want: ""},
		{name: "not a doi", iri: "https://doi.org/handle", want: ""},
		{name: "a prefix without a suffix", iri: "https://doi.org/10.1109", want: ""},
		{name: "another host", iri: "https://example.org/10.1/x", want: ""},
		{
			name: "a percent-encoded SICI doi is decoded",
			iri:  "https://doi.org/10.1002/(SICI)1097-4571(199806)49:8%3C693::AID-ASI4%3E3.0.CO;2-0",
			want: "10.1002/(SICI)1097-4571(199806)49:8<693::AID-ASI4>3.0.CO;2-0",
		},
		{name: "a broken encoding is kept as written", iri: "https://doi.org/10.1/a%zz", want: "10.1/a%zz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dblpDOI(tc.iri); got != tc.want {
				t.Errorf("dblpDOI(%q) = %q, want %q", tc.iri, got, tc.want)
			}
		})
	}
}

// TestDblpAuthorNames pins how the folded signatures become the author list:
// ordered by ordinal whatever order they arrived in, the homonym number dropped,
// an entry without a readable ordinal kept last, blanks skipped.
func TestDblpAuthorNames(t *testing.T) {
	cases := []struct {
		name   string
		folded string
		want   string
	}{
		{name: "reordered by ordinal", folded: "3 C\t1 A\t2 B", want: "A; B; C"},
		{name: "ordinal ten after two", folded: "10 J\t2 B", want: "B; J"},
		{name: "homonym number dropped", folded: "1 Xiangyu Zhang 0005", want: "Xiangyu Zhang"},
		{name: "a name ending in a short number is kept", folded: "1 Agent 007", want: "Agent 007"},
		{name: "a bare number is not stripped to nothing", folded: "1 0005", want: "0005"},
		{name: "an unreadable ordinal sorts last", folded: "Zed Unnumbered\t1 A", want: "A; Zed Unnumbered"},
		{name: "an ordinal with no name is skipped", folded: "1\t2 B", want: "B"},
		{name: "entities are unescaped", folded: "1 O&apos;Neil", want: "O'Neil"},
		{name: "nothing", folded: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dblpAuthorNames(tc.folded); got != tc.want {
				t.Errorf("dblpAuthorNames(%q) = %q, want %q", tc.folded, got, tc.want)
			}
		})
	}
}

// TestDblpVenue verifies several folded venues are joined and blanks dropped.
func TestDblpVenue(t *testing.T) {
	if got := dblpVenue("ICLR\t \tCoRR"); got != "ICLR; CoRR" {
		t.Errorf("dblpVenue = %q, want ICLR; CoRR", got)
	}
	if got := dblpVenue(""); got != "" {
		t.Errorf("dblpVenue(\"\") = %q, want empty", got)
	}
}

// TestParseDblpBindings_SkipsUntitledAndHonoursTheLimit verifies a binding with no
// title is not a result, and that no more than limit results are returned even
// when the service sends more.
func TestParseDblpBindings_SkipsUntitledAndHonoursTheLimit(t *testing.T) {
	body := []byte(`{"results":{"bindings":[
		{"year":{"value":"2020"}},
		{"title":{"value":"One."}},
		{"title":{"value":"Two."}},
		{"title":{"value":"Three."}}]}}`)
	got := parseDblpBindings(body, 2)
	if len(got) != 2 || got[0].Title != "One." || got[1].Title != "Two." {
		t.Errorf("parseDblpBindings = %+v, want One. and Two.", got)
	}
}

// TestDblp_MalformedJSONReturnsNil verifies a body that cannot be decoded as SPARQL
// results yields nil rather than panicking, honoring the best-effort contract that
// a malformed response is treated as no results.
func TestDblp_MalformedJSONReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on a malformed body", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a malformed body", got)
	}
}

// TestDblp_Non200ReturnsEmpty verifies a non-200 response, such as the 400 QLever
// answers a query it cannot parse with, degrades to an empty result with no error.
func TestDblp_Non200ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"exception":"Invalid SPARQL query","status":"ERROR"}`))
	}))
	defer srv.Close()
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on non-200", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on non-200", got)
	}
}

// TestDblp_TransportErrorReturnsEmpty verifies a transport failure with a live
// (non-canceled) context degrades to an empty result with no error.
func TestDblp_TransportErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // close so the address refuses connections
	setDblpBase(t, base)

	got, err := NewDBLP().Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on a transport error", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a transport error", got)
	}
}

// TestDblp_ContextCancelled verifies a canceled context surfaces as the returned
// error rather than being softened to an empty result.
func TestDblp_ContextCancelled(t *testing.T) {
	srv := serveFixture(t, "dblp_sparql_search.json")
	setDblpBase(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := NewDBLP().Search(ctx, "attention", 5)
	if err == nil {
		t.Fatal("Search() error = nil, want a context error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a canceled ctx", got)
	}
}

// TestDblp_ContextDeadlineDuringRequest verifies the context-error branch reached
// AFTER the request is in flight: the limiter admits the call, the server then blocks
// until the client's short deadline expires, and Search propagates that context error
// instead of softening it to empty.
func TestDblp_ContextDeadlineDuringRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	setDblpBase(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := NewDBLP().Search(ctx, "attention", 5)
	if err == nil {
		t.Fatal("Search() error = nil, want a context deadline error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a deadline error", got)
	}
}

// TestDBLPProvider_Name verifies the provider stamps the "dblp" origin.
func TestDBLPProvider_Name(t *testing.T) {
	if got := NewDBLP().Name(); got != "dblp" {
		t.Errorf("Name() = %q, want %q", got, "dblp")
	}
}

// captureDefaultLog routes the default slog logger into a buffer for the rest of
// the test and restores the previous one afterwards.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestDblp_ARefusalSilencesTheHostForAWindow drives the refusals a dblp host gives
// an automated client: the Anubis bot-check page recorded from dblp.uni-trier.de
// on 2026-10-03, served with 200 and text/html in place of the JSON (the shape a
// bot wall put in front of the SPARQL service would take), and a bare 429. Each
// must degrade to an empty result, be logged once, and stop the next search from
// asking at all, because the next search builds a fresh provider and a cooldown
// kept on the provider would be forgotten.
func TestDblp_ARefusalSilencesTheHostForAWindow(t *testing.T) {
	challenge := readFixture(t, "dblp_anubis_challenge.html")
	cases := []struct {
		name   string
		status int
		body   []byte
	}{
		{name: "anubis bot check", status: http.StatusOK, body: challenge},
		{name: "rate limited", status: http.StatusTooManyRequests, body: []byte("<html><body><h1>429 Too Many Requests</h1></body></html>")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var asked atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				asked.Add(1)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			t.Cleanup(srv.Close)
			setDblpBase(t, srv.URL)
			logs := captureDefaultLog(t)

			for i := range 2 {
				got, err := newUnpacedDBLP().Search(context.Background(), "attention", 5)
				if err != nil || got != nil {
					t.Fatalf("search %d = %v, %v, want nil, nil", i+1, got, err)
				}
			}
			if n := asked.Load(); n != 1 {
				t.Errorf("dblp was asked %d times, want 1: the second search must wait out the window", n)
			}
			if n := strings.Count(logs.String(), "dblp search"); n != 1 {
				t.Errorf("logged %d times, want once per window:\n%s", n, logs.String())
			}
		})
	}
}

// TestDblp_AnOrdinaryFailureOpensNoWindow pins the other side: a 500 is a broken
// service, not a refusal, and must not stop the next search from asking.
func TestDblp_AnOrdinaryFailureOpensNoWindow(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	setDblpBase(t, srv.URL)

	for range 2 {
		if _, err := newUnpacedDBLP().Search(context.Background(), "attention", 5); err != nil {
			t.Fatalf("Search() error = %v", err)
		}
	}
	if n := asked.Load(); n != 2 {
		t.Errorf("dblp was asked %d times, want 2", n)
	}
}

// TestDblpRefused pins what counts as dblp declining to answer.
func TestDblpRefused(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "json", status: http.StatusOK, body: ` {"head":{},"results":{"bindings":[]}}`, want: false},
		{name: "malformed json is an answer", status: http.StatusOK, body: `{not json`, want: false},
		{name: "html page", status: http.StatusOK, body: "<!doctype html><title>Making sure you're not a bot!</title>", want: true},
		{name: "empty 200", status: http.StatusOK, body: "  ", want: true},
		{name: "json after a byte order mark", status: http.StatusOK, body: "\xef\xbb\xbf\n{\"results\":{}}", want: false},
		{name: "html after a byte order mark", status: http.StatusOK, body: "\xef\xbb\xbf<html></html>", want: true},
		{name: "403", status: http.StatusForbidden, body: "<html></html>", want: true},
		{name: "429", status: http.StatusTooManyRequests, body: "", want: true},
		{name: "503", status: http.StatusServiceUnavailable, body: "", want: true},
		{name: "400 from a query the service cannot parse", status: http.StatusBadRequest, body: `{"exception":"x"}`, want: false},
		{name: "500 html", status: http.StatusInternalServerError, body: "<html></html>", want: false},
		{name: "404", status: http.StatusNotFound, body: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dblpRefused(tc.status, []byte(tc.body)); got != tc.want {
				t.Errorf("dblpRefused(%d, %q) = %t, want %t", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
