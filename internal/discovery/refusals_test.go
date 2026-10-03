// Tests for the process-wide refusal windows.

package discovery

import (
	"testing"
	"time"
)

// TestRefusalWindows walks one window through its life: opened fresh and
// reported as such, extended without being reported again, never shortened by a
// writer holding an older clock reading, reported again once it has lapsed, and
// independent of every other key.
func TestRefusalWindows(t *testing.T) {
	var w refusalWindows
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	const d = challengeCooldown

	if w.quiet("a", now) {
		t.Fatal("a key never refused is quiet")
	}
	if !w.open("a", now) {
		t.Error("the first refusal did not report a fresh window")
	}
	if !w.quiet("a", now.Add(d-time.Second)) {
		t.Error("the key is not quiet inside its window")
	}
	if w.quiet("b", now) {
		t.Error("a refusal of one key silenced another")
	}

	if w.open("a", now.Add(time.Minute)) {
		t.Error("a refusal inside the window reported a fresh one")
	}
	if !w.quiet("a", now.Add(d+30*time.Second)) {
		t.Error("a refusal inside the window did not extend it")
	}

	if w.open("a", now.Add(-time.Hour)) {
		t.Error("a stale writer reported a fresh window")
	}
	if !w.quiet("a", now.Add(d+30*time.Second)) {
		t.Error("a stale writer shortened the window")
	}

	lapsed := now.Add(2 * d)
	if w.quiet("a", lapsed) {
		t.Error("the window outlived its deadline")
	}
	if !w.open("a", lapsed) {
		t.Error("a refusal after the window lapsed did not report a fresh one")
	}
}
