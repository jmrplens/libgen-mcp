//go:build windows

package libgen

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// readRootLockName is the file inside a read root whose lock stands for the
// root on Windows.
const readRootLockName = ".lock"

// openReadRootLock opens the file whose lock stands for the read root at dir,
// creating it when it is missing.
//
// Windows cannot lock a directory, so the lock is on a file inside it, opened
// without following a reparse point, so a link planted at its name is opened
// as itself rather than as whatever it names. os.OpenFile never asks for
// FILE_SHARE_DELETE, so while the owner's handle is open the file cannot be
// deleted, and with it the root cannot either: a temp cleaner on Windows
// cannot take a live root's lock away the way an age-based one could on Unix
// (see the Unix half of this function).
//
// The sweep creates it too. That is what lets a root whose process died
// between making the directory and making this file be judged at all, and it
// is safe because the sweep only opens a root older than readRootLockGrace,
// which no live process leaves without its file.
func openReadRootLock(dir string) (*os.File, error) {
	return os.OpenFile(filepath.Join(dir, readRootLockName),
		os.O_CREATE|os.O_RDWR|windows.O_FILE_FLAG_OPEN_REPARSE_POINT, 0o600)
}

// openReadRootDir opens the read root at dir, to list it. The sweep opens it
// only after Lstat has found a real directory there, and the temp directory on
// Windows is the user's own, under the profile.
func openReadRootDir(dir string) (*os.File, error) { return os.Open(dir) }

// ownedDirectory reports whether info, from an Lstat, is a real directory. A
// symbolic link or a junction is not one to Lstat, so neither is followed.
// Ownership is not compared here: Windows has no user id to compare it with,
// and the temp directory there is the user's own, under the profile.
func ownedDirectory(info fs.FileInfo) bool {
	return info.IsDir()
}

// tryLockFile takes an exclusive lock on the first byte of f without waiting.
// It answers errLockHeld when another handle holds one, and the error as it
// came for any other failure, which means the lock could not be tried at all.
//
// LockFileEx locks per handle, so a second handle on the same file conflicts
// even inside one process, and Windows releases the lock when the handle is
// closed or the process ends, however it ended. That is the same contract the
// flock half gives on Unix, which is all the read root asks of either.
// ERROR_LOCK_VIOLATION is the one answer that means held.
func tryLockFile(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockHeld
	}
	return err
}

// unlockFile gives up the lock tryLockFile took.
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
