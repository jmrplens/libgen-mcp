package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
// the response to just the fields the resolver reads. Beyond the bibliographic
// basics it asks for the availability signals (ebook_access, has_fulltext, ia) so a
// publicly readable book can be surfaced with a direct archive.org link. Nothing
// else belongs here: a field that DiscoveryResult cannot carry is paid for on every
// search and reaches no caller.
const openLibraryFields = "title,author_name,first_publish_year,isbn,key,ebook_access,has_fulltext,ia"

// openLibraryPublicAccess is the ebook_access value OpenLibrary uses for a book that
// is freely readable in full on the Internet Archive (as opposed to "borrowable",
// "printdisabled" or "no_ebook").
const openLibraryPublicAccess = "public"

// openLibraryEmailRPS (a contact email is advertised in the User-Agent) and
// openLibraryAnonRPS (no contact) are the request rates OpenLibrary's etiquette
// grants: 1 request per second by default, tripled for a caller that identifies
// itself with an application name and a contact address
// (https://openlibrary.org/developers/api).
//
// This is the same policy internal/libgen's enrichment path applies to its own
// OpenLibrary hops; the two are separate limiters (they are separate clients on
// separate call paths) but they must agree on the numbers, so a change here
// belongs in openLibraryEnrichRPS as well.
//
// The burst equals the rate rather than exceeding it: a limiter with a burst
// larger than its per-second rate lets a caller fire that whole burst at once, so
// an anonymous caller nominally honoring 1 rps would still open with two or three
// back-to-back requests.
const (
	openLibraryEmailRPS = 3
	openLibraryAnonRPS  = 1
)

// OpenLibraryProvider is a keyless query resolver that turns fuzzy title/author
// queries into canonical identifiers (ISBN/title/year) which feed a Library Genesis
// search. It is primarily a resolver, not a download source, so its results carry no
// PDF URL; the one exception is a publicly readable book, which it surfaces with a
// free-to-read archive.org link and marks open-access. Its limiter and http.Client
// are self-contained, so it never shares state with libgen's client.
type OpenLibraryProvider struct {
	client    *http.Client
	limiter   *rate.Limiter
	userAgent string
}

// NewOpenLibrary constructs an OpenLibraryProvider with its own http.Client. When a
// contact email is supplied it is advertised in the User-Agent and the limiter is
// paced to OpenLibrary's identified allowance (3 rps); without one the provider
// stays anonymous and drops to the unidentified allowance (1 rps), honoring
// https://openlibrary.org/developers/api.
func NewOpenLibrary(email string) *OpenLibraryProvider {
	email = strings.TrimSpace(email)
	// Resolved once here rather than per request: providers are constructed from a
	// command's startup path, which has already handed the stamped version to
	// internal/version.
	ua := discoveryUserAgent()
	rps := openLibraryAnonRPS
	if email != "" {
		ua += " (mailto:" + email + ")"
		rps = openLibraryEmailRPS
	}
	return &OpenLibraryProvider{
		client:    newDiscoveryClient(),
		limiter:   rate.NewLimiter(rate.Limit(rps), rps),
		userAgent: ua,
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

	status, body, err := boundedGetUA(ctx, p.client, rawURL, p.userAgent)
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
	// EbookAccess is the availability tier ("public", "borrowable",
	// "printdisabled", "no_ebook"); only "public" is freely readable in full.
	EbookAccess string `json:"ebook_access"`
	// HasFulltext reports whether OpenLibrary holds any full-text scan; it is
	// requested for client display and gates the readable-book signal alongside
	// EbookAccess.
	HasFulltext bool `json:"has_fulltext"`
	// IA holds the Internet Archive identifiers backing the book; the first one
	// forms the archive.org "details" URL for a publicly readable copy.
	IA []string `json:"ia"`
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
// none). Origin is "openlibrary". For most docs this is a resolver hit — DOI/PDFURL
// stay empty and OpenAccess is false — but a publicly readable book (ebook_access
// "public" with an Internet Archive id) additionally carries a free-to-read
// archive.org link and is marked open-access.
func openLibraryDocToResult(doc openLibraryDoc) DiscoveryResult {
	year := ""
	if doc.FirstPublishYear > 0 {
		year = strconv.Itoa(doc.FirstPublishYear)
	}
	archiveURL := openLibraryArchiveURL(doc)
	return DiscoveryResult{
		Origin:     "openlibrary",
		Title:      strings.TrimSpace(doc.Title),
		Authors:    openLibraryAuthors(doc.AuthorName),
		Year:       year,
		ISBN:       firstNonEmpty(doc.ISBN),
		ArchiveURL: archiveURL,
		OpenAccess: archiveURL != "",
	}
}

// openLibraryArchiveURL returns a free-to-read archive.org "details" URL when the
// doc is a publicly readable book — ebook_access "public", full text held, and an
// Internet Archive id present — and "" otherwise. The id is path-escaped so an odd
// identifier cannot corrupt the URL.
func openLibraryArchiveURL(doc openLibraryDoc) string {
	if doc.EbookAccess != openLibraryPublicAccess || !doc.HasFulltext {
		return ""
	}
	ia := firstNonEmpty(doc.IA)
	if ia == "" {
		return ""
	}
	return "https://archive.org/details/" + url.PathEscape(ia)
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
