package main

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"
)

func writeGoFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
	return path
}

// TestProcessPath_FileDirAndErrors verifies processPath dispatches files and
// directories and reports stat failures.
func TestProcessPath_FileDirAndErrors(t *testing.T) {
	dir := t.TempDir()
	goFile := writeGoFile(t, dir, "sample.go", "package sample\n\nfunc Widget() {}\n")
	writeGoFile(t, dir, "notes.txt", "not go source\n")

	if err := processPath(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatalf("processPath(non-go) error = %v", err)
	}
	if err := processPath(goFile); err != nil {
		t.Fatalf("processPath(file) error = %v", err)
	}
	if err := processPath(dir); err != nil {
		t.Fatalf("processPath(dir) error = %v", err)
	}
	if err := processPath(filepath.Join(dir, "missing.go")); err == nil {
		t.Fatal("processPath(missing) expected error")
	}
}

// TestProcessDir_RecursesAndReportsErrors verifies processDir walks nested
// directories, skips non-Go files, and surfaces read errors.
func TestProcessDir_RecursesAndReportsErrors(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeGoFile(t, root, "top.go", "package sample\n\nfunc Top() {}\n")
	writeGoFile(t, root, "README.md", "# not go\n")
	writeGoFile(t, nested, "inner.go", "package nested\n\nfunc Inner() {}\n")

	if err := processDir(root); err != nil {
		t.Fatalf("processDir() error = %v", err)
	}
	if err := processDir(filepath.Join(root, "does-not-exist")); err == nil {
		t.Fatal("processDir(missing) expected error")
	}
}

// TestProcessFile_DryRunAndBlankValue verifies dry-run mode leaves files
// untouched and blank value specs produce no doc insertion.
func TestProcessFile_DryRunAndBlankValue(t *testing.T) {
	dir := t.TempDir()
	source := "package sample\n\nvar _ = 1\n\nfunc Widget() {}\n"
	path := writeGoFile(t, dir, "sample.go", source)

	dryRun = true
	defer func() { dryRun = false }()
	if err := processFile(path); err != nil {
		t.Fatalf("processFile(dry-run) error = %v", err)
	}
	got, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != source {
		t.Fatalf("processFile(dry-run) modified file:\n%s", got)
	}
}

// TestProcessFile_RegeneratesGroupedGeneratedDoc verifies a grouped const whose
// group comment is a stale generated doc is regenerated via the fallback doc.
func TestProcessFile_RegeneratesGroupedGeneratedDoc(t *testing.T) {
	dir := t.TempDir()
	source := "package sample\n\n// group holds data for widgets.\nconst (\n\tAlpha = 1\n)\n"
	path := writeGoFile(t, dir, "sample.go", source)

	if err := processFile(path); err != nil {
		t.Fatalf("processFile() error = %v", err)
	}
	got, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	assertContains(t, string(got), "// Alpha identifies the alpha constant used by this package.")
}

// TestProcessFile_ReadAndParseErrors verifies processFile reports unreadable
// paths and unparseable sources.
func TestProcessFile_ReadAndParseErrors(t *testing.T) {
	if err := processFile(t.TempDir()); err == nil {
		t.Fatal("processFile(directory) expected a read error")
	}

	dir := t.TempDir()
	broken := writeGoFile(t, dir, "broken.go", "package sample\nfunc (\n")
	if err := processFile(broken); err == nil {
		t.Fatal("processFile(broken) expected a parse error")
	}
}

// TestProcessFile_KeepsDocumentedValue verifies processFile leaves a var that
// already has a hand-written comment untouched.
func TestProcessFile_KeepsDocumentedValue(t *testing.T) {
	dir := t.TempDir()
	source := "package sample\n\n// Setting is the runtime toggle.\nvar Setting = 1\n"
	path := writeGoFile(t, dir, "sample.go", source)

	if err := processFile(path); err != nil {
		t.Fatalf("processFile() error = %v", err)
	}
	got, err := os.ReadFile(path) //#nosec G304 -- test fixture path from t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != source {
		t.Fatalf("processFile() changed a documented var:\n%s", got)
	}
}

// TestProcessFile_WriteError verifies processFile reports a failure when the
// target file cannot be rewritten. It is skipped as root, where file mode bits
// do not block writes.
func TestProcessFile_WriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("write-permission errors are not enforced for root")
	}
	dir := t.TempDir()
	path := writeGoFile(t, dir, "sample.go", "package sample\n\nfunc Widget() {}\n")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := processFile(path); err == nil {
		t.Fatal("processFile(read-only) expected a write error")
	}
}

// TestFirstDoc_PrefersPrimary verifies firstDoc returns the symbol doc when
// present and falls back to the enclosing declaration doc otherwise.
func TestFirstDoc_PrefersPrimary(t *testing.T) {
	t.Parallel()

	primary := &ast.CommentGroup{}
	fallback := &ast.CommentGroup{}
	if got := firstDoc(primary, fallback); got != primary {
		t.Fatal("firstDoc did not prefer the primary comment group")
	}
	if got := firstDoc(nil, fallback); got != fallback {
		t.Fatal("firstDoc did not fall back to the declaration comment group")
	}
}
