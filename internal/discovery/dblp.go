package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// dblpBase is the dblp computer science bibliography root. It is a package variable
// (not a constant) so tests can point it at a local httptest server.
var dblpBase = "https://dblp.org"

// dblp limit bounds: the search API is asked for at least one and at most this many
// hits, defaulting to dblpDefaultLimit when the caller passes a non-positive value.
const (
	dblpMaxLimit     = 50
	dblpDefaultLimit = 10
)

// dblpRate is the courtesy delay this provider paces itself to. dblp publishes no
// rate limit for its search API, so the figure is ours: one request per second,
// matching what the other keyless providers extend to services that ask for nothing.
const dblpRate = time.Second

// dblpRefusals remembers a dblp host that answered with a bot check or a rate
// limit, so the searches after it ask nothing until challengeCooldown has passed.
//
// Since 2026-10 dblp fronts its search API with Anubis, which answers an
// automated client with a 200 "Making sure you're not a bot!" HTML page in place
// of the JSON, and sometimes with a 429. Measured on 2026-10-03 on dblp.org,
// dblp.uni-trier.de and dblp.dagstuhl.de alike, from a consumer ISP address. The
// page wants a browser to solve a proof of work, which this server does not do,
// so the only useful response is to stop asking for a while and say so once.
var dblpRefusals refusalWindows

// DBLPProvider is a keyless discovery source backed by the dblp computer science
// bibliography search API. It contributes precise CS bibliographic data — the venue,
// year and full author list that arXiv and Crossref match poorly for conference
// papers — never full text: dblp is an index, so its results carry no PDF URL and
// are never marked open access. Its http.Client is its own, so it never shares
// state with libgen's client, and its pacing is process-wide (see pacers).
type DBLPProvider struct {
	client *http.Client
	pace   pace
}

// NewDBLP constructs a DBLPProvider with its own http.Client, paced to one request
// per second across every search this process runs (burst 1, so the first request
// goes through immediately and only back-to-back requests wait).
func NewDBLP() *DBLPProvider {
	return &DBLPProvider{
		client: newDiscoveryClient(),
		pace:   pace{limit: rate.Every(dblpRate), burst: 1},
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *DBLPProvider) Name() string { return "dblp" }

// Search queries the dblp publication search API for the given free-text query and
// returns up to limit bibliographic results. It is best-effort: a non-200 status or
// any non-context failure degrades to an empty result with no error, so a failing
// provider never sinks a federated search. Only a context cancellation or deadline
// propagates as an error.
//
// A bot check or a rate limit is also an empty result, and additionally opens a
// refusal window (see dblpRefusals) during which no request is made at all.
func (p *DBLPProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	base := dblpBase
	if dblpRefusals.quiet(base, time.Now()) {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if err := p.pace.wait(ctx, p.Name(), base); err != nil {
		return nil, ctx.Err()
	}

	status, body, err := boundedGet(ctx, p.client, dblpSearchURL(query, limit))
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
			slog.Info("dblp search: the search API answered with a bot check or a rate limit instead of JSON, asking nothing for "+
				challengeCooldown.String(), "status", status)
		}
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, nil
	}
	return parseDblpHits(body), nil
}

// dblpRefused reports whether a response is dblp declining to answer rather than
// answering: a 403 (Anubis's deny verdict), a 429 (dblp's documented rate limit),
// a 503 (what a bot wall or an overloaded front end answers), or a 200 whose body
// is not a JSON object. The search API is asked for format=json, so a 200 opening
// with anything but "{" (after whitespace and a UTF-8 byte order mark) is a page
// put in front of it (the Anubis bot check observed since 2026-10), and parsing
// it would only report "no results" for a search nobody ran.
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

// dblpSearchURL assembles the publication-search request URL: the free-text query on
// q, the JSON representation, and the hit count on h so the response stays bounded
// (dblp otherwise returns 30 hits regardless of what the caller can use).
func dblpSearchURL(query string, limit int) string {
	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("h", strconv.Itoa(clampDblpLimit(limit)))
	return dblpBase + "/search/publ/api?" + params.Encode()
}

// clampDblpLimit maps a caller-supplied limit onto dblp's accepted range,
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

// dblpEnvelope, dblpHits, dblpHit and dblpInfo are the subset of the dblp search
// response the provider reads. A query with no matches omits the hit array entirely,
// which decodes to an empty slice.
type dblpEnvelope struct {
	Result struct {
		Hits dblpHits `json:"hits"`
	} `json:"result"`
}

type dblpHits struct {
	Hit []dblpHit `json:"hit"`
}

type dblpHit struct {
	Info dblpInfo `json:"info"`
}

type dblpInfo struct {
	Title   string            `json:"title"`
	Authors dblpAuthorsHolder `json:"authors"`
	Year    string            `json:"year"`
	Venue   dblpStringOrArray `json:"venue"`
	DOI     string            `json:"doi"`
}

// dblpAuthorsHolder wraps the author list, which dblp nests one level deeper than the
// rest of info.
type dblpAuthorsHolder struct {
	Author dblpAuthorList `json:"author"`
}

// dblpAuthor is one author entry; only the display name is read.
type dblpAuthor struct {
	Text string `json:"text"`
}

// dblpAuthorList is a list of authors that tolerates dblp's two encodings: an array
// for a multi-author record, and a bare object for a single-author one.
type dblpAuthorList []dblpAuthor

// UnmarshalJSON decodes either an array of author objects or a single bare author
// object into the list, so a one-author record does not fail the whole response with
// a JSON type error.
func (l *dblpAuthorList) UnmarshalJSON(data []byte) error {
	var many []dblpAuthor
	if err := json.Unmarshal(data, &many); err == nil {
		*l = many
		return nil
	}
	var one dblpAuthor
	if err := json.Unmarshal(data, &one); err != nil {
		return err
	}
	*l = dblpAuthorList{one}
	return nil
}

// dblpStringOrArray is a dblp field that is a single JSON string for most records but
// an array of strings for a few — venue, for a record dblp files under more than one.
type dblpStringOrArray []string

// UnmarshalJSON decodes either a single string or an array of strings, so the array
// form does not fail the whole response with a JSON type error.
func (s *dblpStringOrArray) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*s = dblpStringOrArray{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// parseDblpHits decodes a dblp search envelope into DiscoveryResults, returning nil
// when the body cannot be decoded (best-effort — a malformed response is treated as
// no results, not an error).
func parseDblpHits(body []byte) []DiscoveryResult {
	var env dblpEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	hits := env.Result.Hits.Hit
	results := make([]DiscoveryResult, 0, len(hits))
	for _, h := range hits {
		results = append(results, dblpInfoToResult(h.Info))
	}
	return results
}

// dblpInfoToResult maps one dblp hit onto a DiscoveryResult: the title, "; "-joined
// author names, the year, the venue as dblp abbreviates it, and the DOI.
//
// OpenAccess stays false and PDFURL stays empty on purpose. dblp records the
// existence of a paper, not its availability: the "ee" electronic-edition link it
// also carries is usually a publisher landing page behind a paywall, so surfacing it
// as a PDF URL would hand the caller something it cannot fetch. A DOI is the one
// actionable key here, and download's own source chain decides whether it can be
// served.
func dblpInfoToResult(info dblpInfo) DiscoveryResult {
	return DiscoveryResult{
		Origin:  "dblp",
		Title:   dblpText(info.Title),
		Authors: dblpAuthorNames(info.Authors.Author),
		Year:    strings.TrimSpace(info.Year),
		Venue:   dblpVenue(info.Venue),
		DOI:     strings.TrimSpace(info.DOI),
	}
}

// dblpAuthorNames joins author display names with "; ", skipping empty entries.
func dblpAuthorNames(authors []dblpAuthor) string {
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		if name := dblpText(a.Text); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "; ")
}

// dblpVenue renders the venue field, joining the multi-venue form with "; ".
func dblpVenue(venue dblpStringOrArray) string {
	parts := make([]string, 0, len(venue))
	for _, v := range venue {
		if s := dblpText(v); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

// dblpText normalizes one dblp text value: XML entities are unescaped and whitespace
// is collapsed. dblp serves its XML records as JSON without undoing the escaping, so
// a title reaches us as "You Don&apos;t Need" and would otherwise be shown to the
// caller as literal markup.
func dblpText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}
