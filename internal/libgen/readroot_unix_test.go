//go:build unix

package libgen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSweepReadRoots_LeavesWhatIsNotThisUsersOwnDirectory covers the shared
// /tmp of a Unix host, where another local user can put anything under the
// read root's prefix. The sweep removes only a real directory this user owns:
// another user's root stays, however dead it looks, and a symbolic link
// planted under the prefix is neither followed nor removed, so the directory
// it points at survives too.
func TestSweepReadRoots_LeavesWhatIsNotThisUsersOwnDirectory(t *testing.T) {
	t.Run("another user's root", func(t *testing.T) {
		tmp := t.TempDir()
		foreign := deadRoot(t, tmp)
		asAnotherUser(t)
		if got := sweepReadRoots(tmp); got != 0 {
			t.Errorf("sweepReadRoots = %d, want 0", got)
		}
		if !exists(foreign) {
			t.Errorf("another user's root %q was removed", foreign)
		}
	})
	t.Run("a symbolic link to a dead root", func(t *testing.T) {
		tmp := t.TempDir()
		target := deadRoot(t, t.TempDir())
		link := filepath.Join(tmp, readRootPrefix+"link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if got := sweepReadRoots(tmp); got != 0 {
			t.Errorf("sweepReadRoots = %d, want 0", got)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Errorf("the planted link was removed: %v", err)
		}
		if !exists(filepath.Join(target, "fetch-1", "book.pdf")) {
			t.Errorf("the sweep followed the link and emptied %q", target)
		}
	})
}

// TestSweepReadRoots_KeepsALiveRootAnAgeCleanerEmptied is why the lock is on
// the directory itself on Unix. An age-based temp cleaner (systemd-tmpfiles
// before 254, tmpreaper, tmpwatch) deletes files nobody has touched for days,
// and a lock file inside the root was one of them: the next sweep then found a
// root with no lock, old enough to remove, and removed it from under its live
// owner. With the lock on the directory there is nothing inside for a cleaner
// to take, so an emptied, old root whose owner is alive stays.
func TestSweepReadRoots_KeepsALiveRootAnAgeCleanerEmptied(t *testing.T) {
	tmp := t.TempDir()
	live := holdRoot(t, tmp)
	if err := os.WriteFile(filepath.Join(live, "stale.pdf"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(live)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if rmErr := os.RemoveAll(filepath.Join(live, e.Name())); rmErr != nil {
			t.Fatal(rmErr)
		}
	}
	backdate(t, live)

	if got := sweepReadRoots(tmp); got != 0 {
		t.Errorf("sweepReadRoots = %d, want 0", got)
	}
	if !exists(live) {
		t.Errorf("the live root %q was removed after a cleaner emptied it", live)
	}
}
