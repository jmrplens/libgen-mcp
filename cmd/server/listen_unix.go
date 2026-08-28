//go:build unix

// Socket permissions on the platforms that have them.

package main

import (
	"os"
	"syscall"
)

// withSocketUmask runs bind with the process umask narrowed so the socket inode
// is created with mode and nothing wider, then restores it.
//
// The kernel creates a unix socket with 0777 &^ umask, so without this the
// socket exists world-connectable for the moment between bind and chmod. umask
// is the only lever that applies at creation time: ListenConfig.Control runs on
// the raw fd before bind, when the path does not exist yet, and binding to a
// temporary name and renaming defeats the listener's unlink-on-close, which
// captured the original name.
//
// umask is process-global and affects every thread, which is acceptable here and
// only here: this runs once at startup, before the server has created anything
// else.
func withSocketUmask(mode os.FileMode, bind func()) {
	old := syscall.Umask(0o777 &^ int(mode.Perm()))
	defer syscall.Umask(old)
	bind()
}

// chmodSocket sets the socket file's permission mode exactly, since a umask can
// only clear bits and never set them.
func chmodSocket(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

// socketModesEnforced reports whether chmodSocket actually enforces a mode on
// this platform.
const socketModesEnforced = true
