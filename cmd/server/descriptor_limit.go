//go:build linux || darwin

// descriptor_limit.go reads how many file descriptors the process may open,
// which the ceilings on what the process holds open are sized from.

package main

import "syscall"

// descriptorLimit returns the descriptors this process may open, and whether
// the platform said.
func descriptorLimit() (uint64, bool) {
	return descriptorLimitFrom(syscall.Getrlimit)
}

// descriptorLimitFrom reads the soft RLIMIT_NOFILE through getrlimit.
//
// The soft limit is the one that refuses an open, and by the time anything here
// runs it is no longer the one the process was started with: the Go runtime
// raises it to the hard limit before main (go.dev/issue/46279), so what this
// reads is the limit the process actually works under. A read that fails says
// nothing, and the caller falls back to [fallbackDescriptorLimit].
//
// Only linux and darwin, whose Rlimit fields are unsigned. The other unix
// systems spell the same call with signed fields or not at all, and a platform
// this server is not built for is better served by the fallback than by a
// conversion nobody runs.
func descriptorLimitFrom(getrlimit func(int, *syscall.Rlimit) error) (uint64, bool) {
	var limit syscall.Rlimit
	if err := getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return 0, false
	}
	return limit.Cur, true
}
