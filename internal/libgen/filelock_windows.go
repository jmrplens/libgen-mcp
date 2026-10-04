//go:build windows

package libgen

import (
	"os"

	"golang.org/x/sys/windows"
)

// fileLocksWork says tryLockFile can tell a held lock from a free one here.
const fileLocksWork = true

// tryLockFile takes an exclusive lock on the first byte of f without waiting,
// and fails if any other handle holds one.
//
// LockFileEx locks per handle, so a second handle on the same file conflicts
// even inside one process, and Windows releases the lock when the handle is
// closed or the process ends, however it ended. That is the same contract the
// flock half gives on Unix, which is all the read root asks of either.
func tryLockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped))
}

// unlockFile gives up the lock tryLockFile took.
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
