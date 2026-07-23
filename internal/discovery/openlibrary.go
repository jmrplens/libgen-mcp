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

// openLibraryBase is the OpenLibrary API root. It is a package variable (not a
// constant) so tests can point it at a local httptest server.
var openLibraryBase = "https://openlibrary.org"

// OpenLibrary limit bounds: the search endpoint is asked for at least one and at
// most this many docs, defaulting to openLibraryDefaultLimit when the caller passes
// a non-positive value.
const (
	openLibraryMaxLimit     = 50
	openLibraryDefaultLimit = 10
)

// openLibraryFields is the projection requested from the search endpoint, trimming
// the response to just the fields the resolver reads.
const openLibraryFields = "title,author_name,first_publish_year,isbn,key"

// OpenLibraryProvider is a keyless query resolver that turns fuzzy title/author
// queries into canonical identifiers (ISBN/title/year) which feed a Library Genesis
// search. It is NOT a download source, so its results never carry a PDF URL and are
// never marked open-access. Its limiter and http.Client are self-contained, so it
// never shares state with libgen's client.
type OpenLibraryProvider struct {
	client  *http.Client
	limiter *rate.Limiter
}

// NewOpenLibrary constructs an OpenLibraryProvider with its own http.Client and a
// rate limiter pacing requests to a polite 2 requests/second (burst 2).
func NewOpenLibrary() *OpenLibraryProvider {
	return &OpenLibraryProvider{
		client:  newDiscoveryClient(),
		limiter: rate.NewLimiter(rate.Every(time.Second), 2),
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *OpenLibraryProvider) Name() string { return "openlibrary" }

// Search resolves the given free-text query against the OpenLibrary search endpoint
// and returns up to limit canonical results. It is best-effort: a non-200 status or
// any non-context failure degrades to an empty result with no error, so a failing
// resolver never sinks a federated search. Only a context cancellation or deadline
// propagates as an error.
func (p *OpenLibraryProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if err := p.limiter.Wait(ctx); err != nil {
		return nil, ctx.Err()
	}

	rawURL := buildOpenLibraryURL(query, limit)

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
	return parseOpenLibraryDocs(body), nil
}

// buildOpenLibraryURL assembles the OpenLibrary search request URL, escaping the
// query and trimming the response with the fields projection.
func buildOpenLibraryURL(query string, limit int) string {
	params := url.Values{}
	params.Set("q", query)
	params.Set("limit", strconv.Itoa(clampOpenLibraryLimit(limit)))
	params.Set("fields", openLibraryFields)
	return openLibraryBase + "/search.json?" + params.Encode()
}

// clampOpenLibraryLimit maps a caller-supplied limit onto OpenLibrary's accepted
// range, substituting the default for a non-positive value and clamping the rest.
func clampOpenLibraryLimit(limit int) int {
	switch {
	case limit <= 0:
		return openLibraryDefaultLimit
	case limit > openLibraryMaxLimit:
		return openLibraryMaxLimit
	default:
		return limit
	}
}

// openLibraryEnvelope and openLibraryDoc are the subset of the OpenLibrary search
// response the resolver reads (mirroring the fields projection).
type openLibraryEnvelope struct {
	Docs []openLibraryDoc `json:"docs"`
}

type openLibraryDoc struct {
	Title            string   `json:"title"`
	AuthorName       []string `json:"author_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	ISBN             []string `json:"isbn"`
	Key              string   `json:"key"`
}

// parseOpenLibraryDocs decodes an OpenLibrary search envelope into DiscoveryResults,
// returning an empty slice when the body cannot be decoded (best-effort — a
// malformed response is treated as no results, not an error).
func parseOpenLibraryDocs(body []byte) []DiscoveryResult {
	var env openLibraryEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	results := make([]DiscoveryResult, 0, len(env.Docs))
	for _, doc := range env.Docs {
		results = append(results, openLibraryDocToResult(doc))
	}
	return results
}

// openLibraryDocToResult maps one OpenLibrary doc onto a DiscoveryResult: the title,
// "; "-joined author names, the first-publish year, and the first ISBN (empty when
// none). Origin is "openlibrary"; because this is a resolver and not a download
// source, DOI/PDFURL stay empty and OpenAccess stays false.
func openLibraryDocToResult(doc openLibraryDoc) DiscoveryResult {
	year := ""
	if doc.FirstPublishYear > 0 {
		year = strconv.Itoa(doc.FirstPublishYear)
	}
	return DiscoveryResult{
		Origin:     "openlibrary",
		Title:      strings.TrimSpace(doc.Title),
		Authors:    openLibraryAuthors(doc.AuthorName),
		Year:       year,
		ISBN:       firstNonEmpty(doc.ISBN),
		OpenAccess: false,
	}
}

// openLibraryAuthors joins author names with "; ", skipping any entry that collapses
// to an empty name.
func openLibraryAuthors(names []string) string {
	kept := make([]string, 0, len(names))
	for _, n := range names {
		if name := strings.TrimSpace(n); name != "" {
			kept = append(kept, name)
		}
	}
	return strings.Join(kept, "; ")
}
