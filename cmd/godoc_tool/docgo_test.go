package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles writes a package tree under a temporary directory and returns it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// read returns a file's contents, or "" when it is not there.
func read(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) //#nosec G304 -- this test's own temporary directory
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

// TestMovePackageDoc_MovesTheCommentAndLeavesTheFileBuilding is the whole
// claim: the comment ends up in doc.go, verbatim, and the file it came from
// still parses without it.
func TestMovePackageDoc_MovesTheCommentAndLeavesTheFileBuilding(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"widget.go": "// Package widget does a thing.\n//\n// And says more about it.\npackage widget\n\n// Do does it.\nfunc Do() {}\n",
	})

	if err := movePackageDoc(dir); err != nil {
		t.Fatalf("movePackageDoc() error = %v", err)
	}

	docGo := read(t, filepath.Join(dir, "doc.go"))
	if !strings.Contains(docGo, "// Package widget does a thing.") || !strings.Contains(docGo, "And says more about it.") {
		t.Errorf("doc.go = %q, want the whole comment", docGo)
	}
	if !strings.HasSuffix(strings.TrimSpace(docGo), "package widget") {
		t.Errorf("doc.go = %q, want it to end on the package clause", docGo)
	}
	source := read(t, filepath.Join(dir, "widget.go"))
	if strings.Contains(source, "Package widget does a thing") {
		t.Errorf("widget.go = %q, want the comment cut from it", source)
	}
	if !strings.Contains(source, "// Do does it.") {
		t.Errorf("widget.go = %q, want every other comment kept", source)
	}
}

// TestMovePackageDoc_CarriesTheBuildConstraint pins the detail that keeps a
// tagged package building. A constraint governs the file it is in, and doc.go
// is a new file of the same package: leaving it behind would give cmd/eval one
// file that builds without the tag, which is a main package with no main
// function, and every plain `go vet ./...` would report it.
func TestMovePackageDoc_CarriesTheBuildConstraint(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"main.go": "//go:build eval\n\n// Command eval runs the harness.\npackage main\n\nfunc main() {}\n",
	})

	if err := movePackageDoc(dir); err != nil {
		t.Fatalf("movePackageDoc() error = %v", err)
	}

	docGo := read(t, filepath.Join(dir, "doc.go"))
	if !strings.HasPrefix(docGo, "//go:build eval\n") {
		t.Errorf("doc.go = %q, want the build constraint first", docGo)
	}
	if source := read(t, filepath.Join(dir, "main.go")); !strings.HasPrefix(source, "//go:build eval\n") {
		t.Errorf("main.go = %q, want the constraint kept where it was too", source)
	}
}

// TestMovePackageDoc_LeavesWhatItShould covers the three shapes it declines to
// touch: a package that already has a doc.go, one with no package comment at
// all, and one whose comment the convention refuses — moving that last one
// would enshrine as the documentation a comment the audit already reports.
func TestMovePackageDoc_LeavesWhatItShould(t *testing.T) {
	testCases := []struct {
		name    string
		files   map[string]string
		wantDoc bool
	}{
		{
			name: "a package that already has one",
			files: map[string]string{
				"doc.go":    "// Package widget does a thing.\npackage widget\n",
				"widget.go": "package widget\n",
			},
			wantDoc: true,
		},
		{
			name:  "a package with no comment",
			files: map[string]string{"widget.go": "package widget\n\n// Do does it.\nfunc Do() {}\n"},
		},
		{
			name:  "a comment the convention refuses",
			files: map[string]string{"widget.go": "// widget does a thing.\npackage widget\n"},
		},
		{
			name:  "a test file's comment is not a package comment to move",
			files: map[string]string{"widget_test.go": "// Package widget tests the thing.\npackage widget\n"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeFiles(t, tc.files)
			before := read(t, filepath.Join(dir, "doc.go"))

			if err := movePackageDoc(dir); err != nil {
				t.Fatalf("movePackageDoc() error = %v", err)
			}
			if got := read(t, filepath.Join(dir, "doc.go")); got != before {
				t.Errorf("doc.go changed to %q, want it left as %q", got, before)
			}
			if tc.wantDoc && before == "" {
				t.Errorf("the fixture was meant to have a doc.go")
			}
		})
	}
}

// TestMovePackageDoc_DryRunWritesNothing verifies the flag every fixer in this
// repository offers does what it says.
func TestMovePackageDoc_DryRunWritesNothing(t *testing.T) {
	dir := writeFiles(t, map[string]string{"widget.go": "// Package widget does a thing.\npackage widget\n"})
	dryRun = true
	t.Cleanup(func() { dryRun = false })

	if err := movePackageDoc(dir); err != nil {
		t.Fatalf("movePackageDoc() error = %v", err)
	}
	if got := read(t, filepath.Join(dir, "doc.go")); got != "" {
		t.Errorf("doc.go = %q, want nothing written", got)
	}
	if source := read(t, filepath.Join(dir, "widget.go")); !strings.Contains(source, "Package widget does a thing") {
		t.Errorf("widget.go = %q, want the comment left where it was", source)
	}
}

// TestMovePackageDocs_WalksATreeAndCollectsEveryFailure verifies both halves of
// the walk: it reaches nested packages, and it does not stop at the first
// failure — a tree where one package cannot be moved is still a tree where the
// others should be.
func TestMovePackageDocs_WalksATreeAndCollectsEveryFailure(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/a.go":                "// Package a does a.\npackage a\n",
		"b/c/c.go":              "// Package c does c.\npackage c\n",
		"b/c/testdata/skip.go":  "// Package skip is not a package of this repository.\npackage skip\n",
		"node_modules/n/n.go":   "// Package n is vendored.\npackage n\n",
		".hidden/h/h.go":        "// Package h is hidden.\npackage h\n",
		"broken/broken.go":      "// Package broken does not parse.\npackage broken\n\nfunc (\n",
		"b/c/nested/nested.go":  "// Package nested does nested.\npackage nested\n",
		"b/c/nested/extra.go":   "package nested\n",
		"b/c/nested/README.txt": "not Go\n",
	})

	err := movePackageDocs(dir)
	if err == nil {
		t.Fatal("movePackageDocs() returned no error although one package does not parse")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error = %v, want it to name the package that failed", err)
	}

	moved := []string{"a/doc.go", "b/c/doc.go", "b/c/nested/doc.go"}
	for _, path := range moved {
		t.Run(path, func(t *testing.T) {
			if read(t, filepath.Join(dir, filepath.FromSlash(path))) == "" {
				t.Errorf("%s was not written, so the walk stopped at the failure", path)
			}
		})
	}
	skipped := []string{"b/c/testdata/doc.go", "node_modules/n/doc.go", ".hidden/h/doc.go"}
	for _, path := range skipped {
		t.Run(path, func(t *testing.T) {
			if read(t, filepath.Join(dir, filepath.FromSlash(path))) != "" {
				t.Errorf("%s was written, and nothing under that directory is a package of this repository", path)
			}
		})
	}
}

// TestMovePackageDocs_TakesAFileAsItsDirectory verifies a path that names a
// file moves the comment of the package that file belongs to, which is what a
// caller passing one source file means.
func TestMovePackageDocs_TakesAFileAsItsDirectory(t *testing.T) {
	dir := writeFiles(t, map[string]string{"widget.go": "// Package widget does a thing.\npackage widget\n"})

	if err := movePackageDocs(filepath.Join(dir, "widget.go")); err != nil {
		t.Fatalf("movePackageDocs() error = %v", err)
	}
	if read(t, filepath.Join(dir, "doc.go")) == "" {
		t.Error("doc.go was not written")
	}
}

// TestMovePackageDocs_ReportsWhatItCannotRead covers the failures a filesystem
// this test owns does not produce on its own, through the seams they are
// behind: a path that is not there, a walk that reports an error, a format of
// bytes that were just parsed, and a write that cannot land.
func TestMovePackageDocs_ReportsWhatItCannotRead(t *testing.T) {
	t.Run("a path that is not there", func(t *testing.T) {
		if err := movePackageDocs(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Error("movePackageDocs() over a path that is not there returned no error")
		}
	})

	t.Run("a walk that fails", func(t *testing.T) {
		restore := walkDir
		walkDir = func(string, fs.WalkDirFunc) error { return errors.New("the walk failed") }
		t.Cleanup(func() { walkDir = restore })

		if err := movePackageDocs(t.TempDir()); err == nil || !strings.Contains(err.Error(), "the walk failed") {
			t.Errorf("movePackageDocs() = %v, want the walk's own error", err)
		}
	})

	t.Run("a format that fails", func(t *testing.T) {
		restore := formatDocGo
		formatDocGo = func([]byte) ([]byte, error) { return nil, errors.New("cannot format") }
		t.Cleanup(func() { formatDocGo = restore })

		dir := writeFiles(t, map[string]string{"widget.go": "// Package widget does a thing.\npackage widget\n"})
		if err := movePackageDoc(dir); err == nil || !strings.Contains(err.Error(), "cannot format") {
			t.Errorf("movePackageDoc() = %v, want the formatter's own error", err)
		}
	})

	t.Run("a write that fails", func(t *testing.T) {
		restore := writeDocGoFile
		writeDocGoFile = func(string, []byte, os.FileMode) error { return errors.New("cannot write") }
		t.Cleanup(func() { writeDocGoFile = restore })

		dir := writeFiles(t, map[string]string{"widget.go": "// Package widget does a thing.\npackage widget\n"})
		if err := movePackageDoc(dir); err == nil || !strings.Contains(err.Error(), "cannot write") {
			t.Errorf("movePackageDoc() = %v, want the writer's own error", err)
		}
	})

	t.Run("a directory that cannot be read", func(t *testing.T) {
		if err := movePackageDoc(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Error("movePackageDoc() over a directory that is not there returned no error")
		}
	})
}

// TestBuildConstraint_TakesTheConstraintAndNothingElse verifies only the
// //go:build line travels: whatever else a header holds belongs to the file it
// is in, and copying it would put a second copy of somebody's note in a file
// they did not write.
func TestBuildConstraint_TakesTheConstraintAndNothingElse(t *testing.T) {
	testCases := []struct {
		name   string
		header string
		want   string
	}{
		{name: "no header", header: "", want: ""},
		{name: "a constraint", header: "//go:build eval\n\n", want: "//go:build eval\n\n"},
		{name: "a constraint under a note", header: "// a note\n\n//go:build eval\n\n", want: "//go:build eval\n\n"},
		{name: "a note alone", header: "// a note about this file\n\n", want: ""},
		{name: "the old syntax alone is not taken", header: "// +build eval\n\n", want: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(buildConstraint([]byte(tc.header))); got != tc.want {
				t.Errorf("buildConstraint(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
