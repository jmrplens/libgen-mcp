package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// setPubMedBase points the package-level pubmedBase at the given test server URL and
// restores it when the test ends, so an httptest server stands in for the live NCBI
// E-utilities endpoints.
func setPubMedBase(t *testing.T, base string) {
	t.Helper()
	old := pubmedBase
	pubmedBase = base
	t.Cleanup(func() { pubmedBase = old })
}

// pubmedStub is a fake E-utilities service: it answers esearch.fcgi and
// esummary.fcgi with the bodies and statuses configured on it, records the query
// parameters of each hop, and counts esummary calls so a test can assert the second
// hop was skipped.
type pubmedStub struct {
	searchBody    []byte
	searchStatus  int
	summaryBody   []byte
	summaryStatus int

	searchQuery  url.Values
	summaryQuery url.Values
	summaryCalls atomic.Int32
}

// serveESearch writes the configured esearch response, recording the query it saw.
func (s *pubmedStub) serveESearch(w http.ResponseWriter, r *http.Request) {
	s.searchQuery = r.URL.Query()
	if s.searchStatus != 0 {
		w.WriteHeader(s.searchStatus)
		return
	}
	_, _ = w.Write(s.searchBody)
}

// serveESummary writes the configured esummary response, recording the query it saw
// and counting the call.
func (s *pubmedStub) serveESummary(w http.ResponseWriter, r *http.Request) {
	s.summaryCalls.Add(1)
	s.summaryQuery = r.URL.Query()
	if s.summaryStatus != 0 {
		w.WriteHeader(s.summaryStatus)
		return
	}
	_, _ = w.Write(s.summaryBody)
}

// start mounts the stub on an httptest server, points pubmedBase at it, and closes it
// when the test ends.
func (s *pubmedStub) start(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/esearch.fcgi", s.serveESearch)
	mux.HandleFunc("/esummary.fcgi", s.serveESummary)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	setPubMedBase(t, srv.URL)
}

// newPubMedStub builds a stub serving the two verbatim live fixtures for the happy
// path, and starts it.
func newPubMedStub(t *testing.T) *pubmedStub {
	t.Helper()
	stub := &pubmedStub{
		searchBody:  readFixture(t, "pubmed_esearch.json"),
		summaryBody: readFixture(t, "pubmed_esummary.json"),
	}
	stub.start(t)
	return stub
}

// TestPubMed_ParsesRecords verifies the happy path across both hops against verbatim
// live responses: three PMIDs resolve to three results carrying title, "; "-joined
// authors, year, journal name as the venue and the DOI, stamped with origin "pubmed".
// OpenAccess must stay false and PDFURL empty — PubMed indexes the literature without
// stating that any of it is free to read.
func TestPubMed_ParsesRecords(t *testing.T) {
	newPubMedStub(t)

	got, err := NewPubMed("").Search(context.Background(), "crispr gene editing", 3)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search() returned %d results, want 3", len(got))
	}

	first := got[0]
	if first.Origin != "pubmed" {
		t.Errorf("first.Origin = %q, want pubmed", first.Origin)
	}
	const wantTitle = "CRISPR-Cas9 system: A new-fangled dawn in gene editing."
	if first.Title != wantTitle {
		t.Errorf("first.Title = %q, want %q", first.Title, wantTitle)
	}
	const wantAuthors = "Gupta D; Bhattacharjee O; Mandal D; Sen MK; Dey D; Dasgupta A; Kazi TA; " +
		"Gupta R; Sinharoy S; Acharya K; Chattopadhyay D; Ravichandiran V; Roy S; Ghosh D"
	if first.Authors != wantAuthors {
		t.Errorf("first.Authors = %q, want every author joined: %q", first.Authors, wantAuthors)
	}
	if first.Year != "2019" {
		t.Errorf("first.Year = %q, want 2019", first.Year)
	}
	if first.Venue != "Life sciences" {
		t.Errorf("first.Venue = %q, want the full journal name", first.Venue)
	}
	if first.DOI != "10.1016/j.lfs.2019.116636" {
		t.Errorf("first.DOI = %q, want the record DOI", first.DOI)
	}
	for i, r := range got {
		if r.OpenAccess {
			t.Errorf("result %d OpenAccess = true, want false (PubMed states no availability)", i)
		}
		if r.PDFURL != "" {
			t.Errorf("result %d PDFURL = %q, want empty (PubMed is an index)", i, r.PDFURL)
		}
	}
}

// TestPubMed_RecordWithoutDOI verifies a record whose articleids carry no doi entry
// still yields a usable bibliographic result — title, authors, year and venue — with
// an empty DOI rather than being dropped or mislabeled. The fixture is a pair of real
// 1955 records, which predate DOIs entirely.
func TestPubMed_RecordWithoutDOI(t *testing.T) {
	stub := &pubmedStub{
		searchBody:  readFixture(t, "pubmed_esearch.json"),
		summaryBody: readFixture(t, "pubmed_esummary_nodoi.json"),
	}
	stub.start(t)

	got, err := NewPubMed("").Search(context.Background(), "cancer", 2)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}
	if got[0].DOI != "" {
		t.Errorf("got[0].DOI = %q, want empty (the record has no doi articleid)", got[0].DOI)
	}
	if got[0].Title != "[Ovarectomy versus roentgen castration in carcinoma mammae]." {
		t.Errorf("got[0].Title = %q, want the record title", got[0].Title)
	}
	if got[0].Authors != "SCHONBAUER L; SCHMIDT-UEBERREITER E" {
		t.Errorf("got[0].Authors = %q, want both joined author names", got[0].Authors)
	}
	if got[0].Year != "1955" {
		t.Errorf("got[0].Year = %q, want 1955", got[0].Year)
	}
	if got[0].Venue != "Wiener klinische Wochenschrift" {
		t.Errorf("got[0].Venue = %q, want the journal name", got[0].Venue)
	}
}

// TestPubMed_ZeroHitsSkipsSummary verifies that when esearch reports no PMIDs the
// provider returns nothing AND never issues the second hop, so a miss costs one
// request rather than two.
func TestPubMed_ZeroHitsSkipsSummary(t *testing.T) {
	stub := &pubmedStub{
		searchBody:  readFixture(t, "pubmed_esearch_zero.json"),
		summaryBody: readFixture(t, "pubmed_esummary.json"),
	}
	stub.start(t)

	got, err := NewPubMed("").Search(context.Background(), "zzzqqqxxnonexistentquery12345", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Search() returned %d results, want 0", len(got))
	}
	if n := stub.summaryCalls.Load(); n != 0 {
		t.Errorf("esummary was called %d times on zero hits, want 0", n)
	}
}

// pubmedErrorRecordFixture is an esummary response whose single uid resolves to the
// error object NCBI returns for an unretrievable record: no title, no authors, just an
// error string. Such a record carries nothing a caller could use, so it is skipped.
const pubmedErrorRecordFixture = `{"header":{"type":"esummary","version":"0.3"},
"result":{"uids":["999999999"],
"999999999":{"uid":"999999999","error":"cannot get document summary"}}}`

// TestPubMed_SkipsUnusableRecords verifies an esummary record with no title (NCBI's
// error object for an unretrievable PMID) contributes no result.
func TestPubMed_SkipsUnusableRecords(t *testing.T) {
	stub := &pubmedStub{
		searchBody:  readFixture(t, "pubmed_esearch.json"),
		summaryBody: []byte(pubmedErrorRecordFixture),
	}
	stub.start(t)

	got, err := NewPubMed("").Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Search() returned %d results, want 0 (the only record is unusable)", len(got))
	}
}

// TestPubMed_MalformedSearchReturnsEmpty verifies a malformed esearch body degrades to
// no results with no error, and never reaches the second hop.
func TestPubMed_MalformedSearchReturnsEmpty(t *testing.T) {
	stub := &pubmedStub{searchBody: []byte("{not json")}
	stub.start(t)

	got, err := NewPubMed("").Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on a malformed esearch body", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results", got)
	}
	if n := stub.summaryCalls.Load(); n != 0 {
		t.Errorf("esummary was called %d times after a malformed esearch, want 0", n)
	}
}

// TestPubMed_MalformedSummaryReturnsEmpty verifies a malformed esummary body degrades
// to no results with no error.
func TestPubMed_MalformedSummaryReturnsEmpty(t *testing.T) {
	stub := &pubmedStub{
		searchBody:  readFixture(t, "pubmed_esearch.json"),
		summaryBody: []byte("{not json"),
	}
	stub.start(t)

	got, err := NewPubMed("").Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on a malformed esummary body", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results", got)
	}
}

// TestPubMed_Non200ReturnsEmpty verifies a non-200 on either hop degrades to an empty
// result with no error, so a failing provider never sinks a federated search.
func TestPubMed_Non200ReturnsEmpty(t *testing.T) {
	cases := []struct {
		name          string
		searchStatus  int
		summaryStatus int
	}{
		{name: "esearch fails", searchStatus: http.StatusTooManyRequests},
		{name: "esummary fails", summaryStatus: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &pubmedStub{
				searchBody:    readFixture(t, "pubmed_esearch.json"),
				summaryBody:   readFixture(t, "pubmed_esummary.json"),
				searchStatus:  tc.searchStatus,
				summaryStatus: tc.summaryStatus,
			}
			stub.start(t)

			got, err := NewPubMed("").Search(context.Background(), "q", 5)
			if err != nil {
				t.Fatalf("Search() error = %v, want nil on non-200", err)
			}
			if got != nil {
				t.Errorf("Search() = %v, want nil results on non-200", got)
			}
		})
	}
}

// TestPubMed_TransportErrorReturnsEmpty verifies a transport failure with a live
// (non-canceled) context degrades to an empty result with no error.
func TestPubMed_TransportErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // close so the address refuses connections
	setPubMedBase(t, base)

	got, err := NewPubMed("").Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on a transport error", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a transport error", got)
	}
}

// TestPubMed_ContextCancelled verifies a canceled context surfaces as the returned
// error rather than being softened to an empty result.
func TestPubMed_ContextCancelled(t *testing.T) {
	newPubMedStub(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := NewPubMed("").Search(ctx, "crispr", 5)
	if err == nil {
		t.Fatal("Search() error = nil, want a context error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a canceled ctx", got)
	}
}

// TestPubMed_ContextDeadlineDuringRequest verifies the context-error branch reached
// AFTER a request is in flight: the limiter admits the call, the server then blocks
// until the client's short deadline expires, and Search propagates that context error
// instead of softening it to empty.
func TestPubMed_ContextDeadlineDuringRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	setPubMedBase(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := NewPubMed("").Search(ctx, "crispr", 5)
	if err == nil {
		t.Fatal("Search() error = nil, want a context deadline error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a deadline error", got)
	}
}

// TestPubMed_RequestParameters verifies both hops carry the NCBI etiquette
// parameters: db=pubmed, the tool name, relevance sorting and JSON on esearch, and
// the comma-joined PMIDs on esummary.
func TestPubMed_RequestParameters(t *testing.T) {
	stub := newPubMedStub(t)

	if _, err := NewPubMed("").Search(context.Background(), "crispr gene editing", 3); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	search := stub.searchQuery
	if got := search.Get("db"); got != "pubmed" {
		t.Errorf("esearch db = %q, want pubmed", got)
	}
	if got := search.Get("term"); got != "crispr gene editing" {
		t.Errorf("esearch term = %q, want the query", got)
	}
	if got := search.Get("retmode"); got != "json" {
		t.Errorf("esearch retmode = %q, want json", got)
	}
	if got := search.Get("retmax"); got != "3" {
		t.Errorf("esearch retmax = %q, want 3", got)
	}
	if got := search.Get("sort"); got != "relevance" {
		t.Errorf("esearch sort = %q, want relevance", got)
	}
	if got := search.Get("tool"); got != "libgen-mcp" {
		t.Errorf("esearch tool = %q, want libgen-mcp", got)
	}

	summary := stub.summaryQuery
	if got := summary.Get("id"); got != "31295471,38786024,36656942" {
		t.Errorf("esummary id = %q, want the comma-joined PMIDs from esearch", got)
	}
	if got := summary.Get("db"); got != "pubmed" {
		t.Errorf("esummary db = %q, want pubmed", got)
	}
	if got := summary.Get("tool"); got != "libgen-mcp" {
		t.Errorf("esummary tool = %q, want libgen-mcp", got)
	}
}

// TestPubMed_ContactEmail verifies NCBI's optional email parameter is sent on both
// hops when a contact is configured, and omitted entirely when none is — the provider
// never invents an address.
func TestPubMed_ContactEmail(t *testing.T) {
	stub := newPubMedStub(t)

	if _, err := NewPubMed("dev@example.com").Search(context.Background(), "q", 3); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if got := stub.searchQuery.Get("email"); got != "dev@example.com" {
		t.Errorf("esearch email = %q, want dev@example.com", got)
	}
	if got := stub.summaryQuery.Get("email"); got != "dev@example.com" {
		t.Errorf("esummary email = %q, want dev@example.com", got)
	}

	if _, err := NewPubMed("  ").Search(context.Background(), "q", 3); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if _, ok := stub.searchQuery["email"]; ok {
		t.Error("esearch carried an email with none configured, want absent")
	}
	if _, ok := stub.summaryQuery["email"]; ok {
		t.Error("esummary carried an email with none configured, want absent")
	}
}

// TestPubMed_LimitClamped verifies a non-positive limit falls back to the default and
// an over-large limit is clamped to the maximum, both observed via esearch's retmax.
func TestPubMed_LimitClamped(t *testing.T) {
	stub := newPubMedStub(t)

	if _, err := NewPubMed("").Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search(limit=0) error = %v", err)
	}
	if got := stub.searchQuery.Get("retmax"); got != "10" {
		t.Errorf("limit=0 sent retmax=%q, want the default 10", got)
	}

	if _, err := NewPubMed("").Search(context.Background(), "q", 9999); err != nil {
		t.Fatalf("Search(limit=9999) error = %v", err)
	}
	if got := stub.searchQuery.Get("retmax"); got != "50" {
		t.Errorf("limit=9999 sent retmax=%q, want the clamped 50", got)
	}
}

// TestPubMedProvider_Name verifies the provider stamps the "pubmed" origin.
func TestPubMedProvider_Name(t *testing.T) {
	if got := NewPubMed("").Name(); got != "pubmed" {
		t.Errorf("Name() = %q, want %q", got, "pubmed")
	}
}

// TestParsePubMedSummaries_DegradedResults covers the esummary shapes that yield no
// results without erroring: a result object with no uids array, a uids array that is
// not a list of strings, and a uid whose record is missing from or undecodable in the
// result object. Each is a source defect, and the best-effort contract turns it into
// no results rather than a failure.
func TestParsePubMedSummaries_DegradedResults(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "no uids array", body: `{"result":{"1":{"title":"T"}}}`},
		{name: "uids not strings", body: `{"result":{"uids":[{"uid":"1"}]}}`},
		{name: "record missing for uid", body: `{"result":{"uids":["1"]}}`},
		{name: "record is not an object", body: `{"result":{"uids":["1"],"1":"broken"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePubMedSummaries([]byte(tc.body)); len(got) != 0 {
				t.Errorf("parsePubMedSummaries(%s) = %v, want no results", tc.name, got)
			}
		})
	}
}

// TestPubMedYear documents pubmedYear's extraction of the publication year from the
// several pubdate spellings PubMed uses, and its refusal to guess when the leading
// token is not a four-digit year.
func TestPubMedYear(t *testing.T) {
	cases := []struct {
		pubdate string
		want    string
	}{
		{pubdate: "2019 Sep 1", want: "2019"},
		{pubdate: "1975 Jun", want: "1975"},
		{pubdate: "2024", want: "2024"},
		{pubdate: "2023 Winter", want: "2023"},
		{pubdate: "", want: ""},
		{pubdate: "n.d.", want: ""},
		{pubdate: "Sep 2019", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.pubdate, func(t *testing.T) {
			if got := pubmedYear(tc.pubdate); got != tc.want {
				t.Errorf("pubmedYear(%q) = %q, want %q", tc.pubdate, got, tc.want)
			}
		})
	}
}
