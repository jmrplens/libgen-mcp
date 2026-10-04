//go:build unix

package libgen

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileLocksWork says tryLockFile can tell a held lock from a free one here.
const fileLocksWork = true

// tryLockFile takes an exclusive advisory lock on f without waiting, and fails
// if any other open file description holds one.
//
// flock rather than fcntl, and that is the property the read root relies on:
// an flock lock belongs to the open file description, so it conflicts with a
// second open of the same file in the SAME process too (which is what lets a
// test hold a root alive), and the kernel drops it when the last descriptor
// closes, however the process ended. A process killed with SIGKILL releases it
// as surely as one that exits cleanly.
func tryLockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// unlockFile gives up the lock tryLockFile took.
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
