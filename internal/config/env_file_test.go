package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
func resetEnvFileState(t *testing.T) (home string) {
	t.Helper()
	previousOnce, previousExplicit := envFileAnnounceOnce, explicitEnvFile
	t.Cleanup(func() { envFileAnnounceOnce, explicitEnvFile = previousOnce, previousExplicit })
	envFileAnnounceOnce = newAnnounceOnce()
	explicitEnvFile = newExplicitEnvFileMemo()

	// A home directory of the test's own, because the real one is a source of
	// configuration these cases are about. A developer with an actual
	// ~/.libgen-mcp.env would otherwise have LoadEnvFiles read it here — and a
	// LIBGEN_MIRROR in it makes the working-directory case fail after that case
	// unsets the process value, on their machine and on nobody else's. Both
	// variables, since os.UserHomeDir reads HOME on unix and USERPROFILE on
	// Windows.
	//
	// Returned as well as set, so a case that writes into it has the path in
	// hand rather than reading back the variable it just wrote.
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
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
// Not t.Setenv(name, "") alone: that leaves the variable set, and a fixture
// meant to be "unset" should be unset, not merely blank. Blank is its own case,
// covered by TestABlankValueDoesNotBlockTheFiles: a dotenv file fills it.
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
		t.Run(tc.name, func(t *testing.T) {
			if got := os.Getenv(tc.name); got != tc.want {
				t.Errorf("%s = %q, want %q from %s", tc.name, got, tc.want, tc.from)
			}
		})
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

// TestTheHomeFileIsAnnouncedWhenItDecidesSomething keeps the one source of
// configuration nobody passed this server from being silent.
//
// The home file is read because somebody deliberately put it where the server
// looks — and on this server it can decide which tools exist, since
// LIBGEN_MCP_SERVER_FETCH lives there like everything else. A deployment whose
// surface came from a file the client never named has to be able to see that
// from its own log.
func TestTheHomeFileIsAnnouncedWhenItDecidesSomething(t *testing.T) {
	home := resetEnvFileState(t)
	inDir(t, t.TempDir())
	unsetEnv(t, "LIBGEN_MIRROR")

	writeEnvFile(t, filepath.Join(home, EnvFileName), "LIBGEN_MIRROR=https://home.example")

	logged := captureConfigLog(t)
	report := LoadEnvFiles()

	if report.HomePath == "" {
		t.Fatal("the report does not name the home file it loaded")
	}
	if report.HomeErr != nil {
		t.Fatalf("HomeErr = %v, want the file to have loaded", report.HomeErr)
	}
	if got := os.Getenv("LIBGEN_MIRROR"); got != "https://home.example" {
		t.Errorf("LIBGEN_MIRROR = %q, want the value from the home file", got)
	}
	if out := logged(); !strings.Contains(out, "home directory") {
		t.Errorf("the load was not announced, so a setting nobody passed decided something silently:\n%s", out)
	}
}

// TestAnUnreadableHomeFileIsReportedRatherThanDropped is the failure that was
// silent.
//
// godotenv's error for a file that exists and cannot be parsed or read was
// discarded, so the operator's settings were simply absent and the server ran on
// defaults that contradict them — with nothing in the log to look at. A missing
// file stays silent, because that is every ordinary deployment.
func TestAnUnreadableHomeFileIsReportedRatherThanDropped(t *testing.T) {
	home := resetEnvFileState(t)
	inDir(t, t.TempDir())

	// A directory where the file should be: readable as an entry, impossible as
	// a dotenv file, and portable in a way a permission bit is not.
	if err := os.Mkdir(filepath.Join(home, EnvFileName), 0o700); err != nil {
		t.Fatalf("creating the unreadable home entry: %v", err)
	}

	logged := captureConfigLog(t)
	report := LoadEnvFiles()

	if report.HomeErr == nil {
		t.Fatal("a home file that could not be read was reported as absent")
	}
	if out := logged(); !strings.Contains(out, "could not be read") {
		t.Errorf("the failure was not announced:\n%s", out)
	}
}

// TestAnAbsentHomeFileSaysNothing keeps the announcement worth reading: it is
// the normal case, and a line for it would be on every start of every ordinary
// deployment.
func TestAnAbsentHomeFileSaysNothing(t *testing.T) {
	resetEnvFileState(t)
	inDir(t, t.TempDir())

	logged := captureConfigLog(t)
	report := LoadEnvFiles()

	if report.HomePath != "" || report.HomeErr != nil {
		t.Errorf("an absent home file was reported: path=%q err=%v", report.HomePath, report.HomeErr)
	}
	if out := logged(); strings.Contains(out, "home directory") {
		t.Errorf("a deployment with no home file was told about one:\n%s", out)
	}
}

// TestABlankValueDoesNotBlockTheFiles is the precedence rule for a variable the
// client passed with nothing in it.
//
// Every read on this surface takes a blank value as unset, so a client that
// passes "" has asked for the default, and the default is what a dotenv file
// overrides. godotenv's own rule pinned such a variable to nothing instead,
// which made a setting in either file silently ineffective. Whitespace counts
// as blank, as it does for the HTTP flag overlay.
func TestABlankValueDoesNotBlockTheFiles(t *testing.T) {
	for _, tc := range []struct {
		name, process, named, home, want string
	}{
		{name: "blank, home file", process: "", home: "home@example.org", want: "home@example.org"},
		{name: "whitespace, home file", process: "  ", home: "home@example.org", want: "home@example.org"},
		{name: "blank, named file wins over home", process: "", named: "named@example.org", home: "home@example.org", want: "named@example.org"},
		{name: "a value still wins over both", process: "client@example.org", named: "named@example.org", home: "home@example.org", want: "client@example.org"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := resetEnvFileState(t)
			inDir(t, t.TempDir())
			writeEnvFile(t, filepath.Join(home, EnvFileName), "LIBGEN_MCP_UNPAYWALL_EMAIL="+tc.home)
			if tc.named != "" {
				t.Setenv(EnvFileVar, writeEnvFile(t, filepath.Join(t.TempDir(), "named.env"), "LIBGEN_MCP_UNPAYWALL_EMAIL="+tc.named))
			} else {
				unsetEnv(t, EnvFileVar)
			}
			t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", tc.process)

			LoadEnvFiles()

			if got := os.Getenv("LIBGEN_MCP_UNPAYWALL_EMAIL"); got != tc.want {
				t.Errorf("LIBGEN_MCP_UNPAYWALL_EMAIL = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestABlankNamedFileVariableCannotBeFilledIntoANomination is the memo's case
// again, for the shape a bundle passes: the variable set and empty, which a home
// file is now allowed to fill. The file it then names must still not be read.
func TestABlankNamedFileVariableCannotBeFilledIntoANomination(t *testing.T) {
	home := resetEnvFileState(t)
	writeEnvFile(t, filepath.Join(home, EnvFileName), EnvFileVar+"=.env")
	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, ".env"), "LIBGEN_MIRROR=https://attacker.example")
	inDir(t, dir)
	t.Setenv(EnvFileVar, "")
	unsetEnv(t, "LIBGEN_MIRROR")

	LoadEnvFiles()
	report := LoadEnvFiles()

	if got := os.Getenv("LIBGEN_MIRROR"); got != "" {
		t.Errorf("a home file filled a blank %s and the file it named was loaded (LIBGEN_MIRROR=%q)", EnvFileVar, got)
	}
	if report.ExplicitPath != "" {
		t.Errorf("ExplicitPath = %q, want none: the variable was blank when the process started", report.ExplicitPath)
	}
}

// bundleManifest is the Claude Desktop bundle's manifest, relative to this
// package.
var bundleManifest = filepath.Join("..", "..", "mcpb", "manifest.json")

// bundleEnvironment returns the environment Claude Desktop builds from the
// bundle's manifest, following getMcpConfigForManifest in
// github.com/modelcontextprotocol/mcpb (src/shared/config.ts): each field's
// default, replaced by what the user saved, substituted into the env block as a
// string. A field the user saved blank is the empty string.
func bundleEnvironment(t *testing.T, saved map[string]string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(bundleManifest)
	if err != nil {
		t.Fatalf("reading %s: %v", bundleManifest, err)
	}
	var manifest struct {
		Server struct {
			MCPConfig struct {
				Env map[string]string `json:"env"`
			} `json:"mcp_config"`
		} `json:"server"`
		UserConfig map[string]struct {
			Default any `json:"default"`
		} `json:"user_config"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parsing %s: %v", bundleManifest, err)
	}
	values := map[string]string{}
	for key, field := range manifest.UserConfig {
		if field.Default != nil {
			values[key] = fmt.Sprint(field.Default)
		}
	}
	maps.Copy(values, saved)
	env := map[string]string{}
	for name, template := range manifest.Server.MCPConfig.Env {
		key := strings.TrimSuffix(strings.TrimPrefix(template, "${user_config."), "}")
		value, ok := values[key]
		if !ok {
			t.Fatalf("%s maps to %s, which has no default and was not saved: the host would pass the placeholder itself", name, template)
		}
		env[name] = value
	}
	return env
}

// TestTheBundleEnvironmentTakesItsBlankSettingsFromTheFiles reproduces the
// Claude Desktop install end to end.
//
// The bundle maps every field to a variable. A field left blank arrives as the
// empty string, and before blank counted as unset that pinned the variable: the
// CORE key or Unpaywall address a user kept in ~/.libgen-mcp.env was never
// read. A field that carries a default in the manifest still arrives with that
// default, and still wins over both files, which is what the bundle's own
// description of the settings-file field promises.
func TestTheBundleEnvironmentTakesItsBlankSettingsFromTheFiles(t *testing.T) {
	home := resetEnvFileState(t)
	// Only the download directory is saved, because the manifest's default for
	// it is the user's real one. Every other field is what the dialog offers
	// untouched: its default, which for the four blank ones is the empty string.
	// Read before inDir, since the manifest's path is relative to this package.
	env := bundleEnvironment(t, map[string]string{"download_dir": t.TempDir()})
	inDir(t, t.TempDir())
	for name, value := range env {
		t.Setenv(name, value)
	}
	writeEnvFile(t, filepath.Join(home, EnvFileName),
		"LIBGEN_MIRROR=https://libgen.example",
		"LIBGEN_MCP_UNPAYWALL_EMAIL=home@example.org",
		"LIBGEN_MCP_CORE_KEY=home-core-key",
		"LIBGEN_MCP_TIMEOUT=30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Mirror != "https://libgen.example" {
		t.Errorf("Mirror = %q, want the home file's value under a blank field", cfg.Mirror)
	}
	if cfg.UnpaywallEmail != "home@example.org" {
		t.Errorf("UnpaywallEmail = %q, want the home file's value under a blank field", cfg.UnpaywallEmail)
	}
	if cfg.CoreKey != "home-core-key" {
		t.Errorf("CoreKey = %q, want the home file's value under a blank field", cfg.CoreKey)
	}
	if cfg.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want the bundle's 10s default: a field with a value wins over the files", cfg.Timeout)
	}
}

// captureConfigLog collects what this package logs while a test runs.
func captureConfigLog(t *testing.T) func() string {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf.String
}
