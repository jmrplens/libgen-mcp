package libgen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// annasMaxBody bounds how many bytes of an Anna's Archive book page are read
// while scanning for an IPFS CID. Book pages run to a few hundred kilobytes;
// 4 MiB is generous.
const annasMaxBody = 4 << 20 // 4 MiB

// annasFastDownloadMaxBody bounds how many bytes of the member fast-download JSON
// response are read, guarding against an oversized or hostile body.
const annasFastDownloadMaxBody = 1 << 20 // 1 MiB

// annasCIDv1 matches an IPFS v1 CID (base32, "bafy…") as published on an Anna's
// book page; annasCIDv0 matches the legacy base58 "Qm…" form. v1 is preferred
// because modern public gateways resolve it most reliably.
var (
	annasCIDv1 = regexp.MustCompile(`\bbaf[a-z2-7]{10,}\b`)
	annasCIDv0 = regexp.MustCompile(`\bQm[1-9A-HJ-NP-Za-km-z]{44}\b`)
)

// defaultIPFSGateways is the ordered list of public IPFS gateways tried when a
// source configures none. dweb.link and w3s.link were verified serving real bytes
// on 2026-07-23; the rest are widely-used alternates kept as fallbacks because
// gateway availability varies by network.
var defaultIPFSGateways = []string{
	"https://dweb.link/ipfs/",
	"https://w3s.link/ipfs/",
	"https://ipfs.io/ipfs/",
	"https://gateway.pinata.cloud/ipfs/",
}

// annasSource resolves an md5 to a downloadable file through Anna's Archive.
//
// The default path is keyless: <mirror>/md5/<md5> serves anonymously with no
// CAPTCHA or JS challenge, and publishes the item's IPFS CID; the source then
// picks the first public gateway that actually serves that content. Anna's
// /slow_download/ route is deliberately NOT used — it sits behind a DDoS-Guard JS
// challenge no plain HTTP client can satisfy (verified 2026-07-23).
//
// When key is set, the member fast-download API is tried first and the keyless
// IPFS path stays as the fallback, so an absent, expired or non-member key costs
// one request and never breaks the source.
//
// MD5 verification is enabled on both paths: items are keyed by the LibGen
// digest, so the streamed bytes are checked against it.
type annasSource struct {
	// mirrors supplies the Anna's Archive base URLs, preferred first.
	mirrors MirrorLister
	// http is the client used for page, API and gateway-probe requests; when nil,
	// http.DefaultClient is used.
	http *http.Client
	// key is the optional account secret enabling the member fast-download API;
	// empty selects keyless (IPFS-only) operation.
	key string
	// gateways overrides the public IPFS gateway list; empty uses
	// defaultIPFSGateways.
	gateways []string
}

// Compile-time assertion that annasSource satisfies the DownloadSource contract.
var _ DownloadSource = annasSource{}

// annasFastDownload is the subset of the member fast-download API response
// consulted here. On success the response carries download_url and an
// account_fast_download_info object and no error field at all; a populated Error
// (e.g. "Not a member") is therefore the failure discriminator.
type annasFastDownload struct {
	// DownloadURL is the direct, member-tier file URL when the call succeeds.
	DownloadURL string `json:"download_url"`
	// Error carries the API's rejection reason when the call fails.
	Error string `json:"error"`
}

// Name identifies the Anna's Archive source.
func (s annasSource) Name() string { return "annas" }

// Supports reports that Anna's Archive can serve any md5-keyed item.
func (s annasSource) Supports(it Item) bool { return it.MD5 != "" }

// Resolve turns an md5 into a streamable URL, trying each discovered mirror in
// order. With a key configured the member API is attempted first and the keyless
// IPFS path is the fallback; without one only IPFS is used. An error is returned
// when no mirror yields a usable URL, so the chain advances to the next source.
func (s annasSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	httpClient := s.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	var lastErr error
	for _, mirror := range s.mirrors.Mirrors(ctx) {
		base := strings.TrimRight(strings.TrimSpace(mirror), "/")
		fileURL, err := s.resolveMirror(ctx, httpClient, base, it.MD5)
		if err != nil {
			lastErr = err
			continue
		}
		return Resolved{FileURL: fileURL, VerifyMD5: true}, nil
	}
	if lastErr != nil {
		return Resolved{}, fmt.Errorf("annas: no mirror resolved %q: %w", it.MD5, lastErr)
	}
	return Resolved{}, fmt.Errorf("annas: no mirrors available for %q", it.MD5)
}

// resolveMirror resolves a single mirror, preferring the member fast-download API
// when a key is configured and falling back to the keyless IPFS path. When both
// fail the IPFS error is returned, wrapping the member error so a rejected key
// stays visible in diagnostics instead of being silently replaced.
func (s annasSource) resolveMirror(ctx context.Context, httpClient *http.Client, base, md5 string) (string, error) {
	var memberErr error
	if s.key != "" {
		fileURL, err := s.resolveViaMemberAPI(ctx, httpClient, base, md5)
		if err == nil {
			return fileURL, nil
		}
		memberErr = err
	}
	fileURL, ipfsErr := s.resolveViaIPFS(ctx, httpClient, base, md5)
	if ipfsErr == nil {
		return fileURL, nil
	}
	if memberErr != nil {
		return "", fmt.Errorf("%w (member API: %w)", ipfsErr, memberErr)
	}
	return "", ipfsErr
}

// resolveViaMemberAPI asks a mirror's member fast-download API for a direct URL.
// It returns an error whenever the key is rejected or the response carries no
// URL, so the caller falls back to the keyless IPFS path.
func (s annasSource) resolveViaMemberAPI(ctx context.Context, httpClient *http.Client, base, md5 string) (string, error) {
	endpoint := fmt.Sprintf("%s/dyn/api/fast_download.json?md5=%s&key=%s",
		base, url.QueryEscape(md5), url.QueryEscape(s.key))

	resp, err := s.get(ctx, httpClient, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("annas: member request to %q: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The body is decoded even on a non-200 because the API reports its rejection
	// reason (e.g. "Not a member") in the JSON alongside a 403.
	var rec annasFastDownload
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, annasFastDownloadMaxBody)).Decode(&rec); decErr != nil {
		return "", fmt.Errorf("annas: decoding member response from %q: %w", base, decErr)
	}
	if rec.Error != "" {
		return "", fmt.Errorf("annas: member API rejected the key: %s", rec.Error)
	}
	if rec.DownloadURL == "" {
		return "", fmt.Errorf("annas: member API returned no URL (HTTP %d)", resp.StatusCode)
	}
	return rec.DownloadURL, nil
}

// resolveViaIPFS reads one mirror's book page for an IPFS CID and returns the
// first gateway URL that actually serves it.
func (s annasSource) resolveViaIPFS(ctx context.Context, httpClient *http.Client, base, md5 string) (string, error) {
	cid, err := s.fetchCID(ctx, httpClient, base, md5)
	if err != nil {
		return "", err
	}
	gateways := s.gateways
	if len(gateways) == 0 {
		gateways = defaultIPFSGateways
	}
	for _, gw := range gateways {
		candidate := strings.TrimRight(gw, "/") + "/" + cid
		if s.probe(ctx, httpClient, candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("annas: no IPFS gateway served %q", cid)
}

// fetchCID requests a mirror's book page and extracts the item's IPFS CID.
func (s annasSource) fetchCID(ctx context.Context, httpClient *http.Client, base, md5 string) (string, error) {
	resp, err := s.get(ctx, httpClient, base+"/md5/"+url.PathEscape(md5), nil)
	if err != nil {
		return "", fmt.Errorf("annas: requesting %q: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("annas: mirror %q returned HTTP %d", base, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, annasMaxBody))
	if err != nil {
		return "", fmt.Errorf("annas: reading %q: %w", base, err)
	}
	cid, ok := extractIPFSCID(body)
	if !ok {
		return "", fmt.Errorf("annas: mirror %q embedded no IPFS CID for %q", base, md5)
	}
	return cid, nil
}

// probe reports whether a gateway actually serves the content, using a
// single-byte Range request so the check stays cheap. Both 206 (range honored)
// and 200 (range ignored but content present) count as success.
func (s annasSource) probe(ctx context.Context, httpClient *http.Client, candidate string) bool {
	resp, err := s.get(ctx, httpClient, candidate, http.Header{"Range": {"bytes=0-0"}})
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	return resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusOK
}

// get issues a GET carrying the shared User-Agent plus any extra headers.
func (s annasSource) get(ctx context.Context, httpClient *http.Client, endpoint string, extra http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return httpClient.Do(req)
}

// extractIPFSCID extracts the item's IPFS CID from an Anna's book page body,
// preferring the v1 (bafy…) form and falling back to v0 (Qm…). The bool reports
// whether one was found.
func extractIPFSCID(body []byte) (string, bool) {
	if m := annasCIDv1.Find(body); m != nil {
		return string(m), true
	}
	if m := annasCIDv0.Find(body); m != nil {
		return string(m), true
	}
	return "", false
}
