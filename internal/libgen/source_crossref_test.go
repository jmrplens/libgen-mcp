package libgen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// crossrefWorkBody wraps a work's link array in the `{"message":{...}}` envelope
// the single-work route answers with, so each test states only the links it cares
// about.
func crossrefWorkBody(links string) string {
	return `{"status":"ok","message":{"DOI":"10.1000/x","link":[` + links + `]}}`
}

// crossrefLinkJSON renders one deposited link.
func crossrefLinkJSON(url, contentType, application string) string {
	return `{"URL":"` + url + `","content-type":"` + contentType +
		`","intended-application":"` + application + `"}`
}

// crossrefAPIServer serves body at any path and records how many requests it saw,
// standing in for the live Crossref single-work route.
func crossrefAPIServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// crossrefFileServer serves a PDF (or a refusal) at /ok.pdf and always refuses
// /blocked.pdf with a 403, mimicking a publisher that deposits a link it will not
// serve to an anonymous client. It records the paths probed, in order.
func crossrefFileServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var probed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed = append(probed, r.URL.Path)
		if strings.Contains(r.URL.Path, "blocked") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "%PDF-1.7 body")
	}))
	t.Cleanup(srv.Close)
	return srv, &probed
}

// TestCrossrefSupportsAnyDOI verifies the source claims every DOI-keyed item and no
// md5- or ISBN-keyed one, which is what puts it in the article chain for any
// registrant rather than only its own.
func TestCrossrefSupportsAnyDOI(t *testing.T) {
	s := crossrefSource{}
	if !s.Supports(Item{DOI: "10.1063/5.0282407"}) {
		t.Error("Supports(DOI) = false, want true")
	}
	if s.Supports(Item{MD5: "0123456789abcdef0123456789abcdef"}) {
		t.Error("Supports(md5) = true, want false (Crossref keys by DOI)")
	}
	if s.Supports(Item{ISBN: "9780000000002"}) {
		t.Error("Supports(ISBN) = true, want false")
	}
}

// TestCrossrefResolveServesProbedLink verifies the happy path: the deposited link
// is probed, confirmed to serve a PDF, and returned with MD5 verification off.
func TestCrossrefResolveServesProbedLink(t *testing.T) {
	files, probed := crossrefFileServer(t)
	api, calls := crossrefAPIServer(t, http.StatusOK,
		crossrefWorkBody(crossrefLinkJSON(files.URL+"/ok.pdf", "application/pdf", "text-mining")))
	s := crossrefSource{http: api.Client(), baseURL: api.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.7554/elife.110170"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != files.URL+"/ok.pdf" {
		t.Errorf("FileURL = %q, want the deposited link", res.FileURL)
	}
	if res.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false (DOI-keyed, no md5 to verify)")
	}
	if res.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", res.Ext)
	}
	if calls.Load() != 1 {
		t.Errorf("api calls = %d, want 1", calls.Load())
	}
	if len(*probed) != 1 {
		t.Errorf("probed %v, want exactly one probe", *probed)
	}
}

// TestCrossrefResolveAcceptsTextMiningLink pins the reason the intended-application
// filter was removed. eLife and the smaller open-access publishers tag their
// reader-facing PDF "text-mining" and serve it to anyone; the old rule discarded
// exactly those links unread, so a fetchable open-access PDF was reported as
// nonexistent. Resolving the link above already proves the tag no longer excludes
// it — this asserts the same for a work whose ONLY link is so tagged.
func TestCrossrefResolveAcceptsTextMiningLink(t *testing.T) {
	files, _ := crossrefFileServer(t)
	api, _ := crossrefAPIServer(t, http.StatusOK, crossrefWorkBody(
		crossrefLinkJSON(files.URL+"/ok.pdf", "application/pdf", "similarity-checking")))
	s := crossrefSource{http: api.Client(), baseURL: api.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1000/only-tdm"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want the similarity-checking link served", err)
	}
	if res.FileURL != files.URL+"/ok.pdf" {
		t.Errorf("FileURL = %q, want the only deposited link", res.FileURL)
	}
}

// TestCrossrefResolvePrefersReaderLinkThenFallsThrough verifies the probe order and
// the failover: the reader-facing ("unspecified") link is tried first, and when the
// publisher refuses it the next candidate is probed rather than the resolve being
// abandoned.
func TestCrossrefResolvePrefersReaderLinkThenFallsThrough(t *testing.T) {
	files, probed := crossrefFileServer(t)
	api, _ := crossrefAPIServer(t, http.StatusOK, crossrefWorkBody(
		crossrefLinkJSON(files.URL+"/tdm.pdf", "application/pdf", "text-mining")+","+
			crossrefLinkJSON(files.URL+"/blocked.pdf", "application/pdf", "unspecified")))
	s := crossrefSource{http: api.Client(), baseURL: api.URL}

	res, err := s.Resolve(context.Background(), Item{DOI: "10.1000/two"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL != files.URL+"/tdm.pdf" {
		t.Errorf("FileURL = %q, want the text-mining link after the reader link was refused", res.FileURL)
	}
	want := []string{"/blocked.pdf", "/tdm.pdf"}
	if strings.Join(*probed, ",") != strings.Join(want, ",") {
		t.Errorf("probe order = %v, want %v (reader-facing link first)", *probed, want)
	}
}

// TestCrossrefResolveRefusedLinkIsACleanMiss verifies the outcome the AIP case
// produces: the publisher deposits a PDF link and serves 403 to every automated
// client. That is a settled answer about this item, so it must be tagged
// ErrNotIndexed — an untagged failure would put Crossref in cooldown for an article
// it answered about correctly — and the message must say the file may still open in
// a browser, which is the only route left.
func TestCrossrefResolveRefusedLinkIsACleanMiss(t *testing.T) {
	files, _ := crossrefFileServer(t)
	api, _ := crossrefAPIServer(t, http.StatusOK, crossrefWorkBody(
		crossrefLinkJSON(files.URL+"/blocked.pdf", "application/pdf", "syndication")))
	s := crossrefSource{http: api.Client(), baseURL: api.URL}

	_, err := s.Resolve(context.Background(), Item{DOI: "10.1063/5.0282407"})
	if err == nil {
		t.Fatal("Resolve() error = nil, want a miss (every deposited link was refused)")
	}
	if !errors.Is(err, ErrNotIndexed) {
		t.Errorf("error %v is not ErrNotIndexed; a refusing publisher says nothing about Crossref's health", err)
	}
	if !strings.Contains(err.Error(), "browser") {
		t.Errorf("error = %q, want it to name the browser as the remaining route", err)
	}
}

// TestCrossrefResolveNoLinkIsACleanMiss verifies that a work with no deposited
// full-text link is a clean miss, distinct in wording from a refused one: the two
// are different facts about the DOI and a caller acts differently on each.
func TestCrossrefResolveNoLinkIsACleanMiss(t *testing.T) {
	api, _ := crossrefAPIServer(t, http.StatusOK, crossrefWorkBody(
		crossrefLinkJSON("http://example.org/x.html", "text/html", "unspecified")))
	s := crossrefSource{http: api.Client(), baseURL: api.URL}

	_, err := s.Resolve(context.Background(), Item{DOI: "10.1000/nolink"})
	if err == nil {
		t.Fatal("Resolve() error = nil, want a miss (no PDF link deposited)")
	}
	if !errors.Is(err, ErrNotIndexed) {
		t.Errorf("error %v is not ErrNotIndexed", err)
	}
	if !strings.Contains(err.Error(), "deposited no full-text PDF link") {
		t.Errorf("error = %q, want the no-link wording", err)
	}
}

// TestCrossrefResolveUnknownDOIIsACleanMiss verifies a 404 from Crossref is tagged
// as a miss rather than unavailability, so the chain skips the start-retry schedule
// instead of re-asking a question that is already answered.
func TestCrossrefResolveUnknownDOIIsACleanMiss(t *testing.T) {
	api, _ := crossrefAPIServer(t, http.StatusNotFound, `{"status":"failed"}`)
	s := crossrefSource{http: api.Client(), baseURL: api.URL}

	_, err := s.Resolve(context.Background(), Item{DOI: "10.1000/unknown"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Errorf("error %v is not ErrNotIndexed, want a 404 read as a settled answer", err)
	}
}

// TestCrossrefResolveServerErrorIsUnavailable verifies a 5xx marks the SOURCE as
// unhealthy, which is what the cooldown acts on, rather than being blamed on the
// item.
func TestCrossrefResolveServerErrorIsUnavailable(t *testing.T) {
	api, _ := crossrefAPIServer(t, http.StatusBadGateway, `{"status":"failed"}`)
	s := crossrefSource{http: api.Client(), baseURL: api.URL}

	_, err := s.Resolve(context.Background(), Item{DOI: "10.1000/broken"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Errorf("error %v is not ErrSourceUnavailable", err)
	}
}

// TestCrossrefPDFCandidates covers the candidate list itself: reader-facing links
// lead, duplicates deposited under several tags collapse to one probe, non-PDF and
// relative URLs are dropped, and the list is capped.
func TestCrossrefPDFCandidates(t *testing.T) {
	tests := []struct {
		name  string
		links []crossrefDownloadLink
		want  []string
	}{
		{
			name: "reader-facing link leads",
			links: []crossrefDownloadLink{
				{URL: "https://e.org/tdm.pdf", ContentType: "application/pdf", IntendedApplication: "text-mining"},
				{URL: "https://e.org/read.pdf", ContentType: "application/pdf", IntendedApplication: "unspecified"},
			},
			want: []string{"https://e.org/read.pdf", "https://e.org/tdm.pdf"},
		},
		{
			name: "one file deposited under several tags is probed once",
			links: []crossrefDownloadLink{
				{URL: "https://e.org/a.pdf", ContentType: "application/pdf", IntendedApplication: "syndication"},
				{URL: "https://e.org/a.pdf", ContentType: "application/pdf", IntendedApplication: "text-mining"},
			},
			want: []string{"https://e.org/a.pdf"},
		},
		{
			name: "an untagged link counts as reader-facing",
			links: []crossrefDownloadLink{
				{URL: "https://e.org/syn.pdf", ContentType: "application/pdf", IntendedApplication: "syndication"},
				{URL: "https://e.org/bare.pdf", ContentType: "application/pdf"},
			},
			want: []string{"https://e.org/bare.pdf", "https://e.org/syn.pdf"},
		},
		{
			name: "non-pdf and non-absolute links are dropped",
			links: []crossrefDownloadLink{
				{URL: "https://e.org/x.html", ContentType: "text/html"},
				{URL: "/relative.pdf", ContentType: "application/pdf"},
				{URL: "ftp://e.org/x.pdf", ContentType: "application/pdf"},
				{URL: "https://e.org/ok.pdf", ContentType: "APPLICATION/PDF"},
			},
			want: []string{"https://e.org/ok.pdf"},
		},
		{
			name:  "no links at all",
			links: nil,
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := crossrefPDFCandidates(tt.links)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("crossrefPDFCandidates() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCrossrefPDFCandidatesCap verifies the candidate list is bounded, so a record
// depositing many variants of one file cannot spend the whole per-item budget on
// probes.
func TestCrossrefPDFCandidatesCap(t *testing.T) {
	var links []crossrefDownloadLink
	for i := range crossrefMaxCandidates + 3 {
		links = append(links, crossrefDownloadLink{
			URL:                 "https://e.org/" + string(rune('a'+i)) + ".pdf",
			ContentType:         "application/pdf",
			IntendedApplication: "syndication",
		})
	}
	if got := crossrefPDFCandidates(links); len(got) != crossrefMaxCandidates {
		t.Errorf("len(candidates) = %d, want the cap %d", len(got), crossrefMaxCandidates)
	}
}
