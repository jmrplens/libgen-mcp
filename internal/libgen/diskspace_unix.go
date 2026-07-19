//go:build unix

package libgen

import "golang.org/x/sys/unix"

// freeSpace returns the number of bytes available to an unprivileged user in the
// filesystem that backs dir. It relies on statfs: available blocks (Bavail)
// multiplied by the fundamental block size (Bsize).
func freeSpace(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
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
