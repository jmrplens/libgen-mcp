package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openFile opens path for a test and closes it when the test ends. The readers
// take an open file rather than a path, so every fixture goes through here.
func openFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path) //#nosec G304 -- a test fixture path.
	if err != nil {
		t.Fatalf("open fixture %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// docFor opens path as a document for a test of one of the unexported readers.
func docFor(t *testing.T, path string) document {
	t.Helper()
	d, err := newDocument(openFile(t, path))
	if err != nil {
		t.Fatalf("newDocument(%s): %v", path, err)
	}
	return d
}

// unreadableFixture creates a directory named name under dir and returns its
// path: something that opens like a file and fails every read, which is what a
// file the caller managed to open but cannot read looks like to the readers.
func unreadableFixture(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNewDocumentReportsAClosedFile covers the one failure newDocument has: a
// descriptor that cannot be described is an error the caller sees, not an empty
// document read as a zero-byte file.
func TestNewDocumentReportsAClosedFile(t *testing.T) {
	f, openErr := os.Open("testdata/sample.txt")
	if openErr != nil {
		t.Fatal(openErr)
	}
	_ = f.Close()

	for name, call := range map[string]func() error{
		"Extract": func() error { _, err := Extract(context.Background(), f, Req{}); return err },
		"Search":  func() error { _, err := Search(context.Background(), f, "the", SearchOpts{}); return err },
		"Outline": func() error { _, err := Outline(context.Background(), f); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if call() == nil {
				t.Errorf("%s on a closed file returned no error", name)
			}
		})
	}
}

// TestReadersReadTheDescriptorNotTheName is the property the read tool's
// containment rests on: once a file is open, what the readers return is that
// file's content, whatever its name resolves to now. The name is pointed at a
// different file after the open, and every mode must still read the first one.
func TestReadersReadTheDescriptorNotTheName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(path, []byte("checked content"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := openFile(t, path)

	// Replace what the name resolves to. A reader that reopened the file by name
	// would now read "swapped content".
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("swapped content"), 0o600); err != nil {
		t.Fatal(err)
	}

	chunk, err := Extract(context.Background(), f, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Text != "checked content" {
		t.Errorf("Extract read %q, want the opened file's content", chunk.Text)
	}
	res, err := Search(context.Background(), f, "swapped", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 0 {
		t.Errorf("Search found %d matches for text only the swapped-in file has", res.TotalMatches)
	}
	res, err = Search(context.Background(), f, "checked", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 1 || !strings.Contains(res.Matches[0].Snippet, "checked") {
		t.Errorf("Search = %+v, want the one match in the opened file", res)
	}
}
