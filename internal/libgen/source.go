package libgen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
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

// shortHash returns a short, filesystem-safe hex token derived from s, used to
// build deterministic partial paths for non-md5 items.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
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
