//go:build !unix && !windows

package libgen

import (
	"errors"
	"os"
)

// fileLocksWork is false here: this platform offers no lock the read root can
// rely on. The root then works without one and the sweep removes nothing,
// because without a lock there is no way to tell a dead process's directory
// from a live one's, and leaving a directory behind is the failure that costs
// nothing.
const fileLocksWork = false

// errLockUnsupported is what tryLockFile answers here.
var errLockUnsupported = errors.New("file locks are not supported on this platform")

// tryLockFile always fails here; see fileLocksWork.
func tryLockFile(*os.File) error { return errLockUnsupported }

// unlockFile has nothing to undo here.
func unlockFile(*os.File) error { return nil }
