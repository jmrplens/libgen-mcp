// docgo.go moves a package comment into a doc.go of its own, which is the one
// place the audit accepts it.

package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// packageDocFile is the file a package comment lives in.
//
// A package comment anywhere else is one file header away from being mistaken
// for one, and one more file away from being duplicated — Go attaches the
// comment above any file's package clause, so a second one is a silent
// second package doc rather than an error. The convention gives it one home
// and the audit reports every other.
const packageDocFile = "doc.go"

// Seams over the standard library, so a test can drive the failure branches
// that a filesystem the tests own, running as root, never produces on its own:
// a walk that reports an error, a format of bytes that were just parsed, and a
// write that cannot land.
var (
	walkDir        = filepath.WalkDir
	formatDocGo    = format.Source
	writeDocGoFile = os.WriteFile
)

// skippedDocDirs are the directories a move never descends into: none of them
// holds a package of this repository.
var skippedDocDirs = map[string]bool{"testdata": true, "vendor": true, "node_modules": true, "dist": true}

// movePackageDocs applies [movePackageDoc] to a directory and everything below
// it, or to the directory of a file.
//
// Every failure is collected rather than returned at the first: a tree where
// one package cannot be moved is still a tree where the others should be, and
// stopping early would make a person run this once per package.
func movePackageDocs(path string) error {
	cleanPath := filepath.Clean(path)
	info, err := os.Stat(cleanPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", cleanPath, err)
	}
	if !info.IsDir() {
		return movePackageDoc(filepath.Dir(cleanPath))
	}
	var errs []error
	walkErr := walkDir(cleanPath, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		if name := d.Name(); p != cleanPath && (strings.HasPrefix(name, ".") || skippedDocDirs[name]) {
			return fs.SkipDir
		}
		if moveErr := movePackageDoc(p); moveErr != nil {
			errs = append(errs, moveErr)
		}
		return nil
	})
	return errors.Join(append(errs, walkErr)...)
}

// packageDocHolder is the source file that carries the package comment.
type packageDocHolder struct {
	path string
	file *ast.File
	src  []byte
	fset *token.FileSet
}

// movePackageDoc moves the package comment of the package in dir into doc.go,
// when a well-formed one lives in another file and no doc.go exists.
//
// The comment is copied verbatim above the package clause of the new file and
// cut from the file it came from. A malformed comment is left where it is, for
// the audit to report: moving it would enshrine as the package's documentation
// a comment the convention already refuses.
func movePackageDoc(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("readdir %s: %w", dir, err)
	}
	var holder *packageDocHolder
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == packageDocFile {
			return nil
		}
		if holder != nil {
			continue
		}
		found, readErr := readDocHolder(filepath.Join(dir, name))
		if readErr != nil {
			return readErr
		}
		holder = found
	}
	if holder == nil {
		return nil
	}
	return holder.moveToDocGo(dir)
}

// readDocHolder reads one file and returns it when it carries a well-formed
// package comment, or nil when it does not.
func readDocHolder(path string) (*packageDocHolder, error) {
	src, err := os.ReadFile(path) //#nosec G304 -- the path comes from a command-line argument
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	fset := token.NewFileSet()
	file, parseErr := parser.ParseFile(fset, path, src, parser.ParseComments)
	if parseErr != nil {
		return nil, fmt.Errorf("parse %s: %w", path, parseErr)
	}
	if file.Doc == nil || !validPackageDoc(file.Name.Name, strings.TrimSpace(file.Doc.Text())) {
		return nil, nil //nolint:nilnil // no holder and no failure is the ordinary answer for a file with no package comment
	}
	return &packageDocHolder{path: path, file: file, src: src, fset: fset}, nil
}

// concat joins byte slices into a new one, so a caller composing a file out of
// three pieces does not have to say so three times.
func concat(pieces ...[]byte) []byte {
	var joined []byte
	for _, piece := range pieces {
		joined = append(joined, piece...)
	}
	return joined
}

// buildConstraint returns the //go:build line of a file header, with the blank
// line that has to follow it, or nothing when the header carries none.
//
// Only the constraint is taken. Whatever else a header holds belongs to the
// file it is in, and copying it would put a second copy of somebody's note in
// a file they did not write.
func buildConstraint(header []byte) []byte {
	for line := range strings.SplitSeq(string(header), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//go:build ") {
			return []byte(strings.TrimSpace(line) + "\n\n")
		}
	}
	return nil
}

// moveToDocGo writes the holder's package comment into doc.go and cuts it from
// the holder.
func (h *packageDocHolder) moveToDocGo(dir string) error {
	docStart := h.fset.Position(h.file.Doc.Pos()).Offset
	docEnd := h.fset.Position(h.file.Doc.End()).Offset
	pkgStart := h.fset.Position(h.file.Package).Offset
	if docStart < 0 || docEnd > pkgStart || pkgStart > len(h.src) {
		return fmt.Errorf("%s: the package comment does not sit above the package clause", h.path)
	}

	// The build constraint is copied rather than moved, because it is not one:
	// a constraint governs the file it is in, and doc.go is a new file of the
	// same package. Leaving it behind would give a tagged package one file
	// that builds without the tag — for cmd/eval, a main package with no main
	// function, which every plain `go vet ./...` would report.
	docGo, err := formatDocGo(concat(buildConstraint(h.src[:docStart]),
		h.src[docStart:docEnd], []byte("\npackage "+h.file.Name.Name+"\n")))
	if err != nil {
		return fmt.Errorf("format doc.go for %s: %w", dir, err)
	}
	remaining, err := formatDocGo(concat(h.src[:docStart], h.src[pkgStart:]))
	if err != nil {
		return fmt.Errorf("format %s without its package comment: %w", h.path, err)
	}

	docPath := filepath.Join(dir, packageDocFile)
	if dryRun {
		fmt.Printf("dry-run: would move the package comment of %s from %s into %s\n",
			h.file.Name.Name, filepath.Base(h.path), docPath)
		return nil
	}
	if writeErr := writeDocGoFile(docPath, docGo, 0o600); writeErr != nil {
		return fmt.Errorf("write %s: %w", docPath, writeErr)
	}
	if writeErr := writeDocGoFile(h.path, remaining, 0o600); writeErr != nil {
		return fmt.Errorf("write %s: %w", h.path, writeErr)
	}
	fmt.Printf("moved the package comment of %s from %s into %s\n",
		h.file.Name.Name, filepath.Base(h.path), docPath)
	return nil
}
