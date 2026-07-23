package libgen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// TestUnpaywallResolve_UsesItemEmail verifies the two per-call-email behaviors of
// unpaywallSource.Resolve: (1) an Item's Email overrides an empty configured email
// and is sent as the email query parameter; (2) with neither the configured nor the
// per-call email set, Resolve returns the "no contact email" error WITHOUT issuing
// any request, so the download chain falls through instead of hitting the API blank.
func TestUnpaywallResolve_UsesItemEmail(t *testing.T) {
	var hits atomic.Int32
	var lastURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastURI = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_oa":true,"best_oa_location":{"url_for_pdf":"https://cdn.example/x.pdf"}}`))
	}))
	t.Cleanup(srv.Close)

	// s.email is empty; the per-call Item.Email must be used instead.
	s := unpaywallSource{email: "", http: srv.Client(), baseURL: srv.URL}
	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x", Email: "caller@example.com"})
	if err != nil {
		t.Fatalf("Resolve() with a per-call email error = %v", err)
	}
	if res.FileURL != "https://cdn.example/x.pdf" {
		t.Errorf("FileURL = %q, want the stubbed PDF URL", res.FileURL)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1", hits.Load())
	}
	if !strings.Contains(lastURI, "email=caller%40example.com") {
		t.Errorf("request URI %q does not carry the per-call email", lastURI)
	}

	// Neither configured nor per-call email: Resolve must error before any request.
	hits.Store(0)
	if _, blankErr := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); blankErr == nil {
		t.Fatal("Resolve() with no email anywhere should return an error")
	}
	if hits.Load() != 0 {
		t.Errorf("server hits = %d with no email, want 0 (must not hit the API blank)", hits.Load())
	}
}

// unpaywallDownloadServer serves the on-demand Unpaywall flow for a DOI download: a
// lookup path returns OA JSON pointing at its own /pdf endpoint, which serves the
// given PDF bytes. It records how many lookups (not /pdf fetches) it received so a
// test can assert Unpaywall was — or was not — consulted.
func unpaywallDownloadServer(t *testing.T, pdf []byte) (base string, lookups *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/pdf", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	})
	// The DOI lookup is served at any other path (the source builds /v2/<doi>).
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"is_oa":true,"best_oa_location":{"url_for_pdf":%q}}`, srv.URL+"/pdf")
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

// newPerCallEmailClient builds a client that has NO configured Unpaywall email and
// whose only configured source is "unpaywall" (disabled without an email, so the
// chain starts empty). The on-demand Unpaywall base URL is pointed at the test
// server so the ad-hoc prepended source resolves against it rather than the live API.
func newPerCallEmailClient(t *testing.T, unpaywallBase string) *Client {
	t.Helper()
	cfg := &config.Config{
		Timeout:                5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
		UnpaywallEmail:         "", // no configured email → unpaywall not in the default chain
		Sources:                []string{"unpaywall"},
	}
	c := New(staticMirrors{}, cfg, WithUnpaywallBaseURL(unpaywallBase))
	c.backoffBase = time.Millisecond
	return c
}

// TestDownloadItem_PerCallEmailPrependsUnpaywall verifies that a per-call email
// pulls the Unpaywall source into a DOI download even when the server configured no
// email (Unpaywall is absent from the default chain): with Item.Email set the
// download succeeds via the ad-hoc Unpaywall source; with Item.Email empty the same
// DOI download does NOT consult Unpaywall (0 lookups) and fails with no usable source,
// proving the default behavior is unchanged.
func TestDownloadItem_PerCallEmailPrependsUnpaywall(t *testing.T) {
	pdf := []byte("%PDF-1.4 open-access article via unpaywall")
	base, lookups := unpaywallDownloadServer(t, pdf)
	c := newPerCallEmailClient(t, base)

	// With a per-call email, the ad-hoc Unpaywall source is prepended and serves it.
	res, err := c.DownloadItem(context.Background(), Item{DOI: "10.1/x", Email: "caller@example.com"}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("DownloadItem with a per-call email error = %v", err)
	}
	if res.Source != "unpaywall" {
		t.Errorf("Source = %q, want %q", res.Source, "unpaywall")
	}
	if res.SizeBytes != int64(len(pdf)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(pdf))
	}
	if lookups.Load() != 1 {
		t.Errorf("Unpaywall lookups = %d, want 1", lookups.Load())
	}

	// With no per-call email the chain is empty (unpaywall disabled): no lookup, error.
	lookups.Store(0)
	if _, noEmailErr := c.DownloadItem(context.Background(), Item{DOI: "10.1/x"}, t.TempDir(), ""); noEmailErr == nil {
		t.Fatal("DownloadItem for a DOI with no email should fail (no usable source)")
	}
	if lookups.Load() != 0 {
		t.Errorf("Unpaywall lookups = %d without a per-call email, want 0", lookups.Load())
	}
}
