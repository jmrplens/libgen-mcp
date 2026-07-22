package libgen

import (
	"os"
	"testing"
	"time"
)

// writeTempFile creates a real temp file with the given contents and returns its
// path and size, so eviction's os.Remove is genuinely exercised against disk.
func writeTempFile(t *testing.T, contents string) (string, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "tempcache-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, werr := f.WriteString(contents); werr != nil {
		t.Fatalf("WriteString: %v", werr)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	info, err := os.Stat(f.Name())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	return f.Name(), info.Size()
}

// TestTempCache_GetMissThenPutHit verifies that a fresh key misses, that after
// put the same key hits and returns its stored path, and that an unrelated key
// still misses.
func TestTempCache_GetMissThenPutHit(t *testing.T) {
	tc := newTempCache(1<<30, time.Minute)
	path, size := writeTempFile(t, "hello world")

	if _, ok := tc.get("md5-a"); ok {
		t.Fatal("get before put should miss")
	}
	tc.put("md5-a", path, size)
	got, ok := tc.get("md5-a")
	if !ok {
		t.Fatal("get after put should hit")
	}
	if got != path {
		t.Errorf("get returned %q, want %q", got, path)
	}
	if _, absent := tc.get("md5-absent"); absent {
		t.Fatal("get of an absent key should miss")
	}
	// A second, distinct key stores independently and hits on its own path.
	path2, size2 := writeTempFile(t, "second entry")
	tc.put("md5-b", path2, size2)
	if gotB, okB := tc.get("md5-b"); !okB || gotB != path2 {
		t.Errorf("get(md5-b) = %q, %v; want %q, true", gotB, okB, path2)
	}
}

// TestTempCache_ReleaseAllowsEviction verifies that once every ref is released
// (refs==0) a TTL-expired entry is evicted: its backing file is removed from disk
// and a subsequent get misses.
func TestTempCache_ReleaseAllowsEviction(t *testing.T) {
	tc := newTempCache(1<<30, 0) // ttl=0: any past atime is expired
	path, size := writeTempFile(t, "evict me please")

	tc.put("md5-a", path, size) // refs=1 (the putting caller holds one ref)
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("file should exist after put: %v", statErr)
	}
	if _, ok := tc.get("md5-a"); !ok { // refs=2
		t.Fatal("get should hit")
	}
	tc.release("md5-a") // refs=1
	tc.release("md5-a") // refs=0

	tc.evict()

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("file should be removed after eviction, stat err = %v", statErr)
	}
	if _, ok := tc.get("md5-a"); ok {
		t.Error("get after eviction should miss")
	}
}

// TestTempCache_ReleaseAllowsSizeEviction verifies that a released entry whose
// size exceeds maxBytes is evicted by the size cap even when it is not yet TTL
// expired.
func TestTempCache_ReleaseAllowsSizeEviction(t *testing.T) {
	path, size := writeTempFile(t, "some bytes over the cap")
	tc := newTempCache(size-1, time.Hour) // maxBytes below the entry size

	tc.put("md5-a", path, size) // refs=1
	tc.release("md5-a")         // refs=0

	tc.evict()

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("oversized released entry should be evicted, stat err = %v", statErr)
	}
	if _, ok := tc.get("md5-a"); ok {
		t.Error("get after size eviction should miss")
	}
}

// TestTempCache_RefcountBlocksEviction verifies that an entry whose refs>0 is
// never evicted, even when both the TTL and the size cap would otherwise remove
// it: the file remains on disk and the entry still hits.
func TestTempCache_RefcountBlocksEviction(t *testing.T) {
	path, size := writeTempFile(t, "held open while read is in progress")
	tc := newTempCache(size-1, 0) // both ttl (0) and size cap would evict a refs==0 entry

	tc.put("md5-a", path, size) // refs=1, held

	tc.evict()

	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("held file (refs>0) must not be removed: %v", statErr)
	}
	got, ok := tc.get("md5-a")
	if !ok || got != path {
		t.Errorf("held entry should still hit: got=%q ok=%v", got, ok)
	}
}
