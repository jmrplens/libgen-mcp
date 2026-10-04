//go:build windows

package libgen

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

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
