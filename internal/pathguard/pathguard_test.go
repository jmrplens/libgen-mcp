package pathguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowLocal turns local-path access on for one test, since the package fails
// closed and every case here is about what a local deployment may reach.
func allowLocal(t *testing.T) {
	t.Helper()
	previous := localAccessAllowed.Load()
	SetLocalAccess(true)
	t.Cleanup(func() { SetLocalAccess(previous) })
}

// rootsFor builds a Roots that allows exactly dir and nothing else implicit.
func rootsFor(dir string) Roots {
	return Roots{Implicit: []string{dir}, EnvName: "LIBGEN_MCP_ALLOWED_READ_DIRS"}
}

// tempDirVars are every environment variable os.TempDir consults: TMPDIR on
// Unix, then TMP and TEMP on Windows.
var tempDirVars = []string{"TMPDIR", "TMP", "TEMP"}

// sandbox returns a directory tree in which "outside every root" is true.
//
// The OS temporary directory is always an implicit root, and t.TempDir hands
// back a directory under it — so a fixture built with t.TempDir alone is inside
// a root whatever else the test says, and a containment test written that way
// passes trivially against a guard that refuses nothing. Isolating os.TempDir to
// one subdirectory makes the siblings genuinely outside.
//
// The isolation is verified rather than assumed, because an isolation that
// quietly did nothing would leave these tests asserting the same trivial pass.
func sandbox(t *testing.T) (outside, allowed string) {
	t.Helper()

	//nolint:usetesting // t.TempDir is the thing being escaped: it lives under the root being isolated.
	base, err := os.MkdirTemp("", "pg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	tmp := filepath.Join(base, "tmp")
	outside = filepath.Join(base, "outside")
	allowed = filepath.Join(base, "allowed")
	for _, dir := range []string{tmp, outside, allowed} {
		if mkErr := os.Mkdir(dir, 0o750); mkErr != nil {
			t.Fatal(mkErr)
		}
	}
	for _, name := range tempDirVars {
		t.Setenv(name, tmp)
	}
	//nolint:usetesting // os.TempDir is the subject of the check, not an alternative to t.TempDir.
	if got, want := filepath.Clean(os.TempDir()), filepath.Clean(tmp); got != want {
		t.Fatalf("os.TempDir() = %q after isolating it to %q; this platform reads a variable %v does not cover",
			got, want, tempDirVars)
	}
	return outside, allowed
}

// writeFile creates a file with content and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRefusesAPathOutsideEveryRoot is the containment itself: the argument this
// guard exists for is a path a model chose, and the text read returns is
// labeled untrusted, so "somewhere else on this disk" has to be a refusal
// rather than a file.
func TestRefusesAPathOutsideEveryRoot(t *testing.T) {
	allowLocal(t)
	outsideDir, allowed := sandbox(t)
	outside := writeFile(t, outsideDir, "secret", "id_rsa")

	_, err := CanonicalFile(outside, rootsFor(allowed))
	if err == nil {
		t.Fatal("a path outside every root was accepted")
	}
	if !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("err = %v, want it to say the path is outside the roots", err)
	}
	if !strings.Contains(err.Error(), "LIBGEN_MCP_ALLOWED_READ_DIRS") {
		t.Errorf("err = %v, want it to name the variable that would widen the roots", err)
	}
}

// TestAcceptsAPathInsideARoot is the other half: a containment that refuses the
// legitimate case is one an operator switches off.
func TestAcceptsAPathInsideARoot(t *testing.T) {
	allowLocal(t)
	dir := t.TempDir()
	inside := writeFile(t, dir, "book.pdf", "%PDF-1.7")

	got, err := CanonicalFile(inside, rootsFor(dir))
	if err != nil {
		t.Fatalf("CanonicalFile() error = %v, want the path accepted", err)
	}
	// The returned path is canonical: on macOS t.TempDir is under a symlinked
	// /var, so comparing against the input would fail for the wrong reason.
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !withinBase(got, wantDir) {
		t.Errorf("CanonicalFile() = %q, want a path under %q", got, wantDir)
	}
}

// TestRefusesASymlinkEscapingItsRoot is the case a prefix check cannot catch:
// the path is inside an allowed directory and the file is not.
func TestRefusesASymlinkEscapingItsRoot(t *testing.T) {
	allowLocal(t)
	outsideDir, allowed := sandbox(t)
	target := writeFile(t, outsideDir, "secret", "id_rsa")

	link := filepath.Join(allowed, "innocent.pdf")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this platform cannot create a symlink: %v", err)
	}

	if _, err := CanonicalFile(link, rootsFor(allowed)); err == nil {
		t.Error("a symlink inside a root pointing outside it was accepted")
	}
}

// TestRefusesAPathThatIsNotARegularFile covers what OpenFile adds over the path
// checks: a directory, a device node or a fifo is not a document, and handing
// one to a reader is at best a confusing failure.
func TestRefusesAPathThatIsNotARegularFile(t *testing.T) {
	allowLocal(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	_, _, err := OpenFile(sub, 0, rootsFor(dir))
	if err == nil {
		t.Fatal("a directory was opened as a file")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("err = %v, want it to say the path is not a regular file", err)
	}
}

// TestOpenFileEnforcesTheSizeBound covers the maxSize argument, which is passed
// on the text legs and deliberately left at zero on the ones that seek.
func TestOpenFileEnforcesTheSizeBound(t *testing.T) {
	allowLocal(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "big.txt", strings.Repeat("x", 100))

	if _, _, err := OpenFile(path, 10, rootsFor(dir)); err == nil {
		t.Error("a file over the bound was opened")
	}

	f, _, err := OpenFile(path, 0, rootsFor(dir))
	if err != nil {
		t.Fatalf("a zero bound must mean no bound, got %v", err)
	}
	_ = f.Close()
}

// TestFailsClosedWithoutLocalAccess pins the transport gate, and pins that it
// lives inside the guard: a path argument added later inherits the refusal by
// calling the guard at all, rather than by remembering a rule.
func TestFailsClosedWithoutLocalAccess(t *testing.T) {
	previous := localAccessAllowed.Load()
	SetLocalAccess(false)
	t.Cleanup(func() { SetLocalAccess(previous) })

	dir := t.TempDir()
	path := writeFile(t, dir, "book.pdf", "%PDF-1.7")

	_, err := CanonicalFile(path, rootsFor(dir))
	if err == nil {
		t.Fatal("a local path was accepted on a deployment with no local access")
	}
	if !strings.Contains(err.Error(), "not available on a remote server") {
		t.Errorf("err = %v, want the remote-server message", err)
	}
	if !strings.Contains(err.Error(), "md5 or doi") {
		t.Errorf("err = %v, want it to name the alternative", err)
	}

	if _, oerr := CanonicalOutputPath(filepath.Join(dir, "out.pdf"), rootsFor(dir)); oerr == nil {
		t.Error("an output path was accepted on a deployment with no local access")
	}
}

// TestOutputPathAcceptsSomethingNotYetThere is what separates a destination from
// a source: a download writes a file that does not exist, so the containment has
// to resolve the part of the path that does.
func TestOutputPathAcceptsSomethingNotYetThere(t *testing.T) {
	allowLocal(t)
	dir := t.TempDir()

	got, err := CanonicalOutputPath(filepath.Join(dir, "not", "there", "yet.pdf"), rootsFor(dir))
	if err != nil {
		t.Fatalf("CanonicalOutputPath() error = %v, want a destination that does not exist accepted", err)
	}
	if !strings.HasSuffix(got, filepath.Join("not", "there", "yet.pdf")) {
		t.Errorf("CanonicalOutputPath() = %q, want the non-existent tail preserved", got)
	}
}

// TestOutputPathRefusesASymlinkedAncestor is the write-side twin of the read
// escape: the destination itself does not exist, so only the existing part of
// the path can be resolved — and that is where the link is.
func TestOutputPathRefusesASymlinkedAncestor(t *testing.T) {
	allowLocal(t)
	elsewhere, allowed := sandbox(t)

	link := filepath.Join(allowed, "downloads")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("this platform cannot create a symlink: %v", err)
	}

	if _, err := CanonicalOutputPath(filepath.Join(link, "book.pdf"), rootsFor(allowed)); err == nil {
		t.Error("a destination under a symlink leaving the root was accepted")
	}
}

// TestWithinBaseIsNotAStringPrefix pins the one mistake this predicate exists to
// avoid: /home/user2 has /home/user as a string prefix and is not under it.
func TestWithinBaseIsNotAStringPrefix(t *testing.T) {
	tests := []struct {
		base, path string
		want       bool
	}{
		{"/home/user", "/home/user", true},
		{"/home/user", "/home/user/docs/a.pdf", true},
		{"/home/user", "/home/user2/a.pdf", false},
		{"/home/user", "/home", false},
		{"/home/user", "/etc/passwd", false},
	}
	for _, tt := range tests {
		t.Run(tt.base+" vs "+tt.path, func(t *testing.T) {
			if got := withinBase(filepath.FromSlash(tt.path), filepath.FromSlash(tt.base)); got != tt.want {
				t.Errorf("withinBase(%q, %q) = %v, want %v", tt.path, tt.base, got, tt.want)
			}
		})
	}
}

// TestHomeIsNotAnImplicitRoot is the judgement the whole containment turns on.
//
// A server started in the user's home directory would, with the working
// directory as an implicit root, allow-list ~/.ssh, ~/.aws, the browser profiles
// and this server's own .env — which is to say it would allow-list exactly what
// the containment exists to protect.
func TestHomeIsNotAnImplicitRoot(t *testing.T) {
	allowLocal(t)
	home, _ := sandbox(t)

	previousHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = previousHome })
	t.Chdir(home)

	secret := writeFile(t, home, ".env", "LIBGEN_MCP_ANNAS_KEY=s3cret")

	// Roots with nothing implicit: the working directory is the only candidate,
	// and it is home.
	_, err := CanonicalFile(secret, Roots{EnvName: "LIBGEN_MCP_ALLOWED_READ_DIRS"})
	if err == nil {
		t.Fatal("a file in the home directory was readable with the server started there")
	}
	if !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("err = %v, want the containment refusal", err)
	}
}

// TestHomeComesBackWhenNamed is the other side of that judgement: it is skipped
// as an implicit root, not forbidden. An operator whose workspace really is
// their home directory says so and gets it back.
func TestHomeComesBackWhenNamed(t *testing.T) {
	allowLocal(t)
	home := t.TempDir()

	previousHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = previousHome })
	t.Chdir(home)

	doc := writeFile(t, home, "notes.txt", "hello")

	if _, err := CanonicalFile(doc, Roots{
		Configured: []string{home},
		EnvName:    "LIBGEN_MCP_ALLOWED_READ_DIRS",
	}); err != nil {
		t.Errorf("CanonicalFile() error = %v, want the named home directory allowed", err)
	}
}

// TestAnUnusableConfiguredRootIsDropped covers the allow-list entry that is a
// typo: it is skipped with a warning rather than taken as matching nothing in
// silence, and it must not take the usable roots down with it.
func TestAnUnusableConfiguredRootIsDropped(t *testing.T) {
	allowLocal(t)
	dir := t.TempDir()
	doc := writeFile(t, dir, "book.pdf", "%PDF-1.7")

	roots := Roots{
		Implicit:   []string{dir},
		Configured: []string{filepath.Join(dir, "does-not-exist"), ""},
		EnvName:    "LIBGEN_MCP_ALLOWED_READ_DIRS",
	}
	if _, err := CanonicalFile(doc, roots); err != nil {
		t.Errorf("CanonicalFile() error = %v; an unusable allow-list entry took the good roots with it", err)
	}
}

// TestOpenNoFollowRefusesASwappedLeaf covers the race the two Lstat/Stat checks
// cannot close on their own, on the platforms where the kernel can.
func TestOpenNoFollowRefusesASwappedLeaf(t *testing.T) {
	if !NoFollowSupported() {
		t.Skip("this platform has no O_NOFOLLOW, so the open cannot refuse a leaf symlink itself")
	}
	dir := t.TempDir()
	target := writeFile(t, dir, "real.txt", "data")
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this platform cannot create a symlink: %v", err)
	}

	f, err := OpenNoFollow(link, os.O_RDONLY, 0)
	if err == nil {
		_ = f.Close()
		t.Fatal("a symlink at the leaf was opened")
	}
}

// TestCanonicalFileRequiresAPath pins the empty-argument message, which is the
// one a caller sees most often and the only one that is not about containment.
func TestCanonicalFileRequiresAPath(t *testing.T) {
	allowLocal(t)
	for _, in := range []string{"", "   "} {
		if _, err := CanonicalFile(in, rootsFor(t.TempDir())); err == nil {
			t.Errorf("CanonicalFile(%q) = nil, want an error", in)
		}
	}
}

// TestNoExistingAncestorIsReported covers the loop's terminating case rather
// than letting it be reached only by accident.
func TestNoExistingAncestorIsReported(t *testing.T) {
	_, err := canonicalizeThroughExistingAncestor(filepath.Join(string(filepath.Separator), "\x00nope", "x"))
	if err == nil {
		t.Skip("this platform resolved a path with a NUL byte, so there is no unreachable ancestor to report")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want something other than a bare not-exist", err)
	}
}
