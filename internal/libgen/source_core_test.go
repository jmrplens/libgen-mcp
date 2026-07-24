package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// coreServer builds an httptest server that answers the CORE search API with the
// named fixture under the given HTTP status, and records the Authorization header
// it received.
func coreServer(t *testing.T, fixture string, status int, gotAuth *string) *httptest.Server {
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
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

// TestCoreSupports verifies the source claims DOI-keyed items only and names itself
// "core".
func TestCoreSupports(t *testing.T) {
	s := coreSource{}
	if !s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(DOI) = false, want true")
	}
	if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(md5-only) = true, want false")
	}
	if s.Name() != "core" {
		t.Errorf("Name() = %q, want %q", s.Name(), "core")
	}
}

// TestCoreResolveOA verifies a DOI CORE holds resolves to the work's downloadUrl,
// authenticates with the Bearer key, declares a pdf extension, and leaves MD5
// verification off.
func TestCoreResolveOA(t *testing.T) {
	var gotAuth string
	srv := coreServer(t, "core_hit.json", http.StatusOK, &gotAuth)
	defer srv.Close()

	s := coreSource{http: srv.Client(), key: "secret-key", apiBase: srv.URL}
	got, err := s.Resolve(context.Background(), Item{DOI: "10.1371/journal.pbio.1002533"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-key")
	}
	if got.FileURL != "https://core.ac.uk/download/12345678.pdf" {
		t.Errorf("FileURL = %q, want the work's downloadUrl", got.FileURL)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
	if got.Header != nil {
		t.Errorf("Header = %v, want nil (the API key must not travel with the file URL)", got.Header)
	}
}

// TestCoreResolveNoKey verifies Resolve fails fast without an API key rather than
// contacting CORE, since the source should not be in the chain without one.
func TestCoreResolveNoKey(t *testing.T) {
	s := coreSource{}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("Resolve() error = %v, want a 'no API key' error", err)
	}
}

// TestCoreResolveKeyRejected verifies a 401 from CORE surfaces as a distinct
// key-rejected error.
func TestCoreResolveKeyRejected(t *testing.T) {
	srv := coreServer(t, "", http.StatusUnauthorized, nil)
	defer srv.Close()

	s := coreSource{http: srv.Client(), key: "bad-key", apiBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil || !strings.Contains(err.Error(), "API key rejected") {
		t.Fatalf("Resolve() error = %v, want an 'API key rejected' error", err)
	}
}

// TestCoreResolveNotInCore verifies a DOI CORE does not hold yields a distinct
// error so the chain advances.
func TestCoreResolveNotInCore(t *testing.T) {
	srv := coreServer(t, "core_miss.json", http.StatusOK, nil)
	defer srv.Close()

	s := coreSource{http: srv.Client(), key: "k", apiBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/nope"})
	if err == nil || !strings.Contains(err.Error(), "not in CORE") {
		t.Fatalf("Resolve() error = %v, want a 'not in CORE' error", err)
	}
}

// TestCoreResolveNoDownloadURL verifies a DOI CORE holds only metadata for (no
// downloadUrl) yields a distinct error, separate from the not-in-CORE case.
func TestCoreResolveNoDownloadURL(t *testing.T) {
	srv := coreServer(t, "core_no_fulltext.json", http.StatusOK, nil)
	defer srv.Close()

	s := coreSource{http: srv.Client(), key: "k", apiBase: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/metadata.only"})
	if err == nil || !strings.Contains(err.Error(), "no downloadable open-access full text") {
		t.Fatalf("Resolve() error = %v, want a 'no downloadable' error", err)
	}
}

// TestCoreResolveHTTPError verifies a non-200, non-auth status surfaces as an
// error.
func TestCoreResolveHTTPError(t *testing.T) {
	srv := coreServer(t, "", http.StatusInternalServerError, nil)
	defer srv.Close()

	s := coreSource{http: srv.Client(), key: "k", apiBase: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail on an HTTP 500")
	}
}

// TestCoreResolveMalformed verifies malformed JSON surfaces as a decode error
// rather than a panic.
func TestCoreResolveMalformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results": [`))
	}))
	defer srv.Close()

	s := coreSource{http: srv.Client(), key: "k", apiBase: srv.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail on a malformed JSON response")
	}
}
