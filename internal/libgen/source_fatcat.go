package libgen

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// fatcatScholarBase is the default base URL for fatcat's own web frontend, Internet
// Archive Scholar. The source appends fatcatLookupPath to it. It is overridable per
// source so tests can point at an httptest server.
//
// The JSON API this source used to call (api.fatcat.wiki) stopped answering — DNS
// resolves, TCP never completes, from every network and client tried — with no
// deprecation notice and no ETA, while scholar.archive.org serves the same catalog.
// The frontend needs no JS for a DOI lookup, so a plain HTTP client can drive it.
const fatcatScholarBase = "https://scholar.archive.org"

// fatcatLookupPath is the DOI lookup endpoint on the Scholar frontend. It answers a
// 302 to /fatcat/release/<uuid> for a known DOI and a 404 for one the catalog does
// not hold.
const fatcatLookupPath = "/fatcat/release/lookup"

// fatcatMaxBody bounds how many bytes of a release page are read while scanning for
// the full-text meta tags, guarding against an unexpectedly large or hostile body.
// Real release pages run to about 30 KB.
const fatcatMaxBody = 2 << 20 // 2 MiB

// fatcatMaxCandidates caps how many preserved copies of one release are probed. A
// heavily-preserved article can list nine or more captures; probing every one would
// turn a single resolve into a burst of requests against a human-facing site for
// little gain, since the earliest captures fatcat lists are the ones it considers
// canonical.
const fatcatMaxCandidates = 4

// fatcatPDFMeta matches a release page's citation_pdf_url meta tag, whose content is
// the full-text URL of one preserved copy. The attribute order is fixed by the
// server-rendered template, so a targeted scan is enough and no HTML parse is needed.
var fatcatPDFMeta = regexp.MustCompile(`name="citation_pdf_url"\s+content="([^"]*)"`)

// fatcatReleaseMarker is the meta tag every release page carries, present whether or
// not the release has a preserved copy. Its absence from a 200 means the response is
// not a release page at all — the "Session Verification" interstitial the host serves
// to clients it wants to vet, or a changed layout — and that must be reported as the
// source being unavailable rather than as the DOI being unpreserved.
var fatcatReleaseMarker = []byte(`name="citation_title"`)

// fatcatSource resolves a DOI to a preserved full-text PDF through fatcat, the
// catalog behind Internet Archive Scholar (https://scholar.archive.org).
//
// It looks the DOI up on the Scholar frontend, follows the redirect to the release
// page, and takes the full-text URLs the page advertises in its citation_pdf_url meta
// tags. Those point at Internet Archive preservation copies (Wayback captures of the
// publisher's own PDF), so this is a legal source. Each candidate is probed before the
// chain commits, because a preserved capture can go bad while the catalog still lists
// it. MD5 verification is disabled: DOI-keyed items carry no LibGen digest.
//
// Three outcomes are kept distinct so a failure stays diagnosable: a DOI the catalog
// does not hold, a release it holds but has preserved nothing for, and a release whose
// preserved copies no longer serve a PDF — all clean misses about one item — versus a
// response that is not a release page at all, which is the source being unavailable.
type fatcatSource struct {
	// http is the client used for lookup, page and probe requests; when nil,
	// http.DefaultClient is used.
	http *http.Client
	// baseURL overrides the Scholar frontend root (defaults to fatcatScholarBase);
	// tests set it to a local httptest server.
	baseURL string
}

// Compile-time assertion that fatcatSource satisfies the DownloadSource contract.
var _ DownloadSource = fatcatSource{}

// Name identifies the fatcat source.
func (s fatcatSource) Name() string { return "fatcat" }

// Supports reports that fatcat can resolve any DOI-keyed item.
func (s fatcatSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve looks the DOI up on Internet Archive Scholar and returns the first
// preserved full-text URL that currently serves a PDF. A DOI the catalog does not
// hold, a release with no preserved copy, and a release whose copies are all dead
// yield distinct clean-miss errors so the chain advances to the next source; a
// response that is not a release page yields an unavailability error so the source is
// set aside instead of looking like empty coverage.
func (s fatcatSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	page, err := s.lookupRelease(ctx, it.DOI)
	if err != nil {
		return Resolved{}, err
	}
	if !bytes.Contains(page, fatcatReleaseMarker) {
		return Resolved{}, unavailable(fmt.Errorf(
			"fatcat: the lookup for %q returned no release page (session challenge or changed layout)", it.DOI))
	}
	candidates := fatcatFulltextURLs(page)
	if len(candidates) == 0 {
		return Resolved{}, notIndexed(fmt.Errorf("fatcat: %q has no preserved full text", it.DOI))
	}
	httpClient := httpClientOr(s.http)
	for _, candidate := range candidates {
		if probePDF(ctx, httpClient, candidate) {
			return Resolved{
				FileURL:   candidate,
				Header:    fatcatFileHeader(),
				VerifyMD5: false,
				Ext:       "pdf",
			}, nil
		}
	}
	return Resolved{}, notIndexed(fmt.Errorf(
		"fatcat: no preserved copy of %q currently serves a PDF (%d tried)", it.DOI, len(candidates)))
}

// fatcatFileHeader returns the headers the download stream must carry to receive the
// archived file itself.
//
// Accept is not optional here. A preserved copy is served by the Wayback Machine, which
// content-negotiates: an Accept-less request for an archived PDF — which is what Go
// sends by default — gets the HTML toolbar page that wraps the capture for human
// readers (200 text/html, which the pipeline then rightly rejects as an error page),
// while the identical request carrying "*/*" gets the raw %PDF bytes.
func fatcatFileHeader() http.Header {
	return http.Header{"Accept": {"*/*"}}
}

// lookupRelease requests the DOI lookup and returns the release page body the
// redirect leads to. A 404 (the DOI is unknown to the catalog) is reported as a clean
// miss; a transient status or a transport failure as unavailability.
func (s fatcatSource) lookupRelease(ctx context.Context, doi string) ([]byte, error) {
	base := s.baseURL
	if base == "" {
		base = fatcatScholarBase
	}
	// fatcat normalizes DOIs to lowercase, so the lookup key is lowercased to match.
	endpoint := strings.TrimRight(base, "/") + fatcatLookupPath + "?doi=" +
		url.QueryEscape(strings.ToLower(doi))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("fatcat: building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := httpClientOr(s.http).Do(req)
	if err != nil {
		return nil, unavailable(fmt.Errorf("fatcat: requesting %q: %w", doi, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, notIndexed(fmt.Errorf("fatcat: %q is unknown to fatcat", doi))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unavailableStatus(resp.StatusCode,
			fmt.Errorf("fatcat: %q returned HTTP %d", doi, resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, fatcatMaxBody))
	if err != nil {
		return nil, unavailable(fmt.Errorf("fatcat: reading the release page for %q: %w", doi, err))
	}
	return body, nil
}

// fatcatFulltextURLs extracts the preserved full-text URLs a release page advertises,
// in page order, capped at fatcatMaxCandidates.
//
// Each value is HTML-unescaped: a capture of a query-driven PDF endpoint carries its
// separator as &amp; in the markup, and requesting that literally would 404. Values
// that cannot be fetched as-is (a relative path, a non-HTTP scheme) and repeats are
// dropped, so every returned candidate is worth one probe.
func fatcatFulltextURLs(page []byte) []string {
	matches := fatcatPDFMeta.FindAllSubmatch(page, -1)
	out := make([]string, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		candidate := strings.TrimSpace(html.UnescapeString(string(m[1])))
		if seen[candidate] || !fatcatFetchableURL(candidate) {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
		if len(out) == fatcatMaxCandidates {
			break
		}
	}
	return out
}

// fatcatFetchableURL reports whether a meta tag's value is an absolute http(s) URL,
// the only shape the download pipeline can stream from. It is a guard on the page's
// output rather than a judgement about the host: a layout change that put a relative
// path or a javascript: URL in the tag would otherwise be handed downstream as a file
// location.
func fatcatFetchableURL(raw string) bool { return absoluteHTTPURL(raw) }
