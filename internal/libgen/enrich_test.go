package libgen

import (
	"context"
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
