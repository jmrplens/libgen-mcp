//go:build !unix

package libgen

import "math"

// freeSpace is a no-op fallback on non-unix platforms: it reports unlimited free
// space so the disk-space precheck never blocks a download.
func freeSpace(_ string) (uint64, error) {
	return math.MaxUint64, nil
}
