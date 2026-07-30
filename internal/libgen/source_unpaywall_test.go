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

// unpaywallStatusServer serves body under status at any path, for the cases where
// the status rather than the payload is what is being pinned.
func unpaywallStatusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestUnpaywallErrorClassification pins which failures Resolve reports as Unpaywall
// answering "this article has no free copy" (ErrNotIndexed) and which as Unpaywall
// being unable to answer (ErrSourceUnavailable).
//
// The chain acts on the difference: a clean miss is returned straight out of
// startAttempt and costs one request, while an unclassified failure is put through
// the whole start-retry schedule and an unavailable one additionally cools the
// source down. The paywalled and no-PDF branches already had tests, but none of
// them looked at the tag.
func TestUnpaywallErrorClassification(t *testing.T) {
	const doi = "10.1234/paywalled"

	t.Run("a paywalled article is a clean miss", func(t *testing.T) {
		srv := unpaywallStatusServer(t, http.StatusOK, `{"is_oa":false}`)
		s := unpaywallSource{email: "e@example.com", http: srv.Client(), baseURL: srv.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("an OA article with no fetchable location is a clean miss", func(t *testing.T) {
		srv := unpaywallStatusServer(t, http.StatusOK, `{"is_oa":true,"oa_locations":[{"host_type":"repository"}]}`)
		s := unpaywallSource{email: "e@example.com", http: srv.Client(), baseURL: srv.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a transient status is unavailability", func(t *testing.T) {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			srv := unpaywallStatusServer(t, status, "")
			s := unpaywallSource{email: "e@example.com", http: srv.Client(), baseURL: srv.URL}

			_, err := s.Resolve(context.Background(), Item{DOI: doi})
			assertUnavailable(t, err)
		}
	})

	t.Run("a transport failure is unavailability", func(t *testing.T) {
		s := unpaywallSource{email: "e@example.com", http: refusingClient(), baseURL: "https://api.unpaywall.invalid"}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
	})

	t.Run("a 404 for an unknown DOI is a clean miss", func(t *testing.T) {
		// Unpaywall answers 404 for a DOI it has no record of. That is a settled
		// answer, so it is tagged: startAttempt returns a clean miss unwrapped and
		// skips the start-retry schedule rather than re-asking a question already
		// answered. It must not put the source in cooldown either — the service is
		// working, it simply does not know this DOI.
		srv := unpaywallStatusServer(t, http.StatusNotFound, `{"error":true,"message":"not found"}`)
		s := unpaywallSource{email: "e@example.com", http: srv.Client(), baseURL: srv.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
		if cooldownWorthy(context.Background(), err) {
			t.Error("an unknown DOI put Unpaywall in cooldown")
		}
	})

	t.Run("a body that is not the expected JSON is neither", func(t *testing.T) {
		// An HTML error page served with a 200 is the shape a captive portal or a
		// misrouted proxy produces. It is no verdict on the article and none on the
		// service, so it must read as neither.
		srv := unpaywallStatusServer(t, http.StatusOK, "<html>gateway</html>")
		s := unpaywallSource{email: "e@example.com", http: srv.Client(), baseURL: srv.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		if err == nil {
			t.Fatal("an undecodable body must not resolve")
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("an undecodable body read as the article having no free copy")
		}
		if cooldownWorthy(context.Background(), err) {
			t.Error("an undecodable body put Unpaywall in cooldown")
		}
	})

	t.Run("a missing contact email is neither", func(t *testing.T) {
		// A deployment with no email configured is a configuration gap, not an outage:
		// cooling the source down would hide the real cause behind a five-minute skip.
		s := unpaywallSource{http: refusingClient()}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		if err == nil {
			t.Fatal("Resolve without an email must fail before touching the API")
		}
		if cooldownWorthy(context.Background(), err) {
			t.Error("a missing email put Unpaywall in cooldown")
		}
	})
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

// TestUnpaywall_OALocationsPreferPublished verifies that when best_oa_location
// carries no direct PDF link, Resolve scans oa_locations and prefers a
// published/publisher version's url_for_pdf over an earlier repository version.
func TestUnpaywall_OALocationsPreferPublished(t *testing.T) {
	const body = `{
	  "is_oa": true,
	  "best_oa_location": {"url_for_pdf": null, "url": "https://landing.example/article"},
	  "oa_locations": [
	    {"url_for_pdf": "https://repo.example/preprint.pdf", "url": "https://repo.example/rec", "host_type": "repository", "version": "submittedVersion"},
	    {"url_for_pdf": "https://publisher.example/final.pdf", "url": "https://publisher.example/rec", "host_type": "publisher", "version": "publishedVersion"}
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != "https://publisher.example/final.pdf" {
		t.Errorf("FileURL = %q, want the published/publisher PDF", res.FileURL)
	}
}

// TestUnpaywall_OALocationsAnyPDF verifies that when no location is a
// published/publisher version, Resolve still returns the first oa_location that
// exposes a url_for_pdf rather than failing.
func TestUnpaywall_OALocationsAnyPDF(t *testing.T) {
	const body = `{
	  "is_oa": true,
	  "best_oa_location": {"url_for_pdf": null, "url": "https://landing.example/article"},
	  "oa_locations": [
	    {"url_for_pdf": null, "url": "https://repo.example/landing", "host_type": "repository", "version": "submittedVersion"},
	    {"url_for_pdf": "https://repo.example/preprint.pdf", "url": "https://repo.example/rec", "host_type": "repository", "version": "acceptedVersion"}
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != "https://repo.example/preprint.pdf" {
		t.Errorf("FileURL = %q, want the only PDF-bearing location", res.FileURL)
	}
}

// TestUnpaywall_LandingURLLastResort verifies that an OA record exposing no
// url_for_pdf anywhere falls back to best_oa_location.url (the landing page) rather
// than failing, since that URL commonly redirects to the article file.
func TestUnpaywall_LandingURLLastResort(t *testing.T) {
	const body = `{
	  "is_oa": true,
	  "best_oa_location": {"url_for_pdf": null, "url": "https://landing.example/article"},
	  "oa_locations": [
	    {"url_for_pdf": null, "url": "https://landing.example/article", "host_type": "publisher", "version": "publishedVersion"}
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != "https://landing.example/article" {
		t.Errorf("FileURL = %q, want the landing-page fallback", res.FileURL)
	}
}

// TestUnpaywall_DistinctDiagnoses verifies the error taxonomy stays distinct: a
// not-open-access record reports "not open access", while an OA record with no
// downloadable location reports "no open-access PDF". The two are separate
// diagnoses so a caller can tell a paywalled DOI from an OA one Unpaywall simply
// cannot serve a file for.
func TestUnpaywall_DistinctDiagnoses(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "not OA",
			body: `{"is_oa": false, "best_oa_location": null, "oa_locations": []}`,
			want: "not open access",
		},
		{
			name: "OA but no downloadable location",
			body: `{"is_oa": true, "best_oa_location": null, "oa_locations": []}`,
			want: "no open-access PDF",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			s := unpaywallSource{email: "mail@jmrp.io", http: srv.Client(), baseURL: srv.URL}
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

// TestUnpaywallRequestBuildError covers the request-construction failure: a base
// URL carrying a control character cannot be turned into a request.
func TestUnpaywallRequestBuildError(t *testing.T) {
	s := unpaywallSource{email: "mail@jmrp.io", baseURL: "http://\x7f", http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Error("Resolve should fail when the endpoint cannot be built into a request")
	}
}

// TestUnpaywallDefaultClientTransportError covers the default-client fallback (http
// is nil) together with the transport-error branch: a dead address makes the
// request fail.
func TestUnpaywallDefaultClientTransportError(t *testing.T) {
	s := unpaywallSource{email: "mail@jmrp.io", baseURL: "http://127.0.0.1:0"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Error("Resolve should fail when the Unpaywall request cannot be sent")
	}
}

// unpaywallRoundTripper records the requested URL and replies with a canned OA
// response, so a test can drive Resolve without a real network host.
type unpaywallRoundTripper struct{ gotURL string }

func (rt *unpaywallRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.gotURL = r.URL.String()
	body := `{"is_oa":true,"best_oa_location":{"url_for_pdf":"https://cdn.example/x.pdf"}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// TestUnpaywallDefaultBaseURL covers the base-URL fallback: with baseURL empty the
// source targets the documented public API host (unpaywallAPIBase). A stub
// transport intercepts the request so no real network call is made.
func TestUnpaywallDefaultBaseURL(t *testing.T) {
	rt := &unpaywallRoundTripper{}
	s := unpaywallSource{email: "mail@jmrp.io", http: &http.Client{Transport: rt}}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != "https://cdn.example/x.pdf" {
		t.Errorf("FileURL = %q, want the stubbed PDF URL", res.FileURL)
	}
	if !strings.HasPrefix(rt.gotURL, unpaywallAPIBase) {
		t.Errorf("request URL %q should default to the public API base %q", rt.gotURL, unpaywallAPIBase)
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
