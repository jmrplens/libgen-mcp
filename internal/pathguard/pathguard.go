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
	if err := RequireLocalAccess("path", "md5 or doi"); err != nil {
		return "", err
	}
	absolutePath, err := absClean(path, "path")
	if err != nil {
		return "", err
	}
	canonicalPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", absolutePath, err)
	}
	if !withinAny(canonicalPath, roots.dirs()) {
		return "", outsideError("path", canonicalPath, roots.EnvName)
	}
	return canonicalPath, nil
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

// CanonicalReadableFile is [CanonicalFile] plus the two conditions a reader
// needs: the path must name a regular file, and when maxSize is positive it must
// not be larger.
//
// It returns a path rather than an open descriptor, and that is a real
// limitation rather than a preference. Handing back a *os.File would close the
// window between checking a path and opening it — a local principal able to
// write in an allowed root, and the OS temporary directory is always one and is
// world-writable, can replace the leaf in between. Closing it needs the code
// that finally reads the document to accept a descriptor, which here means five
// call sites in internal/extract, each of which opens the file itself. That is
// its own change; what is here is the containment, and the leaf swap is
// documented rather than claimed.
//
// maxSize of zero means no size bound. The caller decides: the text legs of
// internal/extract already cap themselves at 8 MiB, and a PDF is read by seeking
// rather than loaded whole, so a byte cap there would refuse large legitimate
// books without bounding the work.
func CanonicalReadableFile(path string, maxSize int64, roots Roots) (string, error) {
	canonicalPath, err := CanonicalFile(path, roots)
	if err != nil {
		return "", err
	}
	// Lstat, not Stat: canonicalPath is already symlink-free, so the two agree
	// unless the leaf became a symlink between resolution and here — and in that
	// race Lstat is the answer that refuses.
	info, err := os.Lstat(canonicalPath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", canonicalPath, err)
	}
	if checkErr := checkRegularAndSize(canonicalPath, info, maxSize); checkErr != nil {
		return "", checkErr
	}
	return canonicalPath, nil
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
	for _, base := range roots {
		if withinBase(path, base) {
			return true
		}
	}
	return false
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
