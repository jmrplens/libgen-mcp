package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// arxivFeedFixture is a realistic two-entry arXiv Atom feed: the first entry
// carries an arxiv:doi and an explicit pdf link; the second has neither, so its
// PDF URL must be reconstructed from the abstract id. Both carry author names and
// a <published> date the parser reads the year from.
const arxivFeedFixture = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <entry>
    <id>http://arxiv.org/abs/2101.00001v1</id>
    <published>2021-01-15T00:00:00Z</published>
    <title>Deep   Neural
    Networks</title>
    <author><name>Jane Doe</name></author>
    <author><name>John Smith</name></author>
    <arxiv:doi>10.1000/xyz123</arxiv:doi>
    <link href="http://arxiv.org/abs/2101.00001v1" rel="alternate" type="text/html"/>
    <link title="pdf" href="http://arxiv.org/pdf/2101.00001v1" rel="related" type="application/pdf"/>
  </entry>
  <entry>
    <id>http://arxiv.org/abs/2002.09876v2</id>
    <published>2020-02-20T00:00:00Z</published>
    <title>Convolutional Models</title>
    <author><name>Ada Lovelace</name></author>
    <link href="http://arxiv.org/abs/2002.09876v2" rel="alternate" type="text/html"/>
  </entry>
</feed>`

// setArxivBase points the package-level arxivBase at the given test server URL and
// restores it when the test ends, so an httptest server stands in for the live
// arXiv API.
func setArxivBase(t *testing.T, base string) {
	t.Helper()
	old := arxivBase
	arxivBase = base
	t.Cleanup(func() { arxivBase = old })
}

// TestArxiv_ParsesEntries verifies that a two-entry Atom feed parses into two
// results with the correct title/authors/year/PDF URL, that only the entry with an
// <arxiv:doi> gets a DOI, that Origin is "arxiv" and OpenAccess is true, and that
// the outgoing request carries the search_query=all: and max_results parameters.
func TestArxiv_ParsesEntries(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(arxivFeedFixture))
	}))
	defer srv.Close()
	setArxivBase(t, srv.URL)

	got, err := NewArxiv().Search(context.Background(), "neural networks", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}
	if !strings.Contains(gotQuery, "search_query=all%3A") && !strings.Contains(gotQuery, "search_query=all:") {
		t.Errorf("request query = %q, want it to contain search_query=all:", gotQuery)
	}
	if !strings.Contains(gotQuery, "max_results=5") {
		t.Errorf("request query = %q, want max_results=5", gotQuery)
	}

	first := got[0]
	if first.Title != "Deep Neural Networks" {
		t.Errorf("first.Title = %q, want collapsed %q", first.Title, "Deep Neural Networks")
	}
	if first.Authors != "Jane Doe; John Smith" {
		t.Errorf("first.Authors = %q, want %q", first.Authors, "Jane Doe; John Smith")
	}
	if first.Year != "2021" {
		t.Errorf("first.Year = %q, want 2021", first.Year)
	}
	if first.DOI != "10.1000/xyz123" {
		t.Errorf("first.DOI = %q, want 10.1000/xyz123", first.DOI)
	}
	if first.PDFURL != "http://arxiv.org/pdf/2101.00001v1" {
		t.Errorf("first.PDFURL = %q, want the explicit pdf link", first.PDFURL)
	}
	if first.Origin != "arxiv" || !first.OpenAccess {
		t.Errorf("first Origin/OpenAccess = %q/%v, want arxiv/true", first.Origin, first.OpenAccess)
	}

	second := got[1]
	if second.DOI != "" {
		t.Errorf("second.DOI = %q, want empty (no arxiv:doi)", second.DOI)
	}
}

// TestArxiv_PDFURLFallback verifies that an entry with no explicit pdf link gets a
// PDF URL constructed from its abstract id (https://arxiv.org/pdf/<absid>).
func TestArxiv_PDFURLFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(arxivFeedFixture))
	}))
	defer srv.Close()
	setArxivBase(t, srv.URL)

	got, err := NewArxiv().Search(context.Background(), "models", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}
	if got[1].PDFURL != "https://arxiv.org/pdf/2002.09876v2" {
		t.Errorf("second.PDFURL = %q, want constructed %q", got[1].PDFURL, "https://arxiv.org/pdf/2002.09876v2")
	}
}

// TestArxiv_Non200ReturnsEmpty verifies that a non-200 response degrades to an
// empty result with no error, so a failing provider never sinks a federated search.
func TestArxiv_Non200ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	setArxivBase(t, srv.URL)

	got, err := NewArxiv().Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on non-200", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on non-200", got)
	}
}

// TestArxiv_ContextCancelled verifies that a canceled context surfaces as the
// returned error (ctx.Err), rather than being softened to an empty result.
func TestArxiv_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(arxivFeedFixture))
	}))
	defer srv.Close()
	setArxivBase(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := NewArxiv().Search(ctx, "neural networks", 5)
	if err == nil {
		t.Fatalf("Search() error = nil, want a context error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on canceled ctx", got)
	}
}

// TestArxiv_LimitClamped verifies that a non-positive limit falls back to the sane
// default and an over-large limit is clamped to the maximum, both observed via the
// max_results query parameter sent to arXiv.
func TestArxiv_LimitClamped(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(arxivFeedFixture))
	}))
	defer srv.Close()
	setArxivBase(t, srv.URL)

	if _, err := NewArxiv().Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search(limit=0) error = %v", err)
	}
	if !strings.Contains(gotQuery, "max_results=10") {
		t.Errorf("limit=0 query = %q, want default max_results=10", gotQuery)
	}

	if _, err := NewArxiv().Search(context.Background(), "q", 9999); err != nil {
		t.Fatalf("Search(limit=9999) error = %v", err)
	}
	if !strings.Contains(gotQuery, "max_results=50") {
		t.Errorf("limit=9999 query = %q, want clamped max_results=50", gotQuery)
	}
}

// TestArxivProvider_Name verifies the arXiv provider stamps the "arxiv" origin.
func TestArxivProvider_Name(t *testing.T) {
	if got := NewArxiv().Name(); got != "arxiv" {
		t.Errorf("Name() = %q, want %q", got, "arxiv")
	}
}

// TestArxiv_TransportErrorReturnsEmpty verifies that a transport failure with a
// live (non-canceled) context degrades to an empty result with no error. Pointing
// the base at an address whose server has been closed makes boundedGet return a
// connection error while ctx.Err() stays nil, exercising the non-context error
// branch of Search that softens to (nil, nil).
func TestArxiv_TransportErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // close so the address refuses connections
	setArxivBase(t, base)

	got, err := NewArxiv().Search(context.Background(), "neural networks", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on a transport error", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a transport error", got)
	}
}

// TestArxiv_ContextDeadlineDuringRequest verifies the context-error branch reached
// AFTER the request is in flight: the limiter admits the call (ctx still live), then
// the server blocks until the client's short deadline expires, so boundedGet fails
// with ctx.Err() != nil and Search propagates that context error rather than
// softening it to empty. This exercises the "return nil, ctx.Err()" inside Search's
// transport-error handling, distinct from the already-canceled limiter path.
func TestArxiv_ContextDeadlineDuringRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the client's context expires
	}))
	defer srv.Close()
	setArxivBase(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := NewArxiv().Search(ctx, "neural networks", 5)
	if err == nil {
		t.Fatal("Search() error = nil, want a context deadline error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a deadline error", got)
	}
}

// TestParseArxivFeed_MalformedReturnsNil verifies that a body that cannot be
// decoded as an Atom feed yields nil rather than panicking, honoring the
// best-effort contract that a malformed feed is treated as no results.
func TestParseArxivFeed_MalformedReturnsNil(t *testing.T) {
	if got := parseArxivFeed([]byte("<<<not xml>>>")); got != nil {
		t.Errorf("parseArxivFeed(malformed) = %v, want nil", got)
	}
}

// TestArxivAbsID verifies the abstract-id extraction: an id containing "/abs/"
// yields the trimmed part after the marker (version suffix preserved), while an id
// without the marker yields "".
func TestArxivAbsID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{name: "abs marker with version", id: "http://arxiv.org/abs/2101.00001v1", want: "2101.00001v1"},
		{name: "abs marker no version", id: "http://arxiv.org/abs/2101.00001", want: "2101.00001"},
		{name: "no abs marker", id: "http://arxiv.org/other/2101.00001", want: ""},
		{name: "empty id", id: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := arxivAbsID(tc.id); got != tc.want {
				t.Errorf("arxivAbsID(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

// TestArxivPDFURL_NoLinkNoAbsID verifies the final fallback of arxivPDFURL: with no
// explicit pdf link and an <id> lacking the "/abs/" marker (so no id can be
// reconstructed), the PDF URL resolves to "".
func TestArxivPDFURL_NoLinkNoAbsID(t *testing.T) {
	e := atomEntry{
		ID:    "http://arxiv.org/other/2101.00001",
		Links: []atomLink{{Title: "alternate", Href: "http://arxiv.org/other/2101.00001", Type: "text/html"}},
	}
	if got := arxivPDFURL(e); got != "" {
		t.Errorf("arxivPDFURL(no pdf link, no abs id) = %q, want empty", got)
	}
}
