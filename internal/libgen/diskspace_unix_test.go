//go:build unix

package libgen

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestFreeSpaceError verifies that probing a nonexistent path surfaces the statfs
// error rather than a bogus free-space value.
func TestFreeSpaceError(t *testing.T) {
	if _, err := freeSpace("/nonexistent/path/that/should/not/exist/libgen-mcp"); err == nil {
		t.Fatal("freeSpace() on a nonexistent path should return an error")
	}
}

// TestFreeSpaceOK verifies that probing a real directory reports a plausible
// (non-zero) amount of available space.
func TestFreeSpaceOK(t *testing.T) {
	free, err := freeSpace(t.TempDir())
	if err != nil {
		t.Fatalf("freeSpace() error = %v", err)
	}
	if free == 0 {
		t.Error("freeSpace() = 0 on a real temp dir, want > 0")
	}
}

// TestFreeSpaceNonPositiveBsize covers the non-positive block-size guard by
// overriding the statfs seam to report a zero Bsize. Real filesystems always
// report a positive block size, so this guard is otherwise unreachable; when it
// trips, freeSpace reports zero free space without an error.
func TestFreeSpaceNonPositiveBsize(t *testing.T) {
	orig := statfs
	statfs = func(_ string, st *unix.Statfs_t) error {
		*st = unix.Statfs_t{}
		st.Bavail = 1000
		st.Bsize = 0
		return nil
	}
	t.Cleanup(func() { statfs = orig })

	free, err := freeSpace("/anything")
	if err != nil {
		t.Fatalf("freeSpace() error = %v", err)
	}
	if free != 0 {
		t.Errorf("freeSpace() with non-positive Bsize = %d, want 0", free)
	}
}
