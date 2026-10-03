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

// europePMCBase is the Europe PMC REST API root. It is a package variable (not a
// constant) so tests can point it at a local httptest server.
var europePMCBase = "https://www.ebi.ac.uk/europepmc/webservices/rest"

// Europe PMC limit bounds: the API accepts up to 1000 per page, and this provider
// asks for at most europePMCMaxLimit, defaulting to europePMCDefaultLimit when the
// caller passes a non-positive value.
const (
	europePMCMaxLimit     = 50
	europePMCDefaultLimit = 10
)

// EuropePMCProvider is a keyless discovery source backed by the Europe PMC search
// endpoint: EMBL-EBI's index of PubMed, PubMed Central, preprint servers and
// Agricola, with the open-access full text of the PMC subset it may redistribute.
// Its limiter and http.Client are its own.
type EuropePMCProvider struct {
	client  *http.Client
	limiter *rate.Limiter
}

// NewEuropePMC constructs a EuropePMCProvider with its own http.Client and a
// limiter of one request a second, burst 2. Europe PMC publishes no rate limit for
// its REST API, so this is the pace the other index providers keep.
func NewEuropePMC() *EuropePMCProvider {
	return &EuropePMCProvider{
		client:  newDiscoveryClient(),
		limiter: rate.NewLimiter(rate.Every(time.Second), 2),
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *EuropePMCProvider) Name() string { return "europepmc" }

// Search queries Europe PMC for the given free-text query and returns up to limit
// results. It is best-effort: a non-200 status or any non-context failure degrades
// to an empty result with no error. Only a context cancellation or deadline
// propagates.
func (p *EuropePMCProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	return p.SearchYears(ctx, query, limit, YearRange{})
}

// SearchYears is Search bounded to the publication years in years, which Europe
// PMC takes as a PUB_YEAR range clause ANDed to the query. Its contract is
// Search's.
func (p *EuropePMCProvider) SearchYears(ctx context.Context, query string, limit int, years YearRange) ([]DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	if err := p.limiter.Wait(ctx); err != nil {
		return nil, ctx.Err()
	}
	status, body, err := boundedGet(ctx, p.client, europePMCSearchURL(query, limit, years))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, nil
	}
	return parseEuropePMC(body), nil
}

// europePMCSearchURL assembles the search request. resultType=lite is the
// projection that still carries the three availability flags, the PMCID and the
// DOI, which is everything mapped here.
func europePMCSearchURL(query string, limit int, years YearRange) string {
	params := url.Values{}
	if !years.IsZero() {
		from, to := years.Bounds()
		query = "(" + query + ") AND PUB_YEAR:[" + strconv.Itoa(from) + " TO " + strconv.Itoa(to) + "]"
	}
	params.Set("query", query)
	params.Set("format", "json")
	params.Set("resultType", "lite")
	params.Set("pageSize", strconv.Itoa(clampEuropePMCLimit(limit)))
	return strings.TrimRight(europePMCBase, "/") + "/search?" + params.Encode()
}

// clampEuropePMCLimit maps a caller-supplied limit onto the accepted range,
// substituting the default for a non-positive value and clamping the rest.
func clampEuropePMCLimit(limit int) int {
	switch {
	case limit <= 0:
		return europePMCDefaultLimit
	case limit > europePMCMaxLimit:
		return europePMCMaxLimit
	default:
		return limit
	}
}

// europePMCEnvelope and europePMCRecord are the subset of a lite search response
// read here.
type europePMCEnvelope struct {
	ResultList struct {
		Result []europePMCRecord `json:"result"`
	} `json:"resultList"`
}

// europePMCRecord is one lite search hit. The three availability flags are "Y" or
// "N" strings.
type europePMCRecord struct {
	PMCID        string `json:"pmcid"`
	DOI          string `json:"doi"`
	Title        string `json:"title"`
	AuthorString string `json:"authorString"`
	JournalTitle string `json:"journalTitle"`
	PubYear      string `json:"pubYear"`
	IsOpenAccess string `json:"isOpenAccess"`
	InEPMC       string `json:"inEPMC"`
}

// parseEuropePMC decodes a lite search response into DiscoveryResults, returning
// nothing when the body cannot be decoded.
func parseEuropePMC(body []byte) []DiscoveryResult {
	var env europePMCEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	results := make([]DiscoveryResult, 0, len(env.ResultList.Result))
	for i := range env.ResultList.Result {
		if r, ok := europePMCRecordToResult(&env.ResultList.Result[i]); ok {
			results = append(results, r)
		}
	}
	return results
}

// europePMCRecordToResult maps one hit onto a DiscoveryResult, dropping a hit with
// neither a title nor a DOI.
//
// OpenAccess is set by the rule the download chain's europepmc source applies
// before it serves a file: a PMCID, inEPMC=Y and isOpenAccess=Y. The three flags
// mean different things, measured against the live API on 2026-10-03. inEPMC says
// Europe PMC holds the full text, isOpenAccess that it may redistribute it, and the
// two part often: of the CRISPR papers Europe PMC holds, about 37,000 are held
// under an author-manuscript or free-to-read arrangement without being in the
// open-access subset, and the REST full-text endpoint refuses every one of them
// with a 500. hasPDF adds nothing the other two do not.
//
// FullTextURL is that REST endpoint's JATS XML for an open-access hit, the full
// text as Europe PMC serves it to software. The article's PDF render on
// europepmc.org is not offered: on 2026-10-03 both render paths answered an
// automated client with a 403 challenge page, while the REST endpoint answered.
func europePMCRecordToResult(rec *europePMCRecord) (DiscoveryResult, bool) {
	r := DiscoveryResult{
		Origin:  "europepmc",
		Title:   strings.TrimSuffix(strings.Join(strings.Fields(rec.Title), " "), "."),
		Authors: europePMCAuthors(rec.AuthorString),
		Year:    strings.TrimSpace(rec.PubYear),
		DOI:     strings.TrimSpace(rec.DOI),
		Venue:   strings.Join(strings.Fields(rec.JournalTitle), " "),
	}
	pmcid := strings.TrimSpace(rec.PMCID)
	if pmcid != "" && rec.InEPMC == "Y" && rec.IsOpenAccess == "Y" {
		r.OpenAccess = true
		r.FullTextURL = strings.TrimRight(europePMCBase, "/") + "/" + url.PathEscape(pmcid) + "/fullTextXML"
	}
	return r, r.Title != "" || r.DOI != ""
}

// europePMCAuthors turns Europe PMC's authorString ("Blaha L, Bosinger M, Mueller
// C.") into the "; "-joined form every provider uses. The comma is the separator
// because each personal name is written surname first with initials and no comma of
// its own. A corporate author whose name holds a comma is split in two, which is
// accepted: the structured authorList that would keep it whole comes only with
// resultType=core, which also returns every abstract, grant and MeSH list, many
// times the payload for a field a search hit only uses to identify the record.
func europePMCAuthors(authorString string) string {
	s := strings.TrimSuffix(strings.TrimSpace(authorString), ".")
	if s == "" {
		return ""
	}
	return joinHitAuthors(strings.Split(s, ","))
}
