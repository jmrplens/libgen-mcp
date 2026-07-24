package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// europePMCSearchServer builds an httptest server that answers the Europe PMC
// search API with the named fixture under the given HTTP status, and records the
// query string it was asked for.
func europePMCSearchServer(t *testing.T, fixture string, status int, gotQuery *string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", fixture, err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotQuery != nil {
			*gotQuery = r.URL.Query().Get("query")
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

// europePMCRenderServer builds an httptest server that serves a PDF at the render
// paths whose Content-Type gate serves reports true for, unless the path is listed
// in fail404, which returns 404 so the fallback branch is exercised.
func europePMCRenderServer(t *testing.T, fail404 ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range fail404 {
			if strings.HasPrefix(r.URL.Path, p) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4 europe pmc payload"))
	}))
}

// TestEuropePMCSupports verifies the source claims DOI-keyed items only and names
// itself "europepmc".
func TestEuropePMCSupports(t *testing.T) {
	s := europePMCSource{}
	if !s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(DOI) = false, want true")
	}
	if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(md5-only) = true, want false")
	}
	if s.Name() != "europepmc" {
		t.Errorf("Name() = %q, want %q", s.Name(), "europepmc")
	}
}

// TestEuropePMCResolveOA verifies an open-access DOI resolves to the PMC render
// backend URL, sends an exact-match DOI query, declares a pdf extension, and leaves
// MD5 verification off (DOI items carry no digest).
func TestEuropePMCResolveOA(t *testing.T) {
	const doi = "10.1371/journal.pbio.1002533"
	var gotQuery string
	search := europePMCSearchServer(t, "europepmc_oa.json", http.StatusOK, &gotQuery)
	defer search.Close()
	render := europePMCRenderServer(t)
	defer render.Close()

	s := europePMCSource{http: search.Client(), searchBase: search.URL, renderBase: render.URL}
	got, err := s.Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := `DOI:"` + doi + `"`; gotQuery != want {
		t.Errorf("search query = %q, want %q", gotQuery, want)
	}
	if !strings.Contains(got.FileURL, "/backend/ptpmcrender.fcgi") || !strings.Contains(got.FileURL, "PMC4991899") {
		t.Errorf("FileURL = %q, want the ptpmcrender backend for PMC4991899", got.FileURL)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
}

// TestEuropePMCResolveFallsBackToArticleRender verifies that when the PMC render
// backend does not serve a PDF, Resolve falls back to the article render path.
func TestEuropePMCResolveFallsBackToArticleRender(t *testing.T) {
	search := europePMCSearchServer(t, "europepmc_oa.json", http.StatusOK, nil)
	defer search.Close()
	render := europePMCRenderServer(t, "/backend/ptpmcrender.fcgi")
	defer render.Close()

	s := europePMCSource{http: search.Client(), searchBase: search.URL, renderBase: render.URL}
	got, err := s.Resolve(context.Background(), Item{DOI: "10.1371/journal.pbio.1002533"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !strings.Contains(got.FileURL, "/articles/PMC4991899") {
		t.Errorf("FileURL = %q, want the article render fallback", got.FileURL)
	}
}

// TestEuropePMCResolveNotIndexed verifies a DOI Europe PMC does not index yields a
// distinct "not indexed" error so the chain advances.
func TestEuropePMCResolveNotIndexed(t *testing.T) {
	search := europePMCSearchServer(t, "europepmc_miss.json", http.StatusOK, nil)
	defer search.Close()

	s := europePMCSource{http: search.Client(), searchBase: search.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/nope"})
	if err == nil || !strings.Contains(err.Error(), "not indexed") {
		t.Fatalf("Resolve() error = %v, want a 'not indexed' error", err)
	}
}

// TestEuropePMCResolveNoOpenAccess verifies a DOI that is indexed but has no
// open-access full text yields a distinct error, separate from the not-indexed one.
func TestEuropePMCResolveNoOpenAccess(t *testing.T) {
	search := europePMCSearchServer(t, "europepmc_no_oa.json", http.StatusOK, nil)
	defer search.Close()

	s := europePMCSource{http: search.Client(), searchBase: search.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1016/j.cell.2011.02.013"})
	if err == nil || !strings.Contains(err.Error(), "no open-access full text") {
		t.Fatalf("Resolve() error = %v, want a 'no open-access full text' error", err)
	}
}

// TestEuropePMCResolveIndexedButNotOpenAccess verifies the OA flag is enforced, not
// merely parsed: a record Europe PMC holds in full text (inEPMC=Y, with a PMCID)
// but which is NOT open access must be refused rather than handed back as a PDF
// URL. Without this the source would breach its open-access-only contract for every
// paywalled article PMC happens to hold.
func TestEuropePMCResolveIndexedButNotOpenAccess(t *testing.T) {
	search := europePMCSearchServer(t, "europepmc_indexed_not_oa.json", http.StatusOK, nil)
	defer search.Close()
	// A render server that would happily serve a PDF, so a pass here can only mean
	// the OA guard let the record through.
	render := europePMCRenderServer(t)
	defer render.Close()

	s := europePMCSource{http: search.Client(), searchBase: search.URL, renderBase: render.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.9999/indexed.but.not.oa"})
	if err == nil || !strings.Contains(err.Error(), "no open-access full text") {
		t.Fatalf("Resolve() error = %v, want a 'no open-access full text' error for a non-OA record", err)
	}
}

// TestEuropePMCResolveHTTPError verifies a non-200 from the search API surfaces as
// an error.
func TestEuropePMCResolveHTTPError(t *testing.T) {
	search := europePMCSearchServer(t, "europepmc_miss.json", http.StatusInternalServerError, nil)
	defer search.Close()

	s := europePMCSource{http: search.Client(), searchBase: search.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail on an HTTP 500 from the search API")
	}
}

// TestEuropePMCResolveMalformed verifies a malformed JSON search response surfaces
// as a decode error rather than a panic.
func TestEuropePMCResolveMalformed(t *testing.T) {
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resultList": {`))
	}))
	defer search.Close()

	s := europePMCSource{http: search.Client(), searchBase: search.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail on a malformed JSON response")
	}
}

// TestEuropePMCResolveNoReachableRender verifies that when neither render endpoint
// serves a PDF, Resolve reports it rather than returning a dead URL.
func TestEuropePMCResolveNoReachableRender(t *testing.T) {
	search := europePMCSearchServer(t, "europepmc_oa.json", http.StatusOK, nil)
	defer search.Close()
	render := europePMCRenderServer(t, "/backend", "/articles")
	defer render.Close()

	s := europePMCSource{http: search.Client(), searchBase: search.URL, renderBase: render.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1371/journal.pbio.1002533"})
	if err == nil || !strings.Contains(err.Error(), "no reachable PDF endpoint") {
		t.Fatalf("Resolve() error = %v, want a 'no reachable PDF endpoint' error", err)
	}
}
