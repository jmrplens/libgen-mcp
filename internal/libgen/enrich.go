package libgen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// enrichTimeout is the hard wall-clock budget for a whole Enrich call: both the
// Crossref and OpenLibrary lookups (and any second OpenLibrary hop) must finish
// within it, otherwise enrichment degrades to nil rather than delaying the core
// details response.
const enrichTimeout = 6 * time.Second

// enrichMaxBody bounds how many bytes of any enrichment response are read before
// decoding, guarding against an unexpectedly large or hostile body.
const enrichMaxBody = 1 << 20 // 1 MiB

// crossrefBase and openLibraryBase are the enrichment API roots. They are package
// variables (not constants) so tests can point them at local httptest servers.
var (
	crossrefBase    = "https://api.crossref.org"
	openLibraryBase = "https://openlibrary.org"
)

// CrossrefWork is the subset of a Crossref work record surfaced by enrichment:
// bibliographic container metadata plus reference and citation counts.
type CrossrefWork struct {
	ContainerTitle string   `json:"container_title,omitempty"` // journal/book title
	ISSN           []string `json:"issn,omitempty"`
	Volume         string   `json:"volume,omitempty"`
	Issue          string   `json:"issue,omitempty"`
	Publisher      string   `json:"publisher,omitempty"`
	PublishedYear  int      `json:"published_year,omitempty"`
	ReferenceCount int      `json:"reference_count,omitempty"`
	CitationCount  int      `json:"citation_count,omitempty"` // is-referenced-by-count
	Subjects       []string `json:"subjects,omitempty"`
}

// OLBook is the subset of an OpenLibrary work (plus its ISBN record's cover)
// surfaced by enrichment: subjects, a description and links.
type OLBook struct {
	Subjects    []string `json:"subjects,omitempty"`
	Description string   `json:"description,omitempty"`
	CoverURL    string   `json:"cover_url,omitempty"`
	OpenLibURL  string   `json:"open_library_url,omitempty"`
}

// Enrichment bundles the best-effort external metadata found for a record. Either
// side may be nil when that source yielded nothing.
type Enrichment struct {
	Crossref    *CrossrefWork `json:"crossref,omitempty"`
	OpenLibrary *OLBook       `json:"open_library,omitempty"`
}

// Enrich fetches best-effort metadata for a DOI (Crossref) and/or ISBN
// (OpenLibrary), running both concurrently under a hard timeout. It returns nil
// when neither yields anything (or on any error). It NEVER returns an error —
// enrichment is advisory and must never fail or slow the core details response
// beyond its budget.
func (c *Client) Enrich(ctx context.Context, doi, isbn string) *Enrichment {
	ctx, cancel := context.WithTimeout(ctx, enrichTimeout)
	defer cancel()

	var (
		wg sync.WaitGroup
		cr *CrossrefWork
		ol *OLBook
	)
	if doi != "" {
		wg.Go(func() { cr = c.fetchCrossref(ctx, doi) })
	}
	if isbn != "" {
		wg.Go(func() { ol = c.fetchOpenLibrary(ctx, isbn) })
	}
	wg.Wait()

	if cr == nil && ol == nil {
		return nil
	}
	return &Enrichment{Crossref: cr, OpenLibrary: ol}
}

// enrichGet issues a GET to an enrichment API after waiting on the separate
// enrichment rate limiter, setting the polite-pool User-Agent (the package
// userAgent plus a mailto when a contact email is configured). It returns the
// response on a 200 and nil otherwise (including any transport or limiter error),
// so callers degrade silently. The caller owns closing the returned body.
func (c *Client) enrichGet(ctx context.Context, rawURL string) *http.Response {
	if err := c.enrichLimiter.Wait(ctx); err != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil
	}
	ua := userAgent
	if c.enrichEmail != "" {
		ua += " (mailto:" + c.enrichEmail + ")"
	}
	req.Header.Set("User-Agent", ua)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil
	}
	return resp
}

// fetchCrossref requests the Crossref work for a DOI and parses it, returning nil
// on any failure (non-200, transport error, or unparseable body).
func (c *Client) fetchCrossref(ctx context.Context, doi string) *CrossrefWork {
	resp := c.enrichGet(ctx, crossrefBase+"/works/"+url.PathEscape(doi))
	if resp == nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	return parseCrossref(io.LimitReader(resp.Body, enrichMaxBody))
}

// crossrefEnvelope is the subset of the Crossref `{"message":{...}}` response that
// enrichment reads; field tags map the hyphenated JSON keys onto Go fields.
type crossrefEnvelope struct {
	Message struct {
		ContainerTitle []string `json:"container-title"`
		ISSN           []string `json:"ISSN"`
		Volume         string   `json:"volume"`
		Issue          string   `json:"issue"`
		Publisher      string   `json:"publisher"`
		Published      struct {
			DateParts [][]int `json:"date-parts"`
		} `json:"published"`
		ReferencesCount int      `json:"references-count"`
		IsReferencedBy  int      `json:"is-referenced-by-count"`
		Subject         []string `json:"subject"`
	} `json:"message"`
}

// parseCrossref decodes a Crossref work envelope into a CrossrefWork, mapping the
// first container-title and the published year from date-parts[0][0]. Missing
// fields stay zero; it returns nil only when the body cannot be decoded.
func parseCrossref(r io.Reader) *CrossrefWork {
	var env crossrefEnvelope
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		return nil
	}
	m := env.Message
	w := &CrossrefWork{
		ISSN:           m.ISSN,
		Volume:         m.Volume,
		Issue:          m.Issue,
		Publisher:      m.Publisher,
		ReferenceCount: m.ReferencesCount,
		CitationCount:  m.IsReferencedBy,
		Subjects:       m.Subject,
	}
	if len(m.ContainerTitle) > 0 {
		w.ContainerTitle = m.ContainerTitle[0]
	}
	if len(m.Published.DateParts) > 0 && len(m.Published.DateParts[0]) > 0 {
		w.PublishedYear = m.Published.DateParts[0][0]
	}
	return w
}

// olISBNRecord is the subset of an OpenLibrary /isbn/{isbn}.json record read at
// hop 1: the cover ids and the linked work keys.
type olISBNRecord struct {
	Covers []int `json:"covers"`
	Works  []struct {
		Key string `json:"key"`
	} `json:"works"`
}

// fetchOpenLibrary resolves an ISBN to an OLBook in at most two hops: the ISBN
// record (cover + work key), then the work record (subjects + description). It
// returns nil when hop 1 fails or when nothing at all could be gathered.
func (c *Client) fetchOpenLibrary(ctx context.Context, isbn string) *OLBook {
	rec := c.fetchOLISBN(ctx, isbn)
	if rec == nil {
		return nil
	}
	book := &OLBook{}
	if len(rec.Covers) > 0 && rec.Covers[0] > 0 {
		book.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", rec.Covers[0])
	}
	if len(rec.Works) > 0 && rec.Works[0].Key != "" {
		book.OpenLibURL = openLibraryBase + rec.Works[0].Key
		c.fetchOLWork(ctx, rec.Works[0].Key, book)
	}
	if book.CoverURL == "" && book.OpenLibURL == "" && len(book.Subjects) == 0 && book.Description == "" {
		return nil
	}
	return book
}

// fetchOLISBN requests hop 1, the OpenLibrary ISBN record, returning nil on any
// failure (non-200, transport error, or unparseable body).
func (c *Client) fetchOLISBN(ctx context.Context, isbn string) *olISBNRecord {
	resp := c.enrichGet(ctx, openLibraryBase+"/isbn/"+url.PathEscape(isbn)+".json")
	if resp == nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	var rec olISBNRecord
	if err := json.NewDecoder(io.LimitReader(resp.Body, enrichMaxBody)).Decode(&rec); err != nil {
		return nil
	}
	return &rec
}

// olWorkRecord is the subset of an OpenLibrary work record read at hop 2. The
// description is a raw message because OpenLibrary sends it as either a plain
// string or an object `{"type":..., "value":"..."}`.
type olWorkRecord struct {
	Subjects    []string        `json:"subjects"`
	Description json.RawMessage `json:"description"`
}

// fetchOLWork requests hop 2, the OpenLibrary work record, and fills the book's
// Subjects and Description from it. It is silent on any failure — the ISBN
// record's cover/link already gathered on hop 1 still stand.
func (c *Client) fetchOLWork(ctx context.Context, workKey string, book *OLBook) {
	resp := c.enrichGet(ctx, openLibraryBase+workKey+".json")
	if resp == nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var rec olWorkRecord
	if err := json.NewDecoder(io.LimitReader(resp.Body, enrichMaxBody)).Decode(&rec); err != nil {
		return
	}
	book.Subjects = rec.Subjects
	book.Description = parseOLDescription(rec.Description)
}

// parseOLDescription extracts the text from an OpenLibrary description, which may
// be a plain JSON string or an object with a "value" field. It returns "" when
// the value is absent or neither form decodes.
func parseOLDescription(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.Value)
	}
	return ""
}
