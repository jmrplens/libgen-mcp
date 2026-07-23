package discovery

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// arxivBase is the arXiv API root. It is a package variable (not a constant) so
// tests can point it at a local httptest server.
var arxivBase = "https://export.arxiv.org"

// arXiv limit bounds: the API is asked for at least one and at most this many
// results, defaulting to arxivDefaultLimit when the caller passes a non-positive
// value.
const (
	arxivMinLimit     = 1
	arxivMaxLimit     = 50
	arxivDefaultLimit = 10
)

// arxivRate is arXiv's requested courtesy delay between API requests (~3s); the
// provider paces itself to it so repeated live searches stay polite.
const arxivRate = 3 * time.Second

// ArxivProvider is a keyless open-access discovery source backed by the arXiv
// Atom API. Its limiter and http.Client are self-contained, so it never shares
// state with libgen's client.
type ArxivProvider struct {
	client  *http.Client
	limiter *rate.Limiter
}

// NewArxiv constructs an ArxivProvider with its own http.Client and a rate limiter
// pacing requests to arXiv's ~3s courtesy delay (burst 1, so the first request
// goes through immediately and only back-to-back requests wait).
func NewArxiv() *ArxivProvider {
	return &ArxivProvider{
		client:  newDiscoveryClient(),
		limiter: rate.NewLimiter(rate.Every(arxivRate), 1),
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *ArxivProvider) Name() string { return "arxiv" }

// Search queries the arXiv API for the given free-text query and returns up to
// limit open-access results. It is best-effort: a non-200 status or any non-context
// failure degrades to an empty result with no error, so a failing provider never
// sinks a federated search. Only a context cancellation or deadline propagates as
// an error.
func (p *ArxivProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if err := p.limiter.Wait(ctx); err != nil {
		return nil, ctx.Err()
	}

	rawURL := fmt.Sprintf("%s/api/query?search_query=all:%s&max_results=%d",
		arxivBase, url.QueryEscape(query), clampArxivLimit(limit))

	status, body, err := boundedGet(ctx, p.client, rawURL)
	if err != nil {
		// Context errors propagate so the federation layer can tell "caller went
		// away" from "source degraded"; everything else degrades to empty.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, nil
	}
	return parseArxivFeed(body), nil
}

// clampArxivLimit maps a caller-supplied limit onto arXiv's accepted range,
// substituting the default for a non-positive value and clamping the rest.
func clampArxivLimit(limit int) int {
	switch {
	case limit <= 0:
		return arxivDefaultLimit
	case limit < arxivMinLimit:
		return arxivMinLimit
	case limit > arxivMaxLimit:
		return arxivMaxLimit
	default:
		return limit
	}
}

// atomFeed, atomEntry, atomAuthor and atomLink are the subset of the arXiv Atom
// response the provider reads. Tags carry only local names, so namespaced elements
// (e.g. arxiv:doi) match regardless of their prefix.
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string       `xml:"id"`
	Title     string       `xml:"title"`
	Published string       `xml:"published"`
	Authors   []atomAuthor `xml:"author"`
	DOI       string       `xml:"doi"`
	Links     []atomLink   `xml:"link"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Href  string `xml:"href,attr"`
	Title string `xml:"title,attr"`
	Type  string `xml:"type,attr"`
}

// parseArxivFeed decodes an arXiv Atom feed into DiscoveryResults, returning an
// empty slice when the body cannot be decoded (best-effort — a malformed feed is
// treated as no results, not an error).
func parseArxivFeed(body []byte) []DiscoveryResult {
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil
	}
	results := make([]DiscoveryResult, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		results = append(results, arxivEntryToResult(e))
	}
	return results
}

// arxivEntryToResult maps one Atom entry onto a DiscoveryResult: whitespace-collapsed
// title, "; "-joined author names, the publication year from <published>, the
// arxiv:doi when present, and a directly-fetchable PDF URL. Origin is "arxiv" and
// OpenAccess is always true.
func arxivEntryToResult(e atomEntry) DiscoveryResult {
	names := make([]string, 0, len(e.Authors))
	for _, a := range e.Authors {
		if n := strings.TrimSpace(a.Name); n != "" {
			names = append(names, n)
		}
	}
	year := ""
	if len(e.Published) >= 4 {
		year = e.Published[:4]
	}
	return DiscoveryResult{
		Origin:     "arxiv",
		Title:      strings.Join(strings.Fields(e.Title), " "),
		Authors:    strings.Join(names, "; "),
		Year:       year,
		DOI:        strings.TrimSpace(e.DOI),
		PDFURL:     arxivPDFURL(e),
		OpenAccess: true,
	}
}

// arxivPDFURL returns the entry's directly-fetchable PDF URL, preferring an
// explicit <link title="pdf" .../> and otherwise constructing one from the abstract
// id embedded in the entry <id> (e.g. .../abs/2101.00001v1 → .../pdf/2101.00001v1).
func arxivPDFURL(e atomEntry) string {
	for _, l := range e.Links {
		if l.Title == "pdf" && l.Href != "" {
			return l.Href
		}
	}
	if id := arxivAbsID(e.ID); id != "" {
		return "https://arxiv.org/pdf/" + id
	}
	return ""
}

// arxivAbsID extracts the arXiv abstract id from an entry <id> URL such as
// "http://arxiv.org/abs/2101.00001v1", returning the part after "/abs/" (or "" when
// the marker is absent).
func arxivAbsID(id string) string {
	if _, after, found := strings.Cut(id, "/abs/"); found {
		return strings.TrimSpace(after)
	}
	return ""
}
