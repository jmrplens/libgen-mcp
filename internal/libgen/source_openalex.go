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

// openAlexAPIBase is the default OpenAlex works endpoint used to look up the
// open-access locations of a DOI. It is stored as a field on openalexSource so
// tests can point the source at an httptest server instead of the live API.
const openAlexAPIBase = "https://api.openalex.org/works"

// openAlexMaxBody bounds how many bytes of an OpenAlex JSON response are read,
// guarding against an unexpectedly large or hostile body.
const openAlexMaxBody = 1 << 20 // 1 MiB

// openAlexSelect trims the work record to the three fields consulted here. The
// full record carries authorships, topics and citation lists that would dwarf the
// part actually read, and OpenAlex honors the projection server-side.
const openAlexSelect = "open_access,best_oa_location,locations"

// openalexSource resolves a DOI to a freely downloadable PDF using OpenAlex
// (https://openalex.org), the open catalog OurResearch built on the same
// open-access index that backs Unpaywall.
//
// It is keyless by design, and that is the whole reason it is in the chain: the
// single-entity lookup it uses (works/doi:<doi>) is free and unmetered — it costs
// zero credits with or without an API key, where every search or filter endpoint
// is billed — so it needs no credential, no contact address, and no configuration.
// unpaywallSource covers the same index but refuses to run without a contact
// email, so on a default keyless deployment this is the only prefix-agnostic
// open-access resolver in the chain. It sits immediately after Unpaywall so a
// deployment that did configure an email keeps that source's precedence.
//
// Only a direct PDF link is served. An OA record whose locations expose no
// pdf_url is reported as a clean miss rather than falling back to a landing page:
// measured against the records that reach that branch, the landing URL is a DOI
// resolver or a repository splash page that answers HTML, which the pipeline would
// otherwise store as a .pdf. MD5 verification is disabled because DOI-keyed items
// carry no LibGen digest.
type openalexSource struct {
	// http is the client used for API lookups; when nil, http.DefaultClient is used.
	http *http.Client
	// baseURL overrides the API endpoint (defaults to openAlexAPIBase); tests set it
	// to a local httptest server.
	baseURL string
}

// Compile-time assertion that openalexSource satisfies the DownloadSource contract.
var _ DownloadSource = openalexSource{}

// openAlexWork is the subset of an OpenAlex work record consulted here: the OA
// status, the catalog's own best location, and the full location list scanned
// when the best one carries no direct PDF link.
type openAlexWork struct {
	OpenAccess     openAlexOAStatus   `json:"open_access"`
	BestOALocation *openAlexLocation  `json:"best_oa_location"`
	Locations      []openAlexLocation `json:"locations"`
}

// openAlexOAStatus is the open-access summary of a work, read only to tell a
// paywalled DOI apart from an OA one that exposes no fetchable file.
type openAlexOAStatus struct {
	IsOA bool `json:"is_oa"`
}

// openAlexLocation is the subset of one OpenAlex location read here: whether the
// location is open access, its direct PDF link, and the version used to prefer a
// published copy over a repository preprint.
type openAlexLocation struct {
	IsOA    bool   `json:"is_oa"`
	PDFURL  string `json:"pdf_url"`
	Version string `json:"version"`
}

// isPublished reports whether this location is the published copy, the version
// preferred when several open-access locations expose a PDF.
func (loc openAlexLocation) isPublished() bool { return loc.Version == "publishedVersion" }

// pdfURL picks a directly-downloadable PDF from the record: OpenAlex's own best
// location first, then a published open-access location, then any open-access
// location exposing a pdf_url. Every candidate is checked with absoluteHTTPURL, so
// a null, relative or non-http value in the catalog can never reach the download
// pipeline. It returns "" when no location offers a usable PDF link.
func (w openAlexWork) pdfURL() string {
	if w.BestOALocation != nil && absoluteHTTPURL(w.BestOALocation.PDFURL) {
		return w.BestOALocation.PDFURL
	}
	var fallback string
	for i := range w.Locations {
		loc := w.Locations[i]
		if !loc.IsOA || !absoluteHTTPURL(loc.PDFURL) {
			continue
		}
		if loc.isPublished() {
			return loc.PDFURL
		}
		if fallback == "" {
			fallback = loc.PDFURL
		}
	}
	return fallback
}

// Name identifies the OpenAlex source.
func (s openalexSource) Name() string { return "openalex" }

// Supports reports that OpenAlex can resolve any DOI-keyed item.
func (s openalexSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve looks the item's DOI up in OpenAlex and, when the work is open access
// with a direct PDF link, returns that link. A paywalled work, an OA work with no
// PDF location, or any API/transport error yields an error so the caller tries the
// next source.
//
// The two negative outcomes stay distinct — "not open access" versus "no
// open-access PDF" — because they are different facts about the DOI: the first
// says no free copy exists, the second that one exists somewhere OpenAlex cannot
// hand us a file for.
func (s openalexSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	rec, err := s.lookup(ctx, it.DOI)
	if err != nil {
		return Resolved{}, err
	}
	fileURL := rec.pdfURL()
	if fileURL == "" {
		if !rec.OpenAccess.IsOA {
			return Resolved{}, notIndexed(fmt.Errorf("openalex: %q is not open access", it.DOI))
		}
		return Resolved{}, notIndexed(fmt.Errorf("openalex: no open-access PDF for %q", it.DOI))
	}
	return Resolved{FileURL: fileURL, VerifyMD5: false, Ext: "pdf"}, nil
}

// lookup fetches the work record for a DOI from the single-entity endpoint.
//
// It issues the request itself rather than going through jsonFetch because a 404
// here is a settled answer — OpenAlex replying that it has no record of this DOI —
// and must be tagged as a clean miss so the chain skips the start-retry schedule
// and leaves the source out of cooldown. jsonFetch classifies with
// unavailableStatus, which reads a 404 as neither.
func (s openalexSource) lookup(ctx context.Context, doi string) (openAlexWork, error) {
	base := s.baseURL
	if base == "" {
		base = openAlexAPIBase
	}
	// The DOI keeps its raw slashes: the single-entity route is keyed by the
	// unescaped identifier (works/doi:<doi>), so escapeDOIPath percent-encodes any
	// other URL-unsafe character a DOI may carry while leaving the separators alone.
	endpoint := strings.TrimRight(base, "/") + "/doi:" + escapeDOIPath(doi) +
		"?" + url.Values{"select": {openAlexSelect}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return openAlexWork{}, fmt.Errorf("openalex: building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Accept", "application/json")

	resp, err := httpClientOr(s.http).Do(req)
	if err != nil {
		return openAlexWork{}, unavailable(fmt.Errorf("openalex: requesting %q: %w", doi, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return openAlexWork{}, missOrUnavailableStatus(resp.StatusCode,
			fmt.Errorf("openalex: %q returned HTTP %d", doi, resp.StatusCode))
	}

	var rec openAlexWork
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, openAlexMaxBody)).Decode(&rec); decErr != nil {
		return openAlexWork{}, fmt.Errorf("openalex: decoding response for %q: %w", doi, decErr)
	}
	return rec, nil
}
