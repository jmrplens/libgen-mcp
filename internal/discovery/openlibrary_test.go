package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
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

	got, err := NewOpenLibrary("").Search(context.Background(), "go programming", 5)
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

// openLibraryPublicFixture is a two-doc response exercising the availability
// fields: doc A is a publicly readable book (ebook_access "public", has_fulltext,
// an ia id), so it must gain a free-to-read archive.org URL and OpenAccess=true;
// doc B is only borrowable, so it must stay a plain resolver hit (no ArchiveURL,
// OpenAccess=false).
const openLibraryPublicFixture = `{
  "docs": [
    {
      "title": "A Public Domain Classic",
      "author_name": ["Some Author"],
      "first_publish_year": 1890,
      "ebook_access": "public",
      "has_fulltext": true,
      "ia": ["apublicdomainclassic00auth", "apublicdomainclassic00auth_djvu"],
      "cover_i": 12345,
      "key": "/works/OL1W"
    },
    {
      "title": "A Borrowable Book",
      "author_name": ["Other Author"],
      "first_publish_year": 2010,
      "ebook_access": "borrowable",
      "has_fulltext": true,
      "ia": ["aborrowablebook00auth"],
      "key": "/works/OL2W"
    }
  ]
}`

// TestOpenLibrary_PublicBookArchiveURL verifies that a publicly readable book gains
// a free-to-read archive.org URL and is marked open-access, while a merely
// borrowable book stays a plain resolver hit.
func TestOpenLibrary_PublicBookArchiveURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(openLibraryPublicFixture))
	}))
	defer srv.Close()
	setOpenLibraryBase(t, srv.URL)

	got, err := NewOpenLibrary("").Search(context.Background(), "public domain", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}
	if got[0].ArchiveURL != "https://archive.org/details/apublicdomainclassic00auth" {
		t.Errorf("public book ArchiveURL = %q, want the archive.org details URL", got[0].ArchiveURL)
	}
	if !got[0].OpenAccess {
		t.Error("public book OpenAccess = false, want true (freely readable)")
	}
	if got[1].ArchiveURL != "" {
		t.Errorf("borrowable book ArchiveURL = %q, want empty (not public)", got[1].ArchiveURL)
	}
	if got[1].OpenAccess {
		t.Error("borrowable book OpenAccess = true, want false")
	}
}

// TestOpenLibrary_AvailabilityFieldsRequested verifies the search request asks for
// the availability projection (ebook_access, has_fulltext, ia, cover_i) so a
// readable book can be recognized.
func TestOpenLibrary_AvailabilityFieldsRequested(t *testing.T) {
	var gotFields string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("fields")
		_, _ = w.Write([]byte(openLibraryDocsFixture))
	}))
	defer srv.Close()
	setOpenLibraryBase(t, srv.URL)

	if _, err := NewOpenLibrary("").Search(context.Background(), "q", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	for _, field := range []string{"ebook_access", "has_fulltext", "ia", "cover_i"} {
		if !strings.Contains(gotFields, field) {
			t.Errorf("fields projection %q missing %q", gotFields, field)
		}
	}
}

// TestOpenLibrary_UserAgentEtiquette verifies OpenLibrary etiquette: a configured
// contact email is advertised in the User-Agent (unlocking the identified rate),
// while an anonymous provider sends the bare discovery agent with no mailto.
func TestOpenLibrary_UserAgentEtiquette(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		_, _ = w.Write([]byte(openLibraryDocsFixture))
	}))
	defer srv.Close()
	setOpenLibraryBase(t, srv.URL)

	if _, err := NewOpenLibrary("dev@example.com").Search(context.Background(), "q", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(gotUA, "mailto:dev@example.com") {
		t.Errorf("User-Agent = %q, want it to carry the contact email", gotUA)
	}

	if _, err := NewOpenLibrary("").Search(context.Background(), "q", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if strings.Contains(gotUA, "mailto:") {
		t.Errorf("anonymous User-Agent = %q, want no mailto", gotUA)
	}
}

// TestOpenLibrary_RateFromEmail verifies the limiter rate follows the etiquette: an
// identified provider is paced to the 2 rps interval and an anonymous one to 1 rps.
func TestOpenLibrary_RateFromEmail(t *testing.T) {
	if got := NewOpenLibrary("dev@example.com").limiter.Limit(); got != rate.Every(openLibraryEmailRate) {
		t.Errorf("identified limit = %v, want %v (2 rps)", got, rate.Every(openLibraryEmailRate))
	}
	if got := NewOpenLibrary("").limiter.Limit(); got != rate.Every(openLibraryAnonRate) {
		t.Errorf("anonymous limit = %v, want %v (1 rps)", got, rate.Every(openLibraryAnonRate))
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

	if _, err := NewOpenLibrary("").Search(context.Background(), "q", 0); err != nil {
		t.Fatalf("Search(limit=0) error = %v", err)
	}
	if gotQuery.Get("fields") == "" {
		t.Errorf("fields query param absent, want a projection")
	}
	if got := gotQuery.Get("limit"); got != "10" {
		t.Errorf("limit=0 sent limit=%q, want default 10", got)
	}

	if _, err := NewOpenLibrary("").Search(context.Background(), "q", 9999); err != nil {
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

	got, err := NewOpenLibrary("").Search(context.Background(), "anything", 5)
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

	got, err := NewOpenLibrary("").Search(ctx, "go programming", 5)
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
	if got := NewOpenLibrary("").Name(); got != "openlibrary" {
		t.Errorf("Name() = %q, want %q", got, "openlibrary")
	}
}

// TestOpenLibrary_TransportErrorReturnsEmpty verifies that a transport failure with
// a live (non-canceled) context degrades to an empty result with no error. Pointing
// the base at an address whose server has been closed makes boundedGet return a
// connection error while ctx.Err() stays nil, exercising the non-context error
// branch of Search that softens to (nil, nil).
func TestOpenLibrary_TransportErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // close so the address refuses connections
	setOpenLibraryBase(t, base)

	got, err := NewOpenLibrary("").Search(context.Background(), "go programming", 5)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil on a transport error", err)
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a transport error", got)
	}
}

// TestOpenLibrary_ContextDeadlineDuringRequest verifies the context-error branch
// reached AFTER the request is in flight: the limiter admits the call (ctx still
// live), then the server blocks until the client's short deadline expires, so
// boundedGet fails with ctx.Err() != nil and Search propagates that context error
// rather than softening it to empty. This exercises the "return nil, ctx.Err()"
// inside Search's transport-error handling, distinct from the already-canceled
// limiter path.
func TestOpenLibrary_ContextDeadlineDuringRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the client's context expires
	}))
	defer srv.Close()
	setOpenLibraryBase(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := NewOpenLibrary("").Search(ctx, "go programming", 5)
	if err == nil {
		t.Fatal("Search() error = nil, want a context deadline error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a deadline error", got)
	}
}

// TestParseOpenLibraryDocs_MalformedReturnsNil verifies that a body that cannot be
// decoded as an OpenLibrary search envelope yields nil rather than panicking,
// honoring the best-effort contract that a malformed response is treated as no
// results.
func TestParseOpenLibraryDocs_MalformedReturnsNil(t *testing.T) {
	if got := parseOpenLibraryDocs([]byte("{not json")); got != nil {
		t.Errorf("parseOpenLibraryDocs(malformed) = %v, want nil", got)
	}
}
