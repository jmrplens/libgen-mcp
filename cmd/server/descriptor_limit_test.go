//go:build linux || darwin

package main

import (
	"errors"
	"syscall"
	"testing"
)

// TestDescriptorLimitFrom_ReadsTheSoftLimit pins which half of the pair is
// read, and that a failed read says nothing rather than zero.
func TestDescriptorLimitFrom_ReadsTheSoftLimit(t *testing.T) {
	got, ok := descriptorLimitFrom(func(resource int, limit *syscall.Rlimit) error {
		if resource != syscall.RLIMIT_NOFILE {
			t.Errorf("resource = %d, want RLIMIT_NOFILE", resource)
		}
		limit.Cur, limit.Max = 1024, 4096
		return nil
	})
	if !ok || got != 1024 {
		t.Errorf("descriptorLimitFrom = %d, %t; want 1024, true", got, ok)
	}

	if _, ok = descriptorLimitFrom(func(int, *syscall.Rlimit) error { return errors.New("denied") }); ok {
		t.Error("a failed getrlimit was reported as a limit")
	}
}

// TestDescriptorLimit_ReadsThisProcess checks the real call answers on the
// platforms that have it.
func TestDescriptorLimit_ReadsThisProcess(t *testing.T) {
	got, ok := descriptorLimit()
	if !ok || got == 0 {
		t.Errorf("descriptorLimit() = %d, %t; want this process's limit", got, ok)
	}
}
