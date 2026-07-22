package libgen

import (
	"os"
	"path/filepath"
	"sync"
	"time"
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
// eviction; it returns ("", false) on a miss.
func (tc *tempCache) get(key string) (string, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	e, ok := tc.entries[key]
	if !ok {
		return "", false
	}
	e.refs++
	e.atime = time.Now()
	return e.path, true
}

// put stores a freshly downloaded file under key with refs=1 (the caller holds
// one reference) and then runs an eviction pass to stay within the size cap and
// TTL. If key already holds an entry it is overwritten (the caller's fresh copy
// wins); the previous backing file is removed if it differs and is unreferenced.
func (tc *tempCache) put(key, path string, size int64) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if prev, ok := tc.entries[key]; ok && prev.refs == 0 && prev.path != path {
		removeTempFile(prev.path)
	}
	tc.entries[key] = &tempEntry{path: path, size: size, refs: 1, atime: time.Now()}
	tc.evictLocked()
}

// getOrPut atomically resolves a just-downloaded file against the cache: on a hit
// it behaves like get (refs++, atime refreshed) and returns the existing path
// with isNew=false, so the caller discards its duplicate download; on a miss it
// stores the file with refs=1, runs an eviction pass, and returns it with
// isNew=true. Doing both under one lock closes the window where two concurrent
// fetches of the same key would each insert and leak one copy.
func (tc *tempCache) getOrPut(key, path string, size int64) (stored string, isNew bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if e, ok := tc.entries[key]; ok {
		e.refs++
		e.atime = time.Now()
		return e.path, false
	}
	tc.entries[key] = &tempEntry{path: path, size: size, refs: 1, atime: time.Now()}
	tc.evictLocked()
	return path, true
}

// release decrements the refcount for key (never below zero) and refreshes its
// atime so the TTL clock starts from the last use. It is a no-op for an unknown
// key.
func (tc *tempCache) release(key string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	e, ok := tc.entries[key]
	if !ok {
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
func (tc *tempCache) evict() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.evictLocked()
}

// evictLocked is evict's body; the caller must hold tc.mu. It first drops every
// TTL-expired unreferenced entry, then, while the total size is over the cap,
// drops the least-recently-used unreferenced entry until it is within the cap or
// no evictable entry remains.
func (tc *tempCache) evictLocked() {
	now := time.Now()
	for key, e := range tc.entries {
		if e.refs == 0 && tc.ttl >= 0 && now.Sub(e.atime) >= tc.ttl {
			removeTempFile(e.path)
			delete(tc.entries, key)
		}
	}
	for tc.totalSizeLocked() > tc.maxBytes {
		key, ok := tc.lruEvictableLocked()
		if !ok {
			return
		}
		removeTempFile(tc.entries[key].path)
		delete(tc.entries, key)
	}
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
