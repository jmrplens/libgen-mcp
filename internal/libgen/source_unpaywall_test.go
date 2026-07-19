package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// unpaywallTestServer serves the given testdata fixture at any path and records
// the last request URI, so tests can assert both the parsed outcome and that the
// DOI/email were embedded in the request.
func unpaywallTestServer(t *testing.T, fixture string) (*httptest.Server, *string) {
	t.Helper()
	body, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatalf("reading fixture %q: %v", fixture, err)
	}
	var lastURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastURI = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastURI
}

// TestUnpaywallResolveOA verifies that an open-access response resolves to the
// PDF URL with MD5 verification disabled and a pdf extension.
func TestUnpaywallResolveOA(t *testing.T) {
	srv, lastURI := unpaywallTestServer(t, "unpaywall_oa.json")
	s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1371/journal.pone.0000217"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	const wantURL = "https://journals.plos.org/plosone/article/file?id=10.1371/journal.pone.0000217&type=printable"
	if res.FileURL != wantURL {
		t.Errorf("FileURL = %q, want %q", res.FileURL, wantURL)
	}
	if res.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false (DOI-keyed, no md5 to verify)")
	}
	if res.Ext != "pdf" {
		t.Errorf("Ext = %q, want %q", res.Ext, "pdf")
	}
	if !strings.Contains(*lastURI, "email=mail%40jmrp.io") {
		t.Errorf("request URI %q does not carry the escaped email", *lastURI)
	}
	if !strings.Contains(*lastURI, "10.1371") {
		t.Errorf("request URI %q does not carry the DOI", *lastURI)
	}
}

// TestUnpaywallResolveNotOA verifies that a non-open-access response yields an
// error, so the download chain falls through to the next source.
func TestUnpaywallResolveNotOA(t *testing.T) {
	srv, _ := unpaywallTestServer(t, "unpaywall_notoa.json")
	s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}

	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1126/science.1157784"}); err == nil {
		t.Fatal("Resolve() on a non-OA article should return an error")
	}
}

// TestUnpaywallRawSlashInPath verifies the DOI keeps its raw slash in the request
// path (the documented /v2/<doi> shape) rather than being percent-encoded to %2F.
func TestUnpaywallRawSlashInPath(t *testing.T) {
	srv, lastURI := unpaywallTestServer(t, "unpaywall_oa.json")
	s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}

	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1371/journal.pone.0000217"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !strings.Contains(*lastURI, "/10.1371/journal.pone.0000217") {
		t.Errorf("request URI %q does not carry the DOI with a raw slash", *lastURI)
	}
	if strings.Contains(*lastURI, "%2F") {
		t.Errorf("request URI %q percent-encoded the DOI slash, want it raw", *lastURI)
	}
}

// TestUnpaywallNon200 verifies that a non-200 API response yields an error so the
// download chain advances to the next source.
func TestUnpaywallNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() on a non-200 Unpaywall response should return an error")
	}
}

// TestUnpaywallBadJSON verifies that a malformed OA response body is surfaced as a
// decode error rather than silently treated as not-OA.
func TestUnpaywallBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_oa": not-json`))
	}))
	t.Cleanup(srv.Close)
	s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() on a malformed Unpaywall body should return a decode error")
	}
}

// TestUnpaywallSupports verifies that the source claims DOI-keyed items only.
func TestUnpaywallSupports(t *testing.T) {
	s := unpaywallSource{email: "mail@jmrp.io"}
	if s.Supports(Item{DOI: ""}) {
		t.Error("Supports(empty DOI) = true, want false")
	}
	if !s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(non-empty DOI) = false, want true")
	}
	if s.Name() != "unpaywall" {
		t.Errorf("Name() = %q, want %q", s.Name(), "unpaywall")
	}
}
