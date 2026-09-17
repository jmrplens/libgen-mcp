//go:build unix

package pathguard

import "syscall"

// makeFIFO creates a named pipe, which is the portable-enough way to get a path
// that exists and is neither a regular file nor a directory.
func makeFIFO(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
