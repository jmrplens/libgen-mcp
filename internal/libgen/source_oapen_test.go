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

// oapenFixture reads a captured OAPEN REST response from testdata.
func oapenFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// oapenServer stands in for library.oapen.org: /rest/search answers with the named
// search fixture, /rest/items/<uuid> with the named item fixture, and any
// /rest/bitstreams/<uuid>/retrieve with a PDF. It records the query string the
// source asked for so a test can assert how the identifier was submitted.
func oapenServer(t *testing.T, searchFixture, itemFixture string, gotQuery *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search"):
			if gotQuery != nil {
				*gotQuery = r.URL.Query().Get("query")
			}
			_, _ = w.Write(oapenFixture(t, searchFixture))
		case strings.Contains(r.URL.Path, "/items/"):
			_, _ = w.Write(oapenFixture(t, itemFixture))
		case strings.HasSuffix(r.URL.Path, "/retrieve"):
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.6 fixture"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestOapenSupports verifies the source claims DOI-keyed and ISBN-keyed items and
// nothing else, and that it names itself "oapen".
func TestOapenSupports(t *testing.T) {
	s := oapenSource{}
	if !s.Supports(Item{DOI: "10.2867/768526"}) {
		t.Error("Supports(DOI item) = false, want true")
	}
	if !s.Supports(Item{ISBN: "9789286150616"}) {
		t.Error("Supports(ISBN item) = false, want true")
	}
	if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(md5-only item) = true, want false")
	}
	if s.Name() != "oapen" {
		t.Errorf("Name() = %q, want oapen", s.Name())
	}
}

// TestOapenResolveByDOI verifies the happy path: the DOI is submitted to the search
// endpoint, the item's PDF bitstream is chosen over its export/thumbnail siblings,
// and the retrieve URL comes back with a pdf extension and MD5 verification off.
func TestOapenResolveByDOI(t *testing.T) {
	const doi = "10.2867/768526"
	var gotQuery string
	srv := oapenServer(t, "oapen_search_hit.json", "oapen_item_pdf.json", &gotQuery)
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	got, err := s.Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if gotQuery != doi {
		t.Errorf("search query = %q, want %q", gotQuery, doi)
	}
	// fa5e4801… is the ORIGINAL-bundle application/pdf bitstream in the fixture;
	// the .marc.xml, .ris, extracted text and thumbnail must all lose to it.
	want := srv.URL + "/rest/bitstreams/fa5e4801-f872-4845-bd63-fd7d72d14e12/retrieve"
	if got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for an item with no LibGen digest")
	}
}

// TestOapenResolveByISBN verifies an ISBN-keyed item resolves through the same
// path, submitting the normalized ISBN, and that a hyphenated ISBN-13 still matches
// the record's bare spelling.
func TestOapenResolveByISBN(t *testing.T) {
	var gotQuery string
	srv := oapenServer(t, "oapen_search_hit.json", "oapen_item_pdf.json", &gotQuery)
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	got, err := s.Resolve(context.Background(), Item{ISBN: "978-92-86-15061-6"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if gotQuery != "9789286150616" {
		t.Errorf("search query = %q, want the normalized ISBN 9789286150616", gotQuery)
	}
	if !strings.HasSuffix(got.FileURL, "/retrieve") {
		t.Errorf("FileURL = %q, want a bitstream retrieve URL", got.FileURL)
	}
}

// TestOapenResolveRejectsIdentifierMismatch is the source's central correctness
// test. OAPEN's /rest/search is a FREE-TEXT search: querying a DOI it does not hold
// still returns unrelated monographs (verified live — a nonexistent DOI returned 13
// hits). Serving the first hit would hand the caller a different book while
// reporting success, so a hit whose metadata does not state the requested
// identifier must be rejected as a clean miss.
func TestOapenResolveRejectsIdentifierMismatch(t *testing.T) {
	srv := oapenServer(t, "oapen_search_mismatch.json", "oapen_item_mismatch.json", nil)
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/not-a-real-doi-zzz"})
	if err == nil {
		t.Fatal("Resolve() served a hit whose metadata states a different DOI")
	}
	if !errors.Is(err, ErrNotIndexed) {
		t.Errorf("error = %v, want ErrNotIndexed (a wrong-book hit is a miss, not an outage)", err)
	}
}

// TestOapenResolveAcceptsPrefixedDOI verifies a record that states its DOI as a
// doi.org URL — which is how the fixture's oapen.identifier.doi is written — still
// matches a caller's bare DOI.
func TestOapenResolveAcceptsPrefixedDOI(t *testing.T) {
	srv := oapenServer(t, "oapen_search_hit.json", "oapen_item_pdf.json", nil)
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.2867/768526"}); err != nil {
		t.Fatalf("Resolve() error = %v; a https://doi.org/-prefixed record DOI must match a bare one", err)
	}
}

// TestOapenResolveNoHits verifies an empty search result is reported as a clean
// miss, which must not put the source in cooldown.
func TestOapenResolveNoHits(t *testing.T) {
	srv := oapenServer(t, "oapen_search_empty.json", "oapen_item_pdf.json", nil)
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	_, err := s.Resolve(context.Background(), Item{ISBN: "9789286150616"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Resolve() error = %v, want ErrNotIndexed", err)
	}
	if cooldownWorthy(context.Background(), err) {
		t.Error("a clean miss is cooldown-worthy, want it to leave the source in the chain")
	}
}

// TestOapenResolveItemWithoutPDF verifies the source-specific trap: a matching item
// whose bitstreams carry only metadata exports and a thumbnail — no PDF — is a miss,
// never a resolve pointing at a MARC record.
func TestOapenResolveItemWithoutPDF(t *testing.T) {
	srv := oapenServer(t, "oapen_search_hit.json", "oapen_item_nopdf.json", nil)
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.2867/768526"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Resolve() error = %v, want ErrNotIndexed for an item with no PDF bitstream", err)
	}
}

// TestOapenResolveServerError verifies a 5xx from the search endpoint is classified
// as the source being unavailable, so the chain cools it down instead of blaming
// the item.
func TestOapenResolveServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.2867/768526"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable on HTTP 502", err)
	}
}

// TestOapenResolveTransportError verifies an unreachable host is classified as
// unavailable rather than as a miss.
func TestOapenResolveTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing is listening any more

	s := oapenSource{base: base}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.2867/768526"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable when the host is unreachable", err)
	}
}

// TestOapenResolveMalformedJSON verifies a truncated search body fails cleanly, and
// that it is NOT reported as a miss: a body we could not read says nothing about
// whether OAPEN holds the book.
func TestOapenResolveMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"uuid": `))
	}))
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.2867/768526"})
	if err == nil {
		t.Fatal("Resolve() should fail on a malformed search body")
	}
	if errors.Is(err, ErrNotIndexed) {
		t.Errorf("a decode failure was reported as a miss: %v", err)
	}
}

// TestOapenResolveMalformedItemJSON verifies the same for the item lookup, the
// second hop, which is decoded separately.
func TestOapenResolveMalformedItemJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			_, _ = w.Write(oapenFixture(t, "oapen_search_hit.json"))
			return
		}
		_, _ = w.Write([]byte(`{"uuid": `))
	}))
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.2867/768526"}); err == nil {
		t.Fatal("Resolve() should fail on a malformed item body")
	}
}

// TestOapenResolveBitstreamNotServingPDF verifies the probe is load-bearing: an
// item that advertises a PDF bitstream whose retrieve URL answers with something
// else must not be handed to the download pipeline.
func TestOapenResolveBitstreamNotServingPDF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search"):
			_, _ = w.Write(oapenFixture(t, "oapen_search_hit.json"))
		case strings.Contains(r.URL.Path, "/items/"):
			_, _ = w.Write(oapenFixture(t, "oapen_item_pdf.json"))
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>maintenance</html>"))
		}
	}))
	defer srv.Close()

	s := oapenSource{http: srv.Client(), base: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.2867/768526"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable when the bitstream serves HTML", err)
	}
}

// TestOapenResolveMissingIdentifier verifies an item carrying neither identifier is
// refused without a request, since Supports should have kept it out of the chain.
func TestOapenResolveMissingIdentifier(t *testing.T) {
	s := oapenSource{base: "http://127.0.0.1:0"}
	if _, err := s.Resolve(context.Background(), Item{MD5: "abc"}); err == nil {
		t.Fatal("Resolve() with neither a DOI nor an ISBN should fail")
	}
}

// TestOapenBitstreamURLJoinsBase verifies the retrieve link from the API — a
// site-absolute path — is joined onto the configured base without doubling or
// dropping a separator.
func TestOapenBitstreamURLJoinsBase(t *testing.T) {
	s := oapenSource{base: "https://library.oapen.org/"}
	got := s.bitstreamURL(oapenBitstream{RetrieveLink: "/rest/bitstreams/abc/retrieve"})
	if want := "https://library.oapen.org/rest/bitstreams/abc/retrieve"; got != want {
		t.Errorf("bitstreamURL() = %q, want %q", got, want)
	}
	fromUUID := s.bitstreamURL(oapenBitstream{UUID: "abc"})
	if want := "https://library.oapen.org/rest/bitstreams/abc/retrieve"; fromUUID != want {
		t.Errorf("bitstreamURL() without a retrieve link = %q, want %q", fromUUID, want)
	}
	if s.bitstreamURL(oapenBitstream{}) != "" {
		t.Error("bitstreamURL() with neither a link nor a uuid should be empty")
	}
}

// TestOapenDefaultRoot verifies an unconfigured source addresses the production
// host.
func TestOapenDefaultRoot(t *testing.T) {
	if got := (oapenSource{}).root(); got != "https://library.oapen.org" {
		t.Errorf("root() = %q, want https://library.oapen.org", got)
	}
}

// TestOapenPickPDFPrefersOriginalBundle verifies bitstream selection: the
// ORIGINAL-bundle PDF wins over a PDF filed under another bundle, and a non-PDF
// mimetype is never chosen however it is named.
func TestOapenPickPDFPrefersOriginalBundle(t *testing.T) {
	got, ok := oapenPickPDF([]oapenBitstream{
		{UUID: "text", Name: "book.pdf.txt", MimeType: "text/plain", BundleName: "TEXT"},
		{UUID: "preview", Name: "preview.pdf", MimeType: "application/pdf", BundleName: "PREVIEW"},
		{UUID: "original", Name: "book.pdf", MimeType: "application/pdf", BundleName: "ORIGINAL"},
	})
	if !ok || got.UUID != "original" {
		t.Errorf("oapenPickPDF() = %+v, %v; want the ORIGINAL-bundle PDF", got, ok)
	}
	if _, none := oapenPickPDF([]oapenBitstream{{Name: "cover.jpg", MimeType: "image/jpeg", BundleName: "THUMBNAIL"}}); none {
		t.Error("oapenPickPDF() picked a thumbnail, want no PDF")
	}
	// A .pdf-named bitstream the server labels octet-stream is still a PDF worth
	// trying; the probe is what ultimately confirms it.
	if _, byName := oapenPickPDF([]oapenBitstream{{Name: "book.PDF", MimeType: "application/octet-stream", BundleName: "ORIGINAL"}}); !byName {
		t.Error("oapenPickPDF() rejected a .pdf-named ORIGINAL bitstream, want it accepted")
	}
}
