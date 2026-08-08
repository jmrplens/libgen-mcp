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

// unpaywallResponse is the subset of the Unpaywall v2 record consulted here: the OA
// flag, Unpaywall's own best location, and the full list of OA locations scanned
// when the best one carries no direct PDF link.
type unpaywallResponse struct {
	IsOA           bool                `json:"is_oa"`
	BestOALocation *unpaywallLocation  `json:"best_oa_location"`
	OALocations    []unpaywallLocation `json:"oa_locations"`
}

// unpaywallLocation is the subset of one Unpaywall OA location read here: a direct
// PDF link, a landing URL, and the host_type/version used to prefer a
// published/publisher copy over a repository preprint.
type unpaywallLocation struct {
	URLForPDF string `json:"url_for_pdf"`
	URL       string `json:"url"`
	HostType  string `json:"host_type"`
	Version   string `json:"version"`
}

// isPublished reports whether this location is the published/publisher copy, the
// version preferred when several OA locations expose a PDF.
func (loc unpaywallLocation) isPublished() bool {
	return loc.Version == "publishedVersion" || loc.HostType == "publisher"
}

// bestPDFURL picks a directly-downloadable PDF from the record: Unpaywall's own best
// location first, then a published/publisher OA location, then any OA location that
// exposes a url_for_pdf. It returns "" when no location offers a PDF link.
func (rec unpaywallResponse) bestPDFURL() string {
	if rec.BestOALocation != nil && rec.BestOALocation.URLForPDF != "" {
		return rec.BestOALocation.URLForPDF
	}
	var fallback string
	for i := range rec.OALocations {
		loc := rec.OALocations[i]
		if loc.URLForPDF == "" {
			continue
		}
		if loc.isPublished() {
			return loc.URLForPDF
		}
		if fallback == "" {
			fallback = loc.URLForPDF
		}
	}
	return fallback
}

// allLocations lists every OA location in preference order: the record's own best
// location first, then the rest as Unpaywall ordered them.
func (rec unpaywallResponse) allLocations() []unpaywallLocation {
	locs := make([]unpaywallLocation, 0, len(rec.OALocations)+1)
	if rec.BestOALocation != nil {
		locs = append(locs, *rec.BestOALocation)
	}
	return append(locs, rec.OALocations...)
}

// directFileURL is the last-resort pick when NO location advertises a
// url_for_pdf: a location whose plain url still looks like a file rather than a
// landing page, preferring a published/publisher copy. It returns "" when every
// location only offers a landing page.
//
// This replaces an earlier fallback that simply took the best location's url. That
// url is a landing page far more often than not — for 10.1371/journal.pone.0000308
// Unpaywall answers with four locations, none carrying a url_for_pdf, whose best
// url is the bare https://doi.org/… resolver. Fetching it costs a full redirect
// chain (measured: 3 redirects, 1.1 s, 170 kB) only to hand the download layer an
// HTML article page it correctly rejects. Declining here instead turns that into a
// clean miss that costs one API call, and the chain reaches a source that can
// actually serve the article sooner.
func (rec unpaywallResponse) directFileURL() string {
	var fallback string
	for _, loc := range rec.allLocations() {
		if !looksLikeFileURL(loc.URL) {
			continue
		}
		if loc.isPublished() {
			return loc.URL
		}
		if fallback == "" {
			fallback = loc.URL
		}
	}
	return fallback
}

// looksLikeFileURL reports whether raw is plausibly a direct file rather than a
// landing page: its path ends in a known document extension, or its last segment
// is "pdf" (the shape publishers use for /article/<id>/pdf). Anything else — a
// doi.org resolver, a DOAJ or repository record page — is a page, not a file.
func looksLikeFileURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	path := strings.ToLower(strings.TrimRight(u.Path, "/"))
	if path == "" {
		return false
	}
	for _, ext := range []string{".pdf", ".epub", ".djvu"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return strings.HasSuffix(path, "/pdf")
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
	req.Header.Set("User-Agent", userAgent())

	httpClient := httpClientOr(s.http)
	resp, err := httpClient.Do(req)
	if err != nil {
		return Resolved{}, unavailable(fmt.Errorf("unpaywall: requesting %q: %w", it.DOI, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Resolved{}, missOrUnavailableStatus(resp.StatusCode, fmt.Errorf("unpaywall: %q returned HTTP %d", it.DOI, resp.StatusCode))
	}

	var rec unpaywallResponse
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, unpaywallMaxBody)).Decode(&rec); decErr != nil {
		return Resolved{}, fmt.Errorf("unpaywall: decoding response for %q: %w", it.DOI, decErr)
	}

	// Two distinct diagnoses: a paywalled DOI ("not open access") is a different
	// outcome from an OA DOI Unpaywall simply exposes no fetchable location for
	// ("no open-access PDF"). Keeping them separate lets the chain and any log tell
	// the two apart.
	if !rec.IsOA {
		return Resolved{}, notIndexed(fmt.Errorf("unpaywall: %q is not open access", it.DOI))
	}
	fileURL := rec.bestPDFURL()
	if fileURL == "" {
		fileURL = rec.directFileURL()
	}
	if fileURL == "" {
		return Resolved{}, notIndexed(fmt.Errorf("unpaywall: %q is open access but Unpaywall lists no direct file for it, only landing pages", it.DOI))
	}
	// The URL is returned with the scheme Unpaywall recorded, http included. An
	// unconditional http→https promotion was tried and dropped: it rescues nothing
	// (10.1016/j.cell.2011.02.013 resolves to http://www.cell.com/…/pdf, and that
	// host answers 403 to a non-browser client under BOTH schemes — the plain-http
	// form simply redirects to the https one first), while it would break the
	// http-only institutional repositories that Unpaywall still indexes.
	return Resolved{
		FileURL:   fileURL,
		VerifyMD5: false,
		Ext:       "pdf",
	}, nil
}
