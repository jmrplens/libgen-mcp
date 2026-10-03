//go:build unix

// open_unix.go opens caller-supplied local paths without following a symlink at
// the leaf, which is the containment the checks in pathguard.go describe but
// cannot enforce on their own.

package pathguard

import "syscall"

// noFollowFlag makes the kernel refuse a symlink as the last path component, so
// the check and the use become one operation.
//
// It matters because an Lstat proves what a path named a moment ago, not what an
// open would open now: a local principal able to write in an allowed root — and
// the OS temp directory is always one, and /tmp is world-writable — can swap the
// leaf between the two syscalls and redirect the read or the write.
const noFollowFlag = syscall.O_NOFOLLOW

// readOpenFlag is added to the flags of [OpenReadableFile]'s open. The checks
// before the open refuse a fifo, but a fifo planted after them would block a
// plain O_RDONLY open until some writer appeared; non-blocking, the open returns
// at once and the Stat on the descriptor refuses it. A regular file reads the
// same either way.
const readOpenFlag = syscall.O_NONBLOCK

// noFollowSupported reports whether this platform can refuse the leaf swap in
// the open itself. It is what the tests assert against rather than naming an
// operating system.
const noFollowSupported = true
