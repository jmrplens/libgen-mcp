package discovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	xhtml "golang.org/x/net/html"
)

// annasSearchMaxBody bounds how many bytes of a search page are read. A full page
// of results runs to roughly 700 kB; 4 MiB is a generous ceiling.
const annasSearchMaxBody = 4 << 20 // 4 MiB

// annasMD5Href matches a result link, capturing the item's md5.
var annasMD5Href = regexp.MustCompile(`/md5/([0-9a-f]{32})`)

// errChallenged reports that a mirror answered with an anti-bot interstitial
// instead of content: an HTTP 403 whose body is a "checking your browser" page
// asking the caller to run JavaScript and hand back a token.
//
// It is told apart from an ordinary refusal because the two deserve opposite
// responses. A 403 from one mirror is worth trying the next one for; a
// challenge is not, because every Anna's mirror sits behind the same provider
// and answers the same way — so trying the siblings buys nothing and spends
// three refused requests per search on a host that has just asked us to stop.
// Observed 2026-09-22: /search and /md5/ both challenged on every mirror, from
// two unrelated egress addresses.
var errChallenged = errors.New("mirror served a browser challenge instead of content")

// challengeMarkers are the strings an interstitial carries and a search page
// does not. Both are required together with the status, because a bare 403 is
// an ordinary refusal and must keep its ordinary handling.
var challengeMarkers = []string{"ddos-guard", "js-challenge"}

// challengeBodyPrefix is how much of a refusal body is read to classify it. An
// interstitial is under a kilobyte; anything longer is not one, and reading
// further would mean buffering a page this code has already decided to discard.
const challengeBodyPrefix = 4 << 10

// challengeCooldown is how long the provider stays quiet after being challenged.
//
// Giving up within one call was not enough: every subsequent search asked again
// and was refused again, which is a request per search against a host that has
// told us to stop, and the refusal observed on 2026-09-22 widened from /search
// to /md5/ across an afternoon of exactly that.
//
// It is a cooldown rather than a switch because the alternative fails in the
// direction nobody notices. A provider disabled at build time stays dead after
// the challenge lifts, until somebody ships a release; one that forgets after a
// while costs a single request to find out it is welcome again.
//
// Fifteen minutes is the compromise: two orders of magnitude fewer requests than
// asking every search, and short enough that a recovery is picked up inside the
// session that is watching for it. It is deliberately much longer than
// libgen's 45-second mirror cooldown, because a mirror that failed may be
// healthy in a minute and an anti-bot policy will not be.
const challengeCooldown = 15 * time.Minute

// looksChallenged reports whether a non-200 response is an anti-bot
// interstitial rather than an ordinary refusal.
func looksChallenged(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, marker := range challengeMarkers {
		if !strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

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
	// quietUntil is the wall-clock instant, in Unix nanoseconds, before which a
	// search returns without asking anything, because the site was challenged
	// and said so. Zero means nothing has been observed yet.
	//
	// Atomic because Federate runs every provider in its own goroutine, and a
	// server answers several searches at once: two concurrent calls may both
	// meet the challenge, and the only thing that costs is one extra request.
	// now is a seam so a test can move time without sleeping.
	quietUntil atomic.Int64
	now        func() time.Time
}

// NewAnnas builds a provider searching the given Anna's Archive mirrors, equipped
// with its own bounded http.Client (via newDiscoveryClient) so a stalled mirror can
// never hang a search on the timeout-less http.DefaultClient.
func NewAnnas(m MirrorLister) *AnnasProvider {
	return &AnnasProvider{mirrors: m, http: newDiscoveryClient(), now: time.Now}
}

// clock reads the provider's time source, defaulting to the real one so a
// zero-value AnnasProvider — which the tests build directly — still works.
func (p *AnnasProvider) clock() time.Time {
	if p.now == nil {
		return time.Now()
	}
	return p.now()
}

// quiet reports whether a challenge is still being waited out, so no request is
// made at all.
func (p *AnnasProvider) quiet() bool {
	until := p.quietUntil.Load()
	return until != 0 && p.clock().UnixNano() < until
}

// beQuiet starts the cooldown and reports whether this call is the one that
// started it, so the reason is logged once per window rather than per search.
func (p *AnnasProvider) beQuiet() bool {
	next := p.clock().Add(challengeCooldown).UnixNano()
	previous := p.quietUntil.Swap(next)
	return previous == 0 || previous < p.clock().UnixNano()
}

// Name reports the origin label stamped on this provider's results.
func (p *AnnasProvider) Name() string { return "annas" }

// Search returns up to limit md5-keyed results for the query, best-effort: it
// tries each mirror in order and returns an empty slice rather than an error when
// none answers, so a federated search is never failed by this provider. Only a
// context error propagates.
func (p *AnnasProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	// Bound the whole call the same way arXiv/Crossref/OpenLibrary do, so trying
	// several mirrors in sequence can never outlive the discovery budget.
	// Nothing is asked while a challenge is being waited out. This is the half
	// that matters for traffic: giving up within one call still left every
	// later search asking again and being refused again.
	if p.quiet() {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

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
			// A challenge is answered by stopping, not by asking the siblings.
			// They are the same deployment behind the same provider and answer
			// the same way, so the only thing trying them adds is refused
			// requests against a host that has just said no. Said once, at WARN,
			// because otherwise this is indistinguishable from "nothing matched"
			// — and the two call for completely different reactions.
			if errors.Is(err, errChallenged) {
				if p.beQuiet() {
					slog.Warn("annas search: mirror is serving a browser challenge, asking nothing for "+challengeCooldown.String(),
						"mirror", base, "hint", "search needs the HTML site, which no API key unlocks; a configured "+
							"member key still serves download through the member API")
				}
				return nil, nil
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
	req.Header.Set("User-Agent", discoveryUserAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The body is read before the status is reported on, because a refusal
		// and an interstitial share a status code and only the body tells them
		// apart. A read error here leaves it classified as the ordinary refusal
		// it was already going to be.
		prefix, _ := io.ReadAll(io.LimitReader(resp.Body, challengeBodyPrefix))
		if looksChallenged(resp.StatusCode, prefix) {
			return nil, fmt.Errorf("annas search: %q: %w", base, errChallenged)
		}
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
