//go:build !linux && !darwin

// descriptor_limit_other.go answers for the platforms with no RLIMIT_NOFILE
// this server reads. Windows has no per-process descriptor limit of that kind.

package main

// descriptorLimit says the platform has no limit to read, so the ceilings are
// sized as though the limit were [fallbackDescriptorLimit].
//
// A ceiling sized that way protects no descriptor on Windows, where nothing
// runs out at 1024. It is kept rather than dropped so the platform is not left
// unbounded by default: a figure every operator can read off the startup line
// is better than a process that holds whatever it is offered.
func descriptorLimit() (uint64, bool) {
	return 0, false
}
