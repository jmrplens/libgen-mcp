//go:build unix

package libgen

import "golang.org/x/sys/unix"

// statfs is a seam over unix.Statfs so a test can force the (in practice
// unreachable, since real filesystems report a positive block size) non-positive
// Bsize guard in freeSpace. Production always uses the real syscall.
var statfs = unix.Statfs

// freeSpace returns the number of bytes available to an unprivileged user in the
// filesystem that backs dir. It relies on statfs: available blocks (Bavail)
// multiplied by the fundamental block size (Bsize).
func freeSpace(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := statfs(dir, &st); err != nil {
		return 0, err
	}
	// Bsize is signed (int64) on some platforms and unsigned on others; a
	// non-positive block size is nonsensical, so guard before the uint64
	// conversion (this also proves non-negativity to the overflow checker).
	if st.Bsize <= 0 {
		return 0, nil
	}
	return st.Bavail * uint64(st.Bsize), nil
}
