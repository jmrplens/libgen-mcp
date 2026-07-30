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

// biorxivFixtureServer builds an httptest server that answers the details API with
// the fixture chosen per server path: a request under /<server>/ is answered with
// perServer[server] (or a miss fixture when absent), recording the last path seen.
func biorxivFixtureServer(t *testing.T, perServer map[string]string, gotPath *string) *httptest.Server {
	t.Helper()
	load := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatalf("reading fixture %s: %v", name, err)
		}
		return b
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotPath != nil {
			*gotPath = r.URL.Path
		}
		for server, fixture := range perServer {
			if strings.Contains(r.URL.Path, "/"+server+"/") {
				_, _ = w.Write(load(fixture))
				return
			}
		}
		_, _ = w.Write(load("biorxiv_miss.json"))
	}))
}

// TestBiorxivSupports verifies the source claims only 10.1101 DOIs and names itself
// "biorxiv".
func TestBiorxivSupports(t *testing.T) {
	s := biorxivSource{}
	if !s.Supports(Item{DOI: "10.1101/2020.12.30.424878"}) {
		t.Error("Supports(10.1101 DOI) = false, want true")
	}
	if s.Supports(Item{DOI: "10.1016/j.cell.2011.02.013"}) {
		t.Error("Supports(non-preprint DOI) = true, want false")
	}
	if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(md5-only) = true, want false")
	}
	if s.Name() != "biorxiv" {
		t.Errorf("Name() = %q, want %q", s.Name(), "biorxiv")
	}
}

// TestBiorxivResolveLatestVersion verifies a preprint resolves to the latest
// version's full-text PDF on the bioRxiv content host, keeping the DOI's slashes
// literal, with a pdf extension and MD5 verification off.
func TestBiorxivResolveLatestVersion(t *testing.T) {
	const doi = "10.1101/2020.12.30.424878"
	var gotPath string
	api := biorxivFixtureServer(t, map[string]string{"biorxiv": "biorxiv_hit.json"}, &gotPath)
	defer api.Close()

	s := biorxivSource{http: api.Client(), apiBase: api.URL, contentBase: "https://content.example"}
	got, err := s.Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "/biorxiv/" + doi + "/na/json"; gotPath != want {
		t.Errorf("request path = %q, want %q (DOI slashes must stay literal)", gotPath, want)
	}
	// The hit fixture lists versions 1 and 2, so the latest (v2) must win.
	if want := "https://content.example/content/" + doi + "v2.full.pdf"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
}

// TestBiorxivResolveFallsBackToMedrxiv verifies a DOI absent from bioRxiv but
// present on medRxiv resolves against the medRxiv server.
func TestBiorxivResolveFallsBackToMedrxiv(t *testing.T) {
	const doi = "10.1101/2020.12.30.424878"
	api := biorxivFixtureServer(t, map[string]string{"medrxiv": "biorxiv_hit.json"}, nil)
	defer api.Close()

	s := biorxivSource{http: api.Client(), apiBase: api.URL, contentBase: "https://content.example"}
	got, err := s.Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://content.example/content/" + doi + "v2.full.pdf"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
}

// TestBiorxivContentHostByServer verifies each server maps to its production PDF
// host when no override is configured.
func TestBiorxivContentHostByServer(t *testing.T) {
	s := biorxivSource{}
	if got := s.contentHost("biorxiv"); got != "https://www.biorxiv.org" {
		t.Errorf("contentHost(biorxiv) = %q, want https://www.biorxiv.org", got)
	}
	if got := s.contentHost("medrxiv"); got != "https://www.medrxiv.org" {
		t.Errorf("contentHost(medrxiv) = %q, want https://www.medrxiv.org", got)
	}
}

// TestBiorxivResolveNotFound verifies a DOI neither server carries yields an error
// so the chain advances.
func TestBiorxivResolveNotFound(t *testing.T) {
	api := biorxivFixtureServer(t, map[string]string{}, nil) // both servers answer the miss fixture
	defer api.Close()

	s := biorxivSource{http: api.Client(), apiBase: api.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1101/does.not.exist"})
	if err == nil || !strings.Contains(err.Error(), "not found on bioRxiv or medRxiv") {
		t.Fatalf("Resolve() error = %v, want a not-found error", err)
	}
}

// TestBiorxivResolveErrorThenMissIsNotReportedAsNotFound verifies a lookup FAILURE
// is never laundered into a definitive "not found". When bioRxiv errors (HTTP 500)
// and medRxiv then explicitly misses, the retained failure must surface: reporting
// "not found on bioRxiv or medRxiv" would assert the preprint does not exist when
// one service merely broke, which is a different, actionable condition.
func TestBiorxivResolveErrorThenMissIsNotReportedAsNotFound(t *testing.T) {
	miss, err := os.ReadFile("testdata/biorxiv_miss.json")
	if err != nil {
		t.Fatalf("reading miss fixture: %v", err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/biorxiv/") {
			w.WriteHeader(http.StatusInternalServerError) // the server broke
			return
		}
		_, _ = w.Write(miss) // medRxiv genuinely does not carry it
	}))
	defer api.Close()

	s := biorxivSource{http: api.Client(), apiBase: api.URL}
	_, rerr := s.Resolve(context.Background(), Item{DOI: "10.1101/2020.12.30.424878"})
	if rerr == nil {
		t.Fatal("Resolve() should fail when one server errors and the other misses")
	}
	if strings.Contains(rerr.Error(), "not found on bioRxiv or medRxiv") {
		t.Errorf("a lookup failure was reported as a definitive miss: %v", rerr)
	}
	if !strings.Contains(rerr.Error(), "returned HTTP 500") {
		t.Errorf("error should surface the retained lookup failure, got: %v", rerr)
	}
}

// TestBiorxivResolveBothMissIsNotFound verifies the definitive wording is still
// used when BOTH servers explicitly miss, which is the only case that proves the
// preprint is absent.
func TestBiorxivResolveBothMissIsNotFound(t *testing.T) {
	api := biorxivFixtureServer(t, map[string]string{}, nil) // both answer the miss fixture
	defer api.Close()

	s := biorxivSource{http: api.Client(), apiBase: api.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1101/does.not.exist"})
	if err == nil || !strings.Contains(err.Error(), "not found on bioRxiv or medRxiv") {
		t.Fatalf("Resolve() error = %v, want the definitive not-found wording when both miss", err)
	}
}

// TestBiorxivErrorClassification pins the tag on each outcome, which is the part
// the download chain reads. The wording is already asserted elsewhere; the tag is
// what decides whether the source is re-asked on the start-retry schedule and
// whether it is set aside for five minutes afterwards, so "not found on bioRxiv or
// medRxiv" carrying the wrong tag would still cost a full retry budget per download.
func TestBiorxivErrorClassification(t *testing.T) {
	const doi = "10.1101/2020.12.30.424878"
	miss, err := os.ReadFile("testdata/biorxiv_miss.json")
	if err != nil {
		t.Fatalf("reading miss fixture: %v", err)
	}

	t.Run("both servers miss is a clean miss", func(t *testing.T) {
		api := biorxivFixtureServer(t, map[string]string{}, nil)
		defer api.Close()
		s := biorxivSource{http: api.Client(), apiBase: api.URL}

		_, rerr := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, rerr)
	})

	t.Run("one server broken and the other missing is unavailability", func(t *testing.T) {
		// The retained failure must keep its tag through Resolve's wrapper, or a
		// half-broken API reads as a settled answer about the preprint.
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/biorxiv/") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(miss)
		}))
		defer api.Close()
		s := biorxivSource{http: api.Client(), apiBase: api.URL}

		_, rerr := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, rerr)
		if errors.Is(rerr, ErrNotIndexed) {
			t.Error("a broken lookup was also tagged as a settled miss")
		}
	})

	t.Run("a transient status on both servers is unavailability", func(t *testing.T) {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			s := biorxivSource{http: api.Client(), apiBase: api.URL}

			_, rerr := s.Resolve(context.Background(), Item{DOI: doi})
			assertUnavailable(t, rerr)
			api.Close()
		}
	})

	t.Run("a transport failure is unavailability", func(t *testing.T) {
		s := biorxivSource{http: refusingClient(), apiBase: "https://api.biorxiv.invalid"}

		_, rerr := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, rerr)
	})

	t.Run("a truncated body is neither", func(t *testing.T) {
		// A response that ends mid-JSON proves nothing either way: claiming the
		// preprint is absent on the strength of it would be a fabricated verdict.
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"collection": [{"doi":"10.1101/2020`))
		}))
		defer api.Close()
		s := biorxivSource{http: api.Client(), apiBase: api.URL}

		_, rerr := s.Resolve(context.Background(), Item{DOI: doi})
		if rerr == nil {
			t.Fatal("a truncated body must not resolve")
		}
		if errors.Is(rerr, ErrNotIndexed) {
			t.Error("a truncated body read as the preprint being absent")
		}
		if cooldownWorthy(context.Background(), rerr) {
			t.Error("a truncated body put bioRxiv in cooldown")
		}
	})
}

// TestBiorxivResolveHTTPError verifies a non-200 from the details API surfaces as
// an error rather than a resolved URL.
func TestBiorxivResolveHTTPError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer api.Close()

	s := biorxivSource{http: api.Client(), apiBase: api.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1101/x"}); err == nil {
		t.Fatal("Resolve() should fail on an HTTP 500 from the details API")
	}
}

// TestBiorxivResolveMalformed verifies malformed JSON surfaces as a decode error
// rather than a panic.
func TestBiorxivResolveMalformed(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"collection": [`))
	}))
	defer api.Close()

	s := biorxivSource{http: api.Client(), apiBase: api.URL}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1101/x"}); err == nil {
		t.Fatal("Resolve() should fail on a malformed JSON response")
	}
}

// TestLatestBiorxivVersion verifies the latest-version picker returns the highest
// parseable version and reports a miss on an empty or unparseable collection.
func TestLatestBiorxivVersion(t *testing.T) {
	got, ok := latestBiorxivVersion([]biorxivEntry{{Version: "1"}, {Version: "3"}, {Version: "2"}})
	if !ok || got != "3" {
		t.Errorf("latestBiorxivVersion() = %q, %v; want 3, true", got, ok)
	}
	if _, nilOK := latestBiorxivVersion(nil); nilOK {
		t.Error("latestBiorxivVersion(nil) reported a version, want a miss")
	}
	if _, badOK := latestBiorxivVersion([]biorxivEntry{{Version: "na"}}); badOK {
		t.Error("latestBiorxivVersion(unparseable) reported a version, want a miss")
	}
}

// TestBiorxivSource_RejectsUnbuildableEndpoint covers the request-construction failure.
// The base URL is deployment-supplied, so a value carrying a control character
// has to surface as a clean source error rather than a panic.
func TestBiorxivSource_RejectsUnbuildableEndpoint(t *testing.T) {
	s := biorxivSource{http: http.DefaultClient, apiBase: "http://\x7f-invalid"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1101/x"}); err == nil {
		t.Fatal("an unbuildable endpoint must fail, not resolve")
	}
}

// TestBiorxivSource_FallsBackToTheProductionBase covers the default-base branch that every
// other test skips by injecting a test server. The context is canceled first, so
// the default is selected and the request fails before any dial: the branch is
// exercised without the suite touching the network.
func TestBiorxivSource_FallsBackToTheProductionBase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := biorxivSource{http: http.DefaultClient} // apiBase empty -> production constant
	if _, err := s.Resolve(ctx, Item{DOI: "10.1101/x"}); err == nil {
		t.Fatal("a canceled context must fail the resolve")
	}
}
