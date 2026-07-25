package libgen

import (
	"context"
	"errors"
	"os"
	"sync"
)

// errNoIdentifier is returned by FetchToTemp when the item carries neither an md5
// nor a DOI, so there is nothing to key the fetch (or its cache entry) on.
var errNoIdentifier = errors.New("item has no md5 or doi to fetch")

// noopRelease is a release func that does nothing; it is returned alongside an
// error so callers can always defer release() unconditionally.
func noopRelease() {
	// Intentionally empty: there is no cached file or refcount to release.
}

// FetchToTemp downloads the item to a server-side temp file (reusing a cached
// copy when the same identifier was fetched recently) and returns the path plus a
// release func the caller MUST call when done. release drops the cache refcount so
// the file becomes eligible for eviction; it is safe to call more than once. The
// returned file is named with its correct extension (DownloadItem derives it), so
// a paginated read can dispatch on the extension. On error the returned path is
// empty and release is a no-op.
//
// An optional progress callback reports the transfer the same way DownloadItem
// does, for the read path that would otherwise fetch a large file in silence.
// Only the first callback is used, and a cache hit reports nothing because
// nothing is transferred.
func (c *Client) FetchToTemp(ctx context.Context, item Item, progress ...ProgressFunc) (path string, release func(), err error) {
	key := item.MD5
	if key == "" {
		key = item.DOI
	}
	if key == "" {
		return "", noopRelease, errNoIdentifier
	}

	// Fast path: a recent fetch of the same identifier is still cached. get holds a
	// reference for us, so the file cannot be evicted until we release it.
	if cached, ok := c.tempCache.get(key); ok {
		return cached, c.releaseOnce(key), nil
	}

	// Miss: download into a dedicated per-fetch temp dir so eviction can drop the
	// whole directory. An empty filename lets DownloadItem auto-name the file with
	// its correct extension.
	tempDir, err := os.MkdirTemp("", "libgen-read-*")
	if err != nil {
		return "", noopRelease, err
	}
	// Progress is forwarded here for the same reason download reports it: a read
	// of a large PDF fetches the whole file first, and without this the client
	// waits on a silent multi-megabyte transfer. A cache hit returns above and
	// reports nothing, which is correct — nothing is being transferred.
	res, err := c.DownloadItem(ctx, item, tempDir, "", progress...)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return "", noopRelease, err
	}

	// Resolve against the cache under a single lock: if a concurrent fetch filled
	// the same key while we downloaded, discard our copy and use theirs; otherwise
	// store ours. Either way the caller gets one reference and one release.
	stored, isNew := c.tempCache.getOrPut(key, res.Path, res.SizeBytes)
	if !isNew {
		_ = os.RemoveAll(tempDir)
	}
	return stored, c.releaseOnce(key), nil
}

// releaseOnce returns a release closure that drops exactly one cache reference for
// key on its first call and is a no-op on any subsequent call, so callers can
// defer it and also release early without double-decrementing.
func (c *Client) releaseOnce(key string) func() {
	var once sync.Once
	return func() { once.Do(func() { c.tempCache.release(key) }) }
}
