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

// fatcatAPIBase is the default fatcat (Internet Archive Scholar) API root. The
// source appends /release/lookup to it. It is a field on fatcatSource so tests can
// point the source at an httptest server.
const fatcatAPIBase = "https://api.fatcat.wiki/v0"

// fatcatMaxBody bounds how many bytes of a fatcat release response are read,
// guarding against an unexpectedly large or hostile body.
const fatcatMaxBody = 2 << 20 // 2 MiB

// fatcatSource resolves a DOI to a preserved PDF through fatcat, the catalog
// behind Internet Archive Scholar (https://scholar.archive.org).
//
// It looks the DOI up as a release with its files expanded, then picks a file URL
// hosted on the Internet Archive — preferring a direct archive.org copy over a
// Wayback (web.archive.org) capture, and falling through the URL list otherwise.
// Those copies are preservation copies of open-access works, so this is a legal
// source. It distinguishes a DOI fatcat does not know from one it knows but has
// not preserved a file for. MD5 verification is disabled: DOI-keyed items carry no
// LibGen digest.
type fatcatSource struct {
	// http is the client used for lookup requests; when nil, http.DefaultClient is
	// used.
	http *http.Client
	// apiBase overrides the API root (defaults to fatcatAPIBase); tests set it to a
	// local httptest server.
	apiBase string
}

// Compile-time assertion that fatcatSource satisfies the DownloadSource contract.
var _ DownloadSource = fatcatSource{}

// fatcatResponse is the subset of a fatcat release (with files expanded) consulted
// here.
type fatcatResponse struct {
	Files []fatcatFile `json:"files"`
}

// fatcatFile is the subset of one preserved file record consulted here: its type
// and the mirror URLs that serve it.
type fatcatFile struct {
	Mimetype string      `json:"mimetype"`
	URLs     []fatcatURL `json:"urls"`
}

// fatcatURL is one location a preserved file is served from.
type fatcatURL struct {
	URL string `json:"url"`
	Rel string `json:"rel"`
}

// Name identifies the fatcat source.
func (s fatcatSource) Name() string { return "fatcat" }

// Supports reports that fatcat can resolve any DOI-keyed item.
func (s fatcatSource) Supports(it Item) bool { return it.DOI != "" }

// Resolve looks the DOI up on fatcat and returns the best Internet Archive-hosted
// PDF URL among its preserved files. A DOI fatcat does not know (HTTP 404) and one
// it knows but has preserved no archived file for yield distinct errors so the
// caller tries the next source.
func (s fatcatSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	base := s.apiBase
	if base == "" {
		base = fatcatAPIBase
	}
	// fatcat normalizes DOIs to lowercase, so the lookup key is lowercased to match.
	endpoint := strings.TrimRight(base, "/") + "/release/lookup?doi=" +
		url.QueryEscape(strings.ToLower(it.DOI)) + "&expand=files"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return Resolved{}, fmt.Errorf("fatcat: building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	httpClient := httpClientOr(s.http)
	resp, err := httpClient.Do(req)
	if err != nil {
		return Resolved{}, unavailable(fmt.Errorf("fatcat: requesting %q: %w", it.DOI, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return Resolved{}, notIndexed(fmt.Errorf("fatcat: %q is unknown to fatcat", it.DOI))
	}
	if resp.StatusCode != http.StatusOK {
		return Resolved{}, unavailableStatus(resp.StatusCode, fmt.Errorf("fatcat: %q returned HTTP %d", it.DOI, resp.StatusCode))
	}

	var rec fatcatResponse
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, fatcatMaxBody)).Decode(&rec); decErr != nil {
		return Resolved{}, fmt.Errorf("fatcat: decoding response for %q: %w", it.DOI, decErr)
	}
	fileURL, ok := pickArchivedPDF(rec.Files)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("fatcat: %q has no preserved Internet Archive file", it.DOI))
	}
	return Resolved{FileURL: fileURL, VerifyMD5: false, Ext: "pdf"}, nil
}

// pickArchivedPDF selects the best Internet Archive-hosted PDF URL among a
// release's files: a direct archive.org copy is preferred over another
// archive.org subdomain, which is preferred over a Wayback capture; anything not
// on the Internet Archive is skipped. The bool reports whether one was found.
//
// A file whose mimetype fatcat did not record is admitted only when the URL itself
// looks like a PDF: the source hands its result back with Ext "pdf" and the
// download pipeline rejects only HTML, so an untyped ZIP or XML asset would
// otherwise be saved under a .pdf name with nothing downstream to catch it.
func pickArchivedPDF(files []fatcatFile) (string, bool) {
	best := ""
	bestScore := 0
	for _, f := range files {
		typed := strings.EqualFold(f.Mimetype, "application/pdf")
		if !typed && f.Mimetype != "" {
			continue // explicitly some other type
		}
		for _, u := range f.URLs {
			if !typed && !urlLooksPDF(u.URL) {
				continue // untyped: the URL must vouch for it
			}
			if sc := archiveScore(u.URL); sc > bestScore {
				best, bestScore = u.URL, sc
			}
		}
	}
	return best, best != ""
}

// urlLooksPDF reports whether a URL's path names a PDF, used to vouch for a
// preserved file whose mimetype fatcat did not record. A Wayback capture embeds the
// original URL in its path (…/web/<ts>/https://host/paper.pdf), so matching the
// path's suffix rather than parsing a single extension covers both shapes. Query
// strings are ignored, since the path carries the filename.
func urlLooksPDF(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".pdf")
}

// archiveScore ranks a URL by how directly it is served from the Internet
// Archive: a direct archive.org copy scores highest, another *.archive.org
// subdomain next, a web.archive.org Wayback capture lowest, and anything else zero
// (not an Internet Archive location).
func archiveScore(raw string) int {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return 0
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "archive.org":
		return 3
	case host == "web.archive.org":
		return 1
	case strings.HasSuffix(host, ".archive.org"):
		return 2
	default:
		return 0
	}
}
