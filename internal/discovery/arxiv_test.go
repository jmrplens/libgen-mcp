package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
