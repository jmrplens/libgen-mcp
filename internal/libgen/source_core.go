package libgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// coreAPIBase is the default CORE (https://core.ac.uk) v3 API root. The source
// appends /search/works/ to it. It is a field on coreSource so tests can point the
// source at an httptest server.
const coreAPIBase = "https://api.core.ac.uk/v3"

// coreMaxBody bounds how many bytes of a CORE search response are read, guarding
// against an unexpectedly large or hostile body.
const coreMaxBody = 1 << 20 // 1 MiB

// coreSource resolves a DOI to an open-access PDF through CORE (core.ac.uk), an
// aggregator of open-access research papers.
//
// It is opt-in: a free registered API key (LIBGEN_MCP_CORE_KEY) is required, and
// without one the source is left out of the chain entirely (see
// config.SourceEnabled). It looks the DOI up via the v3 search API, authenticating
// with a Bearer token, and returns the work's downloadUrl. CORE's download URLs are
// frequently stale (they redirect to a fileserver that 404s), so the chosen URL is
// liveness-probed and returned only when it actually serves a PDF; otherwise the
// source reports no downloadable full text so the chain falls through at resolve
// time instead of wasting a download attempt on a dead URL.
//
// The key is sent only to the CORE API host and never travels with the returned
// (or probed) file URL. It distinguishes a rejected key, a DOI CORE does not hold,
// and a DOI held without a live downloadable full text. MD5 verification is
// disabled: DOI-keyed items carry no LibGen digest.
type coreSource struct {
	// http is the client used for lookup and probe requests; when nil,
	// http.DefaultClient is used.
	http *http.Client
	// key is the CORE API key sent as a Bearer token; an empty key makes Resolve
	// fail fast (the source should not have been in the chain without one).
	key string
	// apiBase overrides the API root (defaults to coreAPIBase); tests set it to a
	// local httptest server.
	apiBase string
}

// Compile-time assertion that coreSource satisfies the DownloadSource contract.
var _ DownloadSource = coreSource{}

// coreResponse is the subset of a CORE search response consulted here.
type coreResponse struct {
	Results []coreWork `json:"results"`
}

// coreWork is the subset of one CORE work record consulted here: the direct
// download URL for its full text (empty when CORE holds no downloadable copy).
type coreWork struct {
	DownloadURL string `json:"downloadUrl"`
}

// Name identifies the CORE source.
func (s coreSource) Name() string { return "core" }

// Supports reports that CORE can resolve any DOI-keyed item.
func (s coreSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve looks the DOI up on CORE and returns the work's download URL once it is
// confirmed to serve a live PDF. A missing key, a rejected key, a DOI CORE does not
// hold, and a DOI held without a live downloadable full text (no URL, or a URL that
// no longer serves a PDF) each yield a distinct error so the caller tries the next
// source.
func (s coreSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	if strings.TrimSpace(s.key) == "" {
		return Resolved{}, errors.New("core: no API key (set LIBGEN_MCP_CORE_KEY)")
	}
	downloadURL, err := s.lookupDownloadURL(ctx, it.DOI)
	if err != nil {
		return Resolved{}, err
	}
	// CORE's downloadUrl is often stale. Probe it before committing so a dead URL
	// falls through here rather than costing a wasted download attempt. The probe
	// carries no Authorization, so the key stays on the API host, not the file URL.
	if !probePDF(ctx, s.client(), downloadURL) {
		return Resolved{}, fmt.Errorf("core: %q has no downloadable open-access full text", it.DOI)
	}
	return Resolved{FileURL: downloadURL, VerifyMD5: false, Ext: "pdf"}, nil
}

// lookupDownloadURL queries the CORE search API for the DOI and returns the first
// result's download URL. It reports a rejected key, a DOI CORE does not hold, and a
// DOI held with no download URL as distinct errors.
func (s coreSource) lookupDownloadURL(ctx context.Context, doi string) (string, error) {
	base := s.apiBase
	if base == "" {
		base = coreAPIBase
	}
	// The trailing slash on /search/works/ is requested directly: without it CORE
	// answers HTTP 301 to the slash form, a wasted redirect hop. The DOI is wrapped
	// in quotes so the query matches the exact identifier; the key rides only in the
	// Authorization header, never in the URL.
	endpoint := strings.TrimRight(base, "/") + "/search/works/?q=" +
		url.QueryEscape(`doi:"`+doi+`"`) + "&limit=1"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("core: building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+s.key)

	resp, err := s.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("core: requesting %q: %w", doi, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("core: API key rejected (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("core: %q returned HTTP %d", doi, resp.StatusCode)
	}

	var rec coreResponse
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, coreMaxBody)).Decode(&rec); decErr != nil {
		return "", fmt.Errorf("core: decoding response for %q: %w", doi, decErr)
	}
	if len(rec.Results) == 0 {
		return "", fmt.Errorf("core: %q is not in CORE", doi)
	}
	if rec.Results[0].DownloadURL == "" {
		return "", fmt.Errorf("core: %q has no downloadable open-access full text", doi)
	}
	return rec.Results[0].DownloadURL, nil
}

// client returns the configured HTTP client, or the shared default when none was
// set.
func (s coreSource) client() *http.Client { return httpClientOr(s.http) }
