package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/time/rate"
)

// dblpBase is the root of dblp's public SPARQL query service. It is a package
// variable (not a constant) so tests can point it at a local httptest server.
//
// The provider asks the knowledge graph rather than the search API at
// dblp.org/search/publ/api, which is no longer a route an automated client may
// take: since 2026-10 dblp.org answers it with an Anubis bot check, and its
// robots.txt ends with "User-agent: *" and "Disallow: /". The SPARQL host's own
// robots.txt allows /sparql (with a Crawl-delay of 10), and dblp announced the
// endpoint as an API for scripted queries, asking only that it not be overwhelmed.
var dblpBase = "https://sparql.dblp.org"

// dblp limit bounds: the query asks for at least one and at most this many
// records, defaulting to dblpDefaultLimit when the caller passes a non-positive
// value.
const (
	dblpMaxLimit     = 50
	dblpDefaultLimit = 10
)

// dblpMaxWords caps how many words of the caller's query go into the text search.
// Every word must match, so a long query only narrows the result, and a bounded
// count keeps the request small whatever the caller typed.
const dblpMaxWords = 12

// dblpServerTimeout is the deadline the query service is asked to stop working by.
// It sits under discoveryTimeout, so a query this server has already given up on
// does not go on costing the service anything.
const dblpServerTimeout = "5s"

// dblpRate is the delay this provider paces itself to: one request every ten
// seconds, process-wide. It is the Crawl-delay sparql.dblp.org's robots.txt names,
// and the blog post announcing the endpoint asks scripted clients not to
// overwhelm it. With a burst of one, a search runs at once, and a second search
// inside the window goes without dblp rather than waiting past its budget.
func dblpRate() time.Duration { return 10 * time.Second }

// dblpRefusals remembers a dblp host that answered with a bot check or a rate
// limit, so the searches after it ask nothing until challengeCooldown has passed.
// The SPARQL host states a rate limit of its own without publishing a figure, and
// a bot check put in front of it would answer with an HTML page rather than the
// JSON results asked for, so both are read as a refusal.
var dblpRefusals refusalWindows

// DBLPProvider is a keyless discovery source backed by dblp's SPARQL query service
// over the dblp knowledge graph. It contributes precise CS bibliographic data —
// the venue, year and full author list that arXiv and Crossref match poorly for
// conference papers — never full text: dblp is an index, so its results carry no
// PDF URL and are never marked open access. Its http.Client is its own, so it never
// shares state with libgen's client, and its pacing is process-wide (see pacers).
type DBLPProvider struct {
	client *http.Client
	pace   pace
}

// NewDBLP constructs a DBLPProvider with its own http.Client, paced to dblpRate
// across every search this process runs (burst 1, so the first request goes
// through immediately and only back-to-back requests wait).
func NewDBLP() *DBLPProvider {
	return &DBLPProvider{
		client: newDiscoveryClient(),
		pace:   pace{limit: rate.Every(dblpRate()), burst: 1},
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *DBLPProvider) Name() string { return "dblp" }

// Search queries the dblp knowledge graph for the given free-text query and
// returns up to limit bibliographic results. It is best-effort: a non-200 status or
// any non-context failure degrades to an empty result with no error, so a failing
// provider never sinks a federated search. Only a context cancellation or deadline
// propagates as an error.
//
// A bot check or a rate limit is also an empty result, and additionally opens a
// refusal window (see dblpRefusals) during which no request is made at all.
func (p *DBLPProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	return p.SearchYears(ctx, query, limit, YearRange{})
}

// SearchYears is Search bounded to the publication years in years, which the
// SPARQL query applies as a FILTER on dblp:yearOfPublication. Its contract is
// Search's. A query with no word to search for asks nothing.
func (p *DBLPProvider) SearchYears(ctx context.Context, query string, limit int, years YearRange) ([]DiscoveryResult, error) {
	words := dblpWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	base := dblpBase
	if dblpRefusals.quiet(base, time.Now()) {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if err := p.pace.wait(ctx, p.Name(), base); err != nil {
		return nil, ctx.Err()
	}

	status, body, err := boundedGetHeaders(ctx, p.client, dblpQueryURL(base, words, limit, years), http.Header{
		"User-Agent": {discoveryUserAgent()},
		"Accept":     {"application/sparql-results+json"},
	})
	if err != nil {
		// Context errors propagate so the federation layer can tell "caller went
		// away" from "source degraded"; everything else degrades to empty.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	if dblpRefused(status, body) {
		if dblpRefusals.open(base, time.Now()) {
			slog.Info("dblp search: the SPARQL service answered with a bot check or a rate limit instead of JSON, asking nothing for "+
				challengeCooldown.String(), "status", status)
		}
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, nil
	}
	return parseDblpBindings(body, clampDblpLimit(limit)), nil
}

// dblpRefused reports whether a response is dblp declining to answer rather than
// answering: a 403 (a bot wall's deny verdict), a 429 (a rate limit), a 503 (what
// a bot wall or an overloaded front end answers), or a 200 whose body is not a
// JSON object. The service is asked for SPARQL JSON results, so a 200 opening with
// anything but "{" (after whitespace and a UTF-8 byte order mark) is a page put in
// front of it, and parsing it would only report "no results" for a search nobody
// ran.
func dblpRefused(status int, body []byte) bool {
	switch status {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	case http.StatusOK:
		trimmed := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(body), []byte("\xef\xbb\xbf")))
		return len(trimmed) == 0 || trimmed[0] != '{'
	default:
		return false
	}
}

// dblpWords splits a free-text query into the words the text search is asked for:
// runs of letters and digits, lowercased, de-duplicated, at most dblpMaxWords of
// them. Everything else is a separator, which is also what makes the words safe to
// place inside a SPARQL string literal and QLever's word list: no quote, backslash,
// brace, newline, prefix star or any other character with a meaning in either
// syntax survives the split. The index tokenizes titles the same way, so
// "pre-training" is found as the two words it is indexed as.
func dblpWords(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	words := make([]string, 0, min(len(fields), dblpMaxWords))
	for _, f := range fields {
		if len(words) == dblpMaxWords {
			break
		}
		if !slices.Contains(words, f) {
			words = append(words, f)
		}
	}
	return words
}

// dblpQueryURL assembles the GET request for the SPARQL protocol: the query on
// query, and the service-side deadline on timeout.
func dblpQueryURL(base string, words []string, limit int, years YearRange) string {
	params := url.Values{}
	params.Set("query", dblpSPARQL(words, clampDblpLimit(limit), years))
	params.Set("timeout", dblpServerTimeout)
	return base + "/sparql?" + params.Encode()
}

// dblpListSep separates the values one GROUP_CONCAT folds together. A tab cannot
// occur in a dblp name or venue, and the SPARQL literal spells it as an escape.
const dblpListSep = "\t"

// dblpSPARQL builds the query for words, limit and years.
//
// Matching uses QLever's text index over literals: every word must occur in the
// record's rdfs:label, which dblp writes as the first author (or the two authors),
// the title and the year, as in "Ashish Vaswani et al.: Attention is All you Need.
// (2017)". So a title, a title with its first author's name, or a few title words
// all match. QLever's per-word score is a match count, the same for every title
// that holds the word once, so it ranks nothing: the order is the title's length,
// shortest first, which puts the title the words were taken from ahead of the
// longer titles that merely contain them, and then the newest year.
//
// The records are chosen and ordered in a subquery before anything else is read,
// so the joins for venue, DOI and authors touch only the records returned. The
// authors come from the signatures, each with its ordinal, because rdfs:label
// names only the first and dblp:authoredBy carries no order. A record with no
// signature at all is not returned.
func dblpSPARQL(words []string, limit int, years YearRange) string {
	yearPattern := "OPTIONAL { ?publ dblp:yearOfPublication ?year }"
	if !years.IsZero() {
		from, to := years.Bounds()
		yearPattern = "?publ dblp:yearOfPublication ?year .\n      FILTER(?year >= \"" + strconv.Itoa(from) + "\"^^xsd:gYear && ?year <= \"" +
			strconv.Itoa(to) + "\"^^xsd:gYear)"
	}
	return `PREFIX dblp: <https://dblp.org/rdf/schema#>
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>
PREFIX ql: <http://qlever.cs.uni-freiburg.de/builtin-functions/>
SELECT ?title ?year (GROUP_CONCAT(DISTINCT ?venue; separator="\t") AS ?venues) (SAMPLE(?doiIRI) AS ?doi) (GROUP_CONCAT(DISTINCT CONCAT(STR(?ordinal), " ", ?name); separator="\t") AS ?authors) WHERE {
  {
    SELECT ?publ ?title ?year WHERE {
      ?text ql:contains-word "` + strings.Join(words, " ") + `" .
      ?text ql:contains-entity ?label .
      ?publ rdfs:label ?label .
      ?publ dblp:title ?title .
      ` + yearPattern + `
    } ORDER BY STRLEN(?title) DESC(?year) LIMIT ` + strconv.Itoa(limit) + `
  }
  OPTIONAL { ?publ dblp:publishedIn ?venue }
  OPTIONAL { ?publ dblp:doi ?doiIRI }
  ?publ dblp:hasSignature ?signature .
  ?signature dblp:signatureOrdinal ?ordinal .
  ?signature dblp:signatureDblpName ?name .
} GROUP BY ?publ ?title ?year ORDER BY STRLEN(?title) DESC(?year)`
}

// clampDblpLimit maps a caller-supplied limit onto the accepted range,
// substituting the default for a non-positive value and clamping the rest.
func clampDblpLimit(limit int) int {
	switch {
	case limit <= 0:
		return dblpDefaultLimit
	case limit > dblpMaxLimit:
		return dblpMaxLimit
	default:
		return limit
	}
}

// dblpResults is the subset of a SPARQL JSON results document the provider reads:
// one binding per record, each variable holding its value as a string. A variable
// the record has no value for is absent from its binding.
type dblpResults struct {
	Results struct {
		Bindings []map[string]dblpTerm `json:"bindings"`
	} `json:"results"`
}

// dblpTerm is one bound value. Its type and datatype are not read: a year is a
// gYear literal and a DOI an IRI, and both are taken from their text.
type dblpTerm struct {
	Value string `json:"value"`
}

// parseDblpBindings decodes a SPARQL JSON results document into at most limit
// DiscoveryResults, in the order the service returned them, returning nil when the
// body cannot be decoded (best-effort — a malformed response is treated as no
// results, not an error). A binding with no title is skipped.
func parseDblpBindings(body []byte, limit int) []DiscoveryResult {
	var doc dblpResults
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	results := make([]DiscoveryResult, 0, min(len(doc.Results.Bindings), limit))
	for _, b := range doc.Results.Bindings {
		if len(results) == limit {
			break
		}
		if r := dblpBindingToResult(b); r.Title != "" {
			results = append(results, r)
		}
	}
	return results
}

// dblpBindingToResult maps one binding onto a DiscoveryResult: the title, the
// authors in signature order, the year, the venue as dblp abbreviates it, and the
// DOI.
//
// OpenAccess stays false and PDFURL stays empty on purpose. dblp records the
// existence of a paper, not its availability: the electronic-edition link it also
// carries is usually a publisher landing page behind a paywall, so surfacing it as a
// PDF URL would hand the caller something it cannot fetch. A DOI is the one
// actionable key here, and download's own source chain decides whether it can be
// served.
func dblpBindingToResult(b map[string]dblpTerm) DiscoveryResult {
	return DiscoveryResult{
		Origin:  "dblp",
		Title:   dblpText(b["title"].Value),
		Authors: dblpAuthorNames(b["authors"].Value),
		Year:    strings.TrimSpace(b["year"].Value),
		Venue:   dblpVenue(b["venues"].Value),
		DOI:     dblpDOI(b["doi"].Value),
	}
}

// dblpAuthor is one signature as the query folds it: its ordinal and the name.
type dblpAuthor struct {
	ordinal int
	name    string
}

// dblpUnorderedAuthor is the ordinal given to a signature whose own cannot be read,
// so it sorts after every numbered one.
const dblpUnorderedAuthor = 1 << 30

// dblpAuthorNames orders the folded signatures by ordinal and joins their names
// through joinHitAuthors. GROUP_CONCAT promises no order, which is why each name
// travels with its ordinal. An entry whose ordinal cannot be read sorts last, in
// the order it came.
func dblpAuthorNames(folded string) string {
	var authors []dblpAuthor
	for entry := range strings.SplitSeq(folded, dblpListSep) {
		ordinal, name, _ := strings.Cut(strings.TrimSpace(entry), " ")
		n, err := strconv.Atoi(ordinal)
		if err != nil {
			n, name = dblpUnorderedAuthor, entry
		}
		if name = dblpPersonName(name); name != "" {
			authors = append(authors, dblpAuthor{ordinal: n, name: name})
		}
	}
	slices.SortStableFunc(authors, func(a, b dblpAuthor) int { return a.ordinal - b.ordinal })
	names := make([]string, len(authors))
	for i, a := range authors {
		names[i] = a.name
	}
	return joinHitAuthors(names)
}

// dblpPersonName normalizes one name and drops the homonym number dblp appends to
// tell people of the same name apart ("Xiangyu Zhang 0005"): it is a key into
// dblp's person pages, not part of anybody's name.
func dblpPersonName(s string) string {
	fields := strings.Fields(html.UnescapeString(s))
	if n := len(fields); n > 1 && isFourDigits(fields[n-1]) {
		fields = fields[:n-1]
	}
	return strings.Join(fields, " ")
}

// dblpVenue renders the folded venues, joining several with "; ".
func dblpVenue(folded string) string {
	var parts []string
	for v := range strings.SplitSeq(folded, dblpListSep) {
		if s := dblpText(v); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

// dblpDOI reads a DOI out of the IRI dblp writes it as
// ("https://doi.org/10.1109/CVPR.2016.90"), returning "" for anything that is not
// one.
func dblpDOI(iri string) string {
	iri = strings.TrimSpace(iri)
	// The resolver host, with or without "dx." and whatever the scheme, is cut
	// at its path: the knowledge graph spells it https://doi.org/ today, and its
	// owl:sameAs links still use the older forms.
	if _, rest, ok := strings.Cut(iri, "doi.org/"); ok {
		iri = rest
	}
	if !strings.HasPrefix(iri, "10.") || !strings.Contains(iri, "/") {
		return ""
	}
	return iri
}

// dblpText normalizes one dblp text value: XML entities are unescaped and whitespace
// is collapsed. The knowledge graph carries plain literals, but dblp's records are
// XML at the source, and an entity left in one would otherwise be shown to the
// caller as literal markup.
func dblpText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}
