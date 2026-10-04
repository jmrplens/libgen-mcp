package libgen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/libgen-mcp/v2/internal/mcpotel"
)

// tempEntry is one cached temp download: the file path, its size in bytes, the
// number of live references (a read in progress holds one), and the last time it
// was accessed (used for both TTL and LRU eviction).
type tempEntry struct {
	path  string
	size  int64
	refs  int
	atime time.Time
}

// tempCache is a bounded, refcounted cache of downloaded temp files keyed by an
// identifier (md5 or doi). It lets a paginated read fetch a file once and reuse
// it across page requests. Eviction honors a total-size cap and a TTL, but never
// removes an entry with live references (a read is in progress). All state is
// guarded by mu; disk removal (os.Remove) happens while holding mu but the
// blocking work (the download itself) runs entirely outside the cache.
type tempCache struct {
	mu       sync.Mutex
	entries  map[string]*tempEntry
	maxBytes int64
	ttl      time.Duration
}

// newTempCache builds an empty tempCache bounded by maxBytes of total on-disk
// size and a per-entry ttl (idle time before an unreferenced entry is evicted).
func newTempCache(maxBytes int64, ttl time.Duration) *tempCache {
	return &tempCache{
		entries:  make(map[string]*tempEntry),
		maxBytes: maxBytes,
		ttl:      ttl,
	}
}

// get returns (path, true) on a hit, incrementing the entry's refcount and
// refreshing its atime so the caller's read holds the file open against
// eviction; it returns ("", false) on a miss, which includes an entry whose
// file is no longer on disk (see liveLocked).
func (tc *tempCache) get(key string) (string, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	e, ok := tc.liveLocked(key)
	if !ok {
		return "", false
	}
	e.refs++
	e.atime = time.Now()
	return e.path, true
}

// liveLocked returns key's entry when its file is still on disk; the caller
// must hold tc.mu.
//
// An entry whose file has gone, because the read root it lived in was removed
// from under the process, is forgotten and reported as a miss, so the caller
// fetches the file again instead of being handed a path to nothing. A read
// that still holds the old path keeps its reference to the old entry, and its
// release cannot reach a new one stored under the same key (see release).
func (tc *tempCache) liveLocked(key string) (*tempEntry, bool) {
	e, ok := tc.entries[key]
	if !ok {
		return nil, false
	}
	if _, err := os.Stat(e.path); err != nil {
		delete(tc.entries, key)
		return nil, false
	}
	return e, true
}

// put stores a freshly downloaded file under key with refs=1 (the caller holds
// one reference) and then runs an eviction pass to stay within the size cap and
// TTL. If key already holds an entry it is overwritten (the caller's fresh copy
// wins); the previous backing file is removed if it differs and is unreferenced.
func (tc *tempCache) put(ctx context.Context, key, path string, size int64) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if prev, ok := tc.entries[key]; ok && prev.refs == 0 && prev.path != path {
		removeTempFile(prev.path)
	}
	tc.entries[key] = &tempEntry{path: path, size: size, refs: 1, atime: time.Now()}
	tc.evictLocked(ctx)
}

// getOrPut atomically resolves a just-downloaded file against the cache: on a hit
// it behaves like get (refs++, atime refreshed) and returns the existing path
// with isNew=false, so the caller discards its duplicate download; on a miss it
// stores the file with refs=1, runs an eviction pass, and returns it with
// isNew=true. Doing both under one lock closes the window where two concurrent
// fetches of the same key would each insert and leak one copy.
func (tc *tempCache) getOrPut(ctx context.Context, key, path string, size int64) (stored string, isNew bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if e, ok := tc.liveLocked(key); ok {
		e.refs++
		e.atime = time.Now()
		return e.path, false
	}
	tc.entries[key] = &tempEntry{path: path, size: size, refs: 1, atime: time.Now()}
	tc.evictLocked(ctx)
	return path, true
}

// release gives back the reference a get or getOrPut took on key's entry for
// path: it decrements the refcount (never below zero) and refreshes the atime
// so the TTL clock starts from the last use.
//
// The path is what binds a release to the entry it was taken on. An entry can
// be forgotten while a read still holds it (its file vanished, see
// liveLocked, or its root did, see dropUnder) and a new one stored under the
// same key, with a file in a new directory. A release by key alone would then
// drop a reference the new entry's reader is still counting on and leave its
// file open to eviction mid-read. A release whose entry is gone, or was
// replaced, does nothing.
func (tc *tempCache) release(key, path string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	e, ok := tc.entries[key]
	if !ok || e.path != path {
		return
	}
	if e.refs > 0 {
		e.refs--
	}
	e.atime = time.Now()
}

// evict removes entries that are past the TTL and, while the total cached size
// still exceeds maxBytes, the least-recently-used entry — but only entries with
// refs==0. Each removed entry's backing file (and its per-fetch temp dir) is
// deleted from disk.
func (tc *tempCache) evict(ctx context.Context) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.evictLocked(ctx)
}

// evictLocked is evict's body; the caller must hold tc.mu. It first drops every
// TTL-expired unreferenced entry, then, while the total size is over the cap,
// drops the least-recently-used unreferenced entry until it is within the cap or
// no evictable entry remains.
func (tc *tempCache) evictLocked(ctx context.Context) {
	now := time.Now()
	for key, e := range tc.entries {
		if e.refs == 0 && tc.ttl >= 0 && now.Sub(e.atime) >= tc.ttl {
			removeTempFile(e.path)
			delete(tc.entries, key)
			// Counted by reason rather than in total, which is the whole value
			// of the instrument: a cache that is full because nothing expires
			// is a different deployment from one that is full because it is
			// busy, and the two want opposite changes to the configuration.
			mcpotel.RecordReadCacheEviction(ctx, mcpotel.ReasonTTL)
		}
	}
	for tc.totalSizeLocked() > tc.maxBytes {
		key, ok := tc.lruEvictableLocked()
		if !ok {
			return
		}
		removeTempFile(tc.entries[key].path)
		delete(tc.entries, key)
		mcpotel.RecordReadCacheEviction(ctx, mcpotel.ReasonSizePressure)
	}
}

// purge removes every cached file with its per-fetch directory, referenced or
// not, forgets them all, and returns how many it removed.
//
// It is for a process that is ending. Eviction honors references because a
// read is still going to open the file; at the end there is no read left to
// answer, and a file kept for one would outlive the process.
func (tc *tempCache) purge() int {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	n := len(tc.entries)
	for key, e := range tc.entries {
		removeTempFile(e.path)
		delete(tc.entries, key)
	}
	return n
}

// dropUnder forgets every entry whose file lives under dir, referenced or not.
//
// It is for a read root that was removed from under the running process. Every
// file in it went with it, so each such entry is a path to nothing, and its
// size would still count against the cap and push out files that do exist.
// Nothing is removed from disk: there is nothing left there to remove.
func (tc *tempCache) dropUnder(dir string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	prefix := dir + string(filepath.Separator)
	for key, e := range tc.entries {
		if strings.HasPrefix(e.path, prefix) {
			delete(tc.entries, key)
		}
	}
}

// observe publishes how full this cache is and what it is bounded by.
//
// The callbacks take the cache's own lock, which is what makes them safe to run
// on the SDK's collection goroutine, and they read rather than compute: the
// total is a sum over entries, which is the same walk eviction already does.
func (tc *tempCache) observe() {
	mcpotel.ObserveBounded(mcpotel.InstrumentReadCacheBytes, mcpotel.InstrumentReadCacheCapacity, mcpotel.Gauges{
		Current: func() int64 {
			tc.mu.Lock()
			defer tc.mu.Unlock()
			return tc.totalSizeLocked()
		},
		Capacity: func() int64 { return tc.maxBytes },
	})
}

// totalSizeLocked returns the sum of all cached entry sizes; the caller must
// hold tc.mu.
func (tc *tempCache) totalSizeLocked() int64 {
	var total int64
	for _, e := range tc.entries {
		total += e.size
	}
	return total
}

// lruEvictableLocked returns the key of the least-recently-used entry with
// refs==0, or ok=false when no entry is evictable; the caller must hold tc.mu.
func (tc *tempCache) lruEvictableLocked() (string, bool) {
	var (
		lruKey string
		lruAt  time.Time
		found  bool
	)
	for key, e := range tc.entries {
		if e.refs != 0 {
			continue
		}
		if !found || e.atime.Before(lruAt) {
			lruKey, lruAt, found = key, e.atime, true
		}
	}
	return lruKey, found
}

// removeTempFile deletes a cached temp file and, when it lives in a dedicated
// per-fetch subdirectory (created by FetchToTemp), that directory too. Errors are
// ignored: eviction is best-effort cleanup.
func removeTempFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
	if dir := filepath.Dir(path); filepath.Base(dir) != "" {
		_ = os.Remove(dir)
	}
}
