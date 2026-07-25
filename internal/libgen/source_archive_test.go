package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// archiveFixture reads a captured OpenLibrary or archive.org response from testdata.
func archiveFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// archiveServer stands in for both hops: /search.json answers with the named
// OpenLibrary fixture, /metadata/<id> with the fixture chosen for that identifier
// (falling back to the unknown-item fixture), and /download/… serves PDF bytes. The
// OpenLibrary query is recorded so a test can assert how the ISBN was submitted.
func archiveServer(t *testing.T, olFixture string, perItem map[string]string, gotQuery *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search.json"):
			if gotQuery != nil {
				*gotQuery = r.URL.Query().Get("q")
			}
			_, _ = w.Write(archiveFixture(t, olFixture))
		case strings.Contains(r.URL.Path, "/metadata/"):
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			fixture, ok := perItem[id]
			if !ok {
				fixture = "archive_meta_unknown.json"
			}
			_, _ = w.Write(archiveFixture(t, fixture))
		case strings.Contains(r.URL.Path, "/download/"):
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.5 fixture"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestArchiveSupports verifies the source claims ISBN-keyed items only: it reaches
// the Internet Archive through OpenLibrary, which is queried by ISBN, so an md5- or
// DOI-keyed item has no way in.
func TestArchiveSupports(t *testing.T) {
	s := archiveSource{}
	if !s.Supports(Item{ISBN: "978-0-14-143951-8"}) {
		t.Error("Supports(ISBN item) = false, want true")
	}
	if s.Supports(Item{DOI: "10.2867/768526"}) {
		t.Error("Supports(DOI item) = true, want false")
	}
	if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(md5 item) = true, want false")
	}
	if s.Supports(Item{ISBN: "not-an-isbn"}) {
		t.Error("Supports(malformed ISBN) = true, want false")
	}
	if s.Name() != "archive" {
		t.Errorf("Name() = %q, want archive", s.Name())
	}
}

// TestArchiveResolvePublicItem verifies the happy path: OpenLibrary is queried by
// ISBN, the first unrestricted Internet Archive candidate is taken, and its
// full-resolution PDF is chosen over the grayscale derivative, the extracted text
// and the animated-GIF preview.
func TestArchiveResolvePublicItem(t *testing.T) {
	var gotQuery string
	srv := archiveServer(t, "openlibrary_isbn_public.json",
		map[string]string{"bwb_KS-179-237": "archive_meta_public.json"}, &gotQuery)
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	got, err := s.Resolve(context.Background(), Item{ISBN: "978-0-14-143951-8"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "isbn:9780141439518"; gotQuery != want {
		t.Errorf("OpenLibrary query = %q, want %q", gotQuery, want)
	}
	// The metadata fixture is prideprejudice00aust's, served for the first IA
	// candidate; the download URL is built from the candidate id and the file name.
	want := srv.URL + "/download/bwb_KS-179-237/prideprejudice00aust.pdf"
	if got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for an ISBN-keyed item")
	}
}

// TestArchiveResolveRejectsBorrowable verifies the first correctness gate: a book
// OpenLibrary reports as lending-only ("borrowable") is never followed to
// archive.org, because a lending item's files either refuse the download or serve
// an unusable encrypted copy.
func TestArchiveResolveRejectsBorrowable(t *testing.T) {
	srv := archiveServer(t, "openlibrary_isbn_borrowable.json", nil, nil)
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9780316769488"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Resolve() error = %v, want ErrNotIndexed for a borrowable-only book", err)
	}
	if !strings.Contains(err.Error(), "borrowable") {
		t.Errorf("error = %v, want it to name the access tier that disqualified the book", err)
	}
}

// TestArchiveResolveSkipsLendingRestrictedItem verifies the second, independent
// gate — the one that actually protects the caller. OpenLibrary can report a work
// as publicly readable while an individual scan of it is lending-restricted, and
// such an item still advertises .pdf and .epub files. Downloading one appears to
// succeed and yields something unusable, so a candidate whose archive.org metadata
// carries access-restricted-item must be skipped in favor of the next candidate.
func TestArchiveResolveSkipsLendingRestrictedItem(t *testing.T) {
	srv := archiveServer(t, "openlibrary_isbn_public.json", map[string]string{
		"bwb_KS-179-237":  "archive_meta_restricted.json", // first candidate: lending only
		"bwb_W8-AAE-980":  "archive_meta_public.json",     // second: freely downloadable
		"prideprejudice…": "archive_meta_public.json",
	}, nil)
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	got, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if strings.Contains(got.FileURL, "bwb_KS-179-237") {
		t.Fatalf("FileURL = %q, want the restricted candidate skipped", got.FileURL)
	}
	if !strings.Contains(got.FileURL, "bwb_W8-AAE-980") {
		t.Errorf("FileURL = %q, want the next, unrestricted candidate", got.FileURL)
	}
}

// TestArchiveResolveAllCandidatesRestricted verifies that when every candidate is
// lending-restricted the outcome is a clean miss rather than a download of
// something unusable — and that a miss does not cool the source down.
func TestArchiveResolveAllCandidatesRestricted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search.json") {
			_, _ = w.Write(archiveFixture(t, "openlibrary_isbn_public.json"))
			return
		}
		_, _ = w.Write(archiveFixture(t, "archive_meta_restricted.json"))
	}))
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Resolve() error = %v, want ErrNotIndexed when every scan is lending-restricted", err)
	}
	if cooldownWorthy(context.Background(), err) {
		t.Error("a clean miss is cooldown-worthy, want the source left in the chain")
	}
}

// TestArchiveResolveUnknownISBN verifies an ISBN OpenLibrary does not know is a
// clean miss.
func TestArchiveResolveUnknownISBN(t *testing.T) {
	srv := archiveServer(t, "openlibrary_isbn_none.json", nil, nil)
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9799999999992"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Resolve() error = %v, want ErrNotIndexed", err)
	}
}

// TestArchiveResolveOpenLibraryServerError verifies a 5xx from the first hop is
// classified as the source being unavailable.
func TestArchiveResolveOpenLibraryServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable on HTTP 503", err)
	}
}

// TestArchiveResolveMetadataServerError verifies a broken second hop is surfaced as
// unavailability and never laundered into "the Archive does not hold this book".
func TestArchiveResolveMetadataServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search.json") {
			_, _ = w.Write(archiveFixture(t, "openlibrary_isbn_public.json"))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
	}
	if errors.Is(err, ErrNotIndexed) {
		t.Errorf("a broken metadata hop was reported as a miss: %v", err)
	}
}

// TestArchiveResolveTransportError verifies an unreachable OpenLibrary is
// unavailability rather than a miss.
func TestArchiveResolveTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	s := archiveSource{olBase: base, iaBase: base}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
	}
}

// TestArchiveResolveMalformedJSON verifies a truncated OpenLibrary body fails
// cleanly and is not mistaken for a miss.
func TestArchiveResolveMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"docs": [`))
	}))
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"})
	if err == nil {
		t.Fatal("Resolve() should fail on a malformed OpenLibrary body")
	}
	if errors.Is(err, ErrNotIndexed) {
		t.Errorf("a decode failure was reported as a miss: %v", err)
	}
}

// TestArchiveResolveMalformedMetadata verifies the same for the archive.org
// metadata hop.
func TestArchiveResolveMalformedMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search.json") {
			_, _ = w.Write(archiveFixture(t, "openlibrary_isbn_public.json"))
			return
		}
		_, _ = w.Write([]byte(`{"files": [`))
	}))
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"}); err == nil {
		t.Fatal("Resolve() should fail on a malformed metadata body")
	}
}

// TestArchiveResolveUnknownItem verifies archive.org's answer for an identifier it
// does not hold — an empty JSON object — is treated as a candidate to skip, not as
// a downloadable item.
func TestArchiveResolveUnknownItem(t *testing.T) {
	srv := archiveServer(t, "openlibrary_isbn_public.json", nil, nil) // every id → {}
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Resolve() error = %v, want ErrNotIndexed when no candidate item exists", err)
	}
}

// TestArchiveResolveMissingISBN verifies an item with no usable ISBN is refused
// without a request, since Supports should have kept it out of the chain.
func TestArchiveResolveMissingISBN(t *testing.T) {
	s := archiveSource{olBase: "http://127.0.0.1:0"}
	if _, err := s.Resolve(context.Background(), Item{MD5: "abc"}); err == nil {
		t.Fatal("Resolve() without an ISBN should fail")
	}
}

// TestArchiveDefaultRoots verifies an unconfigured source addresses the production
// hosts, and that a configured base loses its trailing separator so joined paths
// never double a slash.
func TestArchiveDefaultRoots(t *testing.T) {
	var zero archiveSource
	if got := zero.openLibraryRoot(); got != "https://openlibrary.org" {
		t.Errorf("openLibraryRoot() = %q, want https://openlibrary.org", got)
	}
	if got := zero.archiveRoot(); got != "https://archive.org" {
		t.Errorf("archiveRoot() = %q, want https://archive.org", got)
	}
	configured := archiveSource{olBase: "https://ol.example/", iaBase: "https://ia.example/"}
	if got := configured.openLibraryRoot(); got != "https://ol.example" {
		t.Errorf("openLibraryRoot() = %q, want the base without its trailing slash", got)
	}
	if got := configured.downloadURL("item id", "book file.pdf"); got != "https://ia.example/download/item%20id/book%20file.pdf" {
		t.Errorf("downloadURL() = %q, want both segments path-escaped", got)
	}
}

// TestArchiveIsRestricted verifies the lending gate reads every spelling of the
// flag archive.org uses, and that an unflagged item is allowed through.
func TestArchiveIsRestricted(t *testing.T) {
	cases := []struct {
		name string
		meta archiveItemMetadata
		want bool
	}{
		{"unflagged", archiveItemMetadata{Identifier: "x", MediaType: "texts"}, false},
		{"string true", archiveItemMetadata{Identifier: "x", MediaType: "texts", AccessRestricted: []byte(`"true"`)}, true},
		{"bool true", archiveItemMetadata{Identifier: "x", MediaType: "texts", AccessRestricted: []byte(`true`)}, true},
		{"explicit false", archiveItemMetadata{Identifier: "x", MediaType: "texts", AccessRestricted: []byte(`"false"`)}, false},
		{"json null", archiveItemMetadata{Identifier: "x", MediaType: "texts", AccessRestricted: []byte(`null`)}, false},
		{"lending collection", archiveItemMetadata{Identifier: "x", MediaType: "texts", Collection: flexStrings{"inlibrary"}}, true},
		{"open collection", archiveItemMetadata{Identifier: "x", MediaType: "texts", Collection: flexStrings{"americana"}}, false},
	}
	for _, c := range cases {
		if got := archiveIsRestricted(c.meta); got != c.want {
			t.Errorf("%s: archiveIsRestricted() = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFlexStringsUnmarshal verifies archive.org's habit of writing a single-valued
// list as a bare string is absorbed, since a strict []string would fail to decode
// the whole item and lose a downloadable book.
func TestFlexStringsUnmarshal(t *testing.T) {
	var one flexStrings
	if err := one.UnmarshalJSON([]byte(`"internetarchivebooks"`)); err != nil {
		t.Fatalf("UnmarshalJSON(string) error = %v", err)
	}
	if len(one) != 1 || one[0] != "internetarchivebooks" {
		t.Errorf("UnmarshalJSON(string) = %v, want one element", one)
	}
	var many flexStrings
	if err := many.UnmarshalJSON([]byte(`["a","b"]`)); err != nil {
		t.Fatalf("UnmarshalJSON(list) error = %v", err)
	}
	if len(many) != 2 {
		t.Errorf("UnmarshalJSON(list) = %v, want two elements", many)
	}
	var none flexStrings
	if err := none.UnmarshalJSON([]byte(`null`)); err != nil || none != nil {
		t.Errorf("UnmarshalJSON(null) = %v, %v; want nil, nil", none, err)
	}
	var bad flexStrings
	if err := bad.UnmarshalJSON([]byte(`42`)); err == nil {
		t.Error("UnmarshalJSON(number) = nil error, want a decode failure")
	}
}

// TestArchivePickFile verifies file selection: the full-resolution PDF beats the
// grayscale derivative and the EPUB, an EPUB is taken when no PDF exists, and DRM
// derivatives (ACS-encrypted PDF, LCP-encrypted EPUB) are never chosen.
func TestArchivePickFile(t *testing.T) {
	full, ok := archivePickFile([]archiveFile{
		{Name: "book_djvu.txt", Format: "DjVuTXT"},
		{Name: "book_bw.pdf", Format: "Grayscale PDF"},
		{Name: "book.epub", Format: "EPUB"},
		{Name: "book.pdf", Format: "Text PDF"},
	})
	if !ok || full.Name != "book.pdf" {
		t.Errorf("archivePickFile() = %+v, %v; want the full-resolution PDF", full, ok)
	}
	epub, ok := archivePickFile([]archiveFile{
		{Name: "book_lcp.epub", Format: "LCP Encrypted EPUB"},
		{Name: "book.epub", Format: "EPUB"},
	})
	if !ok || epub.Name != "book.epub" {
		t.Errorf("archivePickFile() = %+v, %v; want the unencrypted EPUB", epub, ok)
	}
	if _, drm := archivePickFile([]archiveFile{{Name: "book_encrypted.pdf", Format: "ACS Encrypted PDF"}}); drm {
		t.Error("archivePickFile() chose a DRM-encrypted file, want none")
	}
	if _, scan := archivePickFile([]archiveFile{{Name: "scan.jp2.zip", Format: "Single Page Processed JP2 ZIP"}}); scan {
		t.Error("archivePickFile() chose a non-book file, want none")
	}
}

// TestArchiveResolveEPUBOnlyItem verifies an item offering only an EPUB resolves to
// it with the right extension, since the reader supports EPUB as well as PDF.
func TestArchiveResolveEPUBOnlyItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search.json"):
			_, _ = w.Write(archiveFixture(t, "openlibrary_isbn_public.json"))
		case strings.Contains(r.URL.Path, "/metadata/"):
			_, _ = w.Write([]byte(`{"metadata":{"identifier":"epubonly","mediatype":"texts","collection":"americana"},` +
				`"files":[{"name":"epubonly.epub","format":"EPUB"},{"name":"epubonly_djvu.txt","format":"DjVuTXT"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	got, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Ext != "epub" {
		t.Errorf("Ext = %q, want epub", got.Ext)
	}
	if !strings.HasSuffix(got.FileURL, "/epubonly.epub") {
		t.Errorf("FileURL = %q, want the EPUB", got.FileURL)
	}
}

// TestArchiveResolvePDFNotServed verifies the probe backs the metadata gate up: a
// candidate whose PDF URL answers with an HTML page (archive.org's login or borrow
// interstitial) is skipped rather than handed to the download pipeline.
func TestArchiveResolvePDFNotServed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search.json"):
			_, _ = w.Write(archiveFixture(t, "openlibrary_isbn_public.json"))
		case strings.Contains(r.URL.Path, "/metadata/"):
			_, _ = w.Write(archiveFixture(t, "archive_meta_public.json"))
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>log in to borrow</html>"))
		}
	}))
	defer srv.Close()

	s := archiveSource{http: srv.Client(), olBase: srv.URL, iaBase: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{ISBN: "9780141439518"}); err == nil {
		t.Fatal("Resolve() returned a URL that does not serve the file")
	}
}
