package libgen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// openalexTestServer serves the given testdata fixture at any path and records the
// last request URI, so tests can assert both the parsed outcome and the shape of
// the request that produced it.
func openalexTestServer(t *testing.T, fixture string) (*httptest.Server, *string) {
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

// openalexBodyServer serves body under status at any path, for the cases where the
// payload or the status rather than a whole fixture is what is being pinned.
func openalexBodyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOpenAlexResolveOA verifies that an open-access record resolves to the PDF
// URL with MD5 verification disabled and a pdf extension, and that the request was
// the free single-entity lookup: the DOI in the path behind the doi: prefix, and
// the field projection that keeps the response small.
func TestOpenAlexResolveOA(t *testing.T) {
	srv, lastURI := openalexTestServer(t, "openalex_oa.json")
	s := openalexSource{http: srv.Client(), baseURL: srv.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1093/bib/bbad467"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	const wantURL = "https://academic.oup.com/bib/article-pdf/25/1/bbad467/55123365/bbad467.pdf"
	if res.FileURL != wantURL {
		t.Errorf("FileURL = %q, want %q", res.FileURL, wantURL)
	}
	if res.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false (DOI-keyed, no md5 to verify)")
	}
	if res.Ext != "pdf" {
		t.Errorf("Ext = %q, want %q", res.Ext, "pdf")
	}
	if !strings.Contains(*lastURI, "/doi:10.1093/bib/bbad467") {
		t.Errorf("request URI %q is not the single-entity doi: lookup with a raw slash", *lastURI)
	}
	if strings.Contains(*lastURI, "%2F") {
		t.Errorf("request URI %q percent-encoded the DOI slash, want it raw", *lastURI)
	}
	if !strings.Contains(*lastURI, "select=") {
		t.Errorf("request URI %q does not project the response with select", *lastURI)
	}
}

// TestOpenAlexResolveNotOA verifies that a paywalled record yields an error so the
// download chain falls through to the next source, and that the error names the
// paywall rather than a missing file.
func TestOpenAlexResolveNotOA(t *testing.T) {
	srv, _ := openalexTestServer(t, "openalex_notoa.json")
	s := openalexSource{http: srv.Client(), baseURL: srv.URL}

	_, err := s.Resolve(context.Background(), Item{DOI: "10.2307/2937956"})
	if err == nil {
		t.Fatal("Resolve() on a paywalled work should return an error")
	}
	if !strings.Contains(err.Error(), "not open access") {
		t.Errorf("Resolve() error = %q, want it to name the paywall", err.Error())
	}
	assertCleanMiss(t, err)
}

// TestOpenAlexErrorClassification pins which failures Resolve reports as OpenAlex
// answering "no free copy here" (ErrNotIndexed) and which as OpenAlex being unable
// to answer (ErrSourceUnavailable).
//
// The chain acts on the difference: a clean miss is returned straight out of
// startAttempt and costs one request, while an unavailable one additionally cools
// the source down.
func TestOpenAlexErrorClassification(t *testing.T) {
	const doi = "10.1234/whatever"

	t.Run("a paywalled work is a clean miss", func(t *testing.T) {
		srv := openalexBodyServer(t, http.StatusOK, `{"open_access":{"is_oa":false}}`)
		s := openalexSource{http: srv.Client(), baseURL: srv.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("an OA work with no PDF location is a clean miss", func(t *testing.T) {
		srv := openalexBodyServer(t, http.StatusOK,
			`{"open_access":{"is_oa":true},"best_oa_location":{"is_oa":true,"pdf_url":null},"locations":[]}`)
		s := openalexSource{http: srv.Client(), baseURL: srv.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a 404 for a DOI OpenAlex has no record of is a clean miss", func(t *testing.T) {
		// OpenAlex answers 404 for a DOI outside the catalog, and does so with an
		// HTML body. That is a settled answer about the DOI, so it must skip the
		// start-retry schedule and must NOT cool the source down: the service is
		// working, it simply does not know this identifier.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<!doctype html><title>404 Not Found</title>")
		}))
		t.Cleanup(srv.Close)
		s := openalexSource{http: srv.Client(), baseURL: srv.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a transient status is unavailability", func(t *testing.T) {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			srv := openalexBodyServer(t, status, "")
			s := openalexSource{http: srv.Client(), baseURL: srv.URL}

			_, err := s.Resolve(context.Background(), Item{DOI: doi})
			assertUnavailable(t, err)
		}
	})

	t.Run("a transport failure is unavailability", func(t *testing.T) {
		s := openalexSource{http: refusingClient(), baseURL: "https://api.openalex.invalid"}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
	})

	t.Run("a body that is not the expected JSON is neither", func(t *testing.T) {
		// An HTML error page served with a 200 is the shape a captive portal or a
		// misrouted proxy produces. It is no verdict on the work and none on the
		// service, so it must read as neither.
		srv := openalexBodyServer(t, http.StatusOK, "<html>gateway</html>")
		s := openalexSource{http: srv.Client(), baseURL: srv.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		if err == nil {
			t.Fatal("an undecodable body must not resolve")
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("an undecodable body read as the work having no free copy")
		}
		if cooldownWorthy(context.Background(), err) {
			t.Error("an undecodable body put OpenAlex in cooldown")
		}
	})
}

// TestOpenAlex_DistinctDiagnoses verifies the two negative outcomes stay separate:
// a paywalled work reports "not open access", while an open-access work OpenAlex
// exposes no file for reports "no open-access PDF". Collapsing them would hide
// which of the two happened from the chain log.
func TestOpenAlex_DistinctDiagnoses(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "not OA",
			body: `{"open_access":{"is_oa":false},"best_oa_location":null,"locations":[]}`,
			want: "not open access",
		},
		{
			name: "OA but no downloadable location",
			body: `{"open_access":{"is_oa":true},"best_oa_location":null,"locations":[]}`,
			want: "no open-access PDF",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := openalexBodyServer(t, http.StatusOK, tc.body)
			s := openalexSource{http: srv.Client(), baseURL: srv.URL}

			_, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
			if err == nil {
				t.Fatalf("Resolve() error = nil, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Resolve() error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestOpenAlex_LocationsPreferPublished verifies that when best_oa_location carries
// no direct PDF link, Resolve scans locations and prefers the published version
// over an earlier repository preprint, rather than taking whichever comes first.
func TestOpenAlex_LocationsPreferPublished(t *testing.T) {
	const body = `{
	  "open_access": {"is_oa": true},
	  "best_oa_location": {"is_oa": true, "pdf_url": null},
	  "locations": [
	    {"is_oa": true, "pdf_url": "https://repo.example/preprint.pdf", "version": "submittedVersion"},
	    {"is_oa": true, "pdf_url": "https://publisher.example/final.pdf", "version": "publishedVersion"}
	  ]
	}`
	srv := openalexBodyServer(t, http.StatusOK, body)
	s := openalexSource{http: srv.Client(), baseURL: srv.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != "https://publisher.example/final.pdf" {
		t.Errorf("FileURL = %q, want the published version", res.FileURL)
	}
}

// TestOpenAlex_LocationsAnyPDF verifies that when no location is the published
// version, Resolve still returns the first open-access location exposing a pdf_url
// rather than failing: an accepted manuscript is a legitimate free copy.
func TestOpenAlex_LocationsAnyPDF(t *testing.T) {
	const body = `{
	  "open_access": {"is_oa": true},
	  "best_oa_location": {"is_oa": true, "pdf_url": null},
	  "locations": [
	    {"is_oa": true, "pdf_url": null, "version": "submittedVersion"},
	    {"is_oa": true, "pdf_url": "https://repo.example/accepted.pdf", "version": "acceptedVersion"}
	  ]
	}`
	srv := openalexBodyServer(t, http.StatusOK, body)
	s := openalexSource{http: srv.Client(), baseURL: srv.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != "https://repo.example/accepted.pdf" {
		t.Errorf("FileURL = %q, want the only PDF-bearing open-access location", res.FileURL)
	}
}

// TestOpenAlex_RejectsUnusableLocations verifies the two filters applied to the
// location list, both of which guard the download pipeline rather than the
// catalog's tidiness.
//
// A location with is_oa false is the publisher's paywalled record: OpenAlex lists
// it for every work, and following its pdf_url would hand the pipeline a file this
// source has no right to serve. A pdf_url that is not an absolute http(s) URL —
// relative, or a javascript: scheme — is something the pipeline cannot stream from
// at all, so it must never leave Resolve.
func TestOpenAlex_RejectsUnusableLocations(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "a paywalled location is not served",
			body: `{"open_access":{"is_oa":true},"best_oa_location":null,
			        "locations":[{"is_oa":false,"pdf_url":"https://publisher.example/paywalled.pdf","version":"publishedVersion"}]}`,
		},
		{
			name: "a relative pdf_url is not served",
			body: `{"open_access":{"is_oa":true},"best_oa_location":null,
			        "locations":[{"is_oa":true,"pdf_url":"/files/x.pdf","version":"publishedVersion"}]}`,
		},
		{
			name: "a javascript pdf_url is not served",
			body: `{"open_access":{"is_oa":true},"best_oa_location":{"is_oa":true,"pdf_url":"javascript:alert(1)"},
			        "locations":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := openalexBodyServer(t, http.StatusOK, tc.body)
			s := openalexSource{http: srv.Client(), baseURL: srv.URL}

			res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
			if err == nil {
				t.Fatalf("Resolve() returned %q, want a miss", res.FileURL)
			}
			assertCleanMiss(t, err)
		})
	}
}

// TestOpenAlex_BestLocationWins verifies that a usable best_oa_location short-circuits
// the location scan: it is OpenAlex's own pick, so a different location must not
// override it even when that one is the published version.
func TestOpenAlex_BestLocationWins(t *testing.T) {
	const body = `{
	  "open_access": {"is_oa": true},
	  "best_oa_location": {"is_oa": true, "pdf_url": "https://best.example/pick.pdf", "version": "acceptedVersion"},
	  "locations": [
	    {"is_oa": true, "pdf_url": "https://publisher.example/final.pdf", "version": "publishedVersion"}
	  ]
	}`
	srv := openalexBodyServer(t, http.StatusOK, body)
	s := openalexSource{http: srv.Client(), baseURL: srv.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != "https://best.example/pick.pdf" {
		t.Errorf("FileURL = %q, want OpenAlex's own best_oa_location", res.FileURL)
	}
}

// TestOpenAlexKeyless verifies the source sends no credential of any kind. The
// single-entity lookup is free and unmetered, which is the reason this source can
// be keyless at all, so an api_key or Authorization creeping in would be a
// regression against its whole premise.
func TestOpenAlexKeyless(t *testing.T) {
	var authSeen atomic.Bool
	var lastURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authSeen.Store(true)
		}
		lastURI = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"open_access":{"is_oa":true},"best_oa_location":{"is_oa":true,"pdf_url":"https://cdn.example/x.pdf"}}`)
	}))
	t.Cleanup(srv.Close)
	s := openalexSource{http: srv.Client(), baseURL: srv.URL}

	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if authSeen.Load() {
		t.Error("Resolve sent an Authorization header; the source must stay keyless")
	}
	if strings.Contains(lastURI, "api_key") {
		t.Errorf("request URI %q carries an api_key; the source must stay keyless", lastURI)
	}
}

// openalexRoundTripper records the requested URL and replies with a canned
// open-access response, so a test can drive Resolve without a real network host.
type openalexRoundTripper struct{ gotURL string }

// RoundTrip records the request URL and returns the canned open-access body.
func (rt *openalexRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.gotURL = r.URL.String()
	body := `{"open_access":{"is_oa":true},"best_oa_location":{"is_oa":true,"pdf_url":"https://cdn.example/x.pdf"}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// TestOpenAlexDefaultBaseURL covers the base-URL fallback: with baseURL empty the
// source targets the documented public API host. A stub transport intercepts the
// request so no real network call is made.
func TestOpenAlexDefaultBaseURL(t *testing.T) {
	rt := &openalexRoundTripper{}
	s := openalexSource{http: &http.Client{Transport: rt}}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != "https://cdn.example/x.pdf" {
		t.Errorf("FileURL = %q, want the stubbed PDF URL", res.FileURL)
	}
	if !strings.HasPrefix(rt.gotURL, openAlexAPIBase) {
		t.Errorf("request URL %q should default to the public API base %q", rt.gotURL, openAlexAPIBase)
	}
}

// TestOpenAlexRequestBuildError covers the request-construction failure: a base URL
// carrying a control character cannot be turned into a request, and that has to
// surface as a source error rather than a panic.
func TestOpenAlexRequestBuildError(t *testing.T) {
	s := openalexSource{baseURL: "http://\x7f", http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Error("Resolve should fail when the endpoint cannot be built into a request")
	}
}

// TestOpenAlexDefaultClientTransportError covers the default-client fallback (http
// is nil) together with the transport-error branch: a dead address makes the
// request fail.
func TestOpenAlexDefaultClientTransportError(t *testing.T) {
	s := openalexSource{baseURL: "http://127.0.0.1:0"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Error("Resolve should fail when the OpenAlex request cannot be sent")
	}
}

// TestOpenAlexSupports verifies that the source claims DOI-keyed items only, and
// reports the name the chain and LIBGEN_MCP_SOURCES address it by.
func TestOpenAlexSupports(t *testing.T) {
	s := openalexSource{}
	if s.Supports(Item{DOI: ""}) {
		t.Error("Supports(empty DOI) = true, want false")
	}
	if s.Supports(Item{MD5: "d41d8cd98f00b204e9800998ecf8427e"}) {
		t.Error("Supports(md5-only item) = true, want false (OpenAlex is keyed by DOI)")
	}
	if !s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(non-empty DOI) = false, want true")
	}
	if s.Name() != "openalex" {
		t.Errorf("Name() = %q, want %q", s.Name(), "openalex")
	}
}
