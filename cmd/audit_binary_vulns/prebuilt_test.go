// prebuilt_test.go covers how binaries somebody else built are found and held
// to the targets the configuration declares.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyBinary copies a built binary to a new path.
func copyBinary(t *testing.T, from, to string) {
	t.Helper()

	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("reading %s: %v", from, err)
	}
	if writeErr := os.WriteFile(to, data, 0o600); writeErr != nil { //#nosec G703 -- a path under the test's own temporary directory
		t.Fatalf("writing %s: %v", to, writeErr)
	}
}

// TestFindPrebuilt_NamesEachBinaryByTheTargetItRecords covers the accepted
// shape: one binary per declared target, named by its build and by the
// platform its own build information records rather than by its file name.
func TestFindPrebuilt_NamesEachBinaryByTheTargetItRecords(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "misleading-name-windows")
	copyBinary(t, buildFixture(t), path)

	found, err := findPrebuilt(filepath.Join(dir, "*"), []build{{id: "server", targets: []target{hostTarget}}})
	if err != nil {
		t.Fatalf("findPrebuilt: %v", err)
	}
	if len(found) != 1 || found[0].build != "server" || found[0].target != hostTarget || found[0].path != path {
		t.Errorf("found = %+v, want the one binary as server for %s", found, hostTarget)
	}
}

// TestFindPrebuilt_RefusesASetThatIsNotTheRelease covers every way the matched
// files can differ from the targets the configuration declares, each of which
// would scan a release other than the one being published.
func TestFindPrebuilt_RefusesASetThatIsNotTheRelease(t *testing.T) {
	t.Parallel()

	bin := buildFixture(t)
	other := target{goos: "plan9", goarch: "arm"}
	if other == hostTarget {
		other = target{goos: "aix", goarch: "ppc64"}
	}

	twice := t.TempDir()
	copyBinary(t, bin, filepath.Join(twice, "a"))
	copyBinary(t, bin, filepath.Join(twice, "b"))

	notGo := t.TempDir()
	if err := os.WriteFile(filepath.Join(notGo, "notes.txt"), []byte("not a binary"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	for _, tc := range []struct {
		name    string
		pattern string
		targets []target
		want    string
	}{
		{name: "a malformed pattern", pattern: "[", targets: []target{hostTarget}, want: "syntax error in pattern"},
		{name: "nothing matched", pattern: filepath.Join(t.TempDir(), "*"), targets: []target{hostTarget}, want: "matches no file"},
		{name: "a file that is not a Go binary", pattern: filepath.Join(notGo, "*"), targets: []target{hostTarget}, want: "reading the build information of"},
		{name: "a target the release does not build", pattern: bin, targets: []target{other}, want: "which the configuration does not release"},
		{name: "two binaries for one target", pattern: filepath.Join(twice, "*"), targets: []target{hostTarget}, want: "were both built for " + hostTarget.String()},
		{name: "a target with no binary", pattern: bin, targets: []target{hostTarget, other}, want: "has no binary for " + other.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			found, err := findPrebuilt(tc.pattern, []build{{id: "server", targets: tc.targets}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("findPrebuilt = %+v, %v; want a refusal containing %q", found, err, tc.want)
			}
		})
	}
}
