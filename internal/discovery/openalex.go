package discovery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/jmrplens/libgen-mcp/v2/internal/openalex"
)

// openAlexBase is the OpenAlex API root. It is a package variable (not a constant)
// so tests can point it at a local httptest server.
var openAlexBase = openalex.APIBase

// OpenAlex limit bounds: the API accepts up to 100 per page, and this provider asks
// for at most openAlexMaxLimit, defaulting to openAlexDefaultLimit when the caller
// passes a non-positive value. A search costs the same ten credits whatever the page
// size, so the bound is about the payload, not the budget.
const (
	openAlexMaxLimit     = 50
	openAlexDefaultLimit = 10
)

// openAlexMaxBody bounds an OpenAlex search response. It is larger than
// discoveryMaxBody because a work's authorships cannot be projected below the whole
// list, and a single large-collaboration paper lists thousands of authors with an
// institution block each (about 2 MB for a three-thousand-author record): a page
// truncated mid-record decodes to nothing at all.
const openAlexMaxBody = 4 << 20 // 4 MiB

// openAlexMaxAuthors caps how many author names one hit carries. A physics
// collaboration paper names thousands, and a search hit is a pointer to a record,
// not the record: the first names identify it, and get_details holds the rest.
const openAlexMaxAuthors = 20

// openAlexSelect projects each work onto the fields mapped here. OpenAlex honors it
// server-side, which keeps a page a fraction of the full records' size.
const openAlexSelect = "doi,display_name,publication_year,authorships,open_access,best_oa_location,primary_location"

// OpenAlexProvider is a discovery source backed by the OpenAlex works search. It is
// keyless by default and spends from the same metered allowance as the download
// chain's openalex source, so it checks the process budget before every search and
// steps aside when a keyless search would eat into the reserve those lookups need.
type OpenAlexProvider struct {
	client  *http.Client
	limiter *rate.Limiter
	// key is the optional OpenAlex API key. With one, the search draws on the key's
	// own allowance and the keyless reserve does not apply.
	key string
	// budget is the allowance this provider spends from and reports to: the
	// process-wide one in production, a private one in a test.
	budget *openalex.Budget
	// now reads the clock the budget is judged against, swapped by tests.
	now func() time.Time
}

// NewOpenAlex constructs an OpenAlexProvider with its own http.Client, a limiter of
// one request a second (burst 2) and the process-wide OpenAlex budget. key is the
// optional API key (LIBGEN_MCP_OPENALEX_KEY); pass "" to stay keyless.
func NewOpenAlex(key string) *OpenAlexProvider {
	return &OpenAlexProvider{
		client:  newDiscoveryClient(),
		limiter: rate.NewLimiter(rate.Every(time.Second), 2),
		key:     strings.TrimSpace(key),
		budget:  openalex.Shared(),
		now:     time.Now,
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *OpenAlexProvider) Name() string { return "openalex" }

// Search queries the OpenAlex works endpoint and returns up to limit results. It is
// best-effort: a skipped search, a non-200 status or any non-context failure
// degrades to an empty result with no error. Only a context cancellation or
// deadline propagates.
//
// Without a key, a search the budget cannot pay for while keeping
// openalex.KeylessReserve credits back is not sent at all. That is the guard on
// the shared allowance: the download chain's lookups are worth more than one more
// search provider in a federated result, which has six others.
func (p *OpenAlexProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if p.key == "" && !p.budget.Spend(openalex.SearchCost, openalex.KeylessReserve, p.now()) {
		return nil, nil
	}
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, ctx.Err()
	}
	body, ok, err := p.get(ctx, p.searchURL(query, limit))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return parseOpenAlexWorks(body), nil
}

// get performs the request through the shared OpenAlex helper, reports the
// response's rate-limit headers to the budget, and returns the bounded body. ok is
// false for any failure that degrades to empty, and err is set only when the
// context ended.
func (p *OpenAlexProvider) get(ctx context.Context, rawURL string) (body []byte, ok bool, err error) {
	req, err := openalex.NewRequest(ctx, rawURL, p.key)
	if err != nil {
		return nil, false, nil
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// The transport error itself is dropped, so its URL reaches no log. Nothing
		// secret would be in it anyway: the key travels in a header.
		return nil, false, ctx.Err()
	}
	defer func() { _ = resp.Body.Close() }()
	p.budget.Observe(resp.StatusCode, resp.Header, p.now())
	if resp.StatusCode != http.StatusOK {
		return nil, false, nil
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, openAlexMaxBody))
	if err != nil {
		return nil, false, ctx.Err()
	}
	return body, true, nil
}

// searchURL assembles the works-search request URL. No contact address is sent:
// OpenAlex retired its mailto polite pool in February 2026 and ignores the
// parameter, so the User-Agent is the identification and the key is the only
// thing that changes the allowance.
func (p *OpenAlexProvider) searchURL(query string, limit int) string {
	params := url.Values{}
	params.Set("search", query)
	params.Set("per_page", strconv.Itoa(clampOpenAlexLimit(limit)))
	params.Set("select", openAlexSelect)
	return strings.TrimRight(openAlexBase, "/") + "/works?" + params.Encode()
}

// clampOpenAlexLimit maps a caller-supplied limit onto the accepted range,
// substituting the default for a non-positive value and clamping the rest.
func clampOpenAlexLimit(limit int) int {
	switch {
	case limit <= 0:
		return openAlexDefaultLimit
	case limit > openAlexMaxLimit:
		return openAlexMaxLimit
	default:
		return limit
	}
}

// openAlexEnvelope and the types below are the subset of an OpenAlex works page
// read here, mirroring the select= projection.
type openAlexEnvelope struct {
	Results []openAlexWork `json:"results"`
}

// openAlexWork is one work of a search page.
type openAlexWork struct {
	DOI             string               `json:"doi"`
	DisplayName     string               `json:"display_name"`
	PublicationYear int                  `json:"publication_year"`
	Authorships     []openAlexAuthorship `json:"authorships"`
	OpenAccess      openAlexOA           `json:"open_access"`
	BestOALocation  *openAlexLocation    `json:"best_oa_location"`
	PrimaryLocation *openAlexLocation    `json:"primary_location"`
}

// openAlexAuthorship carries the one author field read: the display name.
type openAlexAuthorship struct {
	Author struct {
		DisplayName string `json:"display_name"`
	} `json:"author"`
}

// openAlexOA is the work's open-access summary.
type openAlexOA struct {
	IsOA bool `json:"is_oa"`
}

// openAlexLocation is one place a work is hosted: whether that copy is open access,
// its direct PDF link if OpenAlex knows one, and the venue it belongs to.
type openAlexLocation struct {
	IsOA   bool   `json:"is_oa"`
	PDFURL string `json:"pdf_url"`
	Source *struct {
		DisplayName string `json:"display_name"`
	} `json:"source"`
}

// parseOpenAlexWorks decodes a works page into DiscoveryResults, returning nothing
// when the body cannot be decoded (best-effort, as for every provider).
func parseOpenAlexWorks(body []byte) []DiscoveryResult {
	var env openAlexEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	results := make([]DiscoveryResult, 0, len(env.Results))
	for i := range env.Results {
		if r, ok := openAlexWorkToResult(&env.Results[i]); ok {
			results = append(results, r)
		}
	}
	return results
}

// openAlexWorkToResult maps one work onto a DiscoveryResult, dropping a work with
// neither a title nor a DOI, which would be a row nothing can act on.
//
// OpenAccess is the work's own is_oa, the flag OpenAlex derives from the same
// open-access index Unpaywall publishes. PDFURL is set only from the best
// open-access location, only when that location is itself open access, and only
// when it names an absolute http(s) PDF link: the oa_url beside it is often a
// landing page, and a landing page under pdf_url would be fetched as a file.
func openAlexWorkToResult(w *openAlexWork) (DiscoveryResult, bool) {
	r := DiscoveryResult{
		Origin:     "openalex",
		Title:      strings.Join(strings.Fields(w.DisplayName), " "),
		Authors:    openAlexAuthors(w.Authorships),
		DOI:        openAlexDOI(w.DOI),
		Venue:      openAlexVenue(w.PrimaryLocation),
		PDFURL:     openAlexPDFURL(w.BestOALocation),
		OpenAccess: w.OpenAccess.IsOA,
	}
	if w.PublicationYear > 0 {
		r.Year = strconv.Itoa(w.PublicationYear)
	}
	return r, r.Title != "" || r.DOI != ""
}

// openAlexDOI strips the resolver prefix OpenAlex writes every DOI under, leaving
// the bare identifier the download and get_details tools take. The resolver is
// recognized by its host whatever the scheme or a dx. label in front of it, and a
// "doi:" prefix is dropped too.
func openAlexDOI(raw string) string {
	doi := strings.TrimSpace(raw)
	const doiScheme, resolverHost = "doi:", "doi.org/"
	if len(doi) >= len(doiScheme) && strings.EqualFold(doi[:len(doiScheme)], doiScheme) {
		return strings.TrimSpace(doi[len(doiScheme):])
	}
	if i := strings.Index(strings.ToLower(doi), resolverHost); i > 0 && strings.Contains(doi[:i], "://") {
		return strings.TrimSpace(doi[i+len(resolverHost):])
	}
	return doi
}

// openAlexAuthors joins the authors' display names with "; ", keeping the first
// openAlexMaxAuthors and marking the rest with "et al.".
func openAlexAuthors(authorships []openAlexAuthorship) string {
	names := make([]string, 0, min(len(authorships), openAlexMaxAuthors))
	extra := false
	for _, a := range authorships {
		name := strings.Join(strings.Fields(a.Author.DisplayName), " ")
		if name == "" {
			continue
		}
		if len(names) == openAlexMaxAuthors {
			extra = true
			break
		}
		names = append(names, name)
	}
	joined := strings.Join(names, "; ")
	if extra {
		joined += "; et al."
	}
	return joined
}

// openAlexVenue is the name of the journal, conference or repository the work's
// primary location belongs to, or "" when OpenAlex names none.
func openAlexVenue(loc *openAlexLocation) string {
	if loc == nil || loc.Source == nil {
		return ""
	}
	return strings.Join(strings.Fields(loc.Source.DisplayName), " ")
}

// openAlexPDFURL returns the best open-access location's PDF link when that
// location is open access and the link is an absolute http(s) URL, and "" in
// every other case.
func openAlexPDFURL(loc *openAlexLocation) string {
	if loc == nil || !loc.IsOA {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(loc.PDFURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}
