package pathguard

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Roots describes where a caller-supplied path is allowed to resolve.
//
// It is a value rather than package state because the two path arguments this
// server takes answer to different allow-lists: what `read` may open and what
// `download` may write into are separate decisions, and an operator who widens
// one has not widened the other.
type Roots struct {
	// Implicit are roots this deployment grants without being asked, such as the
	// directory downloads are saved to. They are not named in a refusal message,
	// because an operator cannot act on them.
	Implicit []string
	// Configured are the directories an operator listed in EnvName.
	Configured []string
	// EnvName is the fully spelled variable a refusal points at.
	EnvName string
}

// localAccessAllowed reports whether caller-supplied local paths may be used at
// all. It is false on a deployment where the caller and the filesystem are
// different machines, which is decided once at startup.
var localAccessAllowed atomic.Bool

// SetLocalAccess records whether this process may act on caller-supplied local
// paths. It is called once during startup, from the code that knows which
// transport was chosen.
func SetLocalAccess(allowed bool) { localAccessAllowed.Store(allowed) }

// LocalAccessAllowed reports what [SetLocalAccess] was last told.
func LocalAccessAllowed() bool { return localAccessAllowed.Load() }

// RequireLocalAccess returns nil when local paths may be used, and otherwise an
// error naming the argument that was refused and what to use instead.
//
// what is the tool argument's name; alternative is what the caller should reach
// for on a remote deployment, or empty when there is nothing to suggest.
//
// This is the one place the transport gate lives. It used to be an inline check
// beside one argument's validation, which is a rule that holds for exactly as
// long as the next path argument remembers it.
func RequireLocalAccess(what, alternative string) error {
	if localAccessAllowed.Load() {
		return nil
	}
	if alternative == "" {
		return fmt.Errorf("%s is not available on a remote server", what)
	}
	return fmt.Errorf("%s is not available on a remote server; use %s", what, alternative)
}

// CanonicalFile resolves path to an existing local file, through symlinks, and
// returns it canonicalized provided it lies under one of roots.
func CanonicalFile(path string, roots Roots) (string, error) {
	canonicalPath, _, err := containedFile(path, roots)
	return canonicalPath, err
}

// containedFile is [CanonicalFile] that also returns the root the canonical
// path was found under, which is what [OpenReadableFile] opens it relative to.
func containedFile(path string, roots Roots) (canonicalPath, root string, err error) {
	if accessErr := RequireLocalAccess("path", "md5 or doi"); accessErr != nil {
		return "", "", accessErr
	}
	absolutePath, err := absClean(path, "path")
	if err != nil {
		return "", "", err
	}
	canonicalPath, err = filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", "", fmt.Errorf("resolve path %s: %w", absolutePath, err)
	}
	root, ok := containingRoot(canonicalPath, roots.dirs())
	if !ok {
		return "", "", outsideError("path", canonicalPath, roots.EnvName)
	}
	return canonicalPath, root, nil
}

// CanonicalOutputPath resolves a destination that does not have to exist yet,
// through the symlinks of the part of it that does, and returns it
// canonicalized provided it lies under one of roots.
//
// An existing destination that is not a regular file is refused outright: a
// download that would write through a device node or a directory is not a
// download anyone asked for.
func CanonicalOutputPath(path string, roots Roots) (string, error) {
	if err := RequireLocalAccess("path", ""); err != nil {
		return "", err
	}
	absolutePath, err := absClean(path, "output path")
	if err != nil {
		return "", err
	}
	canonicalPath, err := canonicalizeThroughExistingAncestor(absolutePath)
	if err != nil {
		return "", fmt.Errorf("resolve output path %s: %w", absolutePath, err)
	}
	if info, statErr := os.Lstat(canonicalPath); statErr == nil && !info.Mode().IsRegular() && !info.IsDir() {
		return "", fmt.Errorf("output path %s already exists and is neither a file nor a directory", canonicalPath)
	}
	if !withinAny(canonicalPath, roots.dirs()) {
		return "", outsideError("output path", canonicalPath, roots.EnvName)
	}
	return canonicalPath, nil
}

// beforeOpen runs between [OpenReadableFile]'s checks and its open. It is nil
// outside tests: a test sets it to swap the checked file for something else at
// exactly the moment a racing local principal would, so the refusal can be
// asserted deterministically rather than by winning a race.
var beforeOpen func(canonicalPath string)

// OpenReadableFile resolves path the way [CanonicalFile] does, opens it, and
// returns the open file, provided it is a regular file and, when maxSize is
// positive, no larger than maxSize. The returned file's Name is the canonical
// path, and the caller owns closing it.
//
// It returns a descriptor rather than a path because a path is a question asked
// again by every later open, and the answer can change in between: a local
// principal able to write in an allowed root — the OS temporary directory is
// always one, and /tmp is world-writable — can replace the checked file, or a
// directory above it, with a symlink to a file outside every root after the
// check has passed. Whatever reads the document must therefore read this
// descriptor and never reopen the file by name.
//
// What is defended, on every platform this server ships for:
//
//   - The open is made relative to the root the check found the path under,
//     through [os.Root], so each component is walked by the kernel from that
//     root's own descriptor and a symlink, junction or ".." that would leave it
//     makes the open fail. A leaf or a directory below the root swapped after
//     the check cannot redirect the open outside the roots. On unix this is openat with
//     O_NOFOLLOW per component, the same refusal [OpenNoFollow] gives the
//     download's partial file; on Windows it is the handle-relative equivalent,
//     so Windows gets the same containment even though it has no O_NOFOLLOW.
//   - The root opened is the outermost allowed root holding the path, so a
//     nested root that can be swapped from inside an enclosing one is walked
//     through, not opened by name.
//   - The regular-file and size conditions are re-checked on the open
//     descriptor, so a file swapped for a directory, a device or a fifo after
//     the check is refused. On unix the open is non-blocking, so a fifo planted
//     in that window cannot hang the call before the descriptor is examined.
//   - The opened file must be the file the pre-open Lstat saw ([os.SameFile]).
//     The root directory itself is opened by name, and this is what refuses a
//     root, or a directory above it, swapped for a link after the check.
//
// What is not:
//
//   - A swap of the root or a directory above it (which needs write access
//     outside every root) made after the path was resolved but before the
//     pre-open Lstat, and still in place at the open: the Lstat and the open
//     then agree on the same outside file.
//   - A swap to a different file that is itself inside the roots, which still
//     opens and is a file the caller could have named directly.
//   - A hard link to an outside file, made inside a root, which is not a
//     path-level escape at all and is out of reach of any path check; on Linux
//     fs.protected_hardlinks is what stops an unprivileged principal making one
//     to a file it cannot read.
//
// maxSize of zero means no size bound. The caller decides: the text legs of
// internal/extract already cap themselves at 8 MiB, and a PDF is read by seeking
// rather than loaded whole, so a byte cap there would refuse large legitimate
// books without bounding the work.
func OpenReadableFile(path string, maxSize int64, roots Roots) (*os.File, error) {
	canonicalPath, root, err := containedFile(path, roots)
	if err != nil {
		return nil, err
	}
	// Lstat before the open, so a directory or a device is refused with its own
	// diagnosis without being opened at all, and so the file the check named has
	// an identity the opened one can be compared with.
	info, err := os.Lstat(canonicalPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", canonicalPath, err)
	}
	if checkErr := checkRegularAndSize(canonicalPath, info, maxSize); checkErr != nil {
		return nil, checkErr
	}
	// On Windows an Lstat of an ordinary file records only its path, and
	// os.SameFile reads the file's identity from that path the first time it is
	// asked — which, asked after the open, would be after any swap. Asking now
	// pins the identity of the file the check saw. Elsewhere this is a no-op.
	_ = os.SameFile(info, info)
	if beforeOpen != nil {
		beforeOpen(canonicalPath)
	}
	f, err := openInRoot(root, canonicalPath)
	if err != nil {
		return nil, err
	}
	if checkErr := checkOpened(f, canonicalPath, info, maxSize); checkErr != nil {
		_ = f.Close()
		return nil, checkErr
	}
	return f, nil
}

// checkOpened applies the conditions that must hold of the opened descriptor
// itself: a regular file, within maxSize, and the very file the check named.
//
// The identity check is what covers the root. [os.Root] keeps every component
// below the root from leaving it, but the root directory is opened by name, so a
// root (or a directory above it) replaced by a symlink after the check would
// hand the walk a different tree. The file found there is not the file the
// Lstat saw, and this refuses it.
func checkOpened(f *os.File, canonicalPath string, checked os.FileInfo, maxSize int64) error {
	opened, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", canonicalPath, err)
	}
	if checkErr := checkRegularAndSize(canonicalPath, opened, maxSize); checkErr != nil {
		return checkErr
	}
	if !os.SameFile(checked, opened) {
		return fmt.Errorf("open %s: the file opened is not the file that was checked; it was replaced in between", canonicalPath)
	}
	return nil
}

// openInRoot opens canonicalPath for reading relative to root, which must
// contain it, refusing any resolution that would leave root.
func openInRoot(root, canonicalPath string) (*os.File, error) {
	rel, err := filepath.Rel(root, canonicalPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", canonicalPath, err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", canonicalPath, err)
	}
	// The file outlives the root: closing the root's descriptor does not close
	// one opened through it.
	defer func() { _ = r.Close() }()
	f, err := r.OpenFile(rel, os.O_RDONLY|readOpenFlag, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: it no longer resolves to the file that was checked inside %s: %w", canonicalPath, root, err)
	}
	return f, nil
}

// OpenNoFollow opens path with the caller's flags and mode, refusing a symlink
// at the leaf where the platform can.
//
// [CanonicalOutputPath] refuses a destination that is already a symlink, but it
// refuses a path, and the file is opened by a later syscall: a local principal
// who can write in an allowed root can put a symlink there in between and
// redirect the write to whatever this process may overwrite. The open is the
// only place that race closes, so it happens here rather than at the call site.
//
// The flags are the caller's because the writers differ: a download's partial
// wants O_RDWR to resume by appending after re-hashing, while a fresh
// destination wants O_TRUNC. What they must not differ on is the leaf.
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|noFollowFlag, perm) //#nosec G304 -- the caller resolved the path through symlinks and confined it to the allowed roots.
}

// NoFollowSupported reports whether this platform refuses a leaf symlink in the
// open itself, rather than only through the checks either side of it.
func NoFollowSupported() bool { return noFollowSupported }

// checkRegularAndSize applies the two conditions that hold both before and after
// the open.
func checkRegularAndSize(path string, info os.FileInfo, maxSize int64) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if maxSize > 0 && info.Size() > maxSize {
		return fmt.Errorf("file %s is %d bytes, which exceeds the %d-byte limit", path, info.Size(), maxSize)
	}
	return nil
}

// absClean turns a caller's path into an absolute, lexically cleaned one,
// refusing an empty value in the caller's own terms.
func absClean(path, kind string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s is required", kind)
	}
	absolutePath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", kind, err)
	}
	return absolutePath, nil
}

// canonicalizeThroughExistingAncestor resolves the longest existing prefix of an
// absolute path through symlinks and rejoins the not-yet-existing tail, so a
// destination can be contained before anything has been created at it.
func canonicalizeThroughExistingAncestor(absolutePath string) (string, error) {
	dir := absolutePath
	tail := ""
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if tail == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, tail), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no existing ancestor directory for %s", absolutePath)
		}
		tail = filepath.Join(filepath.Base(dir), tail)
		dir = parent
	}
}

// outsideError explains a containment refusal in terms the caller can act on:
// what was refused, and which variable widens the roots.
func outsideError(kind, canonicalPath, envName string) error {
	return fmt.Errorf(
		"%s %s is outside the allowed directories; use the working directory, the OS temp directory, the download directory, or name it in %s",
		kind, canonicalPath, envName,
	)
}

// dirs returns the canonical roots a path may resolve into: the working
// directory, the OS temporary directory, whatever this deployment grants
// implicitly, and whatever the operator configured.
//
// The working directory is skipped when it is a filesystem root or the user's
// home directory. A stdio server usually starts in the workspace the user just
// opened, which is what makes it a sensible root; some clients start their
// servers in "/", where keeping it would allow-list the entire disk and quietly
// undo the whole containment. Home is the same argument one level down, and it
// is not hypothetical: it holds ~/.ssh, ~/.aws, the browser profiles and this
// server's own .env, so implicitly allow-listing it means a path naming that
// file returns the very keys the containment exists to protect.
//
// Skipped as an implicit root, not forbidden: an operator whose workspace really
// is their home directory names it in the variable and gets it back. That is the
// difference between a default that is safe and a policy that decides for them,
// and the warning below says so rather than leaving a working setup to fail as a
// puzzling refusal.
func (r Roots) dirs() []string {
	type entry struct {
		path       string
		configured bool
	}
	candidates := []entry{}
	if cwd, err := os.Getwd(); err == nil && filepath.Dir(cwd) != cwd && !skipHomeAsImplicitRoot(cwd, r.EnvName) {
		candidates = append(candidates, entry{path: cwd})
	}
	candidates = append(candidates, entry{path: os.TempDir()})
	for _, dir := range r.Implicit {
		candidates = append(candidates, entry{path: dir})
	}
	for _, dir := range r.Configured {
		candidates = append(candidates, entry{path: dir, configured: true})
	}

	allowed := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		canonicalDir, err := canonicalDirPath(c.path)
		if err != nil {
			if c.configured {
				slog.Warn("skipping an unusable allow-list directory", "env", r.EnvName, "path", c.path, "error", err)
			}
			continue
		}
		if _, ok := seen[canonicalDir]; ok {
			continue
		}
		seen[canonicalDir] = struct{}{}
		allowed = append(allowed, canonicalDir)
	}
	return allowed
}

// homeDirWarned keeps the dropped-home-root warning to one line per process. It
// is emitted from a path a tool call reaches rather than from startup, so
// without this a session doing many reads would repeat it on every one.
var homeDirWarned atomic.Bool

// userHomeDir is os.UserHomeDir, replaceable in tests.
var userHomeDir = os.UserHomeDir

// skipHomeAsImplicitRoot reports whether cwd is the user's home directory, and
// says so once when it is.
//
// A false answer whenever the home directory cannot be determined is the safe
// direction here: it keeps the working directory as a root, which is the
// behavior every deployment already has, rather than silently narrowing the
// allow-list on a platform where os.UserHomeDir happens to fail.
// envName is the variable the remedy points at, supplied by the caller because
// this package does not own the names — internal/config does, and a second
// spelling here is a second thing to keep in step.
func skipHomeAsImplicitRoot(cwd, envName string) bool {
	home, err := userHomeDir()
	if err != nil {
		return false
	}
	// A home of "" needs no guard of its own: canonicalDirPath refuses an empty
	// directory, so the next check answers it with the same false.
	canonicalHome, err := canonicalDirPath(home)
	if err != nil {
		return false
	}
	canonicalCWD, err := canonicalDirPath(cwd)
	if err != nil {
		canonicalCWD = filepath.Clean(cwd)
	}
	if canonicalCWD != canonicalHome {
		return false
	}
	if homeDirWarned.CompareAndSwap(false, true) {
		slog.Warn("the working directory is the home directory, so it is not an implicit root for caller-supplied paths",
			"home", canonicalHome,
			"remedy", "start the server from a project directory, or name the directories you want reachable in "+envName)
	}
	return true
}

// canonicalDirPath resolves dir through symlinks and requires it to be a
// directory, so a root that is a file or a dangling link is dropped rather than
// silently matching nothing.
func canonicalDirPath(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("empty directory")
	}
	absolute, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return resolved, nil
}

// withinAny reports whether path lies under any of the given roots.
func withinAny(path string, roots []string) bool {
	_, ok := containingRoot(path, roots)
	return ok
}

// containingRoot returns the outermost of roots that path lies under.
//
// Outermost, because [OpenReadableFile] opens the root by name and walks the
// rest from its descriptor: a nested root lies inside a directory a reader may
// write in, and opening the enclosing root instead keeps the nested one on the
// walked side, where a swap of it cannot lead the open out.
func containingRoot(path string, roots []string) (string, bool) {
	best := ""
	for _, base := range roots {
		if withinBase(path, base) && (best == "" || withinBase(best, base)) {
			best = base
		}
	}
	return best, best != ""
}

// withinBase reports whether path is base or lies under it.
//
// The comparison is on the relative path rather than on a string prefix, because
// "/home/user2" has "/home/user" as a string prefix and is not under it.
func withinBase(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." ||
		(rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
