package libgen

import (
	"os"
	"path/filepath"
	"testing"
)

// tempDirVars are every environment variable os.TempDir consults across the
// platforms this project builds for: TMPDIR on Unix, then TMP and TEMP on
// Windows, which reads them through GetTempPath in that order.
var tempDirVars = []string{"TMPDIR", "TMP", "TEMP"}

// isolateTempDir points os.TempDir at dir for the duration of the test.
//
// Setting TMPDIR alone is a POSIX-only instruction. On Windows os.TempDir never
// reads it, so a test that sets only TMPDIR keeps the real temporary directory
// and its isolation quietly does nothing — which is not a loud failure but a
// silent one: the assertion that follows then reports on a directory the test
// does not control.
//
// That is not hypothetical here. TestFetchToTemp_TempDirCreateError pointed
// TMPDIR at a missing path to make os.MkdirTemp fail; on Windows MkdirTemp
// succeeded against the real temp directory, the download failed later for an
// unrelated reason, and the assertion reported that unrelated error as though it
// were the one under test.
//
// It cannot be used from a parallel test, because testing.T.Setenv cannot.
func isolateTempDir(t *testing.T, dir string) {
	t.Helper()
	for _, name := range tempDirVars {
		t.Setenv(name, dir)
	}
	// Verified rather than assumed, because the failure being fixed here is an
	// isolation that quietly did nothing. If a platform grows another variable,
	// this says so instead of letting the test pass while asserting nothing.
	//nolint:usetesting // os.TempDir is the subject of the assertion, not an alternative to t.TempDir.
	if got, want := filepath.Clean(os.TempDir()), filepath.Clean(dir); got != want {
		t.Fatalf("os.TempDir() = %q after isolating it to %q; this platform reads a variable %v does not cover",
			got, want, tempDirVars)
	}
}
