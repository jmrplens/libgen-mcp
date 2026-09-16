//go:build !unix

// open_other.go carries the leaf-open flags for platforms with no O_NOFOLLOW,
// where the Lstat before the open and the Stat on the descriptor after it are
// all the containment there is.

package pathguard

// noFollowFlag is zero here: Windows has no O_NOFOLLOW, so the leaf swap the
// unix build refuses is refused only by the caller's Lstat before the open and
// its Stat on the descriptor after it. Both still run, and both still catch a
// leaf that is not a regular file; what is missing is the guarantee that the
// file opened is the file that was checked.
const noFollowFlag = 0

// noFollowSupported reports whether this platform can refuse the leaf swap in
// the open itself.
const noFollowSupported = false
