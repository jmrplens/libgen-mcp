package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// crossrefBase is the Crossref REST API root. It is a package variable (not a
// constant) so tests can point it at a local httptest server. This is the works
// SEARCH endpoint host, distinct from the DOI-lookup usage in internal/libgen.
var crossrefBase = "https://api.crossref.org"

// Crossref limit bounds: the API is asked for at least one and at most this many
// rows, defaulting to crossrefDefaultLimit when the caller passes a non-positive
// value.
const (
	crossrefMinLimit     = 1
	crossrefMaxLimit     = 50
	crossrefDefaultLimit = 10
)

// crossrefPDFType is the Crossref link content-type marking a directly-fetchable
// PDF; a link carrying it supplies the result's PDFURL.
const crossrefPDFType = "application/pdf"

// CrossrefProvider is a keyless open-access discovery source backed by the Crossref
// works-search endpoint. Its limiter and http.Client are self-contained, so it
// never shares state with libgen's enrichment client.
type CrossrefProvider struct {
	client  *http.Client
	limiter *rate.Limiter
	email   string // optional polite-pool contact (mailto); empty disables it
}

// NewCrossref constructs a CrossrefProvider with its own http.Client and a rate
// limiter pacing requests to Crossref's generous allowance (2 requests/second,
// burst 2). The email is the optional polite-pool contact used as the mailto query
// parameter; pass "" to omit it.
func NewCrossref(email string) *CrossrefProvider {
	return &CrossrefProvider{
		client:  newDiscoveryClient(),
		limiter: rate.NewLimiter(rate.Every(time.Second), 2),
		email:   strings.TrimSpace(email),
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *CrossrefProvider) Name() string { return "crossref" }

// Search queries the Crossref works endpoint for the given free-text query and
// returns up to limit results. It is best-effort: a non-200 status or any
// non-context failure degrades to an empty result with no error, so a failing
// provider never sinks a federated search. Only a context cancellation or deadline
// propagates as an error.
func (p *CrossrefProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if err := p.limiter.Wait(ctx); err != nil {
		return nil, ctx.Err()
	}

	rawURL := p.buildURL(query, limit)

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
	return parseCrossrefWorks(body), nil
}

// buildURL assembles the Crossref works-search request URL, trimming the response
// with select= and appending the polite-pool mailto when a contact email is set.
func (p *CrossrefProvider) buildURL(query string, limit int) string {
	params := url.Values{}
	params.Set("query", query)
	params.Set("rows", strconv.Itoa(clampCrossrefLimit(limit)))
	params.Set("select", "DOI,title,author,issued,license,link")
	if p.email != "" {
		params.Set("mailto", p.email)
	}
	return crossrefBase + "/works?" + params.Encode()
}

// clampCrossrefLimit maps a caller-supplied limit onto Crossref's accepted range,
// substituting the default for a non-positive value and clamping the rest.
func clampCrossrefLimit(limit int) int {
	switch {
	case limit <= 0:
		return crossrefDefaultLimit
	case limit < crossrefMinLimit:
		return crossrefMinLimit
	case limit > crossrefMaxLimit:
		return crossrefMaxLimit
	default:
		return limit
	}
}

// crossrefWorksEnvelope, crossrefWorkItem, crossrefAuthor, crossrefIssued and
// crossrefLink are the subset of the Crossref works-search response the provider
// reads (mirroring the select= projection).
type crossrefWorksEnvelope struct {
	Message struct {
		Items []crossrefWorkItem `json:"items"`
	} `json:"message"`
}

type crossrefWorkItem struct {
	DOI     string            `json:"DOI"`
	Title   []string          `json:"title"`
	Author  []crossrefAuthor  `json:"author"`
	Issued  crossrefIssued    `json:"issued"`
	License []json.RawMessage `json:"license"`
	Link    []crossrefLink    `json:"link"`
}

type crossrefAuthor struct {
	Given  string `json:"given"`
	Family string `json:"family"`
}

type crossrefIssued struct {
	DateParts [][]int `json:"date-parts"`
}

type crossrefLink struct {
	URL         string `json:"URL"`
	ContentType string `json:"content-type"`
}

// parseCrossrefWorks decodes a Crossref works envelope into DiscoveryResults,
// returning an empty slice when the body cannot be decoded (best-effort — a
// malformed response is treated as no results, not an error).
func parseCrossrefWorks(body []byte) []DiscoveryResult {
	var env crossrefWorksEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	results := make([]DiscoveryResult, 0, len(env.Message.Items))
	for _, item := range env.Message.Items {
		results = append(results, crossrefItemToResult(item))
	}
	return results
}

// crossrefItemToResult maps one Crossref work item onto a DiscoveryResult: the
// first title, "; "-joined "Given Family" author names, the issued year, the DOI,
// a directly-fetchable PDF URL from the links, and OpenAccess heuristically true
// when the work advertises any license.
func crossrefItemToResult(item crossrefWorkItem) DiscoveryResult {
	return DiscoveryResult{
		Origin:     "crossref",
		Title:      firstNonEmpty(item.Title),
		Authors:    crossrefAuthors(item.Author),
		Year:       crossrefYear(item.Issued),
		DOI:        strings.TrimSpace(item.DOI),
		PDFURL:     crossrefPDFURL(item.Link),
		OpenAccess: len(item.License) > 0,
	}
}

// firstNonEmpty returns the first trimmed non-empty string in the slice, or "".
func firstNonEmpty(values []string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// crossrefAuthors joins author records as "Given Family" with "; ", skipping any
// entry that collapses to an empty name.
func crossrefAuthors(authors []crossrefAuthor) string {
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		name := strings.TrimSpace(a.Given + " " + a.Family)
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "; ")
}

// crossrefYear extracts the publication year from the issued date-parts, returning
// "" when no year is present.
func crossrefYear(issued crossrefIssued) string {
	if len(issued.DateParts) == 0 || len(issued.DateParts[0]) == 0 {
		return ""
	}
	if year := issued.DateParts[0][0]; year > 0 {
		return strconv.Itoa(year)
	}
	return ""
}

// crossrefPDFURL returns the first link whose content-type marks a PDF, or "".
func crossrefPDFURL(links []crossrefLink) string {
	for _, l := range links {
		if l.ContentType == crossrefPDFType && l.URL != "" {
			return l.URL
		}
	}
	return ""
}
