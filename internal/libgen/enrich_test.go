package libgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// crossrefFixture is a realistic Crossref `{"message":{...}}` body carrying the
// container title, ISSN, volume, issue, publisher, published year, reference and
// citation counts, and subjects that the parser maps onto CrossrefWork.
const crossrefFixture = `{
  "status": "ok",
  "message": {
    "container-title": ["Journal of Test Studies"],
    "ISSN": ["1234-5678", "8765-4321"],
    "volume": "42",
    "issue": "7",
    "publisher": "Test Publishing House",
    "published": {"date-parts": [[2019, 5, 1]]},
    "references-count": 31,
    "is-referenced-by-count": 128,
    "subject": ["Computer Science", "Information Systems"]
  }
}`

// setEnrichBases points the package-level enrichment base URLs at the given test
// server URLs and restores them when the test ends, so httptest servers stand in
// for the live Crossref and OpenLibrary APIs.
func setEnrichBases(t *testing.T, crossref, openLibrary string) {
	t.Helper()
	oldCR, oldOL := crossrefBase, openLibraryBase
	crossrefBase, openLibraryBase = crossref, openLibrary
	t.Cleanup(func() { crossrefBase, openLibraryBase = oldCR, oldOL })
}

// TestEnrich_CrossrefByDOI verifies that a DOI lookup against a Crossref-shaped
// server yields a populated CrossrefWork (container title, ISSN, volume, citation
// count and published year) and no OpenLibrary side.
func TestEnrich_CrossrefByDOI(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(crossrefFixture))
	}))
	defer srv.Close()
	setEnrichBases(t, srv.URL, "http://openlibrary.invalid")

	c := newTestClient(staticMirrors{})
	e := c.Enrich(context.Background(), "10.1000/xyz123", "")
	// The DOI's slash must survive into the request path (not be %2F-encoded),
	// or Crossref may miss the record.
	if gotPath != "/works/10.1000/xyz123" {
		t.Errorf("request path = %q, want /works/10.1000/xyz123 (DOI slash preserved)", gotPath)
	}
	if e == nil || e.Crossref == nil {
		t.Fatalf("Enrich() = %+v, want non-nil Crossref", e)
	}
	cr := e.Crossref
	if cr.ContainerTitle != "Journal of Test Studies" {
		t.Errorf("ContainerTitle = %q", cr.ContainerTitle)
	}
	if len(cr.ISSN) != 2 || cr.ISSN[0] != "1234-5678" {
		t.Errorf("ISSN = %v", cr.ISSN)
	}
	if cr.Volume != "42" || cr.Issue != "7" {
		t.Errorf("Volume/Issue = %q/%q", cr.Volume, cr.Issue)
	}
	if cr.CitationCount != 128 || cr.ReferenceCount != 31 {
		t.Errorf("CitationCount/ReferenceCount = %d/%d", cr.CitationCount, cr.ReferenceCount)
	}
	if cr.PublishedYear != 2019 {
		t.Errorf("PublishedYear = %d, want 2019", cr.PublishedYear)
	}
	if e.OpenLibrary != nil {
		t.Errorf("OpenLibrary = %+v, want nil", e.OpenLibrary)
	}
}

// olHandler serves the two-hop OpenLibrary flow: an ISBN record (cover + work
// key) at /isbn/{isbn}.json and the referenced work record at {workKey}.json. The
// work record's description body is provided by the caller so a test can exercise
// both the object and the plain-string description forms.
func olHandler(workDesc string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/isbn/"):
			_, _ = w.Write([]byte(`{"covers": [8231856], "works": [{"key": "/works/OL45804W"}]}`))
		case r.URL.Path == "/works/OL45804W.json":
			_, _ = w.Write([]byte(`{"subjects": ["Fiction", "Adventure"], "description": ` + workDesc + `}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}

// TestEnrich_OpenLibraryByISBN verifies that an ISBN lookup walks the two-hop
// OpenLibrary flow and populates OLBook.Subjects, Description and CoverURL,
// handling the description in both its object and plain-string JSON forms.
func TestEnrich_OpenLibraryByISBN(t *testing.T) {
	cases := []struct {
		name     string
		descJSON string
	}{
		{"object form", `{"type": "/type/text", "value": "A sweeping tale of the sea."}`},
		{"string form", `"A sweeping tale of the sea."`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(olHandler(tc.descJSON))
			defer srv.Close()
			setEnrichBases(t, "http://crossref.invalid", srv.URL)

			c := newTestClient(staticMirrors{})
			e := c.Enrich(context.Background(), "", "9780000000001")
			if e == nil || e.OpenLibrary == nil {
				t.Fatalf("Enrich() = %+v, want non-nil OpenLibrary", e)
			}
			assertOLBook(t, e.OpenLibrary, srv.URL)
		})
	}
}

// assertOLBook checks that an OLBook carries the subjects, description, cover URL
// and work URL that olHandler serves, given the server base URL.
func assertOLBook(t *testing.T, ol *OLBook, baseURL string) {
	t.Helper()
	if len(ol.Subjects) != 2 || ol.Subjects[0] != "Fiction" {
		t.Errorf("Subjects = %v", ol.Subjects)
	}
	if ol.Description != "A sweeping tale of the sea." {
		t.Errorf("Description = %q", ol.Description)
	}
	if ol.CoverURL != "https://covers.openlibrary.org/b/id/8231856-L.jpg" {
		t.Errorf("CoverURL = %q", ol.CoverURL)
	}
	if ol.OpenLibURL != baseURL+"/works/OL45804W" {
		t.Errorf("OpenLibURL = %q", ol.OpenLibURL)
	}
}

// TestEnrich_BothServersFail verifies that when both APIs return HTTP 500, Enrich
// degrades silently to nil rather than surfacing an error.
func TestEnrich_BothServersFail(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer fail.Close()
	setEnrichBases(t, fail.URL, fail.URL)

	c := newTestClient(staticMirrors{})
	if e := c.Enrich(context.Background(), "10.1/x", "9780000000001"); e != nil {
		t.Fatalf("Enrich() = %+v, want nil", e)
	}
}

// TestEnrich_Timeout verifies that Enrich returns nil (and does not hang) when a
// server never responds: the request-scoped context deadline fires and the fetch
// degrades to nil. A 100ms parent deadline keeps the test fast and deterministic —
// the handler blocks until its request context is canceled.
func TestEnrich_Timeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	setEnrichBases(t, slow.URL, slow.URL)

	c := newTestClient(staticMirrors{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan *Enrichment, 1)
	go func() { done <- c.Enrich(ctx, "10.1/x", "9780000000001") }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("Enrich() = %+v, want nil", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Enrich did not return within the deadline; it hung")
	}
}

// TestEnrich_NoInputs verifies that Enrich makes no requests and returns nil when
// neither a DOI nor an ISBN is supplied.
func TestEnrich_NoInputs(t *testing.T) {
	setEnrichBases(t, "http://crossref.invalid", "http://openlibrary.invalid")
	c := newTestClient(staticMirrors{})
	if e := c.Enrich(context.Background(), "", ""); e != nil {
		t.Fatalf("Enrich() = %+v, want nil", e)
	}
}

// TestEnrichGet_ErrorPaths drives enrichGet's three silent-failure branches
// directly: a canceled context fails the rate-limiter wait, a URL carrying a
// control byte fails request construction, and an unreachable address fails the
// transport Do. Each must yield a nil response so callers degrade to no
// enrichment.
func TestEnrichGet_ErrorPaths(t *testing.T) {
	c := newTestClient(staticMirrors{})

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if resp := c.enrichGet(canceled, "http://example.invalid"); resp != nil {
		_ = resp.Body.Close()
		t.Error("enrichGet with a canceled context should return nil (limiter wait fails)")
	}
	if resp := c.enrichGet(context.Background(), "http://\x7f"); resp != nil {
		_ = resp.Body.Close()
		t.Error("enrichGet with an unbuildable URL should return nil")
	}
	if resp := c.enrichGet(context.Background(), "http://127.0.0.1:0"); resp != nil {
		_ = resp.Body.Close()
		t.Error("enrichGet against an unreachable address should return nil")
	}
}

// TestEnrichGet_PoliteMailtoUA verifies that when a contact email is configured
// enrichGet appends a polite-pool mailto to the User-Agent (Crossref and
// OpenLibrary prioritize identified traffic).
func TestEnrichGet_PoliteMailtoUA(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{})
	c.enrichEmail = "polite@example.com"

	resp := c.enrichGet(context.Background(), srv.URL)
	if resp == nil {
		t.Fatal("enrichGet returned nil for a 200 response")
	}
	_ = resp.Body.Close()
	if !strings.Contains(gotUA, "mailto:polite@example.com") {
		t.Errorf("User-Agent = %q, want a mailto polite-pool suffix", gotUA)
	}
}

// TestParseCrossref_MalformedBody verifies parseCrossref returns nil when the body
// cannot be decoded as a Crossref envelope.
func TestParseCrossref_MalformedBody(t *testing.T) {
	if w := parseCrossref(strings.NewReader("not valid json")); w != nil {
		t.Errorf("parseCrossref(malformed) = %+v, want nil", w)
	}
}

// TestFetchOpenLibrary_NothingGathered verifies that an ISBN record carrying
// neither a cover nor a work key yields nil (nothing worth surfacing), exercising
// the all-empty guard.
func TestFetchOpenLibrary_NothingGathered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"covers": [], "works": []}`))
	}))
	defer srv.Close()
	setEnrichBases(t, "http://crossref.invalid", srv.URL)
	c := newTestClient(staticMirrors{})
	if ol := c.fetchOpenLibrary(context.Background(), "9780000000001"); ol != nil {
		t.Errorf("fetchOpenLibrary with empty covers/works = %+v, want nil", ol)
	}
}

// TestFetchOLISBN_MalformedBody verifies that a malformed ISBN record (a 200 whose
// body is not JSON) makes hop 1 return nil, so fetchOpenLibrary yields nil.
func TestFetchOLISBN_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer srv.Close()
	setEnrichBases(t, "http://crossref.invalid", srv.URL)
	c := newTestClient(staticMirrors{})
	if ol := c.fetchOpenLibrary(context.Background(), "9780000000001"); ol != nil {
		t.Errorf("fetchOpenLibrary with a malformed ISBN record = %+v, want nil", ol)
	}
}

// olTwoHopServer serves the ISBN record (with a cover and a work key) at /isbn/…
// and delegates the work-record response at /works/… to workHandler, so a test can
// make hop 2 fail (non-200) or return a malformed body while hop 1 stands.
func olTwoHopServer(t *testing.T, workHandler http.HandlerFunc) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/isbn/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"covers": [8231856], "works": [{"key": "/works/OL1W"}]}`))
	})
	mux.HandleFunc("/works/", workHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestFetchOLWork_HopTwoFailureKeepsHopOne verifies that when the work-record fetch
// fails (non-200) or returns a malformed body, the cover and work URL gathered on
// hop 1 still stand (Subjects/Description simply stay empty).
func TestFetchOLWork_HopTwoFailureKeepsHopOne(t *testing.T) {
	cases := []struct {
		name string
		work http.HandlerFunc
	}{
		{"non-200", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusInternalServerError) }},
		{"malformed", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := olTwoHopServer(t, tc.work)
			setEnrichBases(t, "http://crossref.invalid", base)
			c := newTestClient(staticMirrors{})
			ol := c.fetchOpenLibrary(context.Background(), "9780000000001")
			if ol == nil {
				t.Fatal("fetchOpenLibrary = nil, want the hop-1 cover/link preserved")
			}
			if ol.CoverURL == "" || ol.OpenLibURL == "" {
				t.Errorf("hop-1 data lost after a hop-2 failure: %+v", ol)
			}
			if len(ol.Subjects) != 0 || ol.Description != "" {
				t.Errorf("hop-2 failure should leave subjects/description empty: %+v", ol)
			}
		})
	}
}

// TestParseOLDescription_EmptyAndUndecodable covers parseOLDescription's remaining
// branches: an empty raw message and a value that is neither a string nor a
// {"value":…} object both yield "".
func TestParseOLDescription_EmptyAndUndecodable(t *testing.T) {
	if got := parseOLDescription(nil); got != "" {
		t.Errorf("parseOLDescription(nil) = %q, want empty", got)
	}
	if got := parseOLDescription(json.RawMessage("12345")); got != "" {
		t.Errorf("parseOLDescription(number) = %q, want empty", got)
	}
}
