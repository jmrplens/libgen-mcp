package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// gutenbergFixture reads a captured Gutendex response from testdata.
func gutenbergFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// gutenbergServer serves the named fixture for every request, recording the query
// string the provider sent.
func gutenbergServer(t *testing.T, fixture string, gotQuery *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotQuery != nil {
			*gotQuery = r.URL.RawQuery
		}
		_, _ = w.Write(gutenbergFixture(t, fixture))
	}))
}

// withGutendexBase points the provider at a test server for the duration of a test.
func withGutendexBase(t *testing.T, base string) {
	t.Helper()
	old := gutendexBase
	gutendexBase = base
	t.Cleanup(func() { gutendexBase = old })
}

// TestGutenbergName pins the origin label its results are stamped with.
func TestGutenbergName(t *testing.T) {
	if got := NewGutenberg().Name(); got != "gutenberg" {
		t.Errorf("Name() = %q, want gutenberg", got)
	}
}

// TestGutenbergSearch verifies the happy path: the query reaches Gutendex's search
// parameter, and each public-domain hit comes back with a title, its author, a
// directly-fetchable EPUB URL and the open-access flag set.
func TestGutenbergSearch(t *testing.T) {
	var gotQuery string
	srv := gutenbergServer(t, "gutendex_search.json", &gotQuery)
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	got, err := NewGutenberg().Search(context.Background(), "pride and prejudice austen", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Search() returned no results for the captured hit fixture")
	}
	if !strings.Contains(gotQuery, "search=pride+and+prejudice+austen") {
		t.Errorf("request query = %q, want the free-text query in the search parameter", gotQuery)
	}
	first := got[0]
	if first.Origin != "gutenberg" {
		t.Errorf("Origin = %q, want gutenberg", first.Origin)
	}
	if first.Title != "Pride and Prejudice" {
		t.Errorf("Title = %q, want Pride and Prejudice", first.Title)
	}
	if first.Authors != "Austen, Jane" {
		t.Errorf("Authors = %q, want Austen, Jane", first.Authors)
	}
	if !first.OpenAccess {
		t.Error("OpenAccess = false, want true for a public-domain Gutenberg text")
	}
	if !strings.HasPrefix(first.FullTextURL, "https://www.gutenberg.org/") {
		t.Errorf("FullTextURL = %q, want a gutenberg.org file URL", first.FullTextURL)
	}
	if first.Extension != "epub" {
		t.Errorf("Extension = %q, want epub (the preferred format)", first.Extension)
	}
	// Gutenberg has no DOI, ISBN or md5 to offer, and claiming one would send the
	// caller to a download source that cannot key on it.
	if first.DOI != "" || first.ISBN != "" || first.MD5 != "" {
		t.Errorf("result carries an identifier it cannot honor: %+v", first)
	}
}

// TestGutenbergSearchHonorsLimit verifies the caller's limit is applied, since
// Gutendex has no page-size parameter and always answers with a full page.
func TestGutenbergSearchHonorsLimit(t *testing.T) {
	srv := gutenbergServer(t, "gutendex_search.json", nil)
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	got, err := NewGutenberg().Search(context.Background(), "pride and prejudice", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search(limit 1) returned %d results, want 1", len(got))
	}
}

// TestGutenbergSearchSkipsCopyrighted is the provider's correctness test. Gutendex
// also indexes the texts Project Gutenberg hosts WITH PERMISSION, which are still in
// copyright (copyright: true). This server only surfaces freely licensed material,
// so such a record must be dropped even though it carries perfectly good download
// links.
func TestGutenbergSearchSkipsCopyrighted(t *testing.T) {
	srv := gutenbergServer(t, "gutendex_copyrighted.json", nil)
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	got, err := NewGutenberg().Search(context.Background(), "the samurai strategy", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() surfaced %d in-copyright records, want none: %+v", len(got), got)
	}
}

// TestGutenbergSearchNoHits verifies an empty catalog answer degrades to no results
// and no error.
func TestGutenbergSearchNoHits(t *testing.T) {
	srv := gutenbergServer(t, "gutendex_empty.json", nil)
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	got, err := NewGutenberg().Search(context.Background(), "zzzzqqqxnotarealbooktitle", 10)
	if err != nil {
		t.Fatalf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() = %+v, want no results", got)
	}
}

// TestGutenbergSearchServerError verifies a 5xx degrades to an empty result rather
// than sinking a federated search.
func TestGutenbergSearchServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	got, err := NewGutenberg().Search(context.Background(), "anything", 10)
	if err != nil || got != nil {
		t.Fatalf("Search() = %v, %v; want nil, nil on a 502", got, err)
	}
}

// TestGutenbergSearchTransportError verifies an unreachable host degrades the same
// way.
func TestGutenbergSearchTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()
	withGutendexBase(t, base)

	got, err := NewGutenberg().Search(context.Background(), "anything", 10)
	if err != nil || got != nil {
		t.Fatalf("Search() = %v, %v; want nil, nil when the host is unreachable", got, err)
	}
}

// TestGutenbergSearchMalformedJSON verifies a truncated body degrades to no results
// instead of propagating a decode error.
func TestGutenbergSearchMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results": [`))
	}))
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	got, err := NewGutenberg().Search(context.Background(), "anything", 10)
	if err != nil || got != nil {
		t.Fatalf("Search() = %v, %v; want nil, nil on malformed JSON", got, err)
	}
}

// TestGutenbergSearchCanceledContext verifies a canceled context propagates, which
// is how the federation layer tells "caller went away" from "provider degraded".
func TestGutenbergSearchCanceledContext(t *testing.T) {
	srv := gutenbergServer(t, "gutendex_search.json", nil)
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewGutenberg().Search(ctx, "anything", 10); err == nil {
		t.Fatal("Search() with a canceled context = nil error, want the context error")
	}
}

// TestGutenbergPickFormat verifies format selection: EPUB is preferred, plain text
// is the fallback, an HTML reading page or a cover image is never chosen, and a
// record offering nothing usable yields no URL.
func TestGutenbergPickFormat(t *testing.T) {
	url, ext := gutenbergPickFormat(map[string]string{
		"text/html":                 "https://www.gutenberg.org/ebooks/1342.html.images",
		"image/jpeg":                "https://www.gutenberg.org/cache/epub/1342/pg1342.cover.medium.jpg",
		"text/plain; charset=utf-8": "https://www.gutenberg.org/ebooks/1342.txt.utf-8",
		"application/epub+zip":      "https://www.gutenberg.org/ebooks/1342.epub3.images",
	})
	if ext != "epub" || !strings.HasSuffix(url, ".epub3.images") {
		t.Errorf("gutenbergPickFormat() = %q, %q; want the EPUB", url, ext)
	}
	txtURL, txtExt := gutenbergPickFormat(map[string]string{
		"text/html":                 "https://www.gutenberg.org/ebooks/26301.html.images",
		"text/plain; charset=utf-8": "https://www.gutenberg.org/ebooks/26301.txt.utf-8",
	})
	if txtExt != "txt" || txtURL == "" {
		t.Errorf("gutenbergPickFormat() = %q, %q; want the plain-text fallback", txtURL, txtExt)
	}
	if noneURL, _ := gutenbergPickFormat(map[string]string{"image/jpeg": "https://example.invalid/cover.jpg"}); noneURL != "" {
		t.Errorf("gutenbergPickFormat() = %q, want no URL when only a cover image is offered", noneURL)
	}
	// A record whose only usable format is missing its URL must not yield a link.
	if emptyURL, _ := gutenbergPickFormat(map[string]string{"application/epub+zip": "  "}); emptyURL != "" {
		t.Errorf("gutenbergPickFormat() = %q, want no URL for a blank format link", emptyURL)
	}
}

// TestGutenbergAuthors verifies author names are joined as the rest of discovery
// renders them, and that an empty name is dropped rather than leaving a stray
// separator.
func TestGutenbergAuthors(t *testing.T) {
	got := gutenbergAuthors([]gutendexPerson{{Name: "Austen, Jane"}, {Name: "  "}, {Name: "Doe, John"}})
	if got != "Austen, Jane; Doe, John" {
		t.Errorf("gutenbergAuthors() = %q, want %q", got, "Austen, Jane; Doe, John")
	}
	if gutenbergAuthors(nil) != "" {
		t.Error("gutenbergAuthors(nil) should be empty")
	}
}

// TestGutenbergSkipsNonText verifies a Gutendex record that is a sound recording
// rather than a book is dropped: this server offers books, and an audio item has no
// text to read.
func TestGutenbergSkipsNonText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":1,"results":[{"id":9,"title":"A Reading","copyright":false,` +
			`"media_type":"Sound","formats":{"audio/ogg":"https://www.gutenberg.org/files/9/9.ogg"}}]}`))
	}))
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	got, err := NewGutenberg().Search(context.Background(), "a reading", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() surfaced a non-text record: %+v", got)
	}
}

// TestGutenbergSkipsUnknownCopyright verifies a record whose copyright status
// Gutendex does not state (null) is dropped: this server only surfaces material it
// knows to be freely licensed.
func TestGutenbergSkipsUnknownCopyright(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":1,"results":[{"id":7,"title":"Unknown Status","copyright":null,` +
			`"media_type":"Text","formats":{"application/epub+zip":"https://www.gutenberg.org/ebooks/7.epub3.images"}}]}`))
	}))
	defer srv.Close()
	withGutendexBase(t, srv.URL)

	got, err := NewGutenberg().Search(context.Background(), "unknown status", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search() surfaced a record of unknown copyright status: %+v", got)
	}
}
