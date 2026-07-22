// Package discovery federates keyless open-access literature sources (arXiv,
// Crossref, OpenLibrary) into a single result shape the search tool can present
// and the read/download tools can act on. Every provider is best-effort: a
// failing source degrades to an empty result rather than sinking a federated
// search, and no source requires an API key, account, or login.
package discovery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// discoveryMaxBody bounds how many bytes of any provider response are read before
// parsing, guarding against an unexpectedly large or hostile body.
const discoveryMaxBody = 1 << 20 // 1 MiB

// discoveryTimeout is the per-provider wall-clock budget for a single Search call:
// the whole request (limiter wait plus HTTP round-trip plus body read) must finish
// within it, otherwise discovery degrades to empty rather than delaying search.
const discoveryTimeout = 6 * time.Second

// discoveryUserAgent identifies libgen-mcp to open-access APIs; sources such as
// arXiv request courtesy identification with a contact/repository URL.
const discoveryUserAgent = "libgen-mcp/1.0.0 (+https://github.com/jmrplens/libgen-mcp)"

// DiscoveryResult is one open-access hit from a discovery provider, in a shape
// the search tool can present and the read/download tools can act on. The name is
// the shared contract type referenced by every provider and the federation layer,
// so the revive stutter suggestion is intentionally waived here.
//
//nolint:revive // DiscoveryResult is the deliberate cross-package contract name.
type DiscoveryResult struct {
	Origin     string `json:"origin"` // "arxiv" | "crossref" | "openlibrary"
	Title      string `json:"title,omitempty"`
	Authors    string `json:"authors,omitempty"`
	Year       string `json:"year,omitempty"`
	DOI        string `json:"doi,omitempty"`
	ISBN       string `json:"isbn,omitempty"`
	PDFURL     string `json:"pdf_url,omitempty"` // a directly-fetchable OA PDF when known
	OpenAccess bool   `json:"open_access"`
}

// Provider is a keyless open-access discovery source.
type Provider interface {
	// Name reports the origin label this provider stamps on its results.
	Name() string
	// Search returns up to limit open-access results for the query, best-effort:
	// only a context error is returned; other failures degrade to an empty slice.
	Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error)
}

// newDiscoveryClient builds the shared *http.Client used by discovery providers,
// with a sane overall timeout so a stalled connection can never outlive the
// per-provider context budget.
func newDiscoveryClient() *http.Client {
	return &http.Client{Timeout: discoveryTimeout + time.Second}
}

// boundedGet issues a GET after setting the discovery User-Agent, and returns the
// response body bytes bounded to discoveryMaxBody together with the status code.
// It NEVER swallows a context error: a canceled or expired ctx propagates so the
// federation layer can distinguish "caller went away" from "source degraded". Any
// other transport failure surfaces as an error the caller may choose to soften to
// an empty result.
func boundedGet(ctx context.Context, client *http.Client, rawURL string) (status int, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", discoveryUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, discoveryMaxBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// firstNonEmpty returns the first trimmed non-empty string in the slice, or "".
func firstNonEmpty(values []string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
