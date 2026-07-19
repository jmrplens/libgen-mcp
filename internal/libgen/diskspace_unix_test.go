//go:build unix

package libgen

import "testing"

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
