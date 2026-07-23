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

// unpaywallAPIBase is the default Unpaywall REST endpoint used to look up the
// open-access status of a DOI. It is stored as a field on unpaywallSource so
// tests can point the source at an httptest server instead of the live API.
const unpaywallAPIBase = "https://api.unpaywall.org/v2"

// unpaywallMaxBody bounds how many bytes of an Unpaywall JSON response are read,
// guarding against an unexpectedly large or hostile body.
const unpaywallMaxBody = 1 << 20 // 1 MiB

// unpaywallSource resolves a DOI to a freely downloadable PDF using the Unpaywall
// API (https://unpaywall.org). It serves only open-access articles: when a DOI is
// not OA or exposes no PDF link, Resolve returns an error so the download chain
// advances to the next source.
type unpaywallSource struct {
	// email is the required Unpaywall contact address sent as the email query
	// parameter on every request.
	email string
	// http is the client used for API lookups; when nil, http.DefaultClient is used.
	http *http.Client
	// baseURL overrides the API endpoint (defaults to unpaywallAPIBase); tests set
	// it to a local httptest server.
	baseURL string
}

// unpaywallResponse is the subset of the Unpaywall v2 record consulted here.
type unpaywallResponse struct {
	IsOA           bool `json:"is_oa"`
	BestOALocation *struct {
		URLForPDF string `json:"url_for_pdf"`
	} `json:"best_oa_location"`
}

// Name identifies the Unpaywall source.
func (s unpaywallSource) Name() string { return "unpaywall" }

// Supports reports that Unpaywall can resolve any DOI-keyed item.
func (s unpaywallSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve looks up the item's DOI on Unpaywall and, when the article is
// open-access with a PDF link, returns that link. MD5 verification is disabled
// because DOI-keyed items carry no LibGen digest. A non-OA article, a missing PDF
// link, or any API/transport error yields an error so the caller tries the next
// source.
func (s unpaywallSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	// A per-call email (supplied on demand) overrides the configured one for this
	// item only. With neither present, fail before touching the API so the chain
	// falls through gracefully instead of sending Unpaywall an emailless request.
	email := s.email
	if it.Email != "" {
		email = it.Email
	}
	if email == "" {
		return Resolved{}, errors.New("unpaywall: no contact email (set LIBGEN_MCP_UNPAYWALL_EMAIL or supply one)")
	}
	base := s.baseURL
	if base == "" {
		base = unpaywallAPIBase
	}
	// The DOI's slashes stay literal in the path: the Unpaywall v2 API keys records
	// by the unescaped DOI (its documented shape is /v2/<doi>), so its slashes must
	// not be percent-encoded. Encoding "/" as %2F was verified against the live API
	// to still return 200 today, but the raw form is the documented, canonical one.
	// escapeDOIPath keeps the slashes but percent-encodes any other URL-unsafe
	// characters a DOI may carry (e.g. '#', '?', space) so they cannot corrupt the
	// request.
	endpoint := fmt.Sprintf("%s/%s?email=%s",
		strings.TrimRight(base, "/"),
		escapeDOIPath(it.DOI),
		url.QueryEscape(email),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return Resolved{}, fmt.Errorf("unpaywall: building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	httpClient := s.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Resolved{}, fmt.Errorf("unpaywall: requesting %q: %w", it.DOI, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Resolved{}, fmt.Errorf("unpaywall: %q returned HTTP %d", it.DOI, resp.StatusCode)
	}

	var rec unpaywallResponse
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, unpaywallMaxBody)).Decode(&rec); decErr != nil {
		return Resolved{}, fmt.Errorf("unpaywall: decoding response for %q: %w", it.DOI, decErr)
	}

	if !rec.IsOA || rec.BestOALocation == nil || rec.BestOALocation.URLForPDF == "" {
		return Resolved{}, fmt.Errorf("unpaywall: no open-access PDF for %q", it.DOI)
	}
	return Resolved{
		FileURL:   rec.BestOALocation.URLForPDF,
		VerifyMD5: false,
		Ext:       "pdf",
	}, nil
}
