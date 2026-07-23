package libgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
//
// Note on candidate hosts (verified 2026-07-23): the API's "list" is not
// guaranteed to contain genuine libgen.li-family hosts — it has been observed
// consistently returning a fixed set of non-family hosts (e.g. libgen.net,
// libgen.me, libgen.xyz — client-rendered SPAs with no server-side ads.php route
// — and annas-archive.gl, which uses an unrelated /md5/<hash> scheme and is
// out of scope per the project's Anna's Archive decision). Resolve therefore
// only attempts hosts matching the libgen.<tld> shape (randombookHostRe); other
// hosts are skipped without a request, and a libgen.<tld> host that answers with
// a client-rendered shell instead of the classic ads.php page is reported with a
// distinct, diagnosable error (see resolveViaMirror).
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

// randombookHostRe matches a bare libgen.<tld> hostname (no path/query), the only
// shape resolveViaMirror's ads.php/get.php scraping is designed for. It mirrors
// the convention in internal/mirrors.mirrorHostRe. A discovered candidate that
// does not match — e.g. an unrelated aggregator domain — is skipped rather than
// scraped, since the technique does not apply to it.
var randombookHostRe = regexp.MustCompile(`^https?://libgen\.[a-z]{2,6}/?$`)

// ErrMirrorClientRendered reports that a libgen-family host answered with a
// client-rendered application shell (no server-rendered ads.php content) rather
// than the classic ads.php page resolveViaMirror scrapes. It is distinct from a
// generic ExtractGetLink failure so a site-wide frontend migration is
// diagnosable from logs/errors rather than looking like a one-off missing link.
var ErrMirrorClientRendered = errors.New("randombook: mirror serves a client-rendered page, not the classic ads.php content")

// nuxtShellMarker is a substring reliably present in a Nuxt single-page-app's
// server-sent HTML shell (the mount point the client-side JS hydrates into) but
// never in a classic server-rendered ads.php page.
const nuxtShellMarker = `id="__nuxt"`

// filterLibgenFamily returns the subset of mirrors matching randombookHostRe, in
// order. Candidates outside the libgen.<tld> shape are dropped before any request
// is made, since resolveViaMirror's scraping technique does not apply to them.
func filterLibgenFamily(mirrors []string) []string {
	out := make([]string, 0, len(mirrors))
	for _, m := range mirrors {
		if randombookHostRe.MatchString(strings.TrimSpace(m)) {
			out = append(out, m)
		}
	}
	return out
}

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
	mirrors = filterLibgenFamily(mirrors)
	if len(mirrors) == 0 {
		return Resolved{}, fmt.Errorf("randombook: no usable mirrors discovered for md5 %q", it.MD5)
	}

	var lastErr error
	for _, mirror := range mirrors {
		fileURL, rerr := s.resolveMirror(ctx, mirror, id, it.MD5)
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
	if rec.IsError {
		return "", fmt.Errorf("randombook: by-id API reported an error for md5 %q", md5)
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
	if rec.IsError {
		return nil, fmt.Errorf("randombook: links-by-id API reported an error for id %q", id)
	}
	if rec.Result == nil {
		return nil, fmt.Errorf("%w: randombook links-by-id result missing", ErrLayoutChanged)
	}
	if len(rec.Result.List) == 0 {
		return nil, fmt.Errorf("randombook: no mirror hostnames for id %q", id)
	}
	return rec.Result.List, nil
}

// randombookDownloadPath is the mirror route the site's own UI opens to serve a
// file. It takes the numeric randombook id — not the md5, and not the opaque
// "?l=" landing-page token — and answers with the file bytes through a 302 to the
// family's file host.
//
// Verified live on 2026-07-24: the "?l=" token route returns HTTP 400 even when
// called from inside a real browser session with genuine cookies and same-origin,
// so it is deliberately not used; the id route needs no token, cookie or session
// at all, and the bytes it serves hash to the item's md5.
const randombookDownloadPath = "/api/download?id="

// resolveMirror resolves a single discovered mirror to a streamable URL. It
// prefers the site's own download API and falls back to the classic ads.php link
// chain, which older libgen hosts may still serve. When both fail the ads.php
// error is returned (wrapping the API error for context) so existing diagnoses
// like ErrMirrorClientRendered stay detectable with errors.Is.
func (s randombookSource) resolveMirror(ctx context.Context, mirror, id, md5 string) (string, error) {
	fileURL, apiErr := s.resolveViaDownloadAPI(ctx, mirror, id)
	if apiErr == nil {
		return fileURL, nil
	}
	fileURL, adsErr := s.resolveViaMirror(ctx, mirror, md5)
	if adsErr == nil {
		return fileURL, nil
	}
	return "", fmt.Errorf("%w (download API: %w)", adsErr, apiErr)
}

// resolveViaDownloadAPI returns a mirror's direct download URL for a numeric
// randombook id, after a cheap HEAD probe confirming the mirror is reachable and
// implements the route.
//
// The endpoint is GET-only, so a HEAD following the mirror's redirect comes back
// 405 (Allow: GET); that still proves the host is alive and serving the route,
// which is all this probe decides. A 404 means the mirror does not implement the
// route at all (an older, classic libgen host), so the caller falls back to the
// ads.php chain. HEAD does not validate the id — an unknown id fails only on the
// GET — but id validity does not vary between mirrors, so it is not worth a
// full-body request here: the endpoint ignores Range and would stream the whole
// file.
func (s randombookSource) resolveViaDownloadAPI(ctx context.Context, mirror, id string) (string, error) {
	base := normalizeMirrorBase(mirror)
	endpoint := base + randombookDownloadPath + url.QueryEscape(id)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("randombook: building download probe for %q: %w", base, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("randombook: probing %q: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode >= http.StatusInternalServerError {
		return "", fmt.Errorf("randombook: mirror %q serves no download API (HTTP %d)", base, resp.StatusCode)
	}
	return endpoint, nil
}

// resolveViaMirror runs the LibGen link chain against a single freshly discovered
// mirror: it fetches ads.php?md5=… on that host and extracts the get.php?…&key=…
// link. It returns the absolute get.php URL (which the download pipeline follows
// through the CDN redirect), or an error when the host is unreachable, replies
// non-200, or serves a page without a get.php key.
//
// Trust boundary (SSRF): the mirror hostname comes from the randombook API
// response (lookupMirrors) and is fetched transitively here — the tool issues
// requests to whatever hosts that API returns. This is a deliberate trust
// dependency on the randombook API; it is acceptable for this download tool (the
// API is the whole point of the randombook fallback), but a compromised or
// hostile API could redirect these requests to arbitrary hosts. Documented rather
// than mitigated because the tool's purpose is to fetch files from these mirrors.
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
		if bytes.Contains(body, []byte(nuxtShellMarker)) {
			return "", fmt.Errorf("randombook: mirror %q: %w", base, ErrMirrorClientRendered)
		}
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
//
// Note: the mirror string originates from the untrusted randombook API (see the
// SSRF trust-boundary note on resolveViaMirror); this only normalizes its shape
// and does not validate that the host is a legitimate libgen mirror.
func normalizeMirrorBase(mirror string) string {
	m := strings.TrimRight(strings.TrimSpace(mirror), "/")
	if !strings.Contains(m, "://") {
		m = "https://" + m
	}
	return m
}
