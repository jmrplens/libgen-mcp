package libgen

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// dagstuhlBase is the default root of Schloss Dagstuhl's publication server,
// DROPS. The source appends dagstuhlDocumentPath and the DOI to it. It is
// overridable per source so tests can point at an httptest server.
const dagstuhlBase = "https://drops.dagstuhl.de"

// dagstuhlDocumentPath is the landing-page path a DOI is looked up under. The
// public DOI resolver redirects 10.4230/<suffix> to exactly
// drops.dagstuhl.de/entities/document/10.4230/<suffix>, so the source builds that
// URL itself and skips the doi.org hop: one request instead of two, for the same
// page.
const dagstuhlDocumentPath = "/entities/document/"

// dagstuhlDOIPrefix is the DOI registrant prefix assigned to Schloss Dagstuhl;
// the source claims only DOIs under it.
const dagstuhlDOIPrefix = "10.4230/"

// dagstuhlMaxBody bounds how many bytes of a landing page are read while scanning
// for the full-text meta tag, guarding against an unexpectedly large or hostile
// body. Real document pages run to about 88 KB.
const dagstuhlMaxBody = 2 << 20 // 2 MiB

// dagstuhlPDFMeta matches a document page's citation_pdf_url meta tag, whose
// content is the paper's PDF URL in DROPS storage. The attribute order is fixed by
// the server-rendered template and a document page carries exactly one such tag,
// so a targeted scan is enough and no HTML parse is needed.
var dagstuhlPDFMeta = regexp.MustCompile(`name="citation_pdf_url"\s+content="([^"]*)"`)

// dagstuhlDocumentMarker is the meta tag every document page carries. Its absence
// from a 200 means the response is not a document page at all — a challenge
// interstitial, or a changed layout — and that must be reported as the source
// being unavailable rather than as the DOI being unheld.
var dagstuhlDocumentMarker = []byte(`name="citation_title"`)

// dagstuhlSource resolves a Schloss Dagstuhl DOI to its PDF on the DROPS
// publication server.
//
// Dagstuhl publishes LIPIcs (the proceedings of ICALP, STACS, ESA, SoCG, CPM,
// ECOOP, ITCS and most other top-tier theoretical-computer-science conferences),
// OASIcs, DARTS and the Dagstuhl Reports. Everything it publishes is Creative
// Commons licensed and served without an account, so this is a legal open-access
// source — and one nothing else in the chain reaches.
//
// It is a real gap rather than a redundant one. Dagstuhl registers its DOIs with
// DataCite, not Crossref, so api.crossref.org answers 404 for them and Unpaywall —
// which is built on Crossref — answers 404 in turn; both were confirmed for
// 10.4230/LIPIcs.ICALP.2023.1, 10.4230/LIPIcs.STACS.2015.1 and 10.4230/DagRep.9.1.1.
// Europe PMC returns zero hits, Sci-Hub serves a 2 KB stub with no PDF, and fatcat
// does have a release page but advertises no preserved full text (zero
// citation_pdf_url tags on it).
//
// The landing page has to be fetched; the PDF URL cannot be derived. DROPS files
// live under a storage path that embeds the volume the paper appeared in, and the
// DOI does not carry it: 10.4230/LIPIcs.ICALP.2023.1 is served from
// /storage/00lipics/lipics-vol261-icalp2023/…, 10.4230/LIPIcs.STACS.2015.1 from
// lipics-vol030-stacs2015/…, and 10.4230/DagRep.9.1.1 from a different scheme
// again (/storage/04dagstuhl-reports/volume09/issue01/19021/…). Nothing in the
// identifier predicts "vol261" or "19021", so the page's own citation_pdf_url tag
// is the only route to the file.
//
// MD5 verification is disabled, as for every DOI-keyed item.
type dagstuhlSource struct {
	// http is the client used for the landing-page request; when nil,
	// http.DefaultClient is used.
	http *http.Client
	// baseURL overrides the DROPS root (defaults to dagstuhlBase); tests set it to a
	// local httptest server.
	baseURL string
}

// Compile-time assertion that dagstuhlSource satisfies the DownloadSource contract.
var _ DownloadSource = dagstuhlSource{}

// Name identifies the Dagstuhl source.
func (s dagstuhlSource) Name() string { return "dagstuhl" }

// Supports reports that this source can resolve a DOI-keyed item whose DOI carries
// the Dagstuhl registrant prefix, and only such items.
//
// The whole prefix is claimed rather than only the LIPIcs suffixes, because every
// series Dagstuhl publishes — OASIcs, DARTS and the Dagstuhl Reports as well as
// LIPIcs — is served from the same publication server and advertises its PDF the
// same way. Narrowing the check would drop those for no gain.
func (s dagstuhlSource) Supports(it Item) bool {
	return it.DOI != "" && strings.HasPrefix(it.DOI, dagstuhlDOIPrefix)
}

// Resolve fetches the document's landing page on DROPS and returns the PDF URL it
// advertises.
//
// The outcomes are kept distinct so a failure stays diagnosable: a DOI DROPS does
// not hold (404) and a document page carrying no full-text tag are clean misses
// about one item, so the chain simply moves on; a transport failure or a transient
// status is the source being unavailable, and so is a 200 that is not a document
// page, which is a changed site rather than empty coverage.
func (s dagstuhlSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	if !s.Supports(it) {
		return Resolved{}, notIndexed(fmt.Errorf("dagstuhl: %q is not a Dagstuhl DOI", it.DOI))
	}
	page, err := s.fetchDocumentPage(ctx, it.DOI)
	if err != nil {
		return Resolved{}, err
	}
	if !bytes.Contains(page, dagstuhlDocumentMarker) {
		return Resolved{}, unavailable(fmt.Errorf(
			"dagstuhl: the page for %q is not a document page (challenge or changed layout)", it.DOI))
	}
	fileURL, ok := dagstuhlFulltextURL(page)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("dagstuhl: %q advertises no full-text PDF", it.DOI))
	}
	return Resolved{
		FileURL:   fileURL,
		VerifyMD5: false,
		Ext:       "pdf",
	}, nil
}

// fetchDocumentPage requests the DOI's landing page and returns its body. A 404
// (DROPS does not hold the DOI) is reported as a clean miss; a transport failure or
// a transient status as unavailability.
func (s dagstuhlSource) fetchDocumentPage(ctx context.Context, doi string) ([]byte, error) {
	base := s.baseURL
	if base == "" {
		base = dagstuhlBase
	}
	// The DOI's slashes stay literal in the path (DROPS keys its document pages by
	// the unescaped DOI); escapeDOIPath encodes only other URL-unsafe characters.
	endpoint := strings.TrimRight(base, "/") + dagstuhlDocumentPath + escapeDOIPath(doi)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("dagstuhl: building request for %q: %w", doi, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClientOr(s.http).Do(req)
	if err != nil {
		return nil, unavailable(fmt.Errorf("dagstuhl: requesting %q: %w", doi, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, notIndexed(fmt.Errorf("dagstuhl: %q is not held by DROPS", doi))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unavailableStatus(resp.StatusCode,
			fmt.Errorf("dagstuhl: %q returned HTTP %d", doi, resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, dagstuhlMaxBody))
	if err != nil {
		return nil, unavailable(fmt.Errorf("dagstuhl: reading the document page for %q: %w", doi, err))
	}
	return body, nil
}

// dagstuhlFulltextURL extracts the PDF URL a document page advertises, reporting
// whether it carried a usable one.
//
// The value is HTML-unescaped before use, so a path whose separators arrive as
// entities in the markup is requested as written rather than literally, and it is
// checked to be an absolute http(s) URL so a layout change cannot put a relative
// path or a non-HTTP scheme into the download pipeline.
func dagstuhlFulltextURL(page []byte) (string, bool) {
	m := dagstuhlPDFMeta.FindSubmatch(page)
	if m == nil {
		return "", false
	}
	candidate := strings.TrimSpace(html.UnescapeString(string(m[1])))
	if !absoluteHTTPURL(candidate) {
		return "", false
	}
	return candidate, true
}
