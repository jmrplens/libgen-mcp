//go:build windows

package libgen

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestTryLockFile_ConflictsAcrossDescriptorsAndUnlocks pins the two properties
// the read root's sweep rests on: a second descriptor on the same file cannot
// take a lock the first holds, even inside one process, and it can once the
// first unlocks.
func TestTryLockFile_ConflictsAcrossDescriptorsAndUnlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), readRootLockName)
	open := func() *os.File {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	first, second := open(), open()

	if err := tryLockFile(first); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := tryLockFile(second); !errors.Is(err, errLockHeld) {
		t.Fatalf("a second descriptor trying a lock the first holds got %v, want errLockHeld", err)
	}
	if err := unlockFile(first); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := tryLockFile(second); err != nil {
		t.Errorf("the lock stayed taken after unlock: %v", err)
	}
}
