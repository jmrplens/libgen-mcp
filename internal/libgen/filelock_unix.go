//go:build unix

package libgen

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// readRootOwner is os.Geteuid, as a seam: every root a test can make belongs
// to the user running it, so only a substitute can make one look like
// another user's.
var readRootOwner = os.Geteuid

// openReadRootLock opens the file whose lock stands for the read root at dir,
// which on Unix is the directory itself.
//
// The directory and not a file inside it, because age-based temp cleaners
// (systemd-tmpfiles before 254, tmpreaper, tmpwatch) delete a file nobody has
// touched for days, lock or no lock. A live root's lock file went that way,
// and another process's sweep then found a root with no lock and old enough
// to remove. With the lock on the directory there is nothing inside for a
// cleaner to take, so a live root never looks dead to a sweep.
// systemd-tmpfiles skips a flocked directory on every version. A cleaner that
// removes the emptied directory itself takes the root away outright, which the
// owner notices and recovers from (see readRoot.fetchDir).
//
// It is opened as openReadRootDir opens it.
func openReadRootLock(dir string) (*os.File, error) { return openReadRootDir(dir) }

// openReadRootDir opens the read root at dir, to list it or to lock it.
//
// The flags are what make it safe to open a name another user may have put in
// a shared /tmp. O_DIRECTORY opens nothing but a directory, a terminal above
// all, which O_NOCTTY also refuses to adopt as the process's controlling
// terminal. O_NOFOLLOW refuses a symbolic link in place of the root.
// O_NONBLOCK keeps a FIFO from holding the open, and O_CLOEXEC keeps the lock
// out of any process this one starts.
func openReadRootDir(dir string) (*os.File, error) {
	return os.OpenFile(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
}

// ownedDirectory reports whether info, from an Lstat, is a real directory
// owned by the user this process runs as. A symbolic link is not a directory
// to Lstat, so one in place of a root is never followed.
func ownedDirectory(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return info.IsDir() && int64(st.Uid) == int64(readRootOwner())
}

// tryLockFile takes an exclusive advisory lock on f without waiting. It
// answers errLockHeld when another open file description holds one, and the
// error as it came for any other failure, which means the lock could not be
// tried at all.
//
// flock rather than fcntl, and that is the property the read root relies on:
// an flock lock belongs to the open file description, so it conflicts with a
// second open of the same directory in the SAME process too (which is what
// lets a test hold a root alive), and the kernel drops it when the last
// descriptor closes, however the process ended. A process killed with SIGKILL
// releases it as surely as one that exits cleanly.
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
