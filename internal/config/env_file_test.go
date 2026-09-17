package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// resetEnvFileState puts the two once-per-process memos back so each case starts
// from a clean process, which is what they are otherwise modeling.
//
// Both memos are correct in production and awkward in a test binary: they exist
// so a later call cannot be steered by what an earlier one loaded, which is
// exactly the property the precedence cases have to exercise from scratch.
//
// The resolver is rebuilt through newExplicitEnvFileMemo rather than written out
// here. That is not tidiness: a copy of the sync.OnceValue in this file would be
// the thing under test, and TestANamedFileCannotBeNominatedByALoadedOne would
// keep passing after the once-ness was taken out of production. Measured — it
// did.
func resetEnvFileState(t *testing.T) {
	t.Helper()
	previousOnce, previousExplicit := envFileAnnounceOnce, explicitEnvFile
	t.Cleanup(func() { envFileAnnounceOnce, explicitEnvFile = previousOnce, previousExplicit })
	envFileAnnounceOnce = newAnnounceOnce()
	explicitEnvFile = newExplicitEnvFileMemo()
}

// inDir runs the rest of the test with dir as the working directory, which is
// the thing under test here: a dotenv file's authority depends entirely on where
// it sits relative to the process.
func inDir(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
}

// unsetEnv removes a variable for one test, restoring it afterwards.
//
// Not t.Setenv(name, ""): an empty value is still SET, and godotenv fills in
// only what os.LookupEnv reports as unset — so clearing a variable that way
// pins it to the empty string and no dotenv file can supply it. That is the
// right production behavior (a client passing "" in its env block said
// something) and exactly the wrong fixture for a precedence case.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetting %s: %v", name, err)
	}
}

// writeEnvFile writes a dotenv file and returns its path.
func writeEnvFile(t *testing.T, path string, lines ...string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// TestWorkingDirectoryDotenvIsAnnouncedAndNotLoaded is the security claim this
// file exists for.
//
// A stdio server inherits its working directory from the client, and every
// client that opens a workspace sets it to that workspace — so the directory's
// contents arrive with a cloned repository rather than from the person running
// the server. A .env there could aim LIBGEN_MIRROR at a host of its choosing,
// open the address guard, or widen the paths the read tool accepts, and none of
// it would need a tool call or a model turn.
//
// Both halves are asserted. Not loading it is the fix; announcing it is what
// keeps the fix from being a mystery to somebody whose repository-local file
// stopped taking effect.
func TestWorkingDirectoryDotenvIsAnnouncedAndNotLoaded(t *testing.T) {
	resetEnvFileState(t)
	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, ".env"), "LIBGEN_MIRROR=https://attacker.example", "LIBGEN_MCP_LOG_LEVEL=error")
	inDir(t, dir)
	unsetEnv(t, "LIBGEN_MIRROR")
	unsetEnv(t, EnvFileVar)

	report := LoadEnvFiles()

	if got := os.Getenv("LIBGEN_MIRROR"); got != "" {
		t.Errorf("the working-directory .env set LIBGEN_MIRROR to %q; it must reach nothing", got)
	}
	if report.IgnoredPath == "" {
		t.Fatal("the working-directory .env was not reported, so nobody can tell why it stopped working")
	}
	if !filepath.IsAbs(report.IgnoredPath) {
		t.Errorf("IgnoredPath = %q, want an absolute path so the report names one file and not a relative guess", report.IgnoredPath)
	}
	want := []string{"LIBGEN_MCP_LOG_LEVEL", "LIBGEN_MIRROR"}
	if !slices.Equal(report.IgnoredKeys, want) {
		t.Errorf("IgnoredKeys = %v, want %v (sorted, so the report reads the same every run)", report.IgnoredKeys, want)
	}
}

// TestPrecedenceIsProcessThenNamedThenHome pins the order, which is the whole of
// what a dotenv file may do here.
//
// One variable is carried by all three sources and one by each, so a row that
// silently lost is visible rather than merely absent.
func TestPrecedenceIsProcessThenNamedThenHome(t *testing.T) {
	resetEnvFileState(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	writeEnvFile(t, filepath.Join(home, EnvFileName),
		"LIBGEN_MCP_TIMEOUT=3s", "LIBGEN_MCP_LOG_LEVEL=error", "LIBGEN_MCP_SOURCES=libgen")
	named := writeEnvFile(t, filepath.Join(t.TempDir(), "named.env"),
		"LIBGEN_MCP_TIMEOUT=2s", "LIBGEN_MCP_LOG_LEVEL=warn")

	t.Setenv(EnvFileVar, named)
	t.Setenv("LIBGEN_MCP_TIMEOUT", "1s")
	unsetEnv(t, "LIBGEN_MCP_LOG_LEVEL")
	unsetEnv(t, "LIBGEN_MCP_SOURCES")

	report := LoadEnvFiles()

	for _, tc := range []struct{ name, want, from string }{
		{name: "LIBGEN_MCP_TIMEOUT", want: "1s", from: "the process environment"},
		{name: "LIBGEN_MCP_LOG_LEVEL", want: "warn", from: "the named file"},
		{name: "LIBGEN_MCP_SOURCES", want: "libgen", from: "the home file"},
	} {
		if got := os.Getenv(tc.name); got != tc.want {
			t.Errorf("%s = %q, want %q from %s", tc.name, got, tc.want, tc.from)
		}
	}
	if report.ExplicitPath != named {
		t.Errorf("ExplicitPath = %q, want %q", report.ExplicitPath, named)
	}
	if report.ExplicitErr != nil {
		t.Errorf("ExplicitErr = %v, want the named file read cleanly", report.ExplicitErr)
	}
	if report.ExplicitRelative {
		t.Error("an absolute path was reported as relative")
	}
	if report.HomePath == "" {
		t.Error("the home file was not reported as loaded, though its value won a row above")
	}
}

// TestANamedFileCannotBeNominatedByALoadedOne is the memo's reason for existing,
// stated as the attack it closes.
//
// godotenv sets every key the environment does not already carry, so a home file
// carrying LIBGEN_MCP_ENV_FILE=.env would nominate the working-directory file
// the same run had just announced it was ignoring — the refusal undone by the
// one file the refusal still trusts. Resolving the variable once, before any
// file is read, is what keeps "read from the process environment only" true.
func TestANamedFileCannotBeNominatedByALoadedOne(t *testing.T) {
	resetEnvFileState(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeEnvFile(t, filepath.Join(home, EnvFileName), EnvFileVar+"=.env")

	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, ".env"), "LIBGEN_MIRROR=https://attacker.example")
	inDir(t, dir)
	unsetEnv(t, EnvFileVar)
	unsetEnv(t, "LIBGEN_MIRROR")

	// Twice, because the nomination could only take effect on a later call: the
	// first load is what puts the variable in the environment.
	LoadEnvFiles()
	report := LoadEnvFiles()

	if got := os.Getenv("LIBGEN_MIRROR"); got != "" {
		t.Errorf("a home file nominated the working-directory .env and it was loaded (LIBGEN_MIRROR=%q)", got)
	}
	if report.ExplicitPath != "" {
		t.Errorf("ExplicitPath = %q, want none: the variable was unset when the process started", report.ExplicitPath)
	}
}

// TestANamedWorkingDirectoryDotenvIsNotReportedAsIgnored covers the deliberate
// case, which must not be warned about.
//
// Somebody who names their repository-local .env has made exactly the decision
// the refusal asks for. Reporting it as ignored would be telling them their
// choice did not take, which is both false and the fastest way to have the
// warning tuned out.
func TestANamedWorkingDirectoryDotenvIsNotReportedAsIgnored(t *testing.T) {
	resetEnvFileState(t)
	dir := t.TempDir()
	path := writeEnvFile(t, filepath.Join(dir, ".env"), "LIBGEN_MCP_SOURCES=libgen")
	inDir(t, dir)
	t.Setenv(EnvFileVar, path)
	unsetEnv(t, "LIBGEN_MCP_SOURCES")

	report := LoadEnvFiles()

	if report.IgnoredPath != "" {
		t.Errorf("IgnoredPath = %q, but that file was named on purpose", report.IgnoredPath)
	}
	if got := os.Getenv("LIBGEN_MCP_SOURCES"); got != "libgen" {
		t.Errorf("LIBGEN_MCP_SOURCES = %q, want the named file loaded", got)
	}
}

// TestARelativeNamedPathIsAnnouncedAsOne pins the report a relative value earns.
//
// A relative path is resolved against the working directory the client chose, so
// one relative line in a user-level configuration nominates a different file in
// every repository that person later opens. That is the working-directory load
// again, arriving through the flag meant to replace it, and the only defense is
// saying so.
func TestARelativeNamedPathIsAnnouncedAsOne(t *testing.T) {
	resetEnvFileState(t)
	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, "local.env"), "LIBGEN_MCP_SOURCES=libgen")
	inDir(t, dir)
	t.Setenv(EnvFileVar, "local.env")
	unsetEnv(t, "LIBGEN_MCP_SOURCES")

	report := LoadEnvFiles()

	if !report.ExplicitRelative {
		t.Error("a relative value was not reported as relative")
	}
	if !filepath.IsAbs(report.ExplicitPath) {
		t.Errorf("ExplicitPath = %q, want the resolved absolute path so the report names the file that was actually read", report.ExplicitPath)
	}
}

// TestAMissingNamedFileIsReportedRatherThanFatal covers the typo.
//
// A named file that is not there is worth a line, because the operator believes
// it is configuring the server. It is not worth refusing to start over: the
// settings it would have carried are all optional, and a server that will not
// come up is a worse answer than one that comes up saying what it could not
// read.
func TestAMissingNamedFileIsReportedRatherThanFatal(t *testing.T) {
	resetEnvFileState(t)
	missing := filepath.Join(t.TempDir(), "absent.env")
	t.Setenv(EnvFileVar, missing)

	report := LoadEnvFiles()

	if report.ExplicitErr == nil {
		t.Error("a named file that does not exist was reported as read")
	}
	if report.ExplicitPath != missing {
		t.Errorf("ExplicitPath = %q, want %q so the report names what could not be read", report.ExplicitPath, missing)
	}
}

// TestTheAnnouncementIsBoundedBecauseTheFileIsNot pins the caps on the warning.
//
// The key names come from a file written by whoever the working directory
// belongs to, and they are logged. Without the caps a hostile .env chooses how
// much of an operator's log stream it occupies, and how long one line in it is.
func TestTheAnnouncementIsBoundedBecauseTheFileIsNot(t *testing.T) {
	resetEnvFileState(t)
	dir := t.TempDir()

	lines := make([]string, 0, maxAnnouncedKeys*2)
	for i := range maxAnnouncedKeys * 2 {
		lines = append(lines, "LIBGEN_MCP_KEY_"+string(rune('A'+i))+"=x")
	}
	longKey := "LIBGEN_MCP_" + strings.Repeat("L", maxAnnouncedKeyRunes*2)
	lines = append(lines, longKey+"=x")
	writeEnvFile(t, filepath.Join(dir, ".env"), lines...)
	inDir(t, dir)
	unsetEnv(t, EnvFileVar)

	report := LoadEnvFiles()

	// The report itself carries everything: the caps are on what is logged, so
	// a caller rendering the report is not silently given a subset.
	if len(report.IgnoredKeys) != len(lines) {
		t.Errorf("IgnoredKeys has %d entries, want all %d", len(report.IgnoredKeys), len(lines))
	}
	announced := announcedKeys(report.IgnoredKeys)
	if len(announced) != maxAnnouncedKeys {
		t.Errorf("announced %d keys, want the cap of %d", len(announced), maxAnnouncedKeys)
	}
	for _, key := range announced {
		if len([]rune(key)) > maxAnnouncedKeyRunes+len("...") {
			t.Errorf("an announced key is %d runes, past the cap of %d: %q", len([]rune(key)), maxAnnouncedKeyRunes, key)
		}
	}
}

// TestAnEmptyOrDirectoryDotenvIsNotReported keeps the warning meaningful.
//
// An empty .env sets nothing, and a directory called .env is not a file anybody
// meant as configuration. Warning about either would train an operator to ignore
// the line that matters.
func TestAnEmptyOrDirectoryDotenvIsNotReported(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		resetEnvFileState(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		inDir(t, dir)
		unsetEnv(t, EnvFileVar)
		if got := LoadEnvFiles().IgnoredPath; got != "" {
			t.Errorf("IgnoredPath = %q for an empty file", got)
		}
	})
	t.Run("a directory", func(t *testing.T) {
		resetEnvFileState(t)
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".env"), 0o750); err != nil {
			t.Fatal(err)
		}
		inDir(t, dir)
		unsetEnv(t, EnvFileVar)
		if got := LoadEnvFiles().IgnoredPath; got != "" {
			t.Errorf("IgnoredPath = %q for a directory", got)
		}
	})
}

// TestAnUnreadableDotenvIsStillReported keeps the warning from depending on the
// file being well formed.
//
// The file is untrusted, so it may be anything at all — binary, half-written, a
// shell script somebody called .env. Naming its keys is a courtesy; saying it is
// there and ignored is the part that matters, and a parse failure must weaken
// the warning rather than suppress it.
func TestAnUnreadableDotenvIsStillReported(t *testing.T) {
	resetEnvFileState(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("this is not\x00a dotenv file\n{"), 0o600); err != nil {
		t.Fatal(err)
	}
	inDir(t, dir)
	unsetEnv(t, EnvFileVar)

	report := LoadEnvFiles()

	if report.IgnoredPath == "" {
		t.Error("a .env that could not be parsed was not reported at all")
	}
	if len(report.IgnoredKeys) != 0 {
		t.Errorf("IgnoredKeys = %v, want none: nothing could be parsed out of it", report.IgnoredKeys)
	}
}

// TestAShortKeyListIsAnnouncedWhole is the other side of the caps: they bound a
// hostile file without trimming an ordinary one, which would make the warning
// misleading about the file it is describing.
func TestAShortKeyListIsAnnouncedWhole(t *testing.T) {
	keys := []string{"LIBGEN_MCP_SOURCES", "LIBGEN_MIRROR"}
	if got := announcedKeys(keys); !slices.Equal(got, keys) {
		t.Errorf("announcedKeys(%v) = %v, want it unchanged", keys, got)
	}
}

// TestALongKeyIsTruncatedAndMarked pins the per-key cap.
//
// A key name is chosen by whoever wrote the file, so one of them can be as long
// as the file is. Truncating is the bound; the ellipsis is what stops the
// truncated name reading as the real one — an operator comparing the warning
// against their file would otherwise be looking for a key that does not exist.
//
// Driven directly rather than through a fixture: sorted and capped at ten, a long
// key in a file of many is cut by the count before the length cap can apply, so a
// case built that way exercises neither.
func TestALongKeyIsTruncatedAndMarked(t *testing.T) {
	long := "LIBGEN_MCP_" + strings.Repeat("L", maxAnnouncedKeyRunes*2)

	got := announcedKeys([]string{long})

	if len(got) != 1 {
		t.Fatalf("announcedKeys returned %d entries, want 1", len(got))
	}
	if len([]rune(got[0])) != maxAnnouncedKeyRunes+len("...") {
		t.Errorf("the announced key is %d runes, want %d plus the ellipsis: %q",
			len([]rune(got[0])), maxAnnouncedKeyRunes, got[0])
	}
	if !strings.HasSuffix(got[0], "...") {
		t.Errorf("a truncated key is not marked as one, so it reads as the real name: %q", got[0])
	}
}
