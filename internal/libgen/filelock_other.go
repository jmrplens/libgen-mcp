//go:build !unix && !windows

package libgen

import (
	"errors"
	"os"
)

// errLockUnsupported is what tryLockFile answers here: this platform offers no
// lock the read root can rely on.
//
// It is not errLockHeld, so a process here never makes a read root: it makes
// each fetch's directory loose in the temp directory, as up to 2.2.0, and its
// sweep removes nothing, because without a lock there is no way to tell a dead
// process's directory from a live one's, and leaving a directory behind is the
// failure that costs nothing (see readRoot.unlocked).
var errLockUnsupported = errors.New("file locks are not supported on this platform")

// tryLockFile always fails here; see errLockUnsupported.
func tryLockFile(*os.File) error { return errLockUnsupported }

// unlockFile has nothing to undo here.
func unlockFile(*os.File) error { return nil }
