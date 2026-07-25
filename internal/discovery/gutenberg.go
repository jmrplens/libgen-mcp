package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// gutendexBase is the root of the Gutendex API, a well-used third-party JSON API
// over the Project Gutenberg catalog (Gutenberg itself publishes no JSON API). It
// is a package variable (not a constant) so tests can point it at a local httptest
// server. The files it links to are served by gutenberg.org itself.
var gutendexBase = "https://gutendex.com"

// gutenbergDefaultLimit is how many hits are kept when the caller passes a
// non-positive limit. Gutendex has no page-size parameter — it always answers with
// a full page of 32 — so the limit is applied on this side.
const gutenbergDefaultLimit = 10

// gutenbergRate is the courtesy delay this provider paces itself to. Gutendex
// publishes no rate limit, so the figure is ours: one request per second, matching
// what the other keyless providers extend to services that ask for nothing.
const gutenbergRate = time.Second

// gutenbergTextMedia is the Gutendex media_type of a readable book. The catalog
// also carries sound recordings, which have no text to offer.
const gutenbergTextMedia = "Text"

// gutenbergFormats are the Gutendex format mimetypes worth linking to, best first,
// each paired with the extension it yields. EPUB leads because it preserves the
// book's structure and the read tool understands it; plain text is the universal
// fallback. The HTML reading page, the RDF metadata record, the Kindle build and
// the cover image are all deliberately absent: none of them is the book as a file.
var gutenbergFormats = []struct {
	mime string
	ext  string
}{
	{"application/epub+zip", "epub"},
	{"application/pdf", "pdf"},
	{"text/plain; charset=utf-8", "txt"},
	{"text/plain; charset=us-ascii", "txt"},
	{"text/plain", "txt"},
}

// GutenbergProvider is a keyless discovery source backed by Project Gutenberg's
// catalog, surfacing public-domain books with a directly-fetchable file URL.
//
// It is a discovery provider and NOT a download source, which is a deliberate
// design decision rather than an omission. A DownloadSource is asked to resolve an
// identifier the caller already holds — an md5, a DOI, an ISBN — and Project
// Gutenberg has none of those: its catalog is keyed by a Gutenberg-internal ebook
// id, its texts carry no DOI, and Gutendex exposes no ISBN. The only way to key a
// download source on it would be to match on title and author, and a title match is
// not an identity match: two different editions, translations or abridgements share
// a title, so the source would sometimes serve a DIFFERENT book while reporting
// success. Surfacing the hit with its file URL instead keeps the mapping from query
// to file visible to the caller, who can see the title, author and link before
// fetching anything.
//
// Its limiter and http.Client are self-contained, so it never shares state with
// libgen's client.
type GutenbergProvider struct {
	client  *http.Client
	limiter *rate.Limiter
}

// NewGutenberg constructs a GutenbergProvider with its own http.Client and a rate
// limiter pacing requests to one per second (burst 1, so the first request goes
// through immediately and only back-to-back requests wait).
func NewGutenberg() *GutenbergProvider {
	return &GutenbergProvider{
		client:  newDiscoveryClient(),
		limiter: rate.NewLimiter(rate.Every(gutenbergRate), 1),
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *GutenbergProvider) Name() string { return "gutenberg" }

// Search queries the Gutendex catalog for the given free-text query and returns up
// to limit public-domain books, each with a directly-fetchable file URL. It is
// best-effort: a non-200 status or any non-context failure degrades to an empty
// result with no error, so a failing provider never sinks a federated search. Only
// a context cancellation or deadline propagates as an error.
func (p *GutenbergProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if err := p.limiter.Wait(ctx); err != nil {
		return nil, ctx.Err()
	}

	rawURL := gutendexBase + "/books/?" + url.Values{"search": {query}}.Encode()
	status, body, err := boundedGet(ctx, p.client, rawURL)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, nil
	}
	return parseGutendex(body, limit), nil
}

// gutendexEnvelope is the subset of a Gutendex response the provider reads.
type gutendexEnvelope struct {
	Results []gutendexBook `json:"results"`
}

// gutendexBook is one catalog record: its title and authors, whether it is still in
// copyright, whether it is a text at all, and the map of format mimetype to file
// URL that makes it fetchable.
type gutendexBook struct {
	ID      int              `json:"id"`
	Title   string           `json:"title"`
	Authors []gutendexPerson `json:"authors"`
	// Copyright is a pointer so "still in copyright" (true), "public domain"
	// (false) and "the catalog does not say" (null) stay distinguishable; only an
	// explicit false is surfaced.
	Copyright *bool             `json:"copyright"`
	MediaType string            `json:"media_type"`
	Formats   map[string]string `json:"formats"`
}

// gutendexPerson is a catalog contributor, of which only the name is used.
type gutendexPerson struct {
	Name string `json:"name"`
}

// parseGutendex decodes a Gutendex response into DiscoveryResults, keeping only the
// public-domain texts that offer a fetchable file and stopping at limit. A body it
// cannot decode yields no results (best-effort — a malformed response is treated as
// no results, not an error).
func parseGutendex(body []byte, limit int) []DiscoveryResult {
	var env gutendexEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	if limit <= 0 {
		limit = gutenbergDefaultLimit
	}
	var results []DiscoveryResult
	for _, book := range env.Results {
		if len(results) == limit {
			break
		}
		if res, ok := gutenbergBookToResult(book); ok {
			results = append(results, res)
		}
	}
	return results
}

// gutenbergBookToResult maps one catalog record onto a DiscoveryResult. The bool
// reports whether the record qualifies: it must be a text, be explicitly out of
// copyright, and offer a format worth linking to. Everything else — an in-copyright
// text hosted with the rightsholder's permission, a sound recording, a record whose
// only formats are the HTML reading page and a cover image — is dropped, because
// this server surfaces freely licensed files and a result with no file is noise.
func gutenbergBookToResult(book gutendexBook) (DiscoveryResult, bool) {
	if book.Copyright == nil || *book.Copyright {
		return DiscoveryResult{}, false
	}
	if !strings.EqualFold(book.MediaType, gutenbergTextMedia) {
		return DiscoveryResult{}, false
	}
	fileURL, ext := gutenbergPickFormat(book.Formats)
	if fileURL == "" {
		return DiscoveryResult{}, false
	}
	return DiscoveryResult{
		Origin:      "gutenberg",
		Title:       strings.TrimSpace(book.Title),
		Authors:     gutenbergAuthors(book.Authors),
		Extension:   ext,
		FullTextURL: fileURL,
		OpenAccess:  true,
	}, true
}

// gutenbergPickFormat chooses the best fetchable file from a record's format map,
// returning its URL and the extension it yields, or two empty strings when the
// record offers nothing worth linking to.
func gutenbergPickFormat(formats map[string]string) (fileURL, ext string) {
	for _, want := range gutenbergFormats {
		for mime, link := range formats {
			if !strings.EqualFold(strings.TrimSpace(mime), want.mime) {
				continue
			}
			if trimmed := strings.TrimSpace(link); trimmed != "" {
				return trimmed, want.ext
			}
		}
	}
	return "", ""
}

// gutenbergAuthors joins contributor names with "; ", skipping any entry that
// collapses to an empty name, matching how the other providers render authors.
func gutenbergAuthors(people []gutendexPerson) string {
	kept := make([]string, 0, len(people))
	for _, p := range people {
		if name := strings.TrimSpace(p.Name); name != "" {
			kept = append(kept, name)
		}
	}
	return strings.Join(kept, "; ")
}
