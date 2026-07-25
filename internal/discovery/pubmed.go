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

// pubmedBase is the NCBI E-utilities root. It is a package variable (not a constant)
// so tests can point it at a local httptest server.
var pubmedBase = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"

// PubMed limit bounds: esearch is asked for at least one and at most this many PMIDs,
// defaulting to pubmedDefaultLimit when the caller passes a non-positive value. The
// ceiling also bounds the second hop, since every PMID returned is summarized in one
// request.
const (
	pubmedMaxLimit     = 50
	pubmedDefaultLimit = 10
)

// pubmedRPS is the request rate NCBI allows a caller without an API key: three per
// second, enforced on their side rather than merely requested. The limiter's burst
// equals the rate so a single Search — which spends two requests, esearch then
// esummary — completes without waiting while still never exceeding the allowance.
// An API key would raise this figure; the provider stays keyless and lives at 3.
const pubmedRPS = 3

// pubmedTool is the value NCBI's etiquette asks callers to send as the tool
// parameter, so traffic can be attributed to the application generating it.
const pubmedTool = "libgen-mcp"

// pubmedDOIType is the articleids idtype marking the DOI among the several
// identifiers PubMed lists for a record (pubmed, pmc, pmcid, pii, doi).
const pubmedDOIType = "doi"

// pubmedYearDigits is the length of the four-digit year that opens a well-formed
// PubMed pubdate.
const pubmedYearDigits = 4

// PubMedProvider is a keyless discovery source backed by NCBI's PubMed index. It
// covers the whole biomedical literature rather than the downloadable open-access
// slice, so it surfaces records for discovery and citation even when no free full
// text exists — which is exactly what the europepmc-style OA sources cannot do. Its
// results are therefore bibliographic only: no PDF URL, never marked open access. Its
// limiter and http.Client are self-contained, so it never shares state with libgen's
// client.
type PubMedProvider struct {
	client  *http.Client
	limiter *rate.Limiter
	email   string // optional contact NCBI asks for; empty omits the parameter
}

// NewPubMed constructs a PubMedProvider with its own http.Client and a rate limiter
// pacing requests to NCBI's keyless allowance of three per second. The email is the
// optional contact address NCBI asks callers to identify themselves with (the same
// contact the Crossref polite pool uses); pass "" to omit it — the provider never
// invents one.
func NewPubMed(email string) *PubMedProvider {
	return &PubMedProvider{
		client:  newDiscoveryClient(),
		limiter: rate.NewLimiter(rate.Limit(pubmedRPS), pubmedRPS),
		email:   strings.TrimSpace(email),
	}
}

// Name reports the origin label this provider stamps on its results.
func (p *PubMedProvider) Name() string { return "pubmed" }

// Search queries PubMed for the given free-text query and returns up to limit
// bibliographic results. It takes two hops — esearch for the matching PMIDs, then
// esummary for their records — both inside the single per-call timeout budget, and the
// second is skipped entirely when the first finds nothing.
//
// It is best-effort: a non-200 status or any non-context failure on either hop
// degrades to an empty result with no error, so a failing provider never sinks a
// federated search. Only a context cancellation or deadline propagates as an error.
func (p *PubMedProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	searchBody, err := p.get(ctx, p.esearchURL(query, limit))
	if err != nil || searchBody == nil {
		return nil, err
	}
	ids := parsePubMedIDs(searchBody)
	if len(ids) == 0 {
		return nil, nil
	}

	summaryBody, err := p.get(ctx, p.esummaryURL(ids))
	if err != nil || summaryBody == nil {
		return nil, err
	}
	return parsePubMedSummaries(summaryBody), nil
}

// get performs one paced, bounded GET against an E-utilities endpoint. It returns a
// nil body (with a nil error) when the hop degraded — a transport failure or a
// non-200 — so the caller stops without treating it as a hard failure. A context
// cancellation or deadline is returned as an error instead, since that is the caller
// going away rather than the source misbehaving.
func (p *PubMedProvider) get(ctx context.Context, rawURL string) ([]byte, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, ctx.Err()
	}
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
	return body, nil
}

// esearchURL assembles the first-hop request: the free-text query on term, JSON
// output, the clamped hit ceiling on retmax, and relevance ordering (esearch defaults
// to newest-first, which for a topical query buries the standard references under
// last week's papers).
func (p *PubMedProvider) esearchURL(query string, limit int) string {
	params := p.commonParams()
	params.Set("term", query)
	params.Set("retmax", strconv.Itoa(clampPubMedLimit(limit)))
	params.Set("sort", "relevance")
	return pubmedBase + "/esearch.fcgi?" + params.Encode()
}

// esummaryURL assembles the second-hop request: every PMID from the first hop in one
// comma-separated id parameter, so a page of results costs a single round-trip.
func (p *PubMedProvider) esummaryURL(ids []string) string {
	params := p.commonParams()
	params.Set("id", strings.Join(ids, ","))
	return pubmedBase + "/esummary.fcgi?" + params.Encode()
}

// commonParams builds the parameters both hops share: the pubmed database, JSON
// output, the tool attribution NCBI asks for, and the contact email when one is
// configured.
func (p *PubMedProvider) commonParams() url.Values {
	params := url.Values{}
	params.Set("db", "pubmed")
	params.Set("retmode", "json")
	params.Set("tool", pubmedTool)
	if p.email != "" {
		params.Set("email", p.email)
	}
	return params
}

// clampPubMedLimit maps a caller-supplied limit onto the accepted range, substituting
// the default for a non-positive value and clamping the rest.
func clampPubMedLimit(limit int) int {
	switch {
	case limit <= 0:
		return pubmedDefaultLimit
	case limit > pubmedMaxLimit:
		return pubmedMaxLimit
	default:
		return limit
	}
}

// pubmedSearchEnvelope is the subset of the esearch response the provider reads: the
// list of matching PMIDs, in the order esearch ranked them.
type pubmedSearchEnvelope struct {
	ESearchResult struct {
		IDList []string `json:"idlist"`
	} `json:"esearchresult"`
}

// parsePubMedIDs decodes the PMIDs from an esearch response, returning nil when the
// body cannot be decoded (best-effort — a malformed response is treated as no
// matches, not an error) and skipping any blank entry.
func parsePubMedIDs(body []byte) []string {
	var env pubmedSearchEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	ids := make([]string, 0, len(env.ESearchResult.IDList))
	for _, id := range env.ESearchResult.IDList {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			ids = append(ids, trimmed)
		}
	}
	return ids
}

// pubmedSummaryEnvelope is the esummary response shape: a result object keyed by
// PMID, plus a uids array giving the order those keys should be read in. The records
// stay raw so one undecodable entry cannot sink the rest.
type pubmedSummaryEnvelope struct {
	Result map[string]json.RawMessage `json:"result"`
}

// pubmedRecord is the subset of one esummary record the provider reads.
type pubmedRecord struct {
	Title           string            `json:"title"`
	Authors         []pubmedAuthor    `json:"authors"`
	PubDate         string            `json:"pubdate"`
	FullJournalName string            `json:"fulljournalname"`
	ArticleIDs      []pubmedArticleID `json:"articleids"`
}

// pubmedAuthor is one author entry; PubMed already formats the name as "Family II",
// so it is used as given.
type pubmedAuthor struct {
	Name string `json:"name"`
}

// pubmedArticleID is one of the identifiers PubMed lists for a record, of which only
// the DOI is read.
type pubmedArticleID struct {
	IDType string `json:"idtype"`
	Value  string `json:"value"`
}

// parsePubMedSummaries decodes an esummary response into DiscoveryResults in the order
// its uids array lists them, returning nil when the body cannot be decoded
// (best-effort — a malformed response is treated as no results, not an error).
//
// A record that decodes to no title is skipped: that is the shape NCBI returns for a
// PMID it cannot summarize, and it carries nothing a caller could act on.
func parsePubMedSummaries(body []byte) []DiscoveryResult {
	var env pubmedSummaryEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	uids := pubmedUIDs(env.Result)
	results := make([]DiscoveryResult, 0, len(uids))
	for _, uid := range uids {
		raw, ok := env.Result[uid]
		if !ok {
			continue
		}
		var rec pubmedRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		if strings.TrimSpace(rec.Title) == "" {
			continue
		}
		results = append(results, pubmedRecordToResult(rec))
	}
	return results
}

// pubmedUIDs reads the uids array out of an esummary result object, which fixes the
// order the per-PMID records are read in (esummary echoes the requested order, so this
// preserves esearch's relevance ranking). A result object without a readable uids array
// yields no order and therefore no records.
func pubmedUIDs(result map[string]json.RawMessage) []string {
	raw, ok := result["uids"]
	if !ok {
		return nil
	}
	var uids []string
	if err := json.Unmarshal(raw, &uids); err != nil {
		return nil
	}
	return uids
}

// pubmedRecordToResult maps one esummary record onto a DiscoveryResult: the title,
// "; "-joined author names, the publication year, the full journal name as the venue,
// and the DOI when the record lists one.
//
// OpenAccess stays false and PDFURL stays empty on purpose. PubMed indexes the
// literature without stating whether any given article is free to read — that is
// precisely why it is worth consulting alongside the open-access sources — so
// claiming otherwise would be a guess. The DOI is the actionable key, and download's
// own source chain decides whether it can be served.
func pubmedRecordToResult(rec pubmedRecord) DiscoveryResult {
	return DiscoveryResult{
		Origin:  "pubmed",
		Title:   strings.Join(strings.Fields(rec.Title), " "),
		Authors: pubmedAuthorNames(rec.Authors),
		Year:    pubmedYear(rec.PubDate),
		Venue:   strings.Join(strings.Fields(rec.FullJournalName), " "),
		DOI:     pubmedDOI(rec.ArticleIDs),
	}
}

// pubmedAuthorNames joins author names with "; ", skipping empty entries.
func pubmedAuthorNames(authors []pubmedAuthor) string {
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		if name := strings.TrimSpace(a.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "; ")
}

// pubmedDOI returns the record's DOI, or "" when its identifier list has none — a
// routine case for older literature, which predates DOIs entirely.
func pubmedDOI(ids []pubmedArticleID) string {
	for _, id := range ids {
		if id.IDType == pubmedDOIType {
			if doi := strings.TrimSpace(id.Value); doi != "" {
				return doi
			}
		}
	}
	return ""
}

// pubmedYear extracts the publication year from a PubMed pubdate, which spells dates
// several ways ("2019 Sep 1", "1975 Jun", "2023 Winter", "2024"). It reads the leading
// token and accepts it only when it is exactly four digits, returning "" rather than
// guessing at anything else.
func pubmedYear(pubdate string) string {
	fields := strings.Fields(pubdate)
	if len(fields) == 0 {
		return ""
	}
	year := fields[0]
	if len(year) != pubmedYearDigits {
		return ""
	}
	for _, r := range year {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return year
}
