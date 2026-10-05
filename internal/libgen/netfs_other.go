//go:build !linux

// The half of networkFilesystem for every platform but Linux, where it does
// not look.

package libgen

// networkFilesystem answers "" here: only on Linux does the server know, from
// the kernel's own source, which network filesystems keep a lock on a
// directory to the host that took it. Elsewhere one temp directory must not be
// shared between hosts on a network filesystem, which the configuration
// reference says.
func networkFilesystem(string) (string, error) { return "", nil }
