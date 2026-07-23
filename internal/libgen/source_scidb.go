package libgen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// scidbMaxBody bounds how many bytes of a SciDB article page are read while
// scanning for the embedded PDF link, guarding against a hostile or oversized
// response. Article pages run to a few hundred kilobytes; 4 MiB is generous.
const scidbMaxBody = 4 << 20 // 4 MiB

// scidbViewerFile matches the pdf.js viewer iframe's file parameter, which
// carries the percent-encoded absolute PDF URL. This is the most reliable marker
// on a SciDB page: it is present whenever the article is actually served.
var scidbViewerFile = regexp.MustCompile(`viewer\.html\?file=([^"'&\s]+)`)

// scidbAbsolutePDF matches a bare absolute PDF URL in the page body, used as a
// fallback when the viewer iframe is absent.
var scidbAbsolutePDF = regexp.MustCompile(`https?://[^\s"'<>]+\.pdf[^\s"'<>]*`)

// scidbSource resolves a DOI to a direct PDF URL through Anna's Archive's SciDB
// article viewer. Given a DOI it requests <mirror>/scidb/<doi> on each discovered
// Anna's mirror in order; the first mirror whose page embeds a PDF URL wins. The
// URL is taken from the page, never reconstructed from the DOI, because the CDN
// path is opaque.
//
// SciDB is keyless: no account, no API key, no CAPTCHA and no JS challenge are
// involved. Verified 2026-07-23 across DOIs from 2011, 2016, 2021 and 2024, so it
// also reaches papers published after Sci-Hub stopped indexing, which is where it
// complements scihubSource. MD5 verification is disabled: DOI-keyed items carry
// no LibGen digest.
type scidbSource struct {
	// mirrors supplies the Anna's Archive base URLs, preferred first.
	mirrors MirrorLister
	// http is the client used for page requests; when nil, http.DefaultClient is
	// used.
	http *http.Client
}

// Compile-time assertion that scidbSource satisfies the DownloadSource contract.
var _ DownloadSource = scidbSource{}

// Name identifies the SciDB source.
func (s scidbSource) Name() string { return "scidb" }

// Supports reports that SciDB can resolve any DOI-keyed item.
func (s scidbSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve tries each discovered Anna's mirror in order, requesting
// <mirror>/scidb/<doi>. The first mirror whose page embeds a PDF URL returns a
// Resolved carrying that URL, a pdf fallback extension, MD5 verification
// disabled, and a Referer pointing at the winning mirror. A mirror that errors,
// answers non-200, or embeds no PDF is skipped; when none yields a PDF an error
// is returned so the download chain advances to the next source.
func (s scidbSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	httpClient := s.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	var lastErr error
	for _, mirror := range s.mirrors.Mirrors(ctx) {
		base := strings.TrimRight(strings.TrimSpace(mirror), "/")
		pdfURL, err := s.tryMirror(ctx, httpClient, base, it.DOI)
		if err != nil {
			lastErr = err
			continue
		}
		if pdfURL == "" {
			lastErr = fmt.Errorf("scidb: mirror %q embedded no PDF for %q", base, it.DOI)
			continue
		}
		return Resolved{
			FileURL:   pdfURL,
			Header:    http.Header{"Referer": {base + "/"}},
			VerifyMD5: false,
			Ext:       "pdf",
		}, nil
	}
	if lastErr != nil {
		return Resolved{}, fmt.Errorf("scidb: no mirror resolved %q: %w", it.DOI, lastErr)
	}
	return Resolved{}, fmt.Errorf("scidb: no mirrors available for %q", it.DOI)
}

// tryMirror requests one mirror's SciDB page and returns the extracted PDF URL
// (empty when the page embeds none) or a transport/HTTP error.
func (s scidbSource) tryMirror(ctx context.Context, httpClient *http.Client, base, doi string) (string, error) {
	// The DOI's slashes stay literal in the path: SciDB keys articles by the
	// unescaped DOI. escapeDOIPath keeps the slashes but percent-encodes any other
	// URL-unsafe characters a DOI may carry (e.g. '#', '?', a space).
	endpoint := base + "/scidb/" + escapeDOIPath(doi)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("scidb: building request for %q: %w", base, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("scidb: requesting %q: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Gate extraction on a 200: an error or challenge page can still carry a stale
	// marker, so scraping it would hand back a dead URL. Skip the mirror instead.
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("scidb: mirror %q returned HTTP %d", base, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, scidbMaxBody))
	if err != nil {
		return "", fmt.Errorf("scidb: reading %q: %w", base, err)
	}
	pdfURL, _ := extractSciDBPDF(body)
	return pdfURL, nil
}

// extractSciDBPDF extracts the direct PDF URL from a SciDB page body. It prefers
// the pdf.js viewer iframe's percent-encoded file parameter and falls back to the
// first bare absolute .pdf URL in the page. The bool reports whether one was found.
func extractSciDBPDF(body []byte) (string, bool) {
	if m := scidbViewerFile.FindSubmatch(body); m != nil {
		if decoded, err := url.QueryUnescape(string(m[1])); err == nil && decoded != "" {
			return decoded, true
		}
	}
	if m := scidbAbsolutePDF.Find(body); m != nil {
		return string(m), true
	}
	return "", false
}
