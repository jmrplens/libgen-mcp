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

// coreDownloadServer builds an httptest server standing in for CORE's file host.
// It serves a PDF, a 404, or a non-PDF body per mode, and fails the test if the
// request carries an Authorization header — the API key must never reach the file
// URL, only the API host.
func coreDownloadServer(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("download/probe request carried Authorization %q; the key must stay on the API host", auth)
		}
		switch mode {
		case "404":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error>not found</Error>`))
		case "nonpdf":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>not a pdf</html>"))
		default:
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4 core payload"))
		}
	}))
}

// coreAPIServer builds an httptest server standing in for the CORE search API. It
// answers with body under status, and records the Authorization header and request
// path (so tests can assert the Bearer token and the trailing-slash endpoint).
func coreAPIServer(t *testing.T, body string, status int, gotAuth, gotPath *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		if gotPath != nil {
			*gotPath = r.URL.Path
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// coreFixtureServer answers the CORE search API with the named fixture under the
// given status. Used for cases whose download URL is never probed (empty URL,
// no results, key rejected).
func coreFixtureServer(t *testing.T, fixture string, status int) *httptest.Server {
	t.Helper()
	var body []byte
	if fixture != "" {
		b, err := os.ReadFile("testdata/" + fixture)
		if err != nil {
			t.Fatalf("reading fixture %s: %v", fixture, err)
		}
		body = b
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

// coreLiveSource wires a coreSource to a search API whose downloadUrl points at a
// live PDF-serving endpoint, so Resolve succeeds through the liveness probe. It
// registers cleanup for both servers and returns the source and the expected URL.
func coreLiveSource(t *testing.T) (coreSource, string) {
	t.Helper()
	dl := coreDownloadServer(t, "pdf")
	t.Cleanup(dl.Close)
	downloadURL := dl.URL + "/download/12345678.pdf"
	api := coreAPIServer(t, `{"results":[{"downloadUrl":"`+downloadURL+`"}]}`, http.StatusOK, nil, nil)
	t.Cleanup(api.Close)
	return coreSource{http: api.Client(), key: "k", apiBase: api.URL}, downloadURL
}

// TestCoreErrorClassification pins which failures Resolve reports as CORE answering
// "I hold no free full text for this" (ErrNotIndexed) and which as CORE being
// unusable right now (ErrSourceUnavailable).
//
// CORE is the one source whose credential can be rejected per-request, and that
// case is the reason the distinction matters here: a rejected key fails every item
// identically, so tagging it as a miss would re-ask CORE for every DOI in a session
// instead of setting it aside once. The branches below were all reached by existing
// tests, none of which looked at the tag.
func TestCoreErrorClassification(t *testing.T) {
	const doi = "10.1234/known"

	t.Run("a DOI CORE does not hold is a clean miss", func(t *testing.T) {
		api := coreAPIServer(t, `{"results":[]}`, http.StatusOK, nil, nil)
		defer api.Close()
		s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a held DOI with no download URL is a clean miss", func(t *testing.T) {
		api := coreAPIServer(t, `{"results":[{"downloadUrl":""}]}`, http.StatusOK, nil, nil)
		defer api.Close()
		s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a stale download URL is a clean miss", func(t *testing.T) {
		// CORE's downloadUrl often outlives the file. The probe failing says the copy
		// is gone, not that CORE is broken, so the source must stay in the chain.
		dl := coreDownloadServer(t, "404")
		defer dl.Close()
		api := coreAPIServer(t, `{"results":[{"downloadUrl":"`+dl.URL+`/gone.pdf"}]}`, http.StatusOK, nil, nil)
		defer api.Close()
		s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a rejected key is unavailability", func(t *testing.T) {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			api := coreAPIServer(t, `{"message":"invalid token"}`, status, nil, nil)
			s := coreSource{http: api.Client(), key: "bad", apiBase: api.URL}

			_, err := s.Resolve(context.Background(), Item{DOI: doi})
			assertUnavailable(t, err)
			if errors.Is(err, ErrNotIndexed) {
				t.Errorf("HTTP %d (rejected key) read as the DOI being unheld", status)
			}
			api.Close()
		}
	})

	t.Run("a transient status is unavailability", func(t *testing.T) {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			api := coreAPIServer(t, "", status, nil, nil)
			s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}

			_, err := s.Resolve(context.Background(), Item{DOI: doi})
			assertUnavailable(t, err)
			api.Close()
		}
	})

	t.Run("a transport failure is unavailability", func(t *testing.T) {
		s := coreSource{http: refusingClient(), key: "k", apiBase: "https://api.core.invalid"}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
	})

	t.Run("an undecodable body is neither", func(t *testing.T) {
		api := coreAPIServer(t, "<html>proxy error</html>", http.StatusOK, nil, nil)
		defer api.Close()
		s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		if err == nil {
			t.Fatal("an undecodable body must not resolve")
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("an undecodable body read as the DOI being unheld")
		}
		if cooldownWorthy(context.Background(), err) {
			t.Error("an undecodable body put CORE in cooldown")
		}
	})

	t.Run("a missing key is neither", func(t *testing.T) {
		// No key is a deployment choice, not an outage: CORE simply is not configured,
		// and a cooldown would misreport that as a service problem.
		s := coreSource{http: refusingClient()}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		if err == nil {
			t.Fatal("Resolve without a key must fail before touching the API")
		}
		if cooldownWorthy(context.Background(), err) {
			t.Error("an unconfigured key put CORE in cooldown")
		}
	})
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

// TestCoreResolveOA verifies a DOI CORE holds resolves to a live downloadUrl:
// the search request carries the Bearer key and hits the trailing-slash endpoint,
// the download URL is probed (without the key) and confirmed a PDF, and the result
// declares a pdf extension with MD5 verification off and no leaked header.
func TestCoreResolveOA(t *testing.T) {
	dl := coreDownloadServer(t, "pdf")
	defer dl.Close()
	downloadURL := dl.URL + "/download/12345678.pdf"

	var gotAuth, gotPath string
	api := coreAPIServer(t, `{"results":[{"downloadUrl":"`+downloadURL+`"}]}`, http.StatusOK, &gotAuth, &gotPath)
	defer api.Close()

	s := coreSource{http: api.Client(), key: "secret-key", apiBase: api.URL}
	got, err := s.Resolve(context.Background(), Item{DOI: "10.1371/journal.pbio.1002533"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-key")
	}
	if gotPath != "/search/works/" {
		t.Errorf("request path = %q, want /search/works/ (trailing slash avoids the 301)", gotPath)
	}
	if got.FileURL != downloadURL {
		t.Errorf("FileURL = %q, want the work's downloadUrl %q", got.FileURL, downloadURL)
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

// TestCoreResolveDeadDownloadURL verifies a stale downloadUrl that 404s is rejected
// at resolve time with the "no downloadable" error, so the chain falls through
// instead of wasting a download attempt.
func TestCoreResolveDeadDownloadURL(t *testing.T) {
	dl := coreDownloadServer(t, "404")
	defer dl.Close()
	api := coreAPIServer(t, `{"results":[{"downloadUrl":"`+dl.URL+`/download/82450078.pdf"}]}`, http.StatusOK, nil, nil)
	defer api.Close()

	s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1016/j.cell.2011.02.013"})
	if err == nil || !strings.Contains(err.Error(), "no downloadable open-access full text") {
		t.Fatalf("Resolve() error = %v, want a 'no downloadable' error for a dead URL", err)
	}
}

// TestCoreResolveNonPDFDownloadURL verifies a downloadUrl that answers 200 but with
// a non-PDF body is likewise rejected at resolve time.
func TestCoreResolveNonPDFDownloadURL(t *testing.T) {
	dl := coreDownloadServer(t, "nonpdf")
	defer dl.Close()
	api := coreAPIServer(t, `{"results":[{"downloadUrl":"`+dl.URL+`/download/1.pdf"}]}`, http.StatusOK, nil, nil)
	defer api.Close()

	s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil || !strings.Contains(err.Error(), "no downloadable open-access full text") {
		t.Fatalf("Resolve() error = %v, want a 'no downloadable' error for a non-PDF URL", err)
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
	api := coreFixtureServer(t, "", http.StatusUnauthorized)
	defer api.Close()

	s := coreSource{http: api.Client(), key: "bad-key", apiBase: api.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil || !strings.Contains(err.Error(), "API key rejected") {
		t.Fatalf("Resolve() error = %v, want an 'API key rejected' error", err)
	}
}

// TestCoreResolveNotInCore verifies a DOI CORE does not hold yields a distinct
// error so the chain advances.
func TestCoreResolveNotInCore(t *testing.T) {
	api := coreFixtureServer(t, "core_miss.json", http.StatusOK)
	defer api.Close()

	s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/nope"})
	if err == nil || !strings.Contains(err.Error(), "not in CORE") {
		t.Fatalf("Resolve() error = %v, want a 'not in CORE' error", err)
	}
}

// TestCoreResolveNoDownloadURL verifies a DOI CORE holds only metadata for (no
// downloadUrl) yields the "no downloadable" error, without probing anything.
func TestCoreResolveNoDownloadURL(t *testing.T) {
	api := coreFixtureServer(t, "core_no_fulltext.json", http.StatusOK)
	defer api.Close()

	s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/metadata.only"})
	if err == nil || !strings.Contains(err.Error(), "no downloadable open-access full text") {
		t.Fatalf("Resolve() error = %v, want a 'no downloadable' error", err)
	}
}

// TestCoreResolveHTTPError verifies a non-200, non-auth status surfaces as an
// error.
func TestCoreResolveHTTPError(t *testing.T) {
	api := coreFixtureServer(t, "", http.StatusInternalServerError)
	defer api.Close()

	s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail on an HTTP 500")
	}
}

// TestCoreResolveMalformed verifies malformed JSON surfaces as a decode error
// rather than a panic.
func TestCoreResolveMalformed(t *testing.T) {
	api := coreAPIServer(t, `{"results": [`, http.StatusOK, nil, nil)
	defer api.Close()

	s := coreSource{http: api.Client(), key: "k", apiBase: api.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail on a malformed JSON response")
	}
}

// TestCoreSource_RejectsUnbuildableEndpoint covers the request-construction
// failure. It is not dead code: apiBase is deployment-supplied, and a value with
// a control character in it must surface as a clean source error rather than a
// panic or a silent skip.
func TestCoreSource_RejectsUnbuildableEndpoint(t *testing.T) {
	s := coreSource{http: http.DefaultClient, key: "k", apiBase: "http://\x7f-invalid"}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil {
		t.Fatal("an unbuildable endpoint must fail, not resolve")
	}
	if !strings.Contains(err.Error(), "core") {
		t.Fatalf("error should name the source, got %v", err)
	}
}

// TestCoreSource_FallsBackToTheProductionAPIBase covers the default-base branch,
// which every other test skips by injecting a test server. The context is
// canceled up front so the default is selected and the request then fails
// before any dial — the branch is exercised without the suite touching the
// network.
func TestCoreSource_FallsBackToTheProductionAPIBase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := coreSource{http: http.DefaultClient, key: "k"} // apiBase empty -> coreAPIBase
	if _, err := s.Resolve(ctx, Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("a canceled context must fail the resolve")
	}
}
