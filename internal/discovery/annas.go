package discovery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
)

// annasSearchMaxBody bounds how many bytes of a search page are read. A full page
// of results runs to roughly 700 kB; 4 MiB is a generous ceiling.
const annasSearchMaxBody = 4 << 20 // 4 MiB

// annasMD5Href matches a result link, capturing the item's md5.
var annasMD5Href = regexp.MustCompile(`/md5/([0-9a-f]{32})`)

// MirrorLister supplies candidate base URLs, preferred first. It is declared here
// rather than imported so this package stays independent of the libgen client;
// *mirrors.Manager satisfies it structurally.
type MirrorLister interface {
	// Mirrors returns candidate base URLs, preferred first.
	Mirrors(ctx context.Context) []string
}

// AnnasProvider searches Anna's Archive, which indexes collections this project
// reaches nowhere else (Z-Library, Nexus/STC, DuXiu, Internet Archive, magzdb).
// Its results are md5-keyed and therefore downloadable through the annas source.
//
// Searching is keyless: no account, API key or CAPTCHA is involved. Results are
// NOT open access, so OpenAccess stays false — labeling them otherwise would
// misrepresent them.
type AnnasProvider struct {
	// mirrors supplies the Anna's Archive base URLs, preferred first.
	mirrors MirrorLister
	// http is the client used for search requests; when nil, http.DefaultClient.
	http *http.Client
}

// NewAnnas builds a provider searching the given Anna's Archive mirrors.
func NewAnnas(m MirrorLister) *AnnasProvider { return &AnnasProvider{mirrors: m} }

// Name reports the origin label stamped on this provider's results.
func (p *AnnasProvider) Name() string { return "annas" }

// Search returns up to limit md5-keyed results for the query, best-effort: it
// tries each mirror in order and returns an empty slice rather than an error when
// none answers, so a federated search is never failed by this provider. Only a
// context error propagates.
func (p *AnnasProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	httpClient := p.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	for _, mirror := range p.mirrors.Mirrors(ctx) {
		base := strings.TrimRight(strings.TrimSpace(mirror), "/")
		body, err := p.fetch(ctx, httpClient, base, query)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if out := parseAnnasSearch(body, limit); len(out) > 0 {
			return out, nil
		}
	}
	return nil, nil
}

// fetch requests one mirror's search page and returns its body.
func (p *AnnasProvider) fetch(ctx context.Context, httpClient *http.Client, base, query string) ([]byte, error) {
	endpoint := base + "/search?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", discoveryUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("annas search: %q returned HTTP %d", base, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, annasSearchMaxBody))
}

// parseAnnasSearch walks an Anna's Archive search page and extracts up to limit
// md5-keyed results. Each result card carries one or more anchors linking to
// /md5/<hash>; the anchor whose visible text is non-empty is the title. The page
// also publishes result links inside JavaScript that are not rendered cards, so a
// link is only accepted when its anchor element carries text directly — script
// template fragments have no rendered text node and are skipped naturally.
func parseAnnasSearch(body []byte, limit int) []DiscoveryResult {
	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var out []DiscoveryResult
	seen := map[string]bool{}
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if len(out) >= limit {
			return
		}
		if r, ok := resultFromAnchor(n, seen); ok {
			seen[r.MD5] = true
			out = append(out, r)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// resultFromAnchor reads one node as a result card link, reporting false when the
// node is not an anchor to an md5, carries no rendered title, or repeats an md5
// already collected.
func resultFromAnchor(n *xhtml.Node, seen map[string]bool) (DiscoveryResult, bool) {
	if n.Type != xhtml.ElementNode || n.Data != "a" {
		return DiscoveryResult{}, false
	}
	m := annasMD5Href.FindStringSubmatch(attrValue(n, "href"))
	if m == nil {
		return DiscoveryResult{}, false
	}
	title := strings.TrimSpace(textOfAnchor(n))
	if title == "" || seen[m[1]] {
		return DiscoveryResult{}, false
	}
	ext, size := describeCard(n)
	return DiscoveryResult{Origin: "annas", MD5: m[1], Title: title, Extension: ext, Size: size}, true
}

// cardAncestorDepth bounds how far up from a result link the card's descriptor is
// looked for. The descriptor is a sibling div a few levels above the anchor;
// climbing without a bound would eventually reach the page body and describe some
// other result.
const cardAncestorDepth = 6

// annasDescriptor matches the line each result card carries under its title —
// "English [en] · EPUB · 12.0MB · 2021 · 📘 Book (non-fiction) · 🚀/zlib". The
// fields are not positional: a card with no year simply omits it, so the tokens
// are classified rather than indexed.
var annasDescriptor = regexp.MustCompile(`(?m)^[^
]*·[^
]*\d+(?:\.\d+)?\s*[KMG]B[^
]*$`)

// describeCard reads the file's format and size from the card containing a result
// link, or returns empty strings when the card states neither.
func describeCard(anchor *xhtml.Node) (ext, size string) {
	for n, up := anchor.Parent, 0; n != nil && up < cardAncestorDepth; n, up = n.Parent, up+1 {
		line := annasDescriptor.FindString(textOfAnchor(n))
		if line == "" {
			continue
		}
		return classifyDescriptor(line)
	}
	return "", ""
}

// classifyDescriptor picks the format and size out of a descriptor line by shape,
// since the fields are optional and therefore not positional.
func classifyDescriptor(line string) (ext, size string) {
	for raw := range strings.SplitSeq(line, "·") {
		token := strings.TrimSpace(raw)
		switch {
		case ext == "" && annasFormatToken.MatchString(token):
			ext = strings.ToLower(token)
		case size == "" && annasSizeToken.MatchString(token):
			size = token
		}
	}
	return ext, size
}

// annasFormatToken matches a file format as the cards write it (PDF, EPUB, DJVU,
// CBZ, AZW3), and annasSizeToken a human-readable size (12.0MB, 600KB).
var (
	annasFormatToken = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,4}$`)
	annasSizeToken   = regexp.MustCompile(`^\d+(?:\.\d+)?\s*[KMG]B$`)
)

// textOfAnchor returns the visible text inside an anchor element, joining
// descendant text nodes. It matches how a browser would render the anchor's
// label, so icon spans (which carry no text) contribute nothing.
func textOfAnchor(n *xhtml.Node) string {
	if n.Type == xhtml.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textOfAnchor(c))
	}
	return b.String()
}
