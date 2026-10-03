package discovery

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
// API.
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

// TestDblp_ParsesHits verifies the happy path against a verbatim live dblp response:
// three hits parse into three results carrying title, "; "-joined authors, year,
// venue and DOI, stamped with origin "dblp". Neither OpenAccess nor PDFURL may be
// set — dblp states nothing about free availability, and its ee link is typically a
// publisher landing page.
func TestDblp_ParsesHits(t *testing.T) {
	srv := serveFixture(t, "dblp_search.json")
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "attention is all you need", 3)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search() returned %d results, want 3", len(got))
	}

	first := got[0]
	if first.Origin != "dblp" {
		t.Errorf("first.Origin = %q, want dblp", first.Origin)
	}
	const wantTitle = "Attentional Transfer is All You Need: Technology-aware Layout Pattern Generation."
	if first.Title != wantTitle {
		t.Errorf("first.Title = %q, want %q", first.Title, wantTitle)
	}
	if first.Authors != "Xiaopeng Zhang 0009; Haoyu Yang; Evangeline F. Y. Young" {
		t.Errorf("first.Authors = %q, want the three joined author names", first.Authors)
	}
	if first.Year != "2021" {
		t.Errorf("first.Year = %q, want 2021", first.Year)
	}
	if first.Venue != "DAC" {
		t.Errorf("first.Venue = %q, want DAC", first.Venue)
	}
	if first.DOI != "10.1109/DAC18074.2021.9586227" {
		t.Errorf("first.DOI = %q, want the record DOI", first.DOI)
	}
	for i, r := range got {
		if r.OpenAccess {
			t.Errorf("result %d OpenAccess = true, want false (dblp states no availability)", i)
		}
		if r.PDFURL != "" {
			t.Errorf("result %d PDFURL = %q, want empty (ee is a landing page)", i, r.PDFURL)
		}
	}
}

// TestDblp_UnescapesEntities verifies dblp's XML entity escaping is undone: its JSON
// titles carry entities such as &apos; verbatim, which would otherwise reach the
// caller as literal markup.
func TestDblp_UnescapesEntities(t *testing.T) {
	srv := serveFixture(t, "dblp_search.json")
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "attention", 3)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	const wantTitle = "Attention Is All You Need But You Don't Need All Of It For Inference of Large Language Models."
	if got[1].Title != wantTitle {
		t.Errorf("got[1].Title = %q, want the entity unescaped: %q", got[1].Title, wantTitle)
	}
}

// TestDblp_SingleAuthorAsObject verifies the shape dblp uses for a one-author record:
// info.authors.author is a bare object rather than an array, and it must still decode
// into a single-name Authors string instead of sinking the whole response.
func TestDblp_SingleAuthorAsObject(t *testing.T) {
	srv := serveFixture(t, "dblp_single_author.json")
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "go to statement considered harmful", 2)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}
	if got[0].Authors != "Edsger W. Dijkstra" {
		t.Errorf("got[0].Authors = %q, want the single author name", got[0].Authors)
	}
	if got[0].Venue != "Commun. ACM" {
		t.Errorf("got[0].Venue = %q, want Commun. ACM", got[0].Venue)
	}
}

// TestDblp_ZeroHits verifies a live "no matches" response — whose hits object omits
// the hit array entirely — yields no results and no error.
func TestDblp_ZeroHits(t *testing.T) {
	srv := serveFixture(t, "dblp_zero_hits.json")
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "zzzqqqxxnonexistentquery12345", 10)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Search() returned %d results, want 0", len(got))
	}
}

// dblpVenueArrayFixture is a one-hit response whose venue is a JSON array rather than
// a string — the shape dblp uses when a record belongs to more than one venue. The
// whole response must still decode, with the venues joined.
const dblpVenueArrayFixture = `{"result":{"hits":{"@total":"1","hit":[{
  "info":{"authors":{"author":[{"text":"Ada Lovelace"}]},
  "title":"A Two Venue Paper.","venue":["ICLR","CoRR"],"year":"2020",
  "doi":"10.1000/twovenues","ee":"https://doi.org/10.1000/twovenues"}}]}}}`

// TestDblp_VenueArray verifies the array form of venue decodes and joins, rather than
// failing the whole response with a JSON type error.
func TestDblp_VenueArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dblpVenueArrayFixture))
	}))
	defer srv.Close()
	setDblpBase(t, srv.URL)

	got, err := NewDBLP().Search(context.Background(), "two venues", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search() returned %d results, want 1", len(got))
	}
	if got[0].Venue != "ICLR; CoRR" {
		t.Errorf("Venue = %q, want the joined venues", got[0].Venue)
	}
}

// TestDblp_MalformedJSONReturnsNil verifies a body that cannot be decoded as a dblp
// envelope yields nil rather than panicking, honoring the best-effort contract that a
// malformed response is treated as no results.
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

// TestDblp_Non200ReturnsEmpty verifies a non-200 response degrades to an empty result
// with no error, so a failing provider never sinks a federated search.
func TestDblp_Non200ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
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
	srv := serveFixture(t, "dblp_search.json")
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

// TestDblp_RequestParameters verifies the request dblp receives: the free-text query
// on q, format=json, and the hit count on h.
func TestDblp_RequestParameters(t *testing.T) {
	var got url.Values
	body := readFixture(t, "dblp_search.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	setDblpBase(t, srv.URL)

	if _, err := NewDBLP().Search(context.Background(), "neural networks", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if q := got.Get("q"); q != "neural networks" {
		t.Errorf("q = %q, want %q", q, "neural networks")
	}
	if f := got.Get("format"); f != "json" {
		t.Errorf("format = %q, want json", f)
	}
	if h := got.Get("h"); h != "5" {
		t.Errorf("h = %q, want 5", h)
	}
}

// TestDblp_LimitClamped verifies a non-positive limit falls back to the default and
// an over-large limit is clamped to the maximum, both observed via the h parameter.
func TestDblp_LimitClamped(t *testing.T) {
	var got url.Values
	body := readFixture(t, "dblp_search.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	setDblpBase(t, srv.URL)

	if _, err := NewDBLP().Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search(limit=0) error = %v", err)
	}
	if h := got.Get("h"); h != "10" {
		t.Errorf("limit=0 sent h=%q, want the default 10", h)
	}

	if _, err := NewDBLP().Search(context.Background(), "q", 9999); err != nil {
		t.Fatalf("Search(limit=9999) error = %v", err)
	}
	if h := got.Get("h"); h != "50" {
		t.Errorf("limit=9999 sent h=%q, want the clamped 50", h)
	}
}

// TestDBLPProvider_Name verifies the provider stamps the "dblp" origin.
func TestDBLPProvider_Name(t *testing.T) {
	if got := NewDBLP().Name(); got != "dblp" {
		t.Errorf("Name() = %q, want %q", got, "dblp")
	}
}

// TestParseDblpHits_UnusableFieldShapes verifies the two lenient decoders reject a
// shape they cannot make sense of rather than accepting it silently: a numeric authors
// field and a numeric venue each fail the whole response, which the best-effort
// contract turns into no results.
func TestParseDblpHits_UnusableFieldShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "authors is neither array nor object",
			body: `{"result":{"hits":{"hit":[{"info":{"title":"T","authors":{"author":42}}}]}}}`,
		},
		{
			name: "venue is neither string nor array",
			body: `{"result":{"hits":{"hit":[{"info":{"title":"T","venue":42}}]}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDblpHits([]byte(tc.body)); got != nil {
				t.Errorf("parseDblpHits(%s) = %v, want nil", tc.name, got)
			}
		})
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

// TestDblp_ARefusalSilencesTheHostForAWindow drives the two refusals dblp gives an
// automated client: the Anubis bot-check page recorded from dblp.uni-trier.de on
// 2026-10-03, served with 200 and text/html in place of the JSON, and a bare 429.
// Each must degrade to an empty result, be logged once, and stop the next search
// from asking at all, because the next search builds a fresh provider and a
// cooldown kept on the provider would be forgotten.
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
				got, err := NewDBLP().Search(context.Background(), "attention", 5)
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
		if _, err := NewDBLP().Search(context.Background(), "attention", 5); err != nil {
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
		{name: "json", status: http.StatusOK, body: ` {"result":{}}`, want: false},
		{name: "malformed json is an answer", status: http.StatusOK, body: `{not json`, want: false},
		{name: "html page", status: http.StatusOK, body: "<!doctype html><title>Making sure you're not a bot!</title>", want: true},
		{name: "empty 200", status: http.StatusOK, body: "  ", want: true},
		{name: "429", status: http.StatusTooManyRequests, body: "", want: true},
		{name: "500 html", status: http.StatusInternalServerError, body: "<html></html>", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dblpRefused(tc.status, []byte(tc.body)); got != tc.want {
				t.Errorf("dblpRefused(%d, %q) = %t, want %t", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
