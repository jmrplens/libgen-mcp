package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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

// europePMCBucketServer builds a stand-in PMC Article Datasets bucket holding the
// recorded copy of PMC4991899, the PMCID the europepmc_oa.json fixture names.
func europePMCBucketServer(t *testing.T) *httptest.Server {
	t.Helper()
	return pmcOABucket{
		listing:  pmcOAFixture(t, "pmcoa_list.xml"),
		meta:     map[string][]byte{"PMC4991899.1": pmcOAFixture(t, "pmcoa_meta_published.json")},
		servePDF: true,
	}.serve(t)
}

// TestEuropePMCErrorClassification pins which failures Resolve reports as Europe
// PMC answering "no open-access full text here" (ErrNotIndexed) and which as Europe
// PMC being unable to answer (ErrSourceUnavailable).
//
// The two are one line apart in Resolve — an unindexed DOI and an indexed one whose
// OA flags are off both end in notIndexed, while an unreachable render endpoint ends
// in unavailable — and the chain spends a five-minute cooldown on one and not the
// other. Existing tests reached all of these branches and asserted only the message.
func TestEuropePMCErrorClassification(t *testing.T) {
	const doi = "10.1234/known"

	t.Run("a DOI Europe PMC does not index is a clean miss", func(t *testing.T) {
		search := europePMCSearchServer(t, "europepmc_miss.json", http.StatusOK, nil)
		defer search.Close()
		s := europePMCSource{http: search.Client(), searchBase: search.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("an indexed DOI with no open-access full text is a clean miss", func(t *testing.T) {
		// Europe PMC holds plenty of full text it may not redistribute. Declining to
		// serve it is a correct answer about the article, not a fault.
		search := europePMCSearchServer(t, "europepmc_indexed_not_oa.json", http.StatusOK, nil)
		defer search.Close()
		s := europePMCSource{http: search.Client(), searchBase: search.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a transient status is unavailability", func(t *testing.T) {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				search := europePMCSearchServer(t, "europepmc_oa.json", status, nil)
				s := europePMCSource{http: search.Client(), searchBase: search.URL}

				_, err := s.Resolve(context.Background(), Item{DOI: doi})
				assertUnavailable(t, err)
				search.Close()
			})
		}
	})

	t.Run("a transport failure is unavailability", func(t *testing.T) {
		s := europePMCSource{http: refusingClient(), searchBase: "https://europepmc.invalid/search"}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
	})

	t.Run("a failing PDF bucket is unavailability", func(t *testing.T) {
		// The article IS open access — the search said so. A bucket that cannot
		// answer is the service failing, so tagging it as a miss would deny an
		// article we know is held.
		search := europePMCSearchServer(t, "europepmc_oa.json", http.StatusOK, nil)
		defer search.Close()
		bucket := pmcOABucket{listStatus: http.StatusBadGateway}.serve(t)
		s := europePMCSource{http: search.Client(), searchBase: search.URL, bucketBase: bucket.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
		if errors.Is(err, ErrNotIndexed) {
			t.Error("an unreachable bucket read as the article being unheld")
		}
	})

	t.Run("an undecodable body is neither", func(t *testing.T) {
		search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"hitCount":1,"resultList":{"result":[`))
		}))
		defer search.Close()
		s := europePMCSource{http: search.Client(), searchBase: search.URL}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		if err == nil {
			t.Fatal("a truncated body must not resolve")
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("a truncated body read as the DOI being unindexed")
		}
		if cooldownWorthy(context.Background(), err) {
			t.Error("a truncated body put Europe PMC in cooldown")
		}
	})
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

// TestEuropePMCResolveOA verifies an open-access DOI resolves to the article's PDF
// in the PMC Article Datasets, sends an exact-match DOI query, declares a pdf
// extension, and leaves MD5 verification off (DOI items carry no digest).
func TestEuropePMCResolveOA(t *testing.T) {
	const doi = "10.1371/journal.pbio.1002533"
	var gotQuery string
	search := europePMCSearchServer(t, "europepmc_oa.json", http.StatusOK, &gotQuery)
	defer search.Close()
	bucket := europePMCBucketServer(t)

	s := europePMCSource{http: search.Client(), searchBase: search.URL, bucketBase: bucket.URL}
	got, err := s.Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := `DOI:"` + doi + `"`; gotQuery != want {
		t.Errorf("search query = %q, want %q", gotQuery, want)
	}
	if want := bucket.URL + "/PMC4991899.1/PMC4991899.1.pdf"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
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
	// A bucket that would happily serve a PDF, so a pass here can only mean the OA
	// guard let the record through.
	bucket := europePMCBucketServer(t)

	s := europePMCSource{http: search.Client(), searchBase: search.URL, bucketBase: bucket.URL}
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

// TestEuropePMCResolvePDFNotServed verifies that when the bucket lists a PDF and
// then does not serve it, Resolve reports it rather than returning a dead URL.
func TestEuropePMCResolvePDFNotServed(t *testing.T) {
	search := europePMCSearchServer(t, "europepmc_oa.json", http.StatusOK, nil)
	defer search.Close()
	bucket := pmcOABucket{
		listing: pmcOAFixture(t, "pmcoa_list.xml"),
		meta:    map[string][]byte{"PMC4991899.1": pmcOAFixture(t, "pmcoa_meta_published.json")},
	}.serve(t)

	s := europePMCSource{http: search.Client(), searchBase: search.URL, bucketBase: bucket.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1371/journal.pbio.1002533"})
	if err == nil || !strings.Contains(err.Error(), "listed but not served") {
		t.Fatalf("Resolve() error = %v, want a 'listed but not served' error", err)
	}
}

// TestEuropePMCSource_RejectsUnbuildableEndpoint covers the request-construction failure.
// The base URL is deployment-supplied, so a value carrying a control character
// has to surface as a clean source error rather than a panic.
func TestEuropePMCSource_RejectsUnbuildableEndpoint(t *testing.T) {
	s := europePMCSource{http: http.DefaultClient, searchBase: "http://\x7f-invalid"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("an unbuildable endpoint must fail, not resolve")
	}
}

// TestEuropePMCSource_FallsBackToTheProductionBase covers the default-base branch that every
// other test skips by injecting a test server. The context is canceled first, so
// the default is selected and the request fails before any dial: the branch is
// exercised without the suite touching the network.
func TestEuropePMCSource_FallsBackToTheProductionBase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := europePMCSource{http: http.DefaultClient} // searchBase empty -> production constant
	if _, err := s.Resolve(ctx, Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("a canceled context must fail the resolve")
	}
}
