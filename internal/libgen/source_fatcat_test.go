package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fatcatServer builds an httptest server that answers the release lookup with the
// named fixture under the given HTTP status, and records the DOI it was asked for.
func fatcatServer(t *testing.T, fixture string, status int, gotDOI *string) *httptest.Server {
	t.Helper()
	var body []byte
	if fixture != "" {
		b, err := os.ReadFile("testdata/" + fixture)
		if err != nil {
			t.Fatalf("reading fixture %s: %v", fixture, err)
		}
		body = b
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotDOI != nil {
			*gotDOI = r.URL.Query().Get("doi")
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

// TestFatcatSupports verifies the source claims DOI-keyed items only and names
// itself "fatcat".
func TestFatcatSupports(t *testing.T) {
	s := fatcatSource{}
	if !s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(DOI) = false, want true")
	}
	if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(md5-only) = true, want false")
	}
	if s.Name() != "fatcat" {
		t.Errorf("Name() = %q, want %q", s.Name(), "fatcat")
	}
}

// TestFatcatResolvePrefersDirectArchive verifies a DOI resolves to the direct
// archive.org copy over the Wayback capture and the non-archive URL, lowercases the
// DOI in the lookup, declares a pdf extension, and leaves MD5 verification off.
func TestFatcatResolvePrefersDirectArchive(t *testing.T) {
	const doi = "10.1371/journal.pbio.1002533"
	var gotDOI string
	srv := fatcatServer(t, "fatcat_hit.json", http.StatusOK, &gotDOI)
	defer srv.Close()

	s := fatcatSource{http: srv.Client(), apiBase: srv.URL}
	got, err := s.Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if gotDOI != strings.ToLower(doi) {
		t.Errorf("lookup doi = %q, want the lowercased DOI %q", gotDOI, strings.ToLower(doi))
	}
	if !strings.HasPrefix(got.FileURL, "https://archive.org/download/") {
		t.Errorf("FileURL = %q, want the direct archive.org copy", got.FileURL)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
}

// TestFatcatResolveUnknownDOI verifies an HTTP 404 (DOI unknown to fatcat) yields a
// distinct error, separate from the no-preserved-file case.
func TestFatcatResolveUnknownDOI(t *testing.T) {
	srv := fatcatServer(t, "", http.StatusNotFound, nil)
	defer srv.Close()

	s := fatcatSource{http: srv.Client(), apiBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown to fatcat") {
		t.Fatalf("Resolve() error = %v, want an 'unknown to fatcat' error", err)
	}
}

// TestFatcatResolveNoArchivedFile verifies a DOI fatcat knows but has preserved no
// Internet Archive file for yields a distinct error so the chain advances.
func TestFatcatResolveNoArchivedFile(t *testing.T) {
	srv := fatcatServer(t, "fatcat_no_archive.json", http.StatusOK, nil)
	defer srv.Close()

	s := fatcatSource{http: srv.Client(), apiBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/no.preserved.copy"})
	if err == nil || !strings.Contains(err.Error(), "no preserved Internet Archive file") {
		t.Fatalf("Resolve() error = %v, want a 'no preserved Internet Archive file' error", err)
	}
}

// TestFatcatResolveHTTPError verifies a non-200, non-404 status surfaces as an
// error.
func TestFatcatResolveHTTPError(t *testing.T) {
	srv := fatcatServer(t, "", http.StatusInternalServerError, nil)
	defer srv.Close()

	s := fatcatSource{http: srv.Client(), apiBase: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail on an HTTP 500")
	}
}

// TestFatcatResolveMalformed verifies malformed JSON surfaces as a decode error
// rather than a panic.
func TestFatcatResolveMalformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"files": [`))
	}))
	defer srv.Close()

	s := fatcatSource{http: srv.Client(), apiBase: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail on a malformed JSON response")
	}
}

// TestArchiveScore verifies the Internet Archive host ranking: a direct archive.org
// copy outranks another archive.org subdomain, which outranks a Wayback capture,
// and a non-archive host scores zero.
func TestArchiveScore(t *testing.T) {
	tests := []struct {
		url  string
		want int
	}{
		{"https://archive.org/download/x/y.pdf", 3},
		{"https://scholar.archive.org/work/abc.pdf", 2},
		{"https://web.archive.org/web/2018/https://x/y.pdf", 1},
		{"https://journals.plos.org/x/file.pdf", 0},
		{"not-a-url", 0},
	}
	for _, tt := range tests {
		if got := archiveScore(tt.url); got != tt.want {
			t.Errorf("archiveScore(%q) = %d, want %d", tt.url, got, tt.want)
		}
	}
}

// TestPickArchivedPDFSkipsNonPDF verifies a non-PDF file on the Internet Archive is
// skipped even though its host would otherwise score, so only PDFs are chosen.
func TestPickArchivedPDFSkipsNonPDF(t *testing.T) {
	files := []fatcatFile{
		{Mimetype: "application/xml", URLs: []fatcatURL{{URL: "https://archive.org/download/x/y.xml"}}},
	}
	if got, ok := pickArchivedPDF(files); ok {
		t.Fatalf("pickArchivedPDF() = %q, true; want a miss for a non-PDF file", got)
	}
}
