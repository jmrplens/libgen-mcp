package docgen

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteOrCheckTrailingNewline covers the convention that keeps a generated
// file from being the one text file in the repository without a final newline —
// and the case that must not get one anyway.
func TestWriteOrCheckTrailingNewline(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"one is added when missing", []byte("hello"), "hello\n"},
		{"an existing one is left alone", []byte("hello\n"), "hello\n"},
		// A file with nothing in it is the caller's mistake to report, not one
		// this helper should paper over with a blank line.
		{"empty content stays empty", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact.txt")

			if err := WriteOrCheck(path, tt.in, false, "`make thing`"); err != nil {
				t.Fatalf("WriteOrCheck() error = %v", err)
			}
			if got := readFixture(t, path); got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWriteOrCheckCreatesTheParentDirectory pins the half of the convention a
// check deliberately does not share: writing makes the tree, checking reports it.
func TestWriteOrCheckCreatesTheParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "artifact.txt")

	if err := WriteOrCheck(path, []byte("hello"), false, "`make thing`"); err != nil {
		t.Fatalf("WriteOrCheck() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the artifact is not there: %v", err)
	}
}

// TestWriteOrCheckDoesNotMutateTheCaller guards the copy in ensureTrailingNewline.
// A slice with spare capacity is where appending the newline in place would show,
// and a caller of a writing helper does not expect its argument to change.
func TestWriteOrCheckDoesNotMutateTheCaller(t *testing.T) {
	content := make([]byte, 3, 8)
	copy(content, "abc")
	path := filepath.Join(t.TempDir(), "artifact.txt")

	if err := WriteOrCheck(path, content, false, "`make thing`"); err != nil {
		t.Fatalf("WriteOrCheck() error = %v", err)
	}
	if string(content) != "abc" {
		t.Errorf("the argument became %q; WriteOrCheck wrote into the caller's backing array", content)
	}
}

// TestWriteOrCheckChecks covers the checking half, which is what CI runs: a
// current artifact passes, a stale one is named along with the command that
// refreshes it, a missing one is distinguishable from a stale one, and no check
// ever creates anything.
func TestWriteOrCheckChecks(t *testing.T) {
	t.Run("a current artifact passes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "artifact.txt")
		writeFixture(t, path, "hello\n")

		if err := WriteOrCheck(path, []byte("hello"), true, "`make thing`"); err != nil {
			t.Errorf("WriteOrCheck() error = %v, want nil", err)
		}
	})

	t.Run("a stale artifact names the file and the command", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "artifact.txt")
		writeFixture(t, path, "old\n")

		err := WriteOrCheck(path, []byte("new"), true, "`make thing`")
		if err == nil {
			t.Fatal("WriteOrCheck() error = nil, want a staleness report")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("the report does not name the file: %v", err)
		}
		if !strings.Contains(err.Error(), "`make thing`") {
			t.Errorf("the report does not name the regenerating command: %v", err)
		}
	})

	t.Run("CRLF compares equal to LF", func(t *testing.T) {
		// A Windows checkout must not report drift a Linux one does not see.
		path := filepath.Join(t.TempDir(), "artifact.txt")
		writeFixture(t, path, "one\r\ntwo\r\n")

		if err := WriteOrCheck(path, []byte("one\ntwo\n"), true, "`make thing`"); err != nil {
			t.Errorf("WriteOrCheck() error = %v, want the line endings to be ignored", err)
		}
	})

	t.Run("a missing artifact is told apart from a stale one", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent.txt")

		err := WriteOrCheck(path, []byte("hello"), true, "`make thing`")
		if err == nil {
			t.Fatal("WriteOrCheck() error = nil, want a read failure")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("errors.Is(err, fs.ErrNotExist) = false for %v; the two failures want different fixes", err)
		}
	})

	t.Run("a check creates nothing", func(t *testing.T) {
		// A gate reports a missing tree rather than making one.
		dir := t.TempDir()
		path := filepath.Join(dir, "nested", "artifact.txt")

		if err := WriteOrCheck(path, []byte("hello"), true, "`make thing`"); err == nil {
			t.Fatal("WriteOrCheck() error = nil, want a failure on the missing directory")
		}
		if _, err := os.Stat(filepath.Join(dir, "nested")); !errors.Is(err, fs.ErrNotExist) {
			t.Error("the check created the parent directory")
		}
	})
}

// TestWriteReport covers the other contract: an operator names the destination,
// nothing is compared, and an empty path or the "-" sentinel means stdout.
func TestWriteReport(t *testing.T) {
	t.Run("an empty path writes to the supplied writer", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteReport(&buf, "", []byte("report")); err != nil {
			t.Fatalf("WriteReport() error = %v", err)
		}
		if buf.String() != "report" {
			t.Errorf("stdout = %q, want %q", buf.String(), "report")
		}
	})

	t.Run("the dash sentinel writes to the supplied writer", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteReport(&buf, "-", []byte("report")); err != nil {
			t.Fatalf("WriteReport() error = %v", err)
		}
		if buf.String() != "report" {
			t.Errorf("stdout = %q, want %q", buf.String(), "report")
		}
	})

	t.Run("a path writes the file and creates its directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nested", "report.md")

		var buf bytes.Buffer
		if err := WriteReport(&buf, path, []byte("report")); err != nil {
			t.Fatalf("WriteReport() error = %v", err)
		}
		got, err := os.ReadFile(path) //#nosec G304 -- the path is the test's own temp dir.
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "report" {
			t.Errorf("content = %q, want %q", got, "report")
		}
		if buf.Len() != 0 {
			t.Errorf("stdout got %q; a named path must not also print", buf.String())
		}
	})

	t.Run("no trailing newline is added", func(t *testing.T) {
		// Unlike an artifact, a report is whatever the run produced; the caller
		// decides how it ends.
		path := filepath.Join(t.TempDir(), "report.md")

		if err := WriteReport(&bytes.Buffer{}, path, []byte("report")); err != nil {
			t.Fatalf("WriteReport() error = %v", err)
		}
		got, err := os.ReadFile(path) //#nosec G304 -- the path is the test's own temp dir.
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "report" {
			t.Errorf("content = %q, want it left exactly as given", got)
		}
	})
}

// TestNormalizeNewlines pins the one line the generators' own tests share, which
// is why it is exported at all.
func TestNormalizeNewlines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"CRLF becomes LF", "a\r\nb\r\n", "a\nb\n"},
		{"LF is left alone", "a\nb\n", "a\nb\n"},
		{"a lone CR is left alone", "a\rb", "a\rb"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(NormalizeNewlines([]byte(tt.in))); got != tt.want {
				t.Errorf("NormalizeNewlines(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// writeFixture puts content at path, failing the test if it cannot.
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), GeneratedFileMode); err != nil {
		t.Fatal(err)
	}
}

// readFixture returns what is at path, failing the test if it cannot be read.
func readFixture(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path) //#nosec G304 -- the path is the test's own temp dir.
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}
