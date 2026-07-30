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

// faoBase is the default root of the FAO Knowledge Repository, the Food and
// Agriculture Organization's DSpace 7 instance. It is overridable per source so
// tests can point at an httptest server.
const faoBase = "https://openknowledge.fao.org"

// faoHandlePath and faoBitstreamPath are the two paths this source uses, and the
// pair is chosen to stay inside what the repository's robots.txt permits. That file
// carves the REST API down to exactly one usable endpoint:
//
//	# Only allow access to the core bitstream and mapping endpoints on the rest api, nothing else
//	Allow: /server/api/core/mapping
//	Allow: /server/api/core/bitstreams
//	Disallow: /server/api/*
//	Disallow: /server/api
//
// so the bitstream content endpoint is explicitly permitted while everything else
// under /server/api is not — including /server/api/discover/search/objects, the
// DSpace discovery search, which is therefore never called here. The item page path
// /handle/<prefix>/<id> carries no Disallow at all (only /search, /browse,
// /entities/*, /items/*/full, /statistics and the login and submission routes do).
// robots.txt also sets Crawl-delay: 10; a resolve makes one page request and then
// one file download for one caller-initiated download, rather than crawling.
const (
	faoHandlePath    = "/handle/"
	faoBitstreamPath = "/server/api/core/bitstreams/"
)

// faoHandlePrefix is the Handle System naming authority the repository registers
// its items under. Every FAO DOI's suffix is also the local part of the item's
// handle, so the item page's address follows from the DOI with no lookup.
const faoHandlePrefix = "20.500.14283/"

// faoDOIPrefix is the DOI registrant prefix Crossref assigns to the Food and
// Agriculture Organization of the United Nations; the source claims only DOIs under
// it. It holds about 9,500 records.
const faoDOIPrefix = "10.4060/"

// faoMaxBody bounds how many bytes of an item page are read while scanning for the
// full-text meta tag. The server-rendered Angular page is large — real ones run from
// 700 KB to about 1.2 MB — so the cap is correspondingly generous.
const faoMaxBody = 8 << 20 // 8 MiB

// faoPDFMeta matches an item page's citation_pdf_url meta tag. The attribute order
// is fixed by the server-rendered template, so a targeted scan is enough and no HTML
// parse is needed.
var faoPDFMeta = regexp.MustCompile(`name="citation_pdf_url"\s+content="([^"]*)"`)

// faoRepositoryMarker is the Angular root element every server-rendered page of the
// repository carries, present on an item page and on the "No item found" page alike.
// Its absence from a 200 means the response did not come from the repository at all
// — a challenge interstitial, or a changed layout — and that must be reported as the
// source being unavailable rather than as the DOI being unheld.
var faoRepositoryMarker = []byte(`<ds-root`)

// faoBitstreamURL matches the bitstream UUID inside the frontend download URL the
// citation_pdf_url tag advertises. See faoFileURL for why that URL cannot be used
// as given.
var faoBitstreamURL = regexp.MustCompile(`/bitstreams/([0-9a-fA-F-]{36})/download`)

// faoSource resolves a DOI registered by the Food and Agriculture Organization of
// the United Nations to the document's PDF in the FAO Knowledge Repository.
//
// FAO publishes its entire output openly — the flagship reports (The State of Food
// Security and Nutrition in the World, The State of the World's Forests), technical
// guidance, country humanitarian response plans, standards work and statistical
// yearbooks — under Creative Commons licenses, in all six UN languages, served
// without an account.
//
// Nothing else in the chain reaches any of it. Across eight sampled DOIs spanning
// 2019 to 2024, Unpaywall reported is_oa=false for every one (FAO deposits no OA
// location with Crossref, so the corpus is invisible to a metadata-driven resolver
// however open its licenses are), Europe PMC returned zero hits, fatcat either held
// a release page advertising no preserved full text or answered 404 outright, and
// the LibGen catalog carries a handful of the flagship titles and none of the rest —
// a search for "FAO humanitarian response plan" returns nothing. All eight resolved
// here.
//
// The doi.org hop is skipped, and not only to save a request: FAO registers many of
// its DOIs against www.fao.org/documents/card/…, which answered HTTP 504 for five of
// the eight sampled DOIs while the repository served every one of them. The item
// page's address is derivable instead, because the DOI's suffix is also its handle.
//
// MD5 verification is disabled, as for every DOI-keyed item.
type faoSource struct {
	// http is the client used for the item-page request; when nil,
	// http.DefaultClient is used.
	http *http.Client
	// baseURL overrides the repository root (defaults to faoBase); tests set it to a
	// local httptest server.
	baseURL string
}

// Compile-time assertion that faoSource satisfies the DownloadSource contract.
var _ DownloadSource = faoSource{}

// Name identifies the FAO source.
func (s faoSource) Name() string { return "fao" }

// Supports reports that this source can resolve a DOI-keyed item whose DOI is a
// well-formed FAO DOI, and only such items. A 10.4060 DOI whose suffix could not be
// a handle's local part is refused here rather than turned into a URL that cannot
// exist.
func (s faoSource) Supports(it Item) bool {
	_, ok := faoHandleID(it.DOI)
	return ok
}

// Resolve fetches the document's item page in the repository and returns the URL of
// the PDF it advertises.
//
// The outcomes are kept distinct so a failure stays diagnosable: an item page with
// no full-text tag is a clean miss about one item, so the chain simply moves on; a
// transport failure or a transient status is the source being unavailable, and so is
// a 200 that did not come from the repository, which is a changed site rather than
// empty coverage.
//
// A handle the repository does not hold falls into the first case and deliberately
// so. DSpace answers an unknown handle with **HTTP 200** carrying its ordinary shell
// and the text "No item found for the identifier", not a 404 — so the status line
// cannot distinguish an unheld DOI from a held one, and the absence of the full-text
// tag is what reports both. That conflates an unknown item with a metadata-only one,
// which costs nothing: both are final, correct answers that this source cannot serve
// the item.
func (s faoSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	id, ok := faoHandleID(it.DOI)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("fao: %q is not an FAO DOI", it.DOI))
	}
	page, err := s.fetchItemPage(ctx, id)
	if err != nil {
		return Resolved{}, err
	}
	if !bytes.Contains(page, faoRepositoryMarker) {
		return Resolved{}, unavailable(fmt.Errorf(
			"fao: the page for %q is not a repository page (challenge or changed layout)", it.DOI))
	}
	fileURL, ok := faoFileURL(page, s.root())
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("fao: %q advertises no full-text PDF", it.DOI))
	}
	return Resolved{
		FileURL:   fileURL,
		VerifyMD5: false,
		Ext:       "pdf",
	}, nil
}

// fetchItemPage requests the item page for a handle's local part and returns its
// body. A transport failure or a non-200 status is reported as unavailability; see
// Resolve for why an unheld handle does not arrive as a status at all.
func (s faoSource) fetchItemPage(ctx context.Context, id string) ([]byte, error) {
	endpoint := s.root() + faoHandlePath + faoHandlePrefix + id

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("fao: building request for %s: %w", id, err)
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := httpClientOr(s.http).Do(req)
	if err != nil {
		return nil, unavailable(fmt.Errorf("fao: requesting %s: %w", id, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, unavailableStatus(resp.StatusCode,
			fmt.Errorf("fao: %s returned HTTP %d", id, resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, faoMaxBody))
	if err != nil {
		return nil, unavailable(fmt.Errorf("fao: reading the item page for %s: %w", id, err))
	}
	return body, nil
}

// root returns the repository base URL this source is configured against, without a
// trailing slash.
func (s faoSource) root() string {
	if s.baseURL == "" {
		return faoBase
	}
	return strings.TrimRight(s.baseURL, "/")
}

// faoFileURL turns the URL an item page advertises into one that actually serves the
// file, reporting whether the page carried a usable tag.
//
// The advertised URL cannot be used as given, and this is the trap that would
// otherwise ship a working-looking source that downloads nothing. The
// citation_pdf_url tag names the **frontend** route
// /bitstreams/<uuid>/download, which is an Angular route: to a browser it hands the
// file, but to a plain HTTP client it answers 200 text/html with 372,862 bytes of
// application shell — the same shell an unknown handle gets. The **backend** route
// /server/api/core/bitstreams/<uuid>/content serves the bytes, verified as
// application/pdf at 2,929,735, 12,549,068 and 8,677,202 bytes for three sampled
// items whose frontend URLs all returned the shell. It is also the one REST endpoint
// the repository's robots.txt allows.
//
// The UUID is therefore lifted out of the advertised URL and the backend URL is
// built against the configured root. The tag's value is HTML-unescaped first, so a
// path whose separators arrive as entities in the markup is read as written.
func faoFileURL(page []byte, root string) (string, bool) {
	m := faoPDFMeta.FindSubmatch(page)
	if m == nil {
		return "", false
	}
	advertised := strings.TrimSpace(html.UnescapeString(string(m[1])))
	uuid := faoBitstreamURL.FindStringSubmatch(advertised)
	if uuid == nil {
		return "", false
	}
	return root + faoBitstreamPath + uuid[1] + "/content", true
}

// faoHandleID extracts the handle's local part from an FAO DOI, reporting whether
// the DOI is one at all. It requires the registrant prefix (matched
// case-insensitively, since a DOI is case-insensitive by specification) and a suffix
// that could be a handle's local part.
//
// The suffix is checked rather than trusted because it goes straight into a URL path
// with no lookup in between: FAO's suffixes are short document codes such as
// "cc7949en", "ca5348fr" and "cc2212en-fig07", and restricting the character set to
// what those use means no caller-supplied DOI can escape the handle path into
// another route.
func faoHandleID(doi string) (string, bool) {
	if len(doi) <= len(faoDOIPrefix) || !strings.EqualFold(doi[:len(faoDOIPrefix)], faoDOIPrefix) {
		return "", false
	}
	id := doi[len(faoDOIPrefix):]
	if !faoWellFormedID(id) {
		return "", false
	}
	return id, true
}

// faoWellFormedID reports whether id looks like an FAO handle's local part: ASCII
// letters, digits, dots and hyphens only, at least one letter or digit among them,
// and no ".." run.
//
// Excluding the path separator already makes traversal impossible; the remaining two
// conditions keep a degenerate suffix such as "." or ".." — which a URL parser would
// resolve away into a different path — from reaching the request at all.
func faoWellFormedID(id string) bool {
	alnum := false
	for _, r := range id {
		switch {
		case isASCIILetter(r), r >= '0' && r <= '9':
			alnum = true
		case r == '.', r == '-':
		default:
			return false
		}
	}
	return alnum && !strings.Contains(id, "..")
}
