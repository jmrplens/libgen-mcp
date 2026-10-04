package libgen

import (
	"errors"
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

// readRootLockName is the file inside a read root whose lock says its process
// is alive.
const readRootLockName = ".lock"

// readRootLockGrace is how old a read root found without its lock file must be
// before the sweep takes it for abandoned. A live process creates the root and
// then the lock file inside it, so for an instant a live root has none; a
// process that died in that instant leaves a root that stays without one.
func readRootLockGrace() time.Duration { return time.Minute }

// readRootNow is time.Now, as a seam: the age a root without a lock file is
// judged by is compared against readRootLockGrace, and only a clock the test
// sets can land exactly on the boundary.
var readRootNow = time.Now

// lockReadRoot is holdReadRootLock, as a seam: the lock file of a root this
// process has just created is always free, so only a substitute can make
// taking it fail and show the half-made root is not left behind.
var lockReadRoot = holdReadRootLock

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
}

// path returns the read root, creating it on first use.
//
// The first creation also sweeps the roots dead processes left behind, so a
// server that never reads never touches the temp directory. A failure is
// returned and not remembered, so a temp directory that comes back serves the
// next read rather than failing every read for the life of the process.
func (r *readRoot) path() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dir != "" {
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
	lock, err := lockReadRoot(filepath.Join(dir, readRootLockName))
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	r.dir, r.lock = dir, lock
	return dir, nil
}

// close removes the read root with everything under it and gives up its lock.
// It is safe to call more than once, and on a root that was never created.
func (r *readRoot) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dir == "" {
		return
	}
	// The lock goes first: Windows refuses to remove a file that is still open.
	_ = unlockFile(r.lock)
	_ = r.lock.Close()
	_ = os.RemoveAll(r.dir)
	r.dir, r.lock = "", nil
}

// holdReadRootLock creates the lock file at path and takes its lock, keeping
// the file open so the lock lasts until close or until the process ends.
//
// Where the platform has no usable lock, the file is still created and held,
// so the root has the same shape everywhere; the sweep simply never removes a
// root there (see fileLocksWork).
func holdReadRootLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if !fileLocksWork {
		return f, nil
	}
	if lockErr := tryLockFile(f); lockErr != nil {
		_ = f.Close()
		return nil, lockErr
	}
	return f, nil
}

// sweepReadRoots removes every read root under tmp whose process is gone, and
// returns how many it removed. Nothing else is touched: a directory without
// the read root's prefix is not ours to judge.
func sweepReadRoots(tmp string) int {
	if !fileLocksWork {
		return 0
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), readRootPrefix) {
			continue
		}
		dir := filepath.Join(tmp, e.Name())
		if abandonedReadRoot(dir) && os.RemoveAll(dir) == nil {
			removed++
		}
	}
	return removed
}

// abandonedReadRoot reports whether no live process holds the read root at
// dir.
//
// Its lock answers that: a process that is alive holds it, and the operating
// system releases it when the process ends, however it ended. Taking it here
// is therefore proof the owner is gone, and it is released again at once,
// before the root is removed. A root with no lock file at all is judged by
// its age instead (see readRootLockGrace), and anything that cannot be read is
// left where it is.
func abandonedReadRoot(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, readRootLockName), os.O_RDWR, 0)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
		info, statErr := os.Stat(dir)
		return statErr == nil && readRootNow().Sub(info.ModTime()) >= readRootLockGrace()
	}
	defer func() { _ = f.Close() }()
	if tryLockFile(f) != nil {
		return false
	}
	_ = unlockFile(f)
	return true
}
