package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// openLibraryDocsFixture is a realistic two-doc OpenLibrary search response. Doc A
// carries a title, author_name, first_publish_year and isbn (so those fields must
// be resolved). Doc B omits isbn (so its ISBN must resolve to ""). Neither is a
// download source, so both must have OpenAccess=false and an empty PDFURL.
const openLibraryDocsFixture = `{
  "docs": [
    {
      "title": "The Go Programming Language",
      "author_name": ["Alan Donovan", "Brian Kernighan"],
      "first_publish_year": 2015,
      "isbn": ["9780134190440", "0134190440"],
      "key": "/works/OL17930368W"
    },
    {
      "title": "Introducing Go",
      "author_name": ["Caleb Doxsey"],
      "first_publish_year": 2016,
      "key": "/works/OL17359877W"
    }
  ]
}`

// setOpenLibraryBase points the package-level openLibraryBase at the given test
// server URL and restores it when the test ends, so an httptest server stands in
// for the live OpenLibrary API.
func setOpenLibraryBase(t *testing.T, base string) {
	t.Helper()
	old := openLibraryBase
	openLibraryBase = base
	t.Cleanup(func() { openLibraryBase = old })
}

// TestOpenLibrary_ResolvesDocs verifies that a two-doc search response resolves
// into two results carrying the correct title/authors/year/isbn for the complete
// doc; that Origin is "openlibrary" and both results are non-download (OpenAccess
// false, PDFURL empty); and that the doc missing an isbn resolves to ISBN "".
func TestOpenLibrary_ResolvesDocs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(openLibraryDocsFixture))
	}))
	defer srv.Close()
	setOpenLibraryBase(t, srv.URL)

	got, err := NewOpenLibrary().Search(context.Background(), "go programming", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}

	a := got[0]
	if a.Origin != "openlibrary" {
		t.Errorf("a.Origin = %q, want openlibrary", a.Origin)
	}
	if a.Title != "The Go Programming Language" {
		t.Errorf("a.Title = %q, want %q", a.Title, "The Go Programming Language")
	}
	if a.Authors != "Alan Donovan; Brian Kernighan" {
		t.Errorf("a.Authors = %q, want %q", a.Authors, "Alan Donovan; Brian Kernighan")
	}
	if a.Year != "2015" {
		t.Errorf("a.Year = %q, want 2015", a.Year)
	}
	if a.ISBN != "9780134190440" {
		t.Errorf("a.ISBN = %q, want the first isbn", a.ISBN)
	}

	for i, r := range got {
		if r.OpenAccess {
			t.Errorf("got[%d].OpenAccess = true, want false (resolver, not download source)", i)
		}
		if r.PDFURL != "" {
			t.Errorf("got[%d].PDFURL = %q, want empty (resolver, not download source)", i, r.PDFURL)
		}
	}

	if b := got[1]; b.ISBN != "" {
		t.Errorf("b.ISBN = %q, want empty (doc has no isbn)", b.ISBN)
	}
}

// TestOpenLibrary_FieldsAndLimit verifies that the request carries a fields
// projection and that the limit is clamped before being sent: a non-positive limit
// falls back to the default "10" and an over-large limit is clamped to "50", both
// observed via the limit query parameter.
func TestOpenLibrary_FieldsAndLimit(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(openLibraryDocsFixture))
	}))
	defer srv.Close()
	setOpenLibraryBase(t, srv.URL)

	if _, err := NewOpenLibrary().Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search(limit=0) error = %v", err)
	}
	if gotQuery.Get("fields") == "" {
		t.Errorf("fields query param absent, want a projection")
	}
	if got := gotQuery.Get("limit"); got != "10" {
		t.Errorf("limit=0 sent limit=%q, want default 10", got)
	}

	if _, err := NewOpenLibrary().Search(context.Background(), "q", 9999); err != nil {
		t.Fatalf("Search(limit=9999) error = %v", err)
	}
	if got := gotQuery.Get("limit"); got != "50" {
		t.Errorf("limit=9999 sent limit=%q, want clamped 50", got)
	}
}

// TestOpenLibrary_Non200ReturnsEmpty verifies that a non-200 response degrades to
// an empty result with no error, so a failing resolver never sinks a federated
// search.
func TestOpenLibrary_Non200ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	setOpenLibraryBase(t, srv.URL)

	got, err := NewOpenLibrary().Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on non-200", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on non-200", got)
	}
}

// TestOpenLibrary_ContextCancelled verifies that a canceled context surfaces as the
// returned error (ctx.Err), rather than being softened to an empty result.
func TestOpenLibrary_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(openLibraryDocsFixture))
	}))
	defer srv.Close()
	setOpenLibraryBase(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := NewOpenLibrary().Search(ctx, "go programming", 5)
	if err == nil {
		t.Fatalf("Search() error = nil, want a context error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on canceled ctx", got)
	}
}

// TestOpenLibraryProvider_Name verifies the provider stamps the "openlibrary"
// origin.
func TestOpenLibraryProvider_Name(t *testing.T) {
	if got := NewOpenLibrary().Name(); got != "openlibrary" {
		t.Errorf("Name() = %q, want %q", got, "openlibrary")
	}
}
