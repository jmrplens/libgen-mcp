package libgen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// fatcatStubRelease is the release-page path the stub redirects the DOI lookup to.
// It is the real UUID path scholar.archive.org answered for the captured fixture, so
// the stub's redirect has the same shape as the live one.
const fatcatStubRelease = "/fatcat/release/57a39abe-15b0-4bc1-81dc-7ce6274740cc"

// fatcatStubPDF is the bytes the stub's live full-text endpoint serves: a real PDF
// magic number, which is what probePDF looks for when the Content-Type is
// inconclusive.
const fatcatStubPDF = "%PDF-1.4\nstub body\n%%EOF\n"

// fatcatWaybackWrapper stands in for the HTML toolbar page the Wayback Machine serves
// instead of the archived file when a request carries no Accept header.
const fatcatWaybackWrapper = "<!DOCTYPE html>\n<html><head><title>Wayback Machine</title></head><body></body></html>"

// fatcatFixtureFulltext matches a captured release page's citation_pdf_url tags so a
// test can repoint them at a local server. The DC.identifier twin of each tag is
// deliberately left alone: only the citation tag is what the source reads.
var fatcatFixtureFulltext = regexp.MustCompile(`(name="citation_pdf_url" content=")[^"]*"`)

// fatcatStub is a stand-in for scholar.archive.org. It answers the DOI lookup with
// the same 302 the live site sends, serves the release page that redirect points at,
// and hosts the full-text endpoints the page's meta tags name — so a test can cover
// the whole lookup → release page → candidate probe path offline.
//
// Its fields are set by the caller after the server is started, because a release
// page usually has to embed the stub's own URL; every one of them is read only inside
// the handler, i.e. after the request under test begins.
type fatcatStub struct {
	// srv is the running test server; srv.URL is the source's baseURL.
	srv *httptest.Server
	// gotDOI records the doi query parameter the lookup endpoint was asked for.
	gotDOI string
	// lookupStatus, when non-zero, is what the lookup endpoint answers instead of
	// redirecting, with lookupBody as its body (the 404 and 5xx cases).
	lookupStatus int
	// lookupBody is the body served alongside lookupStatus.
	lookupBody []byte
	// release is the HTML the release page serves.
	release string
	// files maps a request path to the bytes served there, letting one page offer a
	// live candidate and a dead one.
	files map[string]string
}

// startFatcatStub starts a fake scholar.archive.org and registers its shutdown. The
// returned stub has no release page yet: callers fill in release (and files) once
// they can reference stub.srv.URL.
func startFatcatStub(t *testing.T) *fatcatStub {
	t.Helper()
	stub := &fatcatStub{files: map[string]string{}}
	stub.srv = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.srv.Close)
	return stub
}

// serve routes the three endpoint families the source touches: the DOI lookup, the
// release page it redirects to, and the full-text URLs the release page advertises.
func (f *fatcatStub) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case fatcatLookupPath:
		f.gotDOI = r.URL.Query().Get("doi")
		if f.lookupStatus != 0 {
			w.WriteHeader(f.lookupStatus)
			_, _ = w.Write(f.lookupBody)
			return
		}
		http.Redirect(w, r, fatcatStubRelease, http.StatusFound)
	case fatcatStubRelease:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, f.release)
	default:
		body, ok := f.files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// The full-text endpoints content-negotiate the way the Wayback Machine really
		// does: an Accept-less request gets the HTML page that wraps a capture for human
		// readers, and only a request that says it accepts anything gets the file. Every
		// case that expects a PDF therefore fails unless the header is sent.
		if r.Header.Get("Accept") == "" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, fatcatWaybackWrapper)
			return
		}
		_, _ = io.WriteString(w, body)
	}
}

// source returns a fatcatSource pointed at the stub.
func (f *fatcatStub) source() fatcatSource {
	return fatcatSource{http: f.srv.Client(), baseURL: f.srv.URL}
}

// fatcatFixture reads a captured scholar.archive.org response from testdata.
func fatcatFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(b)
}

// fatcatRepointFulltext rewrites every citation_pdf_url in a captured release page to
// target, so the real page shape can be exercised against a local server instead of
// the web.archive.org captures it really names.
func fatcatRepointFulltext(page, target string) string {
	return fatcatFixtureFulltext.ReplaceAllString(page, `${1}`+target+`"`)
}

// fatcatPage renders a minimal release page carrying the given full-text candidates
// in the same meta-tag shape the captured fixtures use, for the cases where the exact
// candidate list matters more than the surrounding page.
func fatcatPage(pdfURLs ...string) string {
	var b strings.Builder
	b.WriteString(`<html><head><meta name="citation_title" content="A Paper">`)
	for _, u := range pdfURLs {
		b.WriteString(`<meta name="citation_pdf_url" content="` + u + `">`)
	}
	b.WriteString(`</head><body></body></html>`)
	return b.String()
}

// TestFatcatSupports verifies the source claims DOI-keyed items only and names
// itself "fatcat".
func TestFatcatSupports(t *testing.T) {
	s := fatcatSource{}
	if !s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(DOI) = false, want true")
	}
	if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(md5-only) = true, want false")
	}
	if s.Name() != "fatcat" {
		t.Errorf("Name() = %q, want %q", s.Name(), "fatcat")
	}
}

// TestFatcatResolveServesPreservedPDF verifies the happy path over a REAL captured
// release page: the DOI is looked up lowercased, the 302 to the release page is
// followed, the citation_pdf_url is taken from it, and the result declares a pdf
// extension with MD5 verification off (a DOI-keyed item carries no LibGen digest).
func TestFatcatResolveServesPreservedPDF(t *testing.T) {
	const doi = "10.1371/journal.PBIO.1002533"
	stub := startFatcatStub(t)
	stub.files["/preserved.pdf"] = fatcatStubPDF
	stub.release = fatcatRepointFulltext(
		fatcatFixture(t, "fatcat_release_hit.html"), stub.srv.URL+"/preserved.pdf",
	)

	got, err := stub.source().Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if stub.gotDOI != strings.ToLower(doi) {
		t.Errorf("lookup doi = %q, want the lowercased DOI %q", stub.gotDOI, strings.ToLower(doi))
	}
	if got.FileURL != stub.srv.URL+"/preserved.pdf" {
		t.Errorf("FileURL = %q, want the page's citation_pdf_url", got.FileURL)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
	// The download stream needs the same Accept the probe sent: without it the Wayback
	// Machine answers with the HTML page that wraps the capture, and the pipeline
	// rejects that as an error page after a successful resolve.
	if got.Header.Get("Accept") != "*/*" {
		t.Errorf("Header Accept = %q, want */* so the stream gets the file and not the toolbar page",
			got.Header.Get("Accept"))
	}
}

// TestFatcatResolveSkipsDeadCapture verifies a release page's first candidate being
// dead does not sink the resolve: preserved Wayback captures do go bad (one live
// candidate for this very DOI answers a redirect loop today), so every candidate is
// probed in page order until one really serves PDF bytes.
func TestFatcatResolveSkipsDeadCapture(t *testing.T) {
	stub := startFatcatStub(t)
	stub.files["/second.pdf"] = fatcatStubPDF
	stub.release = fatcatPage(stub.srv.URL+"/gone.pdf", stub.srv.URL+"/second.pdf")

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want the second candidate to be used", err)
	}
	if got.FileURL != stub.srv.URL+"/second.pdf" {
		t.Errorf("FileURL = %q, want the live second candidate", got.FileURL)
	}
}

// TestFatcatResolveNoPreservedCopy verifies a REAL release page for a DOI fatcat
// knows but has preserved no full text for is diagnosed as a clean miss: the source
// answered correctly, so it must be tagged ErrNotIndexed and must NOT be put in
// cooldown for being honest.
func TestFatcatResolveNoPreservedCopy(t *testing.T) {
	stub := startFatcatStub(t)
	stub.release = fatcatFixture(t, "fatcat_release_no_file.html")

	_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1038/s41586-024-07487-w"})
	if err == nil || !strings.Contains(err.Error(), "has no preserved full text") {
		t.Fatalf("Resolve() error = %v, want a 'no preserved full text' error", err)
	}
	assertCleanMiss(t, err)
}

// TestFatcatResolveUnknownDOI verifies the lookup's HTTP 404 — the answer for a DOI
// the catalog does not hold at all — stays a distinct diagnosis from the
// known-but-unpreserved case, and is likewise a clean miss rather than cooldown
// evidence.
func TestFatcatResolveUnknownDOI(t *testing.T) {
	stub := startFatcatStub(t)
	stub.lookupStatus = http.StatusNotFound
	stub.lookupBody = []byte(fatcatFixture(t, "fatcat_lookup_notfound.html"))

	_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.9999/nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown to fatcat") {
		t.Fatalf("Resolve() error = %v, want an 'unknown to fatcat' error", err)
	}
	assertCleanMiss(t, err)
}

// TestFatcatResolveNoLivePreservedCopy verifies a page whose only candidate does not
// serve PDF bytes is a clean miss about the item, matching how the CORE source treats
// its stale download URLs: one dead capture says nothing about the catalog's health.
func TestFatcatResolveNoLivePreservedCopy(t *testing.T) {
	stub := startFatcatStub(t)
	stub.files["/paper.pdf"] = "<html><body>Sorry, we can't find that page</body></html>"
	stub.release = fatcatPage(stub.srv.URL + "/paper.pdf")

	_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil || !strings.Contains(err.Error(), "currently serves a PDF") {
		t.Fatalf("Resolve() error = %v, want a 'currently serves a PDF' error", err)
	}
	assertCleanMiss(t, err)
}

// TestFatcatResolveTransientStatus verifies a 5xx from the lookup is classified as
// the service being unavailable, so the chain sets the source aside instead of
// reporting empty coverage.
func TestFatcatResolveTransientStatus(t *testing.T) {
	stub := startFatcatStub(t)
	stub.lookupStatus = http.StatusServiceUnavailable

	_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil || !strings.Contains(err.Error(), "returned HTTP 503") {
		t.Fatalf("Resolve() error = %v, want an HTTP 503 error", err)
	}
	assertUnavailable(t, err)
}

// TestFatcatResolveTransportFailure verifies an unreachable host is classified as
// unavailability rather than as the item being absent.
func TestFatcatResolveTransportFailure(t *testing.T) {
	stub := startFatcatStub(t)
	s := stub.source()
	stub.srv.Close()

	_, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil || !strings.Contains(err.Error(), "requesting") {
		t.Fatalf("Resolve() error = %v, want a transport error", err)
	}
	assertUnavailable(t, err)
}

// TestFatcatResolveSessionChallenge verifies the REAL "Session Verification"
// interstitial scholar.archive.org serves to clients it wants to vet is reported as
// the source being unavailable — NOT as the DOI being unpreserved. A challenge that
// read as empty coverage would hide a new wall behind a plausible miss, and would
// keep offering the source items it cannot serve.
func TestFatcatResolveSessionChallenge(t *testing.T) {
	stub := startFatcatStub(t)
	stub.lookupStatus = http.StatusOK
	stub.lookupBody = []byte(fatcatFixture(t, "fatcat_challenge.html"))

	_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err == nil || !strings.Contains(err.Error(), "no release page") {
		t.Fatalf("Resolve() error = %v, want a 'no release page' error", err)
	}
	assertUnavailable(t, err)
	if errors.Is(err, ErrNotIndexed) {
		t.Error("a session challenge was reported as ErrNotIndexed; a new wall would look like empty coverage")
	}
}

// TestFatcatFulltextURLsFromCapturedPage verifies extraction against the REAL
// captured page: both preserved copies are returned in page order, and the first
// one's HTML-escaped query separator is decoded — served as &amp; in the markup, it
// would otherwise be requested as a literal "&amp;type=printable".
func TestFatcatFulltextURLsFromCapturedPage(t *testing.T) {
	got := fatcatFulltextURLs([]byte(fatcatFixture(t, "fatcat_release_hit.html")))
	want := []string{
		"https://web.archive.org/web/20170708142320/http://journals.plos.org/plosbiology/article/file?id=10.1371/journal.pbio.1002533&type=printable",
		"https://web.archive.org/web/20180724110523/https://www.biorxiv.org/content/biorxiv/early/2016/01/06/036103.full.pdf",
	}
	if len(got) != len(want) {
		t.Fatalf("fatcatFulltextURLs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFatcatFulltextURLsFilters verifies the candidate list drops what cannot be
// fetched (a relative or non-HTTP URL), drops duplicates, and is capped so a release
// with many preserved copies cannot turn one resolve into a burst of probes.
func TestFatcatFulltextURLsFilters(t *testing.T) {
	page := fatcatPage(
		"/relative/paper.pdf",
		"javascript:alert(1)",
		"https://archive.org/download/a/1.pdf",
		"https://archive.org/download/a/1.pdf",
		"https://archive.org/download/a/2.pdf",
		"https://archive.org/download/a/3.pdf",
		"https://archive.org/download/a/4.pdf",
		"https://archive.org/download/a/5.pdf",
	)
	got := fatcatFulltextURLs([]byte(page))
	if len(got) != fatcatMaxCandidates {
		t.Fatalf("fatcatFulltextURLs() returned %d candidates (%v), want the cap of %d", len(got), got, fatcatMaxCandidates)
	}
	if got[0] != "https://archive.org/download/a/1.pdf" {
		t.Errorf("first candidate = %q, want the first fetchable URL", got[0])
	}
	for _, c := range got {
		if !strings.HasPrefix(c, "https://") {
			t.Errorf("candidate %q is not a fetchable absolute URL", c)
		}
	}
}

// assertCleanMiss fails unless err is the source's correct answer that it does not
// hold the item: tagged ErrNotIndexed and therefore never cooldown-worthy.
func assertCleanMiss(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotIndexed) {
		t.Errorf("error %v is not tagged ErrNotIndexed", err)
	}
	if cooldownWorthy(context.Background(), err) {
		t.Error("a clean miss put the source in cooldown")
	}
}

// assertUnavailable fails unless err is evidence the source itself could not serve
// anything right now: tagged ErrSourceUnavailable and therefore cooldown-worthy.
func assertUnavailable(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Errorf("error %v is not tagged ErrSourceUnavailable", err)
	}
	if !cooldownWorthy(context.Background(), err) {
		t.Error("an unavailable source was not put in cooldown")
	}
}

// TestFatcatSource_RejectsUnbuildableEndpoint covers the request-construction failure.
// The base URL is deployment-supplied, so a value carrying a control character
// has to surface as a clean source error rather than a panic.
func TestFatcatSource_RejectsUnbuildableEndpoint(t *testing.T) {
	s := fatcatSource{http: http.DefaultClient, baseURL: "http://\x7f-invalid"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("an unbuildable endpoint must fail, not resolve")
	}
}

// TestFatcatSource_FallsBackToTheProductionBase covers the default-base branch that every
// other test skips by injecting a test server. The context is canceled first, so
// the default is selected and the request fails before any dial: the branch is
// exercised without the suite touching the network.
func TestFatcatSource_FallsBackToTheProductionBase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := fatcatSource{http: http.DefaultClient} // baseURL empty -> production constant
	if _, err := s.Resolve(ctx, Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("a canceled context must fail the resolve")
	}
}
