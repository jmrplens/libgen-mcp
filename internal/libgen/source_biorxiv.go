package libgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// biorxivAPIBase is the default bioRxiv/medRxiv details API root. The source
// appends /<server>/<doi>/na/json to it. It is a field on biorxivSource so tests
// can point the source at an httptest server.
const biorxivAPIBase = "https://api.biorxiv.org/details"

// biorxivMaxBody bounds how many bytes of a details JSON response are read,
// guarding against an unexpectedly large or hostile body.
const biorxivMaxBody = 1 << 20 // 1 MiB

// biorxivDOIPrefix is the DOI registrant prefix Cold Spring Harbor assigns to
// bioRxiv and medRxiv preprints; the source claims only DOIs under it.
const biorxivDOIPrefix = "10.1101/"

// biorxivServers lists the two preprint servers the details API covers, tried in
// order: a 10.1101 DOI belongs to exactly one of them and the other simply misses.
var biorxivServers = []string{"biorxiv", "medrxiv"}

// biorxivSource resolves a bioRxiv or medRxiv preprint DOI to its full-text PDF.
//
// It confirms the preprint exists (and learns its latest version) through the
// details API, trying the bioRxiv server first and medRxiv second, then builds the
// versioned .full.pdf URL on the matching content host. Preprints are openly
// licensed, so this is a legal open-access source. It claims only DOIs under the
// 10.1101 prefix, so non-preprint DOIs fall straight through to the next source.
// MD5 verification is disabled: DOI-keyed items carry no LibGen digest.
type biorxivSource struct {
	// http is the client used for details API requests; when nil, http.DefaultClient
	// is used.
	http *http.Client
	// apiBase overrides the details API root (defaults to biorxivAPIBase); tests set
	// it to a local httptest server.
	apiBase string
	// contentBase overrides the PDF content host (defaults to the per-server
	// production hosts); tests set it to a local httptest server.
	contentBase string
}

// Compile-time assertion that biorxivSource satisfies the DownloadSource contract.
var _ DownloadSource = biorxivSource{}

// biorxivResponse is the subset of the details API response consulted here: the
// collection of per-version records for the requested DOI (empty on a miss).
type biorxivResponse struct {
	Collection []biorxivEntry `json:"collection"`
}

// biorxivEntry is the subset of one details record consulted here.
type biorxivEntry struct {
	DOI     string `json:"doi"`
	Version string `json:"version"`
	Server  string `json:"server"`
}

// Name identifies the bioRxiv source.
func (s biorxivSource) Name() string { return "biorxiv" }

// Supports reports that bioRxiv can resolve a DOI-keyed item whose DOI carries the
// 10.1101 preprint prefix, and only such items.
func (s biorxivSource) Supports(it Item) bool {
	return it.DOI != "" && strings.HasPrefix(it.DOI, biorxivDOIPrefix)
}

// errBiorxivNoRecord marks the one outcome that proves a server does not carry a
// DOI: it answered normally and its collection held no record. It is distinguished
// from transport, HTTP and decode failures so Resolve never reports "not found"
// on the strength of a server that merely broke.
var errBiorxivNoRecord = errors.New("no record")

// Resolve confirms the preprint on bioRxiv then medRxiv and returns its latest
// version's full-text PDF URL on the matching content host.
//
// The two failure modes are kept apart. Only when BOTH servers explicitly answered
// "no record" is the DOI definitively absent, and only then is that claimed. If
// either lookup failed for any other reason (transport, non-200, malformed body),
// that failure is surfaced instead: a broken service is an actionable, retryable
// condition, whereas "not found on bioRxiv or medRxiv" tells the caller the
// preprint does not exist — a claim a failed request cannot support.
func (s biorxivSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	var failure error // a lookup that broke, as opposed to one that answered "no"
	misses := 0
	for _, server := range biorxivServers {
		version, err := s.lookupVersion(ctx, server, it.DOI)
		switch {
		case err == nil:
			return Resolved{
				FileURL:   s.pdfURL(server, it.DOI, version),
				VerifyMD5: false,
				Ext:       "pdf",
			}, nil
		case errors.Is(err, errBiorxivNoRecord):
			misses++
		case failure == nil:
			failure = err // keep the first real failure; a later miss must not mask it
		}
	}
	if misses == len(biorxivServers) {
		return Resolved{}, fmt.Errorf("biorxiv: %q not found on bioRxiv or medRxiv", it.DOI)
	}
	return Resolved{}, fmt.Errorf("biorxiv: could not confirm %q: %w", it.DOI, failure)
}

// lookupVersion queries one server's details API for the DOI and returns its
// latest version string. It errors when the server does not carry the DOI or the
// request fails, so Resolve can try the other server.
func (s biorxivSource) lookupVersion(ctx context.Context, server, doi string) (string, error) {
	base := s.apiBase
	if base == "" {
		base = biorxivAPIBase
	}
	// The DOI's slashes stay literal in the path (the details API keys by the
	// unescaped DOI); escapeDOIPath encodes only other URL-unsafe characters.
	endpoint := strings.TrimRight(base, "/") + "/" + server + "/" + escapeDOIPath(doi) + "/na/json"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("biorxiv: building request for %s: %w", server, err)
	}
	req.Header.Set("User-Agent", userAgent)

	httpClient := s.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("biorxiv: requesting %s for %q: %w", server, doi, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("biorxiv: %s returned HTTP %d for %q", server, resp.StatusCode, doi)
	}

	var rec biorxivResponse
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, biorxivMaxBody)).Decode(&rec); decErr != nil {
		return "", fmt.Errorf("biorxiv: decoding %s response for %q: %w", server, doi, decErr)
	}
	version, ok := latestBiorxivVersion(rec.Collection)
	if !ok {
		// The server answered normally and carries nothing for this DOI: the one
		// outcome that is evidence of absence rather than of a failure.
		return "", fmt.Errorf("biorxiv: %s has %w for %q", server, errBiorxivNoRecord, doi)
	}
	return version, nil
}

// pdfURL builds the versioned full-text PDF URL for a DOI on the given server's
// content host, e.g. https://www.biorxiv.org/content/<doi>v<version>.full.pdf.
func (s biorxivSource) pdfURL(server, doi, version string) string {
	return s.contentHost(server) + "/content/" + escapeDOIPath(doi) + "v" + version + ".full.pdf"
}

// contentHost returns the PDF content host for a server: the configured override
// when set (tests), else the production host for bioRxiv or medRxiv.
func (s biorxivSource) contentHost(server string) string {
	if s.contentBase != "" {
		return strings.TrimRight(s.contentBase, "/")
	}
	if server == "medrxiv" {
		return "https://www.medrxiv.org"
	}
	return "https://www.biorxiv.org"
}

// latestBiorxivVersion returns the highest numeric version string among the
// records, so a resolved URL always points at the most recent revision. The bool
// reports whether any record carried a parseable version.
func latestBiorxivVersion(entries []biorxivEntry) (string, bool) {
	best := ""
	bestN := 0
	found := false
	for _, e := range entries {
		n, err := strconv.Atoi(strings.TrimSpace(e.Version))
		if err != nil {
			continue
		}
		if !found || n > bestN {
			best, bestN, found = e.Version, n, true
		}
	}
	return best, found
}
