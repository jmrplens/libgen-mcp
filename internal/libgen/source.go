package libgen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
)

// Item is a download request expressed independently of any particular source: a
// file may be identified by its LibGen MD5, by a DOI, or by both, and carries
// optional bibliographic metadata used to build a human-readable filename. At
// least one identifier should be set; sources decide via Supports which items
// they can resolve.
type Item struct {
	// MD5 is the LibGen file digest, when the file is keyed by md5. Empty for
	// sources that key by other identifiers.
	MD5 string
	// DOI is the Digital Object Identifier, when the file is keyed by DOI (e.g.
	// Unpaywall or Sci-Hub). Empty when unknown.
	DOI string
	// Source, when set, restricts the download to that single named source (one
	// of config.KnownSources). Empty means try the full configured source chain
	// in order with transparent failover.
	Source string
	// Email is an optional per-call Unpaywall contact email, supplied on demand;
	// it overrides the configured email for this item only and is never persisted.
	// When set on a DOI item, it can pull the Unpaywall source into the download
	// chain even if the server configured no email (see Client.withPerCallUnpaywall).
	Email string
	// AnnasKey is an optional per-call Anna's Archive account secret, supplied on
	// demand; it enables the member fast-download path for this item only and is
	// never persisted. When set on an md5 item, it can pull the Anna's source into
	// the download chain even if the server configured no key (see
	// Client.withPerCallAnnas).
	AnnasKey string
	// Meta carries bibliographic fields for naming; may be nil.
	Meta *FileMeta
}

// Resolved is the outcome of resolving an Item against a source: the concrete URL
// to stream, any extra request headers the source needs, whether the streamed
// bytes must be MD5-verified against Item.MD5, and a fallback file extension.
type Resolved struct {
	// FileURL is the direct URL the download pipeline streams from.
	FileURL string
	// Header carries extra request headers the source requires (e.g. a Referer);
	// nil when none are needed.
	Header http.Header
	// VerifyMD5 requests that the streamed bytes be hash-checked against Item.MD5.
	// Set only when the file is keyed by md5 (so the digest is meaningful).
	VerifyMD5 bool
	// Ext is a fallback file extension (without a leading dot) applied when the
	// chosen filename has none; empty to leave naming untouched.
	Ext string
	// Account, when non-nil, carries metered-account state the source observed
	// while resolving (currently only Anna's member quota). It is a by-product of
	// a resolve that already had to happen: the provider exposes this data only on
	// the download call itself, and that call consumes an allowance, so it is
	// never fetched just to report it. Nil whenever no account is involved.
	Account *AccountInfo
}

// AccountInfo reports a provider account's metered download allowance as observed
// during a resolve, so a caller can see how much of the quota remains. Fields the
// provider does not report stay zero.
//
// Naming caveat for Anna's Archive: the API's own field names say "per day" and
// "today", but the site describes the same counter as "fast downloads used (last
// 18 hours)" — a rolling window, not a calendar day. The provider's names are kept
// here so the mapping stays obvious, but callers should read the ceiling as
// "per rolling window" rather than "per midnight-to-midnight day".
type AccountInfo struct {
	// Source is the Name() of the source the allowance belongs to.
	Source string `json:"source" jsonschema:"the download source this account belongs to"`
	// DownloadsLeft is the remaining allowance for the current window.
	DownloadsLeft int `json:"downloads_left" jsonschema:"downloads still available in the current window"`
	// DownloadsPerDay is the account's ceiling per rolling window.
	DownloadsPerDay int `json:"downloads_per_day" jsonschema:"the account's download ceiling per rolling window (18h for Anna's despite the field name)"`
	// DownloadsDoneToday is how much of the ceiling has been consumed.
	DownloadsDoneToday int `json:"downloads_done_today" jsonschema:"downloads already consumed in the current window"`
}

// DownloadSource resolves an Item to a concrete, streamable URL. Implementations
// encapsulate the mirror- or provider-specific logic (link scraping, DOI lookup,
// …) so the shared download pipeline stays source-agnostic. Download tries each
// supporting source in order, advancing to the next when one fails.
type DownloadSource interface {
	// Name identifies the source in results and error messages.
	Name() string
	// Supports reports whether this source can resolve the given Item.
	Supports(it Item) bool
	// Resolve turns the Item into a Resolved (URL + streaming directives), or
	// returns an error so the caller can try the next source.
	Resolve(ctx context.Context, it Item) (Resolved, error)
}

// libgenSource is the default DownloadSource: it resolves an md5 through the
// LibGen link chain (ads.php → get.php → CDN) via the owning Client and requires
// MD5 verification of the streamed bytes. LibGen needs no extra request headers
// (fetchFile sets only the User-Agent), so Resolved.Header is left nil.
type libgenSource struct {
	c *Client
}

// Name identifies the LibGen source.
func (s libgenSource) Name() string { return "libgen" }

// Supports reports that LibGen can serve any md5-keyed item.
func (s libgenSource) Supports(it Item) bool { return it.MD5 != "" }

// Resolve obtains a fresh direct download URL for the item's md5 and marks the
// stream for MD5 verification. The mirror that served the link is recoverable
// from the returned FileURL's host, so it is not carried separately.
func (s libgenSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	fileURL, _, err := s.c.ResolveGetURL(ctx, it.MD5)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{FileURL: fileURL, VerifyMD5: true}, nil
}

// partialKey derives a stable identifier for an item's partial (.part) file and
// serialization lock, so an interrupted download can resume and concurrent
// downloads of the same target never corrupt each other. It is keyed by md5 when
// present (preserving the historical ".libgen-mcp-<md5>.part" path for LibGen),
// else by a hash of the DOI, else by a hash of the resolved FileURL. Every branch
// yields a filesystem-safe token.
func partialKey(it Item, r Resolved) string {
	switch {
	case it.MD5 != "":
		return it.MD5
	case it.DOI != "":
		return "doi-" + shortHash(it.DOI)
	default:
		return "url-" + shortHash(r.FileURL)
	}
}

// sanitizeForPart reduces a source name to a filesystem-safe token for embedding
// in a partial (.part) filename. Source names are known-safe lowercase constants
// ("libgen", "scihub", "unpaywall", "randombook"), so this is a light defensive
// pass: it keeps ASCII letters, digits and '-', mapping anything else to '_'.
func sanitizeForPart(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '_'
		}
	}, name)
}

// shortHash returns a short, filesystem-safe hex token derived from s, used to
// build deterministic partial paths for non-md5 items.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// escapeDOIPath percent-encodes a DOI for safe placement in a URL path while
// keeping its slashes literal. The DOI-keyed APIs (Unpaywall, Sci-Hub) key their
// records by the unescaped DOI, so the "/" separators must reach them raw; but a
// DOI may legitimately contain other URL-reserved characters (e.g. '#', '?', a
// space) that would otherwise be parsed as a fragment or query and corrupt the
// request. Each "/"-separated segment is escaped with url.PathEscape and the parts
// are re-joined with "/", so slashes survive literally while every other unsafe
// character is percent-encoded.
func escapeDOIPath(doi string) string {
	parts := strings.Split(doi, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// mirrorOf reduces a file URL to its "scheme://host" origin, used as the result's
// Mirror label and in download error messages. It falls back to the raw URL when
// it cannot be parsed into a host.
func mirrorOf(fileURL string) string {
	if u, err := url.Parse(fileURL); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return fileURL
}
