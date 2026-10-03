//go:build !unix

// open_other.go carries the leaf-open flags for platforms with no O_NOFOLLOW.
// For a download's writes, the Lstat before the open and the Stat on the
// descriptor after it are all the containment there is. A read does not depend
// on these flags: OpenReadableFile opens through os.Root, which refuses an
// escaping reparse point on Windows as openat with O_NOFOLLOW does on unix.

package pathguard

// noFollowFlag is zero here: Windows has no O_NOFOLLOW, so the leaf swap the
// unix build refuses is refused only by the caller's Lstat before the open and
// its Stat on the descriptor after it. Both still run, and both still catch a
// leaf that is not a regular file; what is missing is the guarantee that the
// file opened is the file that was checked.
const noFollowFlag = 0

// readOpenFlag is zero here: the non-blocking open the unix build uses guards
// against a fifo planted at the path, and a Windows named pipe does not live in
// the filesystem namespace a caller's path can reach.
const readOpenFlag = 0

// noFollowSupported reports whether this platform can refuse the leaf swap in
// the open itself.
const noFollowSupported = false
