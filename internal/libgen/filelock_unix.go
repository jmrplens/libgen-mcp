//go:build unix

package libgen

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile takes an exclusive advisory lock on f without waiting. It
// answers errLockHeld when another open file description holds one, and the
// error as it came for any other failure, which means the lock could not be
// tried at all.
//
// flock rather than fcntl, and that is the property the read root relies on:
// an flock lock belongs to the open file description, so it conflicts with a
// second open of the same file in the SAME process too (which is what lets a
// test hold a root alive), and the kernel drops it when the last descriptor
// closes, however the process ended. A process killed with SIGKILL releases it
// as surely as one that exits cleanly.
//
// EWOULDBLOCK is the one answer that means held. It is the same number as
// EAGAIN on every Unix this builds for, so naming one names both.
func tryLockFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errLockHeld
	}
	return err
}

// unlockFile gives up the lock tryLockFile took.
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
