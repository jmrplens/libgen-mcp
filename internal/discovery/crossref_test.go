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

// crossrefItemsFixture is a realistic two-item Crossref works response. The first
// item carries a license and an application/pdf link (so it must map to
// OpenAccess=true with that PDF URL); the second has neither a license nor a pdf
// link (so OpenAccess=false, PDFURL empty). Both carry a DOI, a title, authors and
// an issued date the parser reads the year from.
const crossrefItemsFixture = `{
  "message": {
    "items": [
      {
        "DOI": "10.1000/xyz123",
        "title": ["Deep Neural Networks"],
        "author": [
          {"given": "Jane", "family": "Doe"},
          {"given": "John", "family": "Smith"}
        ],
        "issued": {"date-parts": [[2021, 1, 15]]},
        "license": [{"URL": "http://creativecommons.org/licenses/by/4.0/"}],
        "link": [
          {"URL": "http://example.org/x.html", "content-type": "text/html"},
          {"URL": "http://example.org/x.pdf", "content-type": "application/pdf"}
        ]
      },
      {
        "DOI": "10.1000/abc456",
        "title": ["Convolutional Models"],
        "author": [{"given": "Ada", "family": "Lovelace"}],
        "issued": {"date-parts": [[2020]]}
      }
    ]
  }
}`

// setCrossrefBase points the package-level crossrefBase at the given test server
// URL and restores it when the test ends, so an httptest server stands in for the
// live Crossref API.
func setCrossrefBase(t *testing.T, base string) {
	t.Helper()
	old := crossrefBase
	crossrefBase = base
	t.Cleanup(func() { crossrefBase = old })
}

// TestCrossref_ParsesItems verifies that a two-item works response parses into two
// results with the correct DOI/title/authors/year, Origin "crossref"; the licensed
// item with a pdf link is OpenAccess=true with PDFURL set, and the other is
// OpenAccess=false with an empty PDFURL.
func TestCrossref_ParsesItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(crossrefItemsFixture))
	}))
	defer srv.Close()
	setCrossrefBase(t, srv.URL)

	got, err := NewCrossref("").Search(context.Background(), "neural networks", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}

	first := got[0]
	if first.Origin != "crossref" {
		t.Errorf("first.Origin = %q, want crossref", first.Origin)
	}
	if first.DOI != "10.1000/xyz123" {
		t.Errorf("first.DOI = %q, want 10.1000/xyz123", first.DOI)
	}
	if first.Title != "Deep Neural Networks" {
		t.Errorf("first.Title = %q, want %q", first.Title, "Deep Neural Networks")
	}
	if first.Authors != "Jane Doe; John Smith" {
		t.Errorf("first.Authors = %q, want %q", first.Authors, "Jane Doe; John Smith")
	}
	if first.Year != "2021" {
		t.Errorf("first.Year = %q, want 2021", first.Year)
	}
	if !first.OpenAccess {
		t.Errorf("first.OpenAccess = false, want true (has a license)")
	}
	if first.PDFURL != "http://example.org/x.pdf" {
		t.Errorf("first.PDFURL = %q, want the application/pdf link", first.PDFURL)
	}

	second := got[1]
	if second.Year != "2020" {
		t.Errorf("second.Year = %q, want 2020", second.Year)
	}
	if second.OpenAccess {
		t.Errorf("second.OpenAccess = true, want false (no license)")
	}
	if second.PDFURL != "" {
		t.Errorf("second.PDFURL = %q, want empty (no pdf link)", second.PDFURL)
	}
}

// TestCrossref_PolitePoolMailto verifies that a non-empty contact email is sent as
// the polite-pool mailto query parameter, and that an empty email adds no mailto.
func TestCrossref_PolitePoolMailto(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(crossrefItemsFixture))
	}))
	defer srv.Close()
	setCrossrefBase(t, srv.URL)

	if _, err := NewCrossref("dev@example.com").Search(context.Background(), "q", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if got := gotQuery.Get("mailto"); got != "dev@example.com" {
		t.Errorf("mailto = %q, want dev@example.com", got)
	}

	if _, err := NewCrossref("").Search(context.Background(), "q", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if _, ok := gotQuery["mailto"]; ok {
		t.Errorf("mailto present with empty email, want absent")
	}
}

// TestCrossref_Non200ReturnsEmpty verifies that a non-200 response degrades to an
// empty result with no error, so a failing provider never sinks a federated search.
func TestCrossref_Non200ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	setCrossrefBase(t, srv.URL)

	got, err := NewCrossref("").Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on non-200", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on non-200", got)
	}
}

// TestCrossref_ContextCancelled verifies that a canceled context surfaces as the
// returned error (ctx.Err), rather than being softened to an empty result.
func TestCrossref_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(crossrefItemsFixture))
	}))
	defer srv.Close()
	setCrossrefBase(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := NewCrossref("").Search(ctx, "neural networks", 5)
	if err == nil {
		t.Fatalf("Search() error = nil, want a context error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on canceled ctx", got)
	}
}

// TestCrossref_LimitClamped verifies that a non-positive limit falls back to the
// sane default and an over-large limit is clamped to the maximum, both observed via
// the rows query parameter sent to Crossref.
func TestCrossref_LimitClamped(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(crossrefItemsFixture))
	}))
	defer srv.Close()
	setCrossrefBase(t, srv.URL)

	if _, err := NewCrossref("").Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search(limit=0) error = %v", err)
	}
	if !strings.Contains(gotQuery, "rows=10") {
		t.Errorf("limit=0 query = %q, want default rows=10", gotQuery)
	}

	if _, err := NewCrossref("").Search(context.Background(), "q", 9999); err != nil {
		t.Fatalf("Search(limit=9999) error = %v", err)
	}
	if !strings.Contains(gotQuery, "rows=50") {
		t.Errorf("limit=9999 query = %q, want clamped rows=50", gotQuery)
	}
}

// TestCrossrefProvider_Name verifies the Crossref provider stamps the "crossref"
// origin.
func TestCrossrefProvider_Name(t *testing.T) {
	if got := NewCrossref("").Name(); got != "crossref" {
		t.Errorf("Name() = %q, want %q", got, "crossref")
	}
}

// TestCrossref_TransportErrorReturnsEmpty verifies that a transport failure with a
// live (non-canceled) context degrades to an empty result with no error. Pointing
// the base at an address whose server has been closed makes boundedGet return a
// connection error while ctx.Err() stays nil, exercising the non-context error
// branch of Search that softens to (nil, nil).
func TestCrossref_TransportErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // close so the address refuses connections
	setCrossrefBase(t, base)

	got, err := NewCrossref("").Search(context.Background(), "neural networks", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on a transport error", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a transport error", got)
	}
}

// TestCrossref_ContextDeadlineDuringRequest verifies the context-error branch
// reached AFTER the request is in flight: the limiter admits the call (ctx still
// live), then the server blocks until the client's short deadline expires, so
// boundedGet fails with ctx.Err() != nil and Search propagates that context error
// rather than softening it to empty. This exercises the "return nil, ctx.Err()"
// inside Search's transport-error handling, distinct from the already-canceled
// limiter path.
func TestCrossref_ContextDeadlineDuringRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the client's context expires
	}))
	defer srv.Close()
	setCrossrefBase(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := NewCrossref("").Search(ctx, "neural networks", 5)
	if err == nil {
		t.Fatal("Search() error = nil, want a context deadline error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a deadline error", got)
	}
}

// TestParseCrossrefWorks_MalformedReturnsNil verifies that a body that cannot be
// decoded as a Crossref works envelope yields nil rather than panicking, honoring
// the best-effort contract that a malformed response is treated as no results.
func TestParseCrossrefWorks_MalformedReturnsNil(t *testing.T) {
	if got := parseCrossrefWorks([]byte("{not json")); got != nil {
		t.Errorf("parseCrossrefWorks(malformed) = %v, want nil", got)
	}
}

// TestCrossrefYear documents crossrefYear's extraction of the publication year
// from the issued date-parts: a positive leading year is rendered as a string,
// while missing date-parts, an empty inner slice, or a non-positive year all yield
// "".
func TestCrossrefYear(t *testing.T) {
	cases := []struct {
		name   string
		issued crossrefIssued
		want   string
	}{
		{name: "no date-parts", issued: crossrefIssued{}, want: ""},
		{name: "empty inner slice", issued: crossrefIssued{DateParts: [][]int{{}}}, want: ""},
		{name: "non-positive year", issued: crossrefIssued{DateParts: [][]int{{0}}}, want: ""},
		{name: "valid year", issued: crossrefIssued{DateParts: [][]int{{2021, 5}}}, want: "2021"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := crossrefYear(tc.issued); got != tc.want {
				t.Errorf("crossrefYear(%+v) = %q, want %q", tc.issued, got, tc.want)
			}
		})
	}
}
