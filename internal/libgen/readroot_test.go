package libgen

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rootSeq numbers the read roots these tests make, so each has a name of its
// own under the prefix the sweep looks for.
var rootSeq atomic.Int64

// newRootDir makes an empty directory under tmp named as a read root is.
func newRootDir(t *testing.T, tmp string) string {
	t.Helper()
	dir := filepath.Join(tmp, readRootPrefix+strconv.FormatInt(rootSeq.Add(1), 10))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// holdRoot makes a read root under tmp the way a live process leaves one: the
// directory, its lock file, and the lock held on a descriptor this test owns
// until it ends. It returns the root's path.
func holdRoot(t *testing.T, tmp string) string {
	t.Helper()
	dir := newRootDir(t, tmp)
	lock, err := holdReadRootLock(filepath.Join(dir, readRootLockName))
	if err != nil {
		t.Fatalf("holdReadRootLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return dir
}

// deadRoot makes a read root under tmp the way a process that ended leaves
// one: the lock file is there and nobody holds it. A cached file sits in a
// per-fetch directory inside, as the cache leaves them.
func deadRoot(t *testing.T, tmp string) string {
	t.Helper()
	dir := newRootDir(t, tmp)
	if err := os.WriteFile(filepath.Join(dir, readRootLockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fetch := filepath.Join(dir, "fetch-1")
	if err := os.Mkdir(fetch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fetch, "book.pdf"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// noLoss is the onLost of a fetch that must find its root where it left it.
func noLoss(t *testing.T) func(string) {
	t.Helper()
	return func(root string) { t.Errorf("root %q was reported lost", root) }
}

// rootOf makes one fetch directory and returns the root it was made under.
func rootOf(t *testing.T, r *readRoot) string {
	t.Helper()
	dir, err := r.fetchDir(noLoss(t))
	if err != nil {
		t.Fatalf("fetchDir: %v", err)
	}
	return filepath.Dir(dir)
}

// removeRootFromUnder deletes the root r holds, the way an operator clearing
// the temp directory or a cleaner aging it out would, and returns its path.
//
// Windows refuses to delete a file that is still open, so there the handle
// holding the lock is closed first, which is also why nothing can take a live
// root away on Windows. On Unix the held lock does not stop the removal, and
// that is the case the root has to notice.
func removeRootFromUnder(t *testing.T, r *readRoot) string {
	t.Helper()
	r.mu.Lock()
	root, lock := r.dir, r.lock
	r.mu.Unlock()
	if runtime.GOOS == "windows" {
		_ = lock.Close()
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestReadRoot_IsCreatedOnceLockedAndRemovedOnClose pins the root's life: one
// directory per process however many fetches it holds, its lock held for as
// long as the root exists, and nothing left once close runs.
func TestReadRoot_IsCreatedOnceLockedAndRemovedOnClose(t *testing.T) {
	isolateTempDir(t, t.TempDir())
	var r readRoot
	t.Cleanup(r.close)

	first := rootOf(t, &r)
	if !strings.HasPrefix(filepath.Base(first), readRootPrefix) {
		t.Errorf("root %q does not carry %q", first, readRootPrefix)
	}
	if again := rootOf(t, &r); again != first {
		t.Fatalf("second fetch went under %q; want the same root %q", again, first)
	}
	// A second descriptor on the lock file is what another process's sweep
	// opens. It must find the lock taken.
	other, err := os.OpenFile(filepath.Join(first, readRootLockName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	locked := tryLockFile(other) == nil
	// Closed before close, not deferred: Windows refuses to remove a directory
	// while a file inside it is still open.
	_ = other.Close()
	if locked {
		t.Fatal("the live root's lock could be taken from a second descriptor")
	}

	r.close()
	if exists(first) {
		t.Errorf("close left %q on disk", first)
	}
	r.close() // a second close has nothing to do and must not panic
}

// TestReadRoot_CloseWithoutARootIsANoOp covers the server that never read: it
// made no root, and closing must not touch anything.
func TestReadRoot_CloseWithoutARootIsANoOp(t *testing.T) {
	var r readRoot
	r.close()
	if r.dir != "" || r.lock != nil {
		t.Errorf("close changed an unused root: dir=%q lock=%v", r.dir, r.lock)
	}
}

// TestReadRoot_AFailureIsNotRemembered keeps a temp directory that was missing
// for one read from failing every read after it.
func TestReadRoot_AFailureIsNotRemembered(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "later")
	isolateTempDir(t, missing)
	var r readRoot
	t.Cleanup(r.close)

	if _, err := r.fetchDir(noLoss(t)); err == nil {
		t.Fatal("fetchDir succeeded under a temp directory that does not exist")
	}
	if err := os.Mkdir(missing, 0o700); err != nil {
		t.Fatal(err)
	}
	if root := rootOf(t, &r); filepath.Dir(root) != missing {
		t.Errorf("root %q is not under %q", root, missing)
	}
}

// TestReadRoot_ARemovedRootIsReplacedOnce covers a root taken away from under
// the running process. The fetch that finds it gone gives it up, says which
// root was lost, and makes a new one, so the process does not fail every
// fetch for the rest of its life. When the new one cannot be made either, the
// fetch fails and the next one tries again.
func TestReadRoot_ARemovedRootIsReplacedOnce(t *testing.T) {
	t.Run("replaced", func(t *testing.T) {
		isolateTempDir(t, t.TempDir())
		var r readRoot
		t.Cleanup(r.close)
		first := rootOf(t, &r)
		removed := removeRootFromUnder(t, &r)
		if removed != first {
			t.Fatalf("removed %q, want the root %q", removed, first)
		}

		var lost []string
		dir, err := r.fetchDir(func(root string) { lost = append(lost, root) })
		if err != nil {
			t.Fatalf("fetchDir after the root was removed: %v", err)
		}
		if len(lost) != 1 || lost[0] != first {
			t.Errorf("lost roots = %v, want only %q", lost, first)
		}
		if root := filepath.Dir(dir); root == first || !exists(dir) {
			t.Errorf("the fetch went to %q, want a new root in place of %q", dir, first)
		}
		if next := rootOf(t, &r); next != filepath.Dir(dir) {
			t.Errorf("the fetch after the replacement went under %q, want %q", next, filepath.Dir(dir))
		}
	})
	t.Run("the replacement fails, the next fetch recovers", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "tmp")
		if err := os.Mkdir(tmp, 0o700); err != nil {
			t.Fatal(err)
		}
		isolateTempDir(t, tmp)
		var r readRoot
		t.Cleanup(r.close)
		first := rootOf(t, &r)
		removeRootFromUnder(t, &r)
		if err := os.Remove(tmp); err != nil {
			t.Fatal(err)
		}

		var lost []string
		if _, err := r.fetchDir(func(root string) { lost = append(lost, root) }); err == nil {
			t.Fatal("fetchDir succeeded with no temp directory to make a root in")
		}
		if len(lost) != 1 || lost[0] != first {
			t.Errorf("lost roots = %v, want only %q", lost, first)
		}
		if err := os.Mkdir(tmp, 0o700); err != nil {
			t.Fatal(err)
		}
		if root := rootOf(t, &r); filepath.Dir(root) != tmp {
			t.Errorf("the next fetch went under %q, want a new root in %q", root, tmp)
		}
	})
}

// TestReadRoot_ALockFailureLeavesNoRoot shows the half-made root is removed
// when its lock cannot be taken, so a failed start does not become the very
// leftover this file exists to prevent.
func TestReadRoot_ALockFailureLeavesNoRoot(t *testing.T) {
	tmp := t.TempDir()
	isolateTempDir(t, tmp)
	refused := errors.New("lock refused")
	prev := lockReadRoot
	t.Cleanup(func() { lockReadRoot = prev })
	lockReadRoot = func(string) (*os.File, error) { return nil, refused }

	var r readRoot
	if _, err := r.fetchDir(noLoss(t)); !errors.Is(err, refused) {
		t.Fatalf("fetchDir error = %v, want the lock failure", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the temp directory kept %d entries after the lock failed", len(entries))
	}
}

// TestReadRoot_FirstUseSweepsAndSaysSo checks the sweep runs when the root is
// first made, and that the log line names it only when something was removed.
func TestReadRoot_FirstUseSweepsAndSaysSo(t *testing.T) {
	t.Run("a dead root is removed and reported", func(t *testing.T) {
		tmp := t.TempDir()
		isolateTempDir(t, tmp)
		dead := deadRoot(t, tmp)
		logs := captureLog(t)
		var r readRoot
		t.Cleanup(r.close)

		rootOf(t, &r)
		if exists(dead) {
			t.Errorf("first use left the dead root %q", dead)
		}
		if !strings.Contains(logs.String(), `"directories":1`) {
			t.Errorf("no record of the sweep in %q", logs.String())
		}
	})
	t.Run("nothing to remove, nothing said", func(t *testing.T) {
		isolateTempDir(t, t.TempDir())
		logs := captureLog(t)
		var r readRoot
		t.Cleanup(r.close)

		rootOf(t, &r)
		if logs.Len() != 0 {
			t.Errorf("a sweep that removed nothing logged %q", logs.String())
		}
	})
}

// TestHoldReadRootLock covers both ways taking the lock can fail: no file can
// be made at the path, and another descriptor already holds it.
func TestHoldReadRootLock(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"the directory does not exist", func(*testing.T) string {
			return filepath.Join(tmp, "no", "such", readRootLockName)
		}},
		{"another descriptor holds the lock", func(t *testing.T) string {
			t.Helper()
			return filepath.Join(holdRoot(t, tmp), readRootLockName)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := holdReadRootLock(tc.path(t))
			if err == nil {
				_ = f.Close()
				t.Fatal("holdReadRootLock succeeded")
			}
			if f != nil {
				t.Errorf("returned a file alongside error %v", err)
			}
		})
	}
}

// TestSweepReadRoots removes exactly the roots no process holds, and nothing
// that is not a read root: live roots, the per-fetch directories versions up
// to 2.2.0 left loose in the temp directory, and files that merely share the
// prefix all stay.
func TestSweepReadRoots(t *testing.T) {
	tmp := t.TempDir()
	deadA, deadB := deadRoot(t, tmp), deadRoot(t, tmp)
	live := holdRoot(t, tmp)
	legacy := filepath.Join(tmp, "libgen-read-123")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	plainFile := filepath.Join(tmp, readRootPrefix+"file")
	if err := os.WriteFile(plainFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := sweepReadRoots(tmp); got != 2 {
		t.Errorf("sweepReadRoots = %d, want 2", got)
	}
	for _, gone := range []string{deadA, deadB} {
		t.Run("removed "+filepath.Base(gone), func(t *testing.T) {
			if exists(gone) {
				t.Errorf("%q survived the sweep", gone)
			}
		})
	}
	for _, kept := range []string{live, legacy, plainFile} {
		t.Run("kept "+filepath.Base(kept), func(t *testing.T) {
			if !exists(kept) {
				t.Errorf("%q was removed", kept)
			}
		})
	}
}

// TestSweepReadRoots_UnreadableTempDirRemovesNothing keeps a sweep of a temp
// directory that cannot be listed from failing the read that started it.
func TestSweepReadRoots_UnreadableTempDirRemovesNothing(t *testing.T) {
	if got := sweepReadRoots(filepath.Join(t.TempDir(), "absent")); got != 0 {
		t.Errorf("sweepReadRoots = %d, want 0", got)
	}
}

// TestAbandonedReadRoot judges one root at a time, including the roots with no
// lock file to ask, which are judged by their age against the grace.
func TestAbandonedReadRoot(t *testing.T) {
	tmp := t.TempDir()
	lockless := func(t *testing.T) string {
		t.Helper()
		return newRootDir(t, tmp)
	}
	// The clock is set relative to the root's own modification time, so the
	// boundary case lands exactly on the grace rather than near it.
	atAge := func(t *testing.T, dir string, age time.Duration) {
		t.Helper()
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		prev := readRootNow
		t.Cleanup(func() { readRootNow = prev })
		readRootNow = func() time.Time { return info.ModTime().Add(age) }
	}
	cases := []struct {
		name string
		dir  func(t *testing.T) string
		want bool
	}{
		{"its lock is free", func(t *testing.T) string { t.Helper(); return deadRoot(t, tmp) }, true},
		{"its lock is held", func(t *testing.T) string { t.Helper(); return holdRoot(t, tmp) }, false},
		{"no lock file, younger than the grace", func(t *testing.T) string {
			t.Helper()
			dir := lockless(t)
			atAge(t, dir, readRootLockGrace()-time.Nanosecond)
			return dir
		}, false},
		{"no lock file, exactly the grace old", func(t *testing.T) string {
			t.Helper()
			dir := lockless(t)
			atAge(t, dir, readRootLockGrace())
			return dir
		}, true},
		{"the lock is not a file", func(t *testing.T) string {
			t.Helper()
			dir := lockless(t)
			if err := os.Mkdir(filepath.Join(dir, readRootLockName), 0o700); err != nil {
				t.Fatal(err)
			}
			return dir
		}, false},
		{"the root itself is gone", func(*testing.T) string { return filepath.Join(tmp, readRootPrefix+"gone") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := abandonedReadRoot(tc.dir(t)); got != tc.want {
				t.Errorf("abandonedReadRoot = %v, want %v", got, tc.want)
			}
		})
	}
}
