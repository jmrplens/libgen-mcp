package libgen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// europePMCSearchBase is the default Europe PMC REST search endpoint used to map a
// DOI to a PMCID and read its open-access/full-text flags. It is a field on
// europePMCSource so tests can point the source at an httptest server.
const europePMCSearchBase = "https://www.ebi.ac.uk/europepmc/webservices/rest/search"

// europePMCRenderBase is the default host serving the article PDF. The PMC render
// path (/backend/ptpmcrender.fcgi) and the article render path
// (/articles/<pmcid>?pdf=render) both live under it.
const europePMCRenderBase = "https://europepmc.org"

// europePMCMaxBody bounds how many bytes of a Europe PMC search JSON response are
// read, guarding against an unexpectedly large or hostile body.
const europePMCMaxBody = 1 << 20 // 1 MiB

// europePMCSource resolves a DOI to a freely downloadable PDF through Europe PMC
// (https://europepmc.org), which mirrors the open-access subset of PubMed Central.
//
// It first maps the DOI to a PMCID via the REST search API and inspects the
// record's open-access and in-EPMC flags; only a DOI that Europe PMC both indexes
// AND holds the full text for yields a PDF. The PDF is then taken from the PMC
// render backend, with the article render path as a fallback when the backend is
// unreachable — both are official Europe PMC endpoints serving the same bytes.
//
// It is keyless, and distinguishes a DOI Europe PMC does not index from one it
// indexes without an open-access full text, so the chain log explains which
// happened. MD5 verification is disabled: DOI-keyed items carry no LibGen digest.
type europePMCSource struct {
	// http is the client used for the search and PDF-probe requests; when nil,
	// http.DefaultClient is used.
	http *http.Client
	// searchBase overrides the REST search endpoint (defaults to
	// europePMCSearchBase); tests set it to a local httptest server.
	searchBase string
	// renderBase overrides the PDF render host (defaults to europePMCRenderBase);
	// tests set it to a local httptest server.
	renderBase string
}

// Compile-time assertion that europePMCSource satisfies the DownloadSource contract.
var _ DownloadSource = europePMCSource{}

// europePMCResponse is the subset of the Europe PMC search response consulted here.
type europePMCResponse struct {
	HitCount   int `json:"hitCount"`
	ResultList struct {
		Result []europePMCResult `json:"result"`
	} `json:"resultList"`
}

// europePMCResult is the subset of a single Europe PMC search result consulted
// here: the PMCID plus the flags that report whether the full text is available.
type europePMCResult struct {
	PMCID        string `json:"pmcid"`
	IsOpenAccess string `json:"isOpenAccess"`
	InEPMC       string `json:"inEPMC"`
	HasPDF       string `json:"hasPDF"`
}

// Name identifies the Europe PMC source.
func (s europePMCSource) Name() string { return "europepmc" }

// Supports reports that Europe PMC can resolve any DOI-keyed item.
func (s europePMCSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve maps the item's DOI to a PMCID via Europe PMC's search API and, when the
// article's full text is held open-access, returns a render URL for its PDF. A DOI
// Europe PMC does not index, or indexes without an open-access full text, yields a
// distinct error so the caller tries the next source.
//
// All three conditions are required: a PMCID to address the record, inEPMC=Y so the
// full text is actually held here, and isOpenAccess=Y so serving it is within this
// source's open-access-only remit. Europe PMC holds plenty of full text it may not
// redistribute freely, so the OA flag is a license check, not a redundant one.
func (s europePMCSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	rec, err := s.lookup(ctx, it.DOI)
	if err != nil {
		return Resolved{}, err
	}
	if rec.PMCID == "" || !strings.EqualFold(rec.InEPMC, "Y") || !strings.EqualFold(rec.IsOpenAccess, "Y") {
		return Resolved{}, notIndexed(fmt.Errorf("europepmc: %q is indexed but has no open-access full text", it.DOI))
	}
	fileURL, err := s.pdfURL(ctx, rec.PMCID)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{FileURL: fileURL, VerifyMD5: false, Ext: "pdf"}, nil
}

// lookup queries the search API for the DOI and returns the first result. It
// returns an error when the DOI is not indexed (zero hits) or the request fails,
// keeping that case distinct from an indexed-but-not-open-access one.
func (s europePMCSource) lookup(ctx context.Context, doi string) (europePMCResult, error) {
	base := s.searchBase
	if base == "" {
		base = europePMCSearchBase
	}
	// The DOI is wrapped in quotes so the query matches the exact identifier rather
	// than a free-text token; url.Values encodes the quotes and the DOI's slashes.
	q := url.Values{
		"query":    {`DOI:"` + doi + `"`},
		"format":   {"json"},
		"pageSize": {"1"},
	}
	// Trim a trailing separator as well as a stray "?": the query is appended with
	// its own "?", and a base ending in "/" would otherwise address a different path.
	endpoint := strings.TrimRight(base, "/?") + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return europePMCResult{}, fmt.Errorf("europepmc: building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := s.client().Do(req)
	if err != nil {
		return europePMCResult{}, unavailable(fmt.Errorf("europepmc: requesting %q: %w", doi, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return europePMCResult{}, unavailableStatus(resp.StatusCode, fmt.Errorf("europepmc: %q returned HTTP %d", doi, resp.StatusCode))
	}

	var rec europePMCResponse
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, europePMCMaxBody)).Decode(&rec); decErr != nil {
		return europePMCResult{}, fmt.Errorf("europepmc: decoding response for %q: %w", doi, decErr)
	}
	if rec.HitCount == 0 || len(rec.ResultList.Result) == 0 {
		return europePMCResult{}, notIndexed(fmt.Errorf("europepmc: %q is not indexed", doi))
	}
	return rec.ResultList.Result[0], nil
}

// pdfURL returns the first render endpoint that actually serves a PDF for the
// PMCID: the PMC render backend, then the article render path as a fallback. It
// probes because the backend can be unreachable from a given network while the
// article path stays up (both are official Europe PMC endpoints).
func (s europePMCSource) pdfURL(ctx context.Context, pmcid string) (string, error) {
	base := s.renderBase
	if base == "" {
		base = europePMCRenderBase
	}
	base = strings.TrimRight(base, "/")
	candidates := []string{
		base + "/backend/ptpmcrender.fcgi?accid=" + url.QueryEscape(pmcid) + "&blobtype=pdf",
		base + "/articles/" + url.PathEscape(pmcid) + "?pdf=render",
	}
	for _, c := range candidates {
		if probePDF(ctx, s.client(), c) {
			return c, nil
		}
	}
	// Both candidates are official Europe PMC hosts, so neither answering is the
	// service being unreachable rather than a statement about this article.
	return "", unavailable(fmt.Errorf("europepmc: no reachable PDF endpoint for %s", pmcid))
}

// client returns the configured HTTP client, or the shared default when none was
// set.
func (s europePMCSource) client() *http.Client { return httpClientOr(s.http) }
