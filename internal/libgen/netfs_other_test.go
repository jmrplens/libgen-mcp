//go:build !linux

package libgen

import (
	"path/filepath"
	"testing"
)

// TestNetworkFilesystem_IsNotLookedAt pins that only Linux checks: elsewhere
// every directory, even one that does not exist, is taken as not shared, so
// a root's lock is tried as it always was.
func TestNetworkFilesystem_IsNotLookedAt(t *testing.T) {
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "absent")} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			if got, err := networkFilesystem(dir); got != "" || err != nil {
				t.Errorf("networkFilesystem(%q) = %q, %v, want \"\", nil", dir, got, err)
			}
		})
	}
}
