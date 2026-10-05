package libgen

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// readRootPrefix names the directory under the OS temp directory that holds
// every file this process fetches for read.
//
// It is deliberately not "libgen-read-", the name each fetch's own directory
// carried directly under the temp directory up to 2.2.0. Those are left alone
// by the sweep below, because nothing marks which process made one: a server
// of that version may still be running beside this one and using it.
const readRootPrefix = "libgen-mcp-read-"

// fetchDirPattern names the directory each fetch downloads into, inside the
// read root.
const fetchDirPattern = "fetch-*"

// legacyFetchPrefix names a fetch directory made loose in the temp directory,
// the layout every fetch had up to 2.2.0. A process whose temp directory has
// no working lock goes back to it (see readRoot.unlocked), because the sweep
// never matches this prefix, so no other process can take such a directory
// for abandoned while this one still reads from it.
const legacyFetchPrefix = "libgen-read-"

// errLockHeld is what tryLockFile answers when another open file holds the
// lock. Every other error it returns means the lock could not be tried at all.
var errLockHeld = errors.New("the lock is held by another open file")

// errLocksUnusable marks a lock that could not be tried at all, as opposed to
// one another file holds (see holdReadRootLock).
var errLocksUnusable = errors.New("file locks do not work here")

// readRootLockGrace is how long a read root that holds no fetch directory
// must go unmodified before the sweep judges it at all. A younger one is left
// alone whatever its lock says.
//
// A live process makes its root and then locks it, so for an instant a live
// root's lock is free. A sweep landing in that instant would take the free
// lock for a dead owner and remove the root its owner is about to use, which
// was measured happening 11 times in 9,600 processes started together. A
// minute is far past that instant. Past it, a root whose lock is free is dead
// however its process ended, including one that died between the two steps.
//
// A root that holds a fetch directory is past that instant whatever its age,
// because its owner makes the first one only once it holds the lock. That is
// what lets a server restarted seconds after it was killed, which is what a
// supervisor does, remove the files the killed one left on its first fetch:
// that root changed last when its owner fetched, seconds earlier, and the
// sweep runs once per process, so a rule on age alone would leave it for the
// whole life of the new process.
func readRootLockGrace() time.Duration { return time.Minute }

// readRootNow is time.Now, as a seam: a root's age is compared against
// readRootLockGrace, and only a clock the test sets can land exactly on the
// boundary.
var readRootNow = time.Now

// takeLock is tryLockFile, as a seam: the lock of a root this process has
// just created is always free, and a filesystem whose locks do not work is not
// one a test can mount, so only a substitute can make taking a lock fail
// either way.
var takeLock = tryLockFile

// readRoot is this process's directory for read fetches: one per process,
// created on first use, holding a lock for as long as the process holds it.
//
// It exists because the files a read fetches outlived the process that fetched
// them. The cache removes them on eviction, and eviction only runs inside a
// live process, so whatever the cache still held when the process ended stayed
// on disk for good: one directory per restart, accumulating wherever TMPDIR
// pointed. Close removes this directory when the process ends cleanly; the
// lock is what lets the next process remove it when it did not.
type readRoot struct {
	mu   sync.Mutex
	dir  string
	lock *os.File

	// unlocked is set, for the rest of the process's life, once taking a new
	// root's lock failed for any reason but another holder: the temp
	// directory's filesystem has no lock that works (NFS without lockd, a
	// cluster filesystem mounted without flock, a FUSE mount that refuses
	// it). An unlocked root is the one thing this must not make, because
	// another process's sweep could take its free lock and remove it
	// mid-read. Each fetch's directory is made loose in the temp directory
	// instead, as up to 2.2.0, which the sweep never matches.
	unlocked bool
}

// fetchDir makes the directory one fetch downloads into, under the read root,
// and returns it.
//
// A root removed from under the running process (an operator clearing the
// temp directory, a cleaner aging it out) is noticed here, because the
// directory cannot be made inside it: the root is given up, onLost is told
// which one so the cache can forget the files that went with it, and a new
// root is made once. Without that, every fetch failed for the rest of the
// process's life. onLost runs with r.mu held, so it must not call back into
// the root.
func (r *readRoot) fetchDir(onLost func(root string)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dir, err := r.makeFetchDirLocked()
	if r.dir == "" || !errors.Is(err, fs.ErrNotExist) {
		return dir, err
	}
	lost := r.dir
	r.dropLocked()
	onLost(lost)
	return r.makeFetchDirLocked()
}

// makeFetchDirLocked makes one fetch directory under the root, making the
// root first when there is none, or loose in the temp directory when this
// process has no root to make (see unlocked). The caller holds r.mu.
func (r *readRoot) makeFetchDirLocked() (string, error) {
	root, err := r.pathLocked()
	if err != nil {
		return "", err
	}
	if root == "" {
		return os.MkdirTemp("", legacyFetchPrefix+"*")
	}
	return os.MkdirTemp(root, fetchDirPattern)
}

// pathLocked returns the read root, creating it on first use, or "" once
// this process has found its temp directory cannot lock one (see unlocked).
// The caller holds r.mu.
//
// The first creation also sweeps the roots dead processes left behind, so a
// server that never fetches a file never touches the temp directory. A failure
// is returned and not remembered, so a temp directory that comes back serves
// the next read rather than failing every read for the life of the process.
func (r *readRoot) pathLocked() (string, error) {
	if r.dir != "" || r.unlocked {
		return r.dir, nil
	}
	tmp := os.TempDir()
	if n := sweepReadRoots(tmp); n > 0 {
		slog.Info("removed read files left by servers that did not exit cleanly", "directories", n, "dir", tmp)
	}
	dir, err := os.MkdirTemp("", readRootPrefix+"*")
	if err != nil {
		return "", err
	}
	lock, err := holdReadRootLock(dir)
	if err == nil {
		r.dir, r.lock = dir, lock
		return dir, nil
	}
	_ = os.RemoveAll(dir)
	if !errors.Is(err, errLocksUnusable) {
		return "", err
	}
	r.unlocked = true
	slog.Warn("file locks do not work in the temp directory, so read files a killed server leaves there will not be cleaned up automatically",
		"dir", tmp, "error", err)
	return "", nil
}

// close removes the read root with everything under it and gives up its lock.
// It is safe to call more than once, and on a root that was never created.
func (r *readRoot) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropLocked()
}

// dropLocked gives up the root: its lock is released and whatever is left of
// the directory is removed. The caller holds r.mu.
func (r *readRoot) dropLocked() {
	if r.dir == "" {
		return
	}
	// The lock goes first: Windows refuses to remove a file that is still open.
	_ = unlockFile(r.lock)
	_ = r.lock.Close()
	_ = os.RemoveAll(r.dir)
	r.dir, r.lock = "", nil
}

// holdReadRootLock takes the lock of the read root at dir and keeps the file
// that holds it open, so the lock lasts until close or until the process ends.
// What that file is depends on the platform (see openReadRootLock).
//
// Only errLockHeld means another file holds the lock. Any other failure to
// take it (ENOLCK on NFSv3 without lockd, ENOSYS on a cluster filesystem
// mounted without flock, ENOTSUP, a platform with no lock at all) is wrapped
// in errLocksUnusable, because there it says nothing about who holds what:
// the lock cannot be relied on, here or in any other process's sweep.
func holdReadRootLock(dir string) (*os.File, error) {
	f, err := openReadRootLock(dir)
	if err != nil {
		return nil, err
	}
	lockErr := takeLock(f)
	if lockErr == nil {
		return f, nil
	}
	_ = f.Close()
	if errors.Is(lockErr, errLockHeld) {
		return nil, lockErr
	}
	return nil, fmt.Errorf("%w: %w", errLocksUnusable, lockErr)
}

// sweepReadRoots removes every read root under tmp whose process is gone, and
// returns how many it removed. Nothing else is touched: an entry without the
// read root's prefix is not ours to judge, and abandonedReadRoot says which of
// the rest are.
func sweepReadRoots(tmp string) int {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), readRootPrefix) {
			continue
		}
		dir := filepath.Join(tmp, e.Name())
		if abandonedReadRoot(dir) && os.RemoveAll(dir) == nil {
			removed++
		}
	}
	return removed
}

// abandonedReadRoot reports whether the read root at dir is one no live
// process holds.
//
// Three things must all be true, and anything that cannot be read leaves the
// root where it is:
//
//   - It is a real directory this user owns, judged without following a link
//     (see ownedDirectory). The temp directory is shared on Unix, and an entry
//     another user planted under the prefix is not ours to open, let alone to
//     remove.
//   - It holds a fetch directory, or it has gone readRootLockGrace without a
//     change. Only a root that holds none can be one its owner has made and
//     not yet locked.
//   - Its lock can be taken. A process that is alive holds it, and the
//     operating system releases it when the process ends, however it ended,
//     so taking it here is proof the owner is gone. It is released again at
//     once, before the root is removed. A lock that cannot be tried at all is
//     no proof of anything, so a lock that fails for any reason, held or not,
//     keeps the root.
//
// The order is what makes the second rule sound: the fetch directory is
// looked for before the lock is tried. Looked for after, it could be one a
// live owner made between the two, having locked its root just after the
// sweep found the lock free.
func abandonedReadRoot(dir string) bool {
	info, err := os.Lstat(dir)
	if err != nil || !ownedDirectory(info) {
		return false
	}
	if readRootNow().Sub(info.ModTime()) < readRootLockGrace() && !holdsFetchDir(dir) {
		return false
	}
	return readRootLockFree(dir)
}

// holdsFetchDir reports whether the read root at dir holds at least one
// fetch directory. A root that cannot be opened or listed holds none, which
// leaves the age rule to judge it.
func holdsFetchDir(dir string) bool {
	f, err := openReadRootDir(dir)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	names, _ := f.Readdirnames(0) // every name: a root holds one entry per cached file
	for _, name := range names {
		if ok, _ := filepath.Match(fetchDirPattern, name); ok {
			return true
		}
	}
	return false
}

// readRootLockFree takes the lock of the read root at dir and gives it back,
// and reports whether it could.
func readRootLockFree(dir string) bool {
	f, err := openReadRootLock(dir)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if takeLock(f) != nil {
		return false
	}
	_ = unlockFile(f)
	return true
}
