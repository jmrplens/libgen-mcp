package libgen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/time/rate"
)

// crossrefMaxBody bounds how many bytes of a Crossref work record are read while
// resolving a download. The single-work route supports no field projection, so the
// whole record arrives — references and abstracts included — and only the link
// array is wanted.
const crossrefMaxBody = 4 << 20 // 4 MiB

// crossrefPDFContentType is the Crossref link content-type marking a full-text PDF.
const crossrefPDFContentType = "application/pdf"

// crossrefMaxCandidates bounds how many advertised links are probed for one DOI.
// Publishers advertise the same file two or three times under different
// intended-application tags, and a record listing more than this is offering
// variants rather than alternatives — probing them all would spend the item's
// budget on near-duplicates of a link that has already refused us.
const crossrefMaxCandidates = 4

// crossrefDownloadApplications are the link intended-application values preferred
// when a work advertises several PDF links: the publisher's generic, reader-facing
// link is tried before the machine-audience ones.
//
// It is a probe ORDER, never a filter. The same tag was once used across the
// codebase as a proxy for "an anonymous client can fetch this", and measuring the
// live API against 20 registrants showed it is not one: every unspecified and
// syndication link sampled answered 403, while eLife and the smaller open-access
// publishers tag their reader-facing file text-mining and serve it to anyone. Since
// no tag predicts the outcome, every advertised link is a candidate and the probe
// decides.
var crossrefDownloadApplications = map[string]bool{
	"":            true,
	"unspecified": true,
}

// crossrefSource resolves a DOI to a full-text PDF using the link metadata the
// PUBLISHER deposits with Crossref (https://api.crossref.org). It is keyless, and
// it closes a gap the other DOI sources leave open: search surfaces a Crossref hit
// with the publisher's own pdf_url, and until this source existed nothing in the
// download chain ever tried that link — read and download went through the
// open-access indexes instead, so the two paths could reach opposite conclusions
// about the same article and the link the server had just published was reachable
// only by a caller with its own HTTP tool.
//
// Every candidate is confirmed with a ranged probe before it is returned. That is
// not optional here as it is elsewhere in the chain: a Crossref link is a
// syndication contract, not a promise of access, and the major publishers answer an
// anonymous client with a 403 or a challenge page. Without the probe the pipeline
// would happily store that page as a .pdf.
//
// It is the last of the article-specific resolvers, after the open-access indexes and
// the archives: when a freely licensed copy exists somewhere, that copy is preferred,
// but a file the publisher itself serves openly should always be tried before falling
// back to a shadow library. Only the two book sources and then the shadow libraries
// follow it. MD5 verification is disabled because DOI-keyed items carry no LibGen
// digest.
type crossrefSource struct {
	// http is the client used for the API lookup and the candidate probes; when
	// nil, http.DefaultClient is used.
	http *http.Client
	// limiter paces the API lookups. It is the Client's enrichment limiter, shared
	// on purpose: enrichment and this source query the same api.crossref.org host
	// from the same process, and a budget only one of them observes is not a budget.
	// Nil disables pacing, which is what a directly constructed source (a test) gets.
	limiter *rate.Limiter
	// email is the optional contact address advertised to Crossref's polite pool
	// through the User-Agent mailto, matching what enrichment sends. Empty omits it.
	email string
	// baseURL overrides the API root (defaults to the package-level crossrefBase);
	// tests set it to a local httptest server.
	baseURL string
}

// Compile-time assertion that crossrefSource satisfies the DownloadSource contract.
var _ DownloadSource = crossrefSource{}

// crossrefDownloadEnvelope is the `{"message":{...}}` wrapper of a Crossref work
// response, narrowed to the part the download path reads.
type crossrefDownloadEnvelope struct {
	Message crossrefDownloadWork `json:"message"`
}

// crossrefDownloadWork is the subset of a Crossref work record consulted here: the
// full-text links the publisher deposited.
type crossrefDownloadWork struct {
	Link []crossrefDownloadLink `json:"link"`
}

// crossrefDownloadLink is one deposited full-text link: where it points, what it
// serves, and the audience the publisher declared for it.
type crossrefDownloadLink struct {
	URL                 string `json:"URL"`
	ContentType         string `json:"content-type"`
	IntendedApplication string `json:"intended-application"`
}

// Name identifies the Crossref source.
func (s crossrefSource) Name() string { return "crossref" }

// Supports reports that Crossref can resolve any DOI-keyed item.
func (s crossrefSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve looks the item's DOI up in Crossref and returns the first advertised PDF
// link that actually serves a PDF to an anonymous client. A DOI with no deposited
// PDF link, and one whose every link is refused, are both reported as clean misses
// so the chain moves on without putting the source in cooldown — neither says
// anything about Crossref's health.
//
// The two misses stay distinct because they are different facts: the publisher
// deposited no full-text link at all, versus it deposited links it will not serve
// to us. The second is the one a caller needs spelled out, because the article may
// well be readable in a browser.
func (s crossrefSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	work, err := s.lookup(ctx, it.DOI)
	if err != nil {
		return Resolved{}, err
	}
	candidates := crossrefPDFCandidates(work.Link)
	if len(candidates) == 0 {
		return Resolved{}, notIndexed(fmt.Errorf("crossref: the publisher deposited no full-text PDF link for %q", it.DOI))
	}
	for _, candidate := range candidates {
		if probePDF(ctx, s.http, candidate) {
			return Resolved{FileURL: candidate, VerifyMD5: false, Ext: "pdf"}, nil
		}
	}
	return Resolved{}, notIndexed(fmt.Errorf(
		"crossref: the publisher's PDF link for %q is not served to automated clients (tried %d, first was %s); it may still open in a browser",
		it.DOI, len(candidates), candidates[0]))
}

// crossrefPDFCandidates returns the deposited PDF links worth probing, in probe
// order: reader-facing links first (see crossrefDownloadApplications), then the
// rest in deposit order. Duplicate URLs are collapsed — publishers commonly deposit
// one file under several intended-application tags, and probing it twice would only
// spend the budget twice — and every candidate passes absoluteHTTPURL, so a
// relative or non-http value in the record can never reach the download pipeline.
// At most crossrefMaxCandidates are returned.
func crossrefPDFCandidates(links []crossrefDownloadLink) []string {
	// Every deposit of a URL is inspected before the URL is placed, because a
	// publisher may deposit one file twice and name the reader-facing audience on
	// the SECOND entry. Classifying on first sight would file that URL under the
	// machine-audience bucket on the strength of a deposit that a later one
	// supersedes, costing it its place at the front of the probe order and, once the
	// cap bites, possibly its place altogether.
	var order []string
	reader := map[string]bool{}
	for _, l := range links {
		if !strings.EqualFold(l.ContentType, crossrefPDFContentType) || !absoluteHTTPURL(l.URL) {
			continue
		}
		if _, ok := reader[l.URL]; !ok {
			order = append(order, l.URL)
		}
		reader[l.URL] = reader[l.URL] || crossrefDownloadApplications[l.IntendedApplication]
	}
	var preferred, others []string
	for _, u := range order {
		if reader[u] {
			preferred = append(preferred, u)
			continue
		}
		others = append(others, u)
	}
	candidates := make([]string, 0, len(preferred)+len(others))
	candidates = append(candidates, preferred...)
	candidates = append(candidates, others...)
	if len(candidates) > crossrefMaxCandidates {
		candidates = candidates[:crossrefMaxCandidates]
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// lookup fetches the work record for a DOI from Crossref's single-work route.
//
// It issues the request itself rather than going through the enrichment client
// because a 404 here is a settled answer — Crossref has no record of this DOI — and
// must be tagged as a clean miss so the chain skips the start-retry schedule and
// leaves the source out of cooldown.
func (s crossrefSource) lookup(ctx context.Context, doi string) (crossrefDownloadWork, error) {
	base := s.baseURL
	if base == "" {
		base = crossrefBase
	}
	// The DOI keeps its raw slashes: Crossref routes on the unescaped identifier
	// (works/<doi>), so escapeDOIPath percent-encodes any other URL-unsafe character
	// a DOI may carry while leaving the separators alone. The route supports no
	// field projection, so no select parameter is sent — one was rejected outright
	// by the live API with "this route does not support select".
	endpoint := strings.TrimRight(base, "/") + "/works/" + escapeDOIPath(doi)

	if s.limiter != nil {
		if werr := s.limiter.Wait(ctx); werr != nil {
			return crossrefDownloadWork{}, fmt.Errorf("crossref: waiting to query for %q: %w", doi, werr)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return crossrefDownloadWork{}, fmt.Errorf("crossref: building request: %w", err)
	}
	ua := userAgent()
	if s.email != "" {
		ua += " (mailto:" + s.email + ")"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClientOr(s.http).Do(req)
	if err != nil {
		return crossrefDownloadWork{}, unavailable(fmt.Errorf("crossref: requesting %q: %w", doi, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return crossrefDownloadWork{}, missOrUnavailableStatus(resp.StatusCode,
			fmt.Errorf("crossref: %q returned HTTP %d", doi, resp.StatusCode))
	}

	var env crossrefDownloadEnvelope
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, crossrefMaxBody)).Decode(&env); decErr != nil {
		return crossrefDownloadWork{}, fmt.Errorf("crossref: decoding response for %q: %w", doi, decErr)
	}
	return env.Message, nil
}
