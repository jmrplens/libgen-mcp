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

// noFollowSupported reports whether this platform can refuse the leaf swap in
// the open itself. It is what the tests assert against rather than naming an
// operating system.
const noFollowSupported = true
