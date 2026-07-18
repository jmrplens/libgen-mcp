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

// randombookAPIBase is the default base URL of the randombook.org HTTP API used
// to discover fresh libgen-family mirror hostnames for a given md5. It is a field
// on randombookSource so tests can point it at an httptest server.
const randombookAPIBase = "https://randombook.org"

// randombookMaxBody bounds how many bytes of a randombook API response are read,
// guarding against an unexpectedly large or hostile body. The JSON payloads are a
// few kilobytes at most.
const randombookMaxBody = 1 << 20 // 1 MiB

// randombookSource is a mirror-discovery fallback keyed by md5. It is NOT a
// primary file provider: given an md5 it queries the randombook.org HTTP API in
// two steps — by-id (md5 → numeric id) then links-by-id (id → a list of fresh
// libgen-family mirror hostnames) — and then reuses the LibGen link chain
// (ads.php → get.php → CDN) against those freshly discovered hosts. The value is
// medium: it rescues a download when the primary libgen.li mirror family is down
// by pointing at sibling mirrors randombook still reaches. The API is
// undocumented and private, so parsing failures are wrapped in ErrLayoutChanged.
//
// Note on the API shape: links-by-id returns both a "list" of mirror hostnames
// and a "links" array. The "links" entries are opaque per-request tokens to
// landing pages, not direct files, so only "list" (the mirror hostnames) is used.
type randombookSource struct {
	// apiBase overrides the randombook API endpoint (defaults to
	// randombookAPIBase); tests set it to a local httptest server.
	apiBase string
	// http is the client used for both the API lookups and the discovered-mirror
	// ads.php requests; when nil, http.DefaultClient is used.
	http *http.Client
}

// Compile-time assertion that randombookSource satisfies the DownloadSource contract.
var _ DownloadSource = randombookSource{}

// randombookByIDResponse is the subset of the by-id API record consulted here. A
// nil Result means the md5 is not indexed by randombook.
type randombookByIDResponse struct {
	Result *struct {
		ID string `json:"id"`
	} `json:"result"`
	IsError bool `json:"isError"`
}

// randombookLinksResponse is the subset of the links-by-id API record consulted
// here. Only List (fresh mirror hostnames) is used; the sibling "links" array
// carries opaque per-request tokens and is deliberately ignored.
type randombookLinksResponse struct {
	Result *struct {
		List []string `json:"list"`
	} `json:"result"`
	IsError bool `json:"isError"`
}

// Name identifies the randombook source.
func (s randombookSource) Name() string { return "randombook" }

// Supports reports that randombook can attempt any md5-keyed item.
func (s randombookSource) Supports(it Item) bool { return it.MD5 != "" }

// Resolve discovers fresh mirror hostnames for the item's md5 via the randombook
// API, then resolves the LibGen link chain against each in turn. The first mirror
// whose ads.php yields a get.php key returns a Resolved carrying that URL with MD5
// verification requested (the file is md5-keyed). A non-indexed md5, an empty
// mirror list, or no mirror yielding a key all return an error so the download
// chain advances to the next source.
func (s randombookSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	id, err := s.lookupID(ctx, it.MD5)
	if err != nil {
		return Resolved{}, err
	}
	mirrors, err := s.lookupMirrors(ctx, id)
	if err != nil {
		return Resolved{}, err
	}

	var lastErr error
	for _, mirror := range mirrors {
		fileURL, rerr := s.resolveViaMirror(ctx, mirror, it.MD5)
		if rerr != nil {
			lastErr = rerr
			continue
		}
		return Resolved{FileURL: fileURL, VerifyMD5: true}, nil
	}
	if lastErr != nil {
		return Resolved{}, fmt.Errorf("randombook: no discovered mirror resolved md5 %q: %w", it.MD5, lastErr)
	}
	return Resolved{}, fmt.Errorf("randombook: no usable mirrors discovered for md5 %q", it.MD5)
}

// lookupID queries the by-id endpoint and returns the numeric id randombook keys
// the md5 by. A nil result means the md5 is not indexed (a normal miss); a present
// result with an empty id means the API shape changed (ErrLayoutChanged).
func (s randombookSource) lookupID(ctx context.Context, md5 string) (string, error) {
	endpoint := s.base() + "/api/search/by-id?id=" + url.QueryEscape(md5)
	var rec randombookByIDResponse
	if err := s.getJSON(ctx, endpoint, &rec); err != nil {
		return "", err
	}
	if rec.Result == nil {
		return "", fmt.Errorf("randombook: md5 %q not indexed", md5)
	}
	if rec.Result.ID == "" {
		return "", fmt.Errorf("%w: randombook by-id result carries no id", ErrLayoutChanged)
	}
	return rec.Result.ID, nil
}

// lookupMirrors queries the links-by-id endpoint and returns the fresh mirror
// hostnames randombook offers for the id. An empty list is a normal miss; a
// missing result object means the API shape changed (ErrLayoutChanged).
func (s randombookSource) lookupMirrors(ctx context.Context, id string) ([]string, error) {
	endpoint := s.base() + "/api/download/links-by-id?id=" + url.QueryEscape(id)
	var rec randombookLinksResponse
	if err := s.getJSON(ctx, endpoint, &rec); err != nil {
		return nil, err
	}
	if rec.Result == nil {
		return nil, fmt.Errorf("%w: randombook links-by-id result missing", ErrLayoutChanged)
	}
	if len(rec.Result.List) == 0 {
		return nil, fmt.Errorf("randombook: no mirror hostnames for id %q", id)
	}
	return rec.Result.List, nil
}

// resolveViaMirror runs the LibGen link chain against a single freshly discovered
// mirror: it fetches ads.php?md5=… on that host and extracts the get.php?…&key=…
// link. It returns the absolute get.php URL (which the download pipeline follows
// through the CDN redirect), or an error when the host is unreachable, replies
// non-200, or serves a page without a get.php key.
func (s randombookSource) resolveViaMirror(ctx context.Context, mirror, md5 string) (string, error) {
	base := normalizeMirrorBase(mirror)
	endpoint := base + "/ads.php?md5=" + url.QueryEscape(md5)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("randombook: building ads request for %q: %w", base, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("randombook: requesting %q: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("randombook: mirror %q returned HTTP %d", base, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return "", fmt.Errorf("randombook: reading %q: %w", base, err)
	}
	link, err := ExtractGetLink(body)
	if err != nil {
		return "", fmt.Errorf("randombook: mirror %q: %w", base, err)
	}
	return base + "/" + link, nil
}

// getJSON issues a GET and decodes the JSON response into out. A transport error
// or non-200 status is returned as-is; a decode failure is wrapped in
// ErrLayoutChanged since the private API could have changed shape.
func (s randombookSource) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("randombook: building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("randombook: requesting %q: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("randombook: %q returned HTTP %d", endpoint, resp.StatusCode)
	}
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, randombookMaxBody)).Decode(out); decErr != nil {
		return fmt.Errorf("%w: randombook: decoding %q: %w", ErrLayoutChanged, endpoint, decErr)
	}
	return nil
}

// base returns the configured API base URL (trailing slash trimmed) or the
// default randombookAPIBase.
func (s randombookSource) base() string {
	if s.apiBase != "" {
		return strings.TrimRight(s.apiBase, "/")
	}
	return randombookAPIBase
}

// client returns the configured HTTP client or http.DefaultClient.
func (s randombookSource) client() *http.Client {
	if s.http != nil {
		return s.http
	}
	return http.DefaultClient
}

// normalizeMirrorBase turns a discovered mirror reference into a clean base URL:
// it trims surrounding whitespace and any trailing slash, and prepends https://
// when the entry is a bare hostname (randombook returns entries like
// "https://libgen.net", but a scheme-less host is tolerated defensively).
func normalizeMirrorBase(mirror string) string {
	m := strings.TrimRight(strings.TrimSpace(mirror), "/")
	if !strings.Contains(m, "://") {
		m = "https://" + m
	}
	return m
}
