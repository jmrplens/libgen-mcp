// env_file.go decides which dotenv files are allowed to configure this process.
//
// A stdio MCP server inherits its working directory from the client, and every
// client that opens a workspace sets it to that workspace. The directory is
// therefore untrusted input: its contents can arrive with a cloned repository,
// an extracted archive or a shared network share, chosen by whoever wrote them
// rather than by the person running the server.
//
// So a working-directory .env is never loaded. The sibling project this is
// adapted from shipped that load and withdrew it under a security audit: a
// two-line file in a cloned repository chose which host received the operator's
// token, and none of it needed a tool call, a model turn or any interaction at
// all. The same shape reaches further here than a token would. A .env could set
// LIBGEN_MCP_ALLOWED_READ_DIRS and hand the read tool the rest of the disk, set
// LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES and aim the server at the operator's LAN,
// or set LIBGEN_MIRROR and decide which host every search and download goes to
// — and the variables that make it work are exactly the ones no MCP client
// sets, so they are always unset and always available to whatever file is found
// first.
//
// The rule is that a dotenv file configures this server only when somebody
// deliberately put it where the server looks (the home file) or deliberately
// named it (LIBGEN_MCP_ENV_FILE). Being in the working directory is not a
// decision anyone made. Git's safe.directory ownership check, direnv's
// per-directory "direnv allow" and VS Code Workspace Trust are three
// independent tools that reached the same conclusion.

package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

// EnvFileName is the file in the user's home directory this server reads
// settings from.
const EnvFileName = ".libgen-mcp.env"

// envFileVarShortName is the short name of the variable naming an additional
// dotenv file, as it appears in knownNames.
const envFileVarShortName = "ENV_FILE"

// EnvFileVar is the fully spelled variable naming a dotenv file to load in
// addition to the home file. It is read from the process environment only,
// which is what makes it an opt-in: a file this server declines to load cannot
// nominate itself.
//
// It exists so the convenience a working-directory load would provide survives
// as a deliberate act. Somebody who keeps settings in a repository-local file
// names that file, by absolute path, in the client configuration or the shell
// that launches the server, and says so.
//
// The absolute path is the recommendation and not a formality. A relative value
// is resolved against the working directory, which the MCP client chooses and
// changes with every workspace it opens, so one relative line in a user-level
// client configuration nominates a different file in every repository that
// person later opens. That is the working-directory load again, which is why a
// relative value is announced as one at startup.
var EnvFileVar = EnvName(envFileVarShortName)

// workingDirEnvFileName is the file this server deliberately does not load. It
// is still looked for, because somebody whose repository-local .env stopped
// taking effect deserves to be told why rather than left debugging it.
const workingDirEnvFileName = ".env"

// maxDotenvBytes bounds how much of the unloaded working-directory file is
// parsed to name its keys in the warning. The file is untrusted, and nothing
// here is worth reading a multi-gigabyte one for.
const maxDotenvBytes = 64 << 10

// maxAnnouncedKeys and maxAnnouncedKeyRunes bound the warning itself: the key
// names come from an untrusted file and are logged, so their number and length
// are capped rather than trusted.
const (
	maxAnnouncedKeys     = 10
	maxAnnouncedKeyRunes = 40
)

// envFileAnnounceOnce keeps the startup announcement to one occurrence per
// process. What it reports is a property of the process, not of the call.
//
// A pointer, so a test can put it back to its starting state without copying a
// sync.Once — which vet refuses, and rightly: a copied Once has its own done
// flag and would let the announcement through a second time.
var envFileAnnounceOnce = newAnnounceOnce()

// newAnnounceOnce builds that memo.
func newAnnounceOnce() *sync.Once { return &sync.Once{} }

// explicitEnvFile resolves EnvFileVar once per process, before any file this
// server loads has had a chance to write it.
//
// godotenv sets every key the environment does not already carry, so reading
// the variable afresh on a later call would honor a value an earlier call's own
// home file supplied — which is how a home file naming ".env" loads the
// working-directory file the same run had just announced it was ignoring.
// Resolving once is what keeps "read from the process environment only" true.
var explicitEnvFile = newExplicitEnvFileMemo()

// newExplicitEnvFileMemo builds that resolver.
//
// It is a constructor rather than an inline value so a test can put the memo
// back to its starting state by calling this, which is the only way a test can
// exercise the once-ness rather than its own replacement for it: a test that
// rebuilt the memo itself would keep passing after the sync.OnceValue here was
// taken away.
func newExplicitEnvFileMemo() func() string {
	return sync.OnceValue(func() string {
		return strings.TrimSpace(os.Getenv(EnvFileVar))
	})
}

// EnvFileReport describes what LoadEnvFiles did, so a caller can render it
// itself instead of parsing logs. LoadEnvFiles already announces the
// security-relevant parts.
type EnvFileReport struct {
	// ExplicitPath is the absolute path of the file EnvFileVar named, whether or
	// not it could be read. Empty when the variable is unset.
	ExplicitPath string
	// ExplicitErr is why the file EnvFileVar named could not be read.
	ExplicitErr error
	// ExplicitRelative records that EnvFileVar named a relative path, so the
	// file that was loaded is whichever one sits in the working directory the
	// client chose. Meaningless when ExplicitPath is empty.
	ExplicitRelative bool
	// HomePath is the home file that was loaded. Empty when there is none.
	HomePath string
	// IgnoredPath is the absolute path of a working-directory .env that was
	// found and deliberately not loaded. Empty when there is none.
	IgnoredPath string
	// IgnoredKeys are the variable names that file would have set, sorted.
	IgnoredKeys []string
}

// LoadEnvFiles populates the process environment from the dotenv files this
// server reads, and is safe to call more than once.
//
// It is separate from [Load] because the environment has to be complete before
// Load reads any of it, and because a caller may want the report.
//
// Every load is best-effort, a missing file being the normal case, and godotenv
// never overwrites a variable that is already set. The resulting precedence,
// highest first, is:
//
//  1. the process environment, which is what the MCP client passed;
//  2. the file [EnvFileVar] names, if any;
//  3. ~/.libgen-mcp.env.
//
// A .env in the working directory is not on that list. When one exists it is
// read far enough to name its keys and reported at WARN, and nothing it
// contains reaches the environment.
func LoadEnvFiles() EnvFileReport {
	var report EnvFileReport

	if explicit := explicitEnvFile(); explicit != "" {
		report.ExplicitPath = absolutePath(explicit)
		report.ExplicitRelative = !filepath.IsAbs(explicit)
		if err := godotenv.Load(explicit); err != nil {
			report.ExplicitErr = err
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		homePath := filepath.Join(home, EnvFileName)
		if godotenv.Load(homePath) == nil {
			report.HomePath = homePath
		}
	}

	report.IgnoredPath, report.IgnoredKeys = ignoredWorkingDirEnvFile(report.ExplicitPath)
	envFileAnnounceOnce.Do(report.announce)
	return report
}

// ignoredWorkingDirEnvFile reports the working-directory .env that was not
// loaded, and the variable names it would have set.
//
// A file the operator named through EnvFileVar was loaded on purpose even when
// it is that same .env, so it is not reported as ignored.
func ignoredWorkingDirEnvFile(explicitPath string) (path string, keys []string) {
	info, err := os.Stat(workingDirEnvFileName)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return "", nil
	}
	found := absolutePath(workingDirEnvFileName)
	if explicitPath != "" && explicitPath == found {
		return "", nil
	}
	return found, dotenvKeys(workingDirEnvFileName)
}

// dotenvKeys returns the sorted variable names a dotenv file sets, reading at
// most maxDotenvBytes of it. A file that cannot be opened or parsed yields no
// names, which weakens the warning without suppressing it.
func dotenvKeys(path string) []string {
	file, err := os.Open(path) // a fixed name in the working directory, read only to name its keys
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()

	values, err := godotenv.Parse(io.LimitReader(file, maxDotenvBytes))
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// absolutePath resolves path for display, falling back to what was given when
// the working directory cannot be determined. It is presentation only: the
// loads themselves use the path as written.
func absolutePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// announce writes the startup record of where configuration came from.
//
// Every line is WARN rather than INFO on purpose. They are the only local
// evidence that this process took configuration from a file outside the client
// configuration, and LIBGEN_MCP_LOG_LEVEL is itself one of the settings such a
// file would like to set.
func (r EnvFileReport) announce() {
	r.announceExplicit()
	if r.IgnoredPath != "" {
		slog.Warn("ignoring the .env file in the working directory",
			"path", r.IgnoredPath,
			"key_count", len(r.IgnoredKeys),
			"keys", announcedKeys(r.IgnoredKeys),
			"reason", "the working directory belongs to whoever wrote it, not to this server",
			"hint", "put these settings in ~/"+EnvFileName+", or name the file with "+EnvFileVar)
	}
}

// announceExplicit reports the file EnvFileVar named, and whether naming it
// relatively made it follow the client rather than stay put.
func (r EnvFileReport) announceExplicit() {
	if r.ExplicitPath == "" {
		return
	}
	if r.ExplicitErr != nil {
		slog.Warn("env file named by "+EnvFileVar+" could not be read",
			"env", EnvFileVar, "path", r.ExplicitPath, "error", r.ExplicitErr)
	} else {
		slog.Warn("loading configuration from the env file named by "+EnvFileVar,
			"env", EnvFileVar, "path", r.ExplicitPath)
	}
	if r.ExplicitRelative {
		slog.Warn(EnvFileVar+" names a relative path, resolved against the working directory the client chose",
			"env", EnvFileVar, "path", r.ExplicitPath,
			"reason", "a relative value follows the client into every workspace it opens, and each one offers a different file",
			"hint", "name an absolute path unless this server is launched for a single workspace")
	}
}

// announcedKeys bounds the untrusted key names before they are logged.
func announcedKeys(keys []string) []string {
	if len(keys) > maxAnnouncedKeys {
		keys = keys[:maxAnnouncedKeys]
	}
	out := make([]string, len(keys))
	for i, key := range keys {
		if runes := []rune(key); len(runes) > maxAnnouncedKeyRunes {
			key = string(runes[:maxAnnouncedKeyRunes]) + "..."
		}
		out[i] = key
	}
	return out
}
