package libgen

import (
	"errors"
	"io/fs"
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

// backdate sets path's modification time two minutes back, past
// readRootLockGrace, so the sweep judges it by its lock rather than leaving it
// for being young.
func backdate(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// holdRoot makes a read root under tmp the way a live process leaves one: the
// directory, with its lock held on a descriptor this test owns until it ends.
// It is backdated, so only the lock keeps it. It returns the root's path.
func holdRoot(t *testing.T, tmp string) string {
	t.Helper()
	dir := newRootDir(t, tmp)
	lock, err := holdReadRootLock(dir)
	if err != nil {
		t.Fatalf("holdReadRootLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	backdate(t, dir)
	return dir
}

// youngDeadRoot makes a read root under tmp the way a process that ended
// leaves one, its lock free, with a cached file in a per-fetch directory
// inside, as the cache leaves them. It has the age it was made with.
func youngDeadRoot(t *testing.T, tmp string) string {
	t.Helper()
	dir := newRootDir(t, tmp)
	fetch := filepath.Join(dir, "fetch-1")
	if err := os.Mkdir(fetch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fetch, "book.pdf"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// deadRoot is youngDeadRoot backdated past the grace: what the sweep removes.
func deadRoot(t *testing.T, tmp string) string {
	t.Helper()
	dir := youngDeadRoot(t, tmp)
	backdate(t, dir)
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
	// A second descriptor on the lock is what another process's sweep opens.
	// It must find the lock taken.
	other, err := openReadRootLock(first)
	if err != nil {
		t.Fatal(err)
	}
	lockErr := tryLockFile(other)
	// Closed before close, not deferred: Windows refuses to remove a directory
	// while a file inside it is still open.
	_ = other.Close()
	if !errors.Is(lockErr, errLockHeld) {
		t.Fatalf("a second descriptor on the live root's lock got %v, want errLockHeld", lockErr)
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

// lockAnswers makes every lock this process tries answer err, and returns a
// count of the attempts.
func lockAnswers(t *testing.T, err error) *atomic.Int32 {
	t.Helper()
	var tries atomic.Int32
	prev := takeLock
	t.Cleanup(func() { takeLock = prev })
	takeLock = func(*os.File) error {
		tries.Add(1)
		return err
	}
	return &tries
}

// TestReadRoot_AHeldLockLeavesNoRootAndIsNotRemembered shows the half-made
// root is removed when another file holds its lock, so a failed start does not
// become the very leftover this file exists to prevent, and that the next
// fetch tries again rather than giving up on roots.
func TestReadRoot_AHeldLockLeavesNoRootAndIsNotRemembered(t *testing.T) {
	tmp := t.TempDir()
	isolateTempDir(t, tmp)
	restore := takeLock
	lockAnswers(t, errLockHeld)

	var r readRoot
	t.Cleanup(r.close)
	if _, err := r.fetchDir(noLoss(t)); !errors.Is(err, errLockHeld) {
		t.Fatalf("fetchDir error = %v, want the held lock", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the temp directory kept %d entries after the lock was held", len(entries))
	}

	takeLock = restore
	if root := rootOf(t, &r); !strings.HasPrefix(filepath.Base(root), readRootPrefix) {
		t.Errorf("the fetch after a held lock went under %q, want a read root", root)
	}
}

// TestReadRoot_UnusableLocksFallBackToLooseFetchDirectories covers a temp
// directory on a filesystem whose locks fail outright (ENOLCK on NFSv3 without
// lockd, ENOSYS on a cluster filesystem mounted without flock). An unlocked
// root would be one any other process's sweep could take and remove mid-read,
// so none is kept: each fetch gets a loose directory under the prefix the
// sweep never matches, the operator is told once, and the lock is not tried
// again.
func TestReadRoot_UnusableLocksFallBackToLooseFetchDirectories(t *testing.T) {
	tmp := t.TempDir()
	isolateTempDir(t, tmp)
	tries := lockAnswers(t, errors.New("no locks on this mount"))
	logs := captureLog(t)
	var r readRoot
	t.Cleanup(r.close)

	for _, fetch := range []string{"first", "second"} {
		t.Run(fetch, func(t *testing.T) {
			dir, err := r.fetchDir(noLoss(t))
			if err != nil {
				t.Fatalf("fetchDir: %v", err)
			}
			if filepath.Dir(dir) != tmp || !strings.HasPrefix(filepath.Base(dir), legacyFetchPrefix) {
				t.Errorf("fetch directory %q is not a loose %s* directory in %q", dir, legacyFetchPrefix, tmp)
			}
		})
	}
	if got := tries.Load(); got != 1 {
		t.Errorf("the lock was tried %d times, want once", got)
	}
	if roots, _ := filepath.Glob(filepath.Join(tmp, readRootPrefix+"*")); len(roots) != 0 {
		t.Errorf("an unlocked read root was left: %v", roots)
	}
	if got := strings.Count(logs.String(), "will not be cleaned up automatically"); got != 1 {
		t.Errorf("the warning was logged %d times, want once: %q", got, logs.String())
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Errorf("the fallback was not logged as a warning: %q", logs.String())
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

// TestHoldReadRootLock covers the three ways taking the lock can fail, each
// with the error that tells the caller which it was: the root is not there to
// lock, another descriptor already holds the lock, and the lock cannot be
// tried at all. Only the last is errLocksUnusable, the one that makes a
// process give up on read roots.
func TestHoldReadRootLock(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		name string
		dir  func(t *testing.T) string
		want error
	}{
		{"the directory does not exist", func(*testing.T) string {
			return filepath.Join(tmp, "no", "such")
		}, fs.ErrNotExist},
		{"another descriptor holds the lock", func(t *testing.T) string {
			t.Helper()
			return holdRoot(t, tmp)
		}, errLockHeld},
		{"the lock cannot be tried", func(t *testing.T) string {
			t.Helper()
			lockAnswers(t, errors.New("no locks on this mount"))
			return newRootDir(t, tmp)
		}, errLocksUnusable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := holdReadRootLock(tc.dir(t))
			if err == nil {
				_ = f.Close()
				t.Fatal("holdReadRootLock succeeded")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
			if !errors.Is(tc.want, errLocksUnusable) && errors.Is(err, errLocksUnusable) {
				t.Errorf("error %v would make the process give up on read roots", err)
			}
			if f != nil {
				t.Errorf("returned a file alongside error %v", err)
			}
		})
	}
}

// TestSweepReadRoots removes exactly the roots no process holds, and nothing
// that is not a read root: live roots, a dead one still inside the grace, the
// per-fetch directories versions up to 2.2.0 left loose in the temp directory,
// a directory that is nobody's root, and a file that merely shares the prefix
// all stay. Every survivor but the young root is backdated past the grace, so
// what keeps it is the rule under test and not its age.
func TestSweepReadRoots(t *testing.T) {
	tmp := t.TempDir()
	deadA, deadB := deadRoot(t, tmp), deadRoot(t, tmp)
	live := holdRoot(t, tmp)
	young := youngDeadRoot(t, tmp)
	legacy := filepath.Join(tmp, legacyFetchPrefix+"123")
	unrelated := filepath.Join(tmp, "someone-elses-dir")
	for _, dir := range []string{legacy, unrelated} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		backdate(t, dir)
	}
	plainFile := filepath.Join(tmp, readRootPrefix+"file")
	if err := os.WriteFile(plainFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	backdate(t, plainFile)

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
	for _, kept := range []string{live, young, legacy, unrelated, plainFile} {
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

// TestAbandonedReadRoot judges one root at a time. Age comes first: a root
// inside the grace is kept whatever its lock says, which is what covers the
// instant between a live process making its root and locking it.
func TestAbandonedReadRoot(t *testing.T) {
	tmp := t.TempDir()
	// The clock is set relative to the root's own modification time, so the
	// boundary case lands exactly on the grace rather than near it.
	atAge := func(t *testing.T, dir string, age time.Duration) {
		t.Helper()
		info, err := os.Lstat(dir)
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
		{"its lock is free, younger than the grace", func(t *testing.T) string {
			t.Helper()
			dir := youngDeadRoot(t, tmp)
			atAge(t, dir, readRootLockGrace()-time.Nanosecond)
			return dir
		}, false},
		{"its lock is free, exactly the grace old", func(t *testing.T) string {
			t.Helper()
			dir := youngDeadRoot(t, tmp)
			atAge(t, dir, readRootLockGrace())
			return dir
		}, true},
		{"it is a file, not a directory", func(t *testing.T) string {
			t.Helper()
			path := filepath.Join(tmp, readRootPrefix+"file")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			backdate(t, path)
			return path
		}, false},
		{"the root itself is gone", func(*testing.T) string { return filepath.Join(tmp, readRootPrefix+"gone") }, false},
		{"its lock cannot be tried", func(t *testing.T) string {
			t.Helper()
			dir := deadRoot(t, tmp)
			lockAnswers(t, errors.New("no locks on this mount"))
			return dir
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := abandonedReadRoot(tc.dir(t)); got != tc.want {
				t.Errorf("abandonedReadRoot = %v, want %v", got, tc.want)
			}
		})
	}
}
