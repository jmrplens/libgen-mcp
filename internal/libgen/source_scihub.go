package libgen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// scihubMaxBody bounds how many bytes of a Sci-Hub article page are read while
// scanning for the embedded PDF link, guarding against a hostile or oversized
// response. Article pages are a few kilobytes; 2 MiB is a generous ceiling.
const scihubMaxBody = 2 << 20 // 2 MiB

// scihubDefaultScheme is the URL scheme used to reach a mirror when scihubSource
// does not override it. Tests set the scheme to "http" to target httptest hosts.
const scihubDefaultScheme = "https"

// locationHrefPDF matches a JavaScript "location.href='…​.pdf…'" assignment used
// by some Sci-Hub mirrors on the download button, as a fallback when the article
// carries no id="pdf" element. The captured group is the (possibly
// backslash-escaped) URL up to the closing quote.
var locationHrefPDF = regexp.MustCompile(`location\.href\s*=\s*'([^']*\.pdf[^']*)'`)

// scihubSource resolves a DOI to a direct PDF URL by scraping a Sci-Hub mirror.
// Given a DOI it requests https://<host>/<doi> on each configured host in order;
// the first host whose article page carries an id="pdf" element (or a
// location.href='…​.pdf' download link) wins. The PDF URL is taken from the page,
// never reconstructed from the DOI, because the CDN path is opaque. MD5
// verification is disabled: DOI-keyed items carry no LibGen digest.
type scihubSource struct {
	// hosts is the ordered list of bare Sci-Hub mirror hosts (no scheme, no
	// path), tried in sequence until one serves an article with a PDF link.
	hosts []string
	// http is the client used for mirror requests; when nil, http.DefaultClient
	// is used.
	http *http.Client
	// scheme overrides the request scheme (defaults to "https"); tests set it to
	// "http" to reach httptest servers listening on 127.0.0.1:port hosts.
	scheme string
}

// Compile-time assertion that scihubSource satisfies the DownloadSource contract.
var _ DownloadSource = scihubSource{}

// Name identifies the Sci-Hub source.
func (s scihubSource) Name() string { return "scihub" }

// Supports reports that Sci-Hub can resolve any DOI-keyed item.
func (s scihubSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve tries each configured host in order, requesting https://<host>/<doi>
// with the DOI kept raw in the path (Sci-Hub expects the unescaped DOI). The
// first host whose page yields a PDF link returns a Resolved carrying that URL,
// a pdf extension, MD5 verification disabled, and a Referer pointing at the
// winning mirror. A host that returns a challenge page, a not-found page, or a
// transport error is skipped. When no host yields a PDF, an error is returned so
// the download chain advances to the next source.
func (s scihubSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	scheme := s.scheme
	if scheme == "" {
		scheme = scihubDefaultScheme
	}
	httpClient := s.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	var lastErr error
	for _, host := range s.hosts {
		pdfURL, err := s.tryHost(ctx, httpClient, scheme, host, it.DOI)
		if err != nil {
			lastErr = err
			continue
		}
		if pdfURL == "" {
			lastErr = fmt.Errorf("scihub: host %q served no PDF link for %q", host, it.DOI)
			continue
		}
		return Resolved{
			FileURL:   pdfURL,
			Header:    http.Header{"Referer": {scheme + "://" + host + "/"}},
			VerifyMD5: false,
			Ext:       "pdf",
		}, nil
	}
	if lastErr != nil {
		return Resolved{}, fmt.Errorf("scihub: no mirror resolved %q: %w", it.DOI, lastErr)
	}
	return Resolved{}, fmt.Errorf("scihub: no hosts configured for %q", it.DOI)
}

// tryHost requests a single mirror and returns the extracted PDF URL (empty when
// the page carries none) or a transport/HTTP error.
func (s scihubSource) tryHost(ctx context.Context, httpClient *http.Client, scheme, host, doi string) (string, error) {
	// The DOI's slashes stay literal in the path: Sci-Hub keys articles by the
	// unescaped DOI, so its slashes must not be percent-encoded. escapeDOIPath keeps
	// the slashes but percent-encodes any other URL-unsafe characters a DOI may
	// carry (e.g. '#', '?', space) so they cannot corrupt the request.
	endpoint := scheme + "://" + host + "/" + escapeDOIPath(doi)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("scihub: building request for %q: %w", host, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("scihub: requesting %q: %w", host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Gate extraction on a 200: a challenge/error page (403/404/5xx) can still
	// embed a stale id="pdf" element, so scraping a PDF link from a non-OK
	// response would hand back a dead URL. Skip the host instead.
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("scihub: host %q returned HTTP %d", host, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, scihubMaxBody))
	if err != nil {
		return "", fmt.Errorf("scihub: reading %q: %w", host, err)
	}
	pdfURL, _ := extractScihubPDF(body)
	return pdfURL, nil
}

// extractScihubPDF extracts the article PDF URL from a Sci-Hub page body. It is
// driven by the page's id="pdf" element (an iframe or embed whose src points at
// the PDF CDN); when that is absent it falls back to a location.href='…​.pdf'
// download link. The raw value is normalized: JavaScript backslash escapes
// (\/ → /) are undone, a protocol-relative //host/… URL is promoted to https,
// and any #viewer fragment is dropped. The bool reports whether a PDF was found.
func extractScihubPDF(body []byte) (string, bool) {
	if raw, ok := pdfElementSrc(body); ok {
		return normalizeScihubURL(raw), true
	}
	if m := locationHrefPDF.FindSubmatch(body); m != nil {
		return normalizeScihubURL(string(m[1])), true
	}
	return "", false
}

// pdfElementSrc parses the HTML body and returns the src attribute of the first
// element carrying id="pdf". The bool reports whether such an element with a
// non-empty src was found.
func pdfElementSrc(body []byte) (string, bool) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", false
	}
	var src string
	var found bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found {
			return
		}
		if n.Type == html.ElementNode {
			var id, elemSrc string
			for _, a := range n.Attr {
				switch a.Key {
				case "id":
					id = a.Val
				case "src":
					elemSrc = a.Val
				}
			}
			if id == "pdf" && elemSrc != "" {
				src, found = elemSrc, true
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return src, found
}

// normalizeScihubURL turns a raw PDF reference scraped from a mirror into a
// clean absolute https URL: it undoes JavaScript backslash escaping, promotes a
// protocol-relative //host/… reference to https, and strips any #viewer fragment.
func normalizeScihubURL(raw string) string {
	u := strings.ReplaceAll(raw, `\/`, "/")
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	return u
}
