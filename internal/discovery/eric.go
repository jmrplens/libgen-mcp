package discovery

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// ericBase is the ERIC API root, run by the Institute of Education Sciences. It is a
// package variable (not a constant) so tests can point it at a local httptest server.
var ericBase = "https://api.ies.ed.gov/eric"

// ericFilesBase is the host ERIC serves its hosted full texts from. It is a constant
// on purpose: the URL is emitted to the caller rather than fetched by this package, so
// a test that redirected it would assert a link nobody could follow.
const ericFilesBase = "https://files.eric.ed.gov/fulltext"

// ERIC limit bounds: the API is asked for at least one and at most this many rows,
// defaulting to ericDefaultLimit when the caller passes a non-positive value. ERIC
// itself accepts up to 200 rows per request; the lower ceiling here keeps a federated
// search's share of the response budget in line with the other providers.
const (
	ericMaxLimit     = 50
	ericDefaultLimit = 10
)

// ericRate is the courtesy delay this provider paces itself to. ERIC publishes no
// rate limit and requires no key, so the figure is ours: one request per second,
// matching what the other keyless providers extend to services that ask for nothing.
const ericRate = time.Second

// ericHosted is the value of a record's e_fulltextauth flag meaning ERIC holds an
// authorized copy of the full text and serves it from ericFilesBase.
//
// That flag — not the ED/EJ prefix of the accession number — is what decides whether
// a PDF exists. The prefix records how the document entered the index (ED for a
// document, EJ for a journal article) and predicts hosting badly in both directions:
// sampled EJ records with the flag set serve a PDF, and sampled ED records without it
// answer 404. Deriving availability from the prefix would therefore both invent dead
// links and hide live ones.
const ericHosted = 1

// ericFields is the field list every request asks for. ERIC returns its full record
// otherwise, including a multi-kilobyte abstract per hit that DiscoveryResult has
// nowhere to put and that would push a page of results against the shared body bound.
var ericFields = strings.Join([]string{
	"id", "title", "author", "publicationdateyear", "source", "sourceid",
	"institution", "url", "isbn", "e_fulltextauth",
}, ",")

// ericSolrSpecials are the characters Lucene query syntax reserves. ERIC parses the
// search parameter as a Lucene query and answers an unparseable one with HTTP 200
// carrying an error object, so an ordinary title — "Reading: A Study" — would silently
// return nothing unless each of these is escaped.
const ericSolrSpecials = `+-&|!(){}[]^"~*?:\/`

// ERICProvider is a keyless discovery source backed by ERIC, the Institute of
// Education Sciences' index of education research. It is the only provider here that
// reaches education grey literature: technical reports, dissertations, conference
// papers and government/agency documents that carry no DOI and therefore appear in
// neither Crossref nor the DOI-keyed full-text sources.
//
// It is a discovery provider only, with no matching DownloadSource. ERIC's hosted full
// texts live at a URL derived from the accession number with no lookup step, so a
// record that has one is surfaced with its pdf_url already filled in and there is
// nothing for a Resolve to resolve. Its limiter and http.Client are self-contained, so
// it never shares state with libgen's client.
type ERICProvider struct {
	client  *http.Client
	limiter *rate.Limiter
}

// NewERIC constructs an ERICProvider with its own http.Client and a rate limiter
// pacing requests to one per second (burst 1, so the first request goes through
// immediately and only back-to-back requests wait).
func NewERIC() *ERICProvider {
	return &ERICProvider{
		client:  newDiscoveryClient(),
		limiter: rate.NewLimiter(rate.Every(ericRate), 1),
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *ERICProvider) Name() string { return "eric" }

// Search queries the ERIC index for the given free-text query and returns up to limit
// results. It is best-effort: a non-200 status, an unparseable body, or ERIC's own
// error envelope degrades to an empty result with no error, so a failing provider never
// sinks a federated search. Only a context cancellation or deadline propagates as an
// error.
//
// A query with no usable terms never leaves the process: an empty search parameter
// would either be rejected or match the whole index.
func (p *ERICProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	solrQuery := ericSolrQuery(query)
	if solrQuery == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if err := p.limiter.Wait(ctx); err != nil {
		return nil, ctx.Err()
	}

	status, body, err := boundedGet(ctx, p.client, ericSearchURL(solrQuery, limit))
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
	return parseERICDocs(body), nil
}

// ericSearchURL assembles the search request URL from an already-escaped Lucene query:
// JSON output, the clamped row count, and the explicit field list that keeps abstracts
// out of the response.
func ericSearchURL(solrQuery string, limit int) string {
	params := url.Values{}
	params.Set("search", solrQuery)
	params.Set("format", "json")
	params.Set("rows", strconv.Itoa(clampERICLimit(limit)))
	params.Set("fields", ericFields)
	return ericBase + "/?" + params.Encode()
}

// clampERICLimit maps a caller-supplied limit onto ERIC's accepted range, substituting
// the default for a non-positive value and clamping the rest.
func clampERICLimit(limit int) int {
	switch {
	case limit <= 0:
		return ericDefaultLimit
	case limit > ericMaxLimit:
		return ericMaxLimit
	default:
		return limit
	}
}

// ericSolrQuery turns a free-text query into a Lucene query ERIC can parse: every
// whitespace-separated term is escaped character by character and the terms are joined
// with AND. It returns "" when nothing usable survives.
//
// Joining with AND is deliberate. ERIC's default operator is OR, which for a two-word
// query returns an order of magnitude more hits than the caller meant — "professional
// development" matches 711k records as OR against 109k as AND. The trade-off is that a
// caller cannot hand this provider fielded Lucene syntax of its own; that syntax is
// escaped into literal terms, which is the right default when the query is a title or
// a topic rather than a hand-written expression.
func ericSolrQuery(query string) string {
	terms := strings.Fields(query)
	escaped := make([]string, 0, len(terms))
	for _, term := range terms {
		if e := escapeSolrTerm(term); e != "" {
			escaped = append(escaped, e)
		}
	}
	return strings.Join(escaped, " AND ")
}

// escapeSolrTerm backslash-escapes every Lucene metacharacter in one term, leaving
// everything else untouched.
func escapeSolrTerm(term string) string {
	var b strings.Builder
	b.Grow(len(term))
	for _, r := range term {
		if r < 128 && strings.ContainsRune(ericSolrSpecials, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ericEnvelope is the subset of an ERIC response the provider reads. Both members are
// optional: a successful query fills response, while one ERIC's Lucene parser rejects
// fills error instead — still under HTTP 200, which is why the error member has to be
// looked at at all.
type ericEnvelope struct {
	Response struct {
		Docs []ericDoc `json:"docs"`
	} `json:"response"`
	Error *ericError `json:"error"`
}

// ericError is ERIC's in-band error object, carrying the Lucene parser's complaint and
// the status code it would have used had it not answered 200.
type ericError struct {
	Msg  string `json:"msg"`
	Code int    `json:"code"`
}

// ericDoc is one record as ERIC returns it under the field list this provider requests.
type ericDoc struct {
	// ID is the accession number (e.g. "ED427241"), which also names the hosted
	// full-text file.
	ID string `json:"id"`
	// Title is the record title.
	Title string `json:"title"`
	// Author lists the authors, already formatted by ERIC ("Family, Given").
	Author []string `json:"author"`
	// Year is the publication year, absent for a record ERIC could not date.
	Year int `json:"publicationdateyear"`
	// Source names the publication the record appeared in: a journal title for a
	// journal article, or a label such as "Online Submission" or "ProQuest LLC" for
	// grey literature.
	Source string `json:"source"`
	// SourceID is the volume/issue/page string for a journal article, or the degree
	// and awarding institution for a thesis.
	SourceID string `json:"sourceid"`
	// Institution lists the organizations that performed the work, which for an
	// agency report is the closest thing the record has to a venue.
	Institution []string `json:"institution"`
	// URL is the publisher or gateway link ERIC files with the record; it is a
	// doi.org link for many journal articles and something unresolvable for the rest.
	URL string `json:"url"`
	// ISBN lists the record's ISBNs, some labeled with an "ISBN-" prefix.
	ISBN []string `json:"isbn"`
	// FullTextAuth is 1 when ERIC hosts an authorized full text for this record.
	FullTextAuth int `json:"e_fulltextauth"`
}

// parseERICDocs decodes an ERIC response into DiscoveryResults, returning nil when the
// body cannot be decoded or carries ERIC's in-band error object (best-effort — either
// is treated as no results, not an error).
//
// A record that decodes to no title is skipped: it carries nothing a caller could read,
// cite or fetch.
func parseERICDocs(body []byte) []DiscoveryResult {
	var env ericEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	if env.Error != nil {
		return nil
	}
	results := make([]DiscoveryResult, 0, len(env.Response.Docs))
	for _, doc := range env.Response.Docs {
		if strings.TrimSpace(doc.Title) == "" {
			continue
		}
		results = append(results, ericDocToResult(doc))
	}
	return results
}

// ericDocToResult maps one ERIC record onto a DiscoveryResult: the title, "; "-joined
// authors, the year, a citation-shaped venue, the DOI when the record's url is one, the
// ISBN when it has one, and the hosted full-text URL when ERIC holds the document.
//
// OpenAccess tracks the hosted full text exactly. ERIC's own copies are US
// government-funded documents it serves to anyone without a login, so a record it hosts
// is genuinely free to read; a record it merely indexes says nothing about the
// publisher's terms, so claiming otherwise would be a guess.
func ericDocToResult(doc ericDoc) DiscoveryResult {
	fullText := ericFullTextURL(doc)
	return DiscoveryResult{
		Origin:     "eric",
		Title:      ericText(doc.Title),
		Authors:    ericAuthors(doc.Author),
		Year:       ericYear(doc.Year),
		Venue:      ericVenue(doc),
		DOI:        ericDOIFromURL(doc.URL),
		ISBN:       ericISBN(doc.ISBN),
		PDFURL:     fullText,
		OpenAccess: fullText != "",
	}
}

// ericFullTextURL returns the deterministic files.eric.ed.gov URL for a record ERIC
// hosts a full text for, and "" otherwise — including for a record with no accession
// number, since the URL is built from it. No lookup is involved: ERIC names every
// hosted file after the accession number, which is why this provider needs no download
// source to make the file reachable.
func ericFullTextURL(doc ericDoc) string {
	id := strings.TrimSpace(doc.ID)
	if doc.FullTextAuth != ericHosted || id == "" {
		return ""
	}
	return ericFilesBase + "/" + url.PathEscape(id) + ".pdf"
}

// ericText normalizes one ERIC text value: XML entities are unescaped and whitespace
// is collapsed. ERIC stores its records escaped and serves them to the JSON API that
// way, so a title reaches us as "Supporting Teachers&apos; Effective Use of
// Technology" and would otherwise be shown to the caller as literal markup.
func ericText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// ericAuthors joins author names with "; ", skipping empty entries.
func ericAuthors(authors []string) string {
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		if name := ericText(a); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "; ")
}

// ericYear renders a record's publication year, mapping ERIC's absent-year encoding
// (the field is simply missing, so it decodes to zero) to "" rather than "0".
func ericYear(year int) string {
	if year <= 0 {
		return ""
	}
	return strconv.Itoa(year)
}

// ericVenue builds the short citation string Venue is documented to carry, from
// whichever of ERIC's three placement fields the record has: the source joined to its
// volume/issue string ("Professional Development in Education, v40 n2 p295-315 2014",
// or "ProQuest LLC, Ed.D. Dissertation, Baylor University"), else the performing
// institution, which for an agency report is the only placement it states.
func ericVenue(doc ericDoc) string {
	source := ericText(doc.Source)
	sourceID := ericText(doc.SourceID)
	switch {
	case source != "" && sourceID != "":
		return source + ", " + sourceID
	case source != "":
		return source
	case sourceID != "":
		return sourceID
	default:
		return ericText(firstNonEmpty(doc.Institution))
	}
}

// ericDOIFromURL extracts the DOI from the link ERIC files with a record, which is a
// doi.org or dx.doi.org URL for many journal articles. It returns "" for every other
// link — a publisher catalog page, a ProQuest dissertation gateway — so a URL that is
// not an identifier is never presented as one.
func ericDOIFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimPrefix(u.Host, "www."))
	if host != "doi.org" && host != "dx.doi.org" {
		return ""
	}
	return strings.Trim(u.Path, "/")
}

// ericISBN returns the record's first ISBN with ERIC's "ISBN-" label stripped, so the
// value is the identifier itself and can be used as a lookup key. ERIC labels older
// records and leaves newer ones bare, so both spellings occur in one index.
func ericISBN(isbns []string) string {
	raw := firstNonEmpty(isbns)
	if raw == "" {
		return ""
	}
	if len(raw) >= len("ISBN-") && strings.EqualFold(raw[:len("ISBN-")], "ISBN-") {
		return strings.TrimSpace(raw[len("ISBN-"):])
	}
	return raw
}
