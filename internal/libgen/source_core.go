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
// appends /search/works to it. It is a field on coreSource so tests can point the
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
// with a Bearer token, and returns the work's downloadUrl. The key is sent only to
// the CORE API host and never travels with the returned file URL. It distinguishes
// a rejected key, a DOI CORE does not hold, and a DOI held without a downloadable
// full text. MD5 verification is disabled: DOI-keyed items carry no LibGen digest.
type coreSource struct {
	// http is the client used for lookup requests; when nil, http.DefaultClient is
	// used.
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

// Resolve looks the DOI up on CORE and returns the work's direct download URL.
// A missing key, a rejected key, a DOI CORE does not hold, and a DOI held without
// a downloadable full text each yield a distinct error so the caller tries the
// next source.
func (s coreSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	if strings.TrimSpace(s.key) == "" {
		return Resolved{}, errors.New("core: no API key (set LIBGEN_MCP_CORE_KEY)")
	}
	base := s.apiBase
	if base == "" {
		base = coreAPIBase
	}
	// The DOI is wrapped in quotes so the query matches the exact identifier; the
	// key rides only in the Authorization header, never in the URL.
	endpoint := strings.TrimRight(base, "/") + "/search/works?q=" +
		url.QueryEscape(`doi:"`+it.DOI+`"`) + "&limit=1"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return Resolved{}, fmt.Errorf("core: building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+s.key)

	httpClient := s.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Resolved{}, fmt.Errorf("core: requesting %q: %w", it.DOI, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Resolved{}, fmt.Errorf("core: API key rejected (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return Resolved{}, fmt.Errorf("core: %q returned HTTP %d", it.DOI, resp.StatusCode)
	}

	var rec coreResponse
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, coreMaxBody)).Decode(&rec); decErr != nil {
		return Resolved{}, fmt.Errorf("core: decoding response for %q: %w", it.DOI, decErr)
	}
	if len(rec.Results) == 0 {
		return Resolved{}, fmt.Errorf("core: %q is not in CORE", it.DOI)
	}
	if rec.Results[0].DownloadURL == "" {
		return Resolved{}, fmt.Errorf("core: %q has no downloadable open-access full text", it.DOI)
	}
	return Resolved{FileURL: rec.Results[0].DownloadURL, VerifyMD5: false, Ext: "pdf"}, nil
}
