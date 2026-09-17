// env_flags.go gives the settings whose only home was an environment variable a
// flag each, so one command can configure the whole server.

package main

import (
	"flag"
	"os"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// envBackedFlags are those settings.
//
// # Why the flag writes the variable instead of being read directly
//
// Each of these is consumed somewhere that reads the process environment and
// nothing else: config.Load builds the whole Config from it, and
// internal/discovery reads the private-address policy through a package-level
// setter that config.Load feeds. Threading a flag value to each of those would
// mean new parameters through unrelated call paths, and it would create a second
// source of truth in every one: a reader that consults the flag and a reader
// that consults the variable, disagreeing the first time somebody adds a caller.
//
// So the flag sets the variable, once, before anything reads either. There stays
// exactly one reader per setting, the precedence is the one this server already
// documents — an explicitly passed flag beats the environment beats the default
// — and nothing downstream needs to know a flag exists.
//
// # Why the credential-shaped settings are not on this list
//
// LIBGEN_MCP_ANNAS_KEY, LIBGEN_MCP_CORE_KEY and LIBGEN_MCP_UNPAYWALL_EMAIL are
// deliberately left without flags, and that is a security position rather than
// an oversight. A secret on a command line is visible to every user on the
// machine through ps, is captured by process accounting, and lands in shell
// history. The environment is not perfect either, but it is not world-readable
// on any platform this server supports, and a --annas-key flag would make the
// insecure path the convenient one. The contact email is on the list for the
// same reason in reverse: it is a credential in Unpaywall's protocol, since it
// is what identifies the caller being rate-limited.
var envBackedFlags = []struct {
	// flagName is what the operator types.
	flagName string
	// envShortName is the setting as knownNames spells it, without the prefix.
	envShortName string
	// value holds the parsed flag, filled by registerEnvBackedFlags.
	value *string
	// usage is the help text.
	usage string
}{
	{
		flagName:     "log-level",
		envShortName: "LOG_LEVEL",
		usage:        "Logging verbosity: debug, info, warn or error",
	},
	{
		flagName:     "download-dir",
		envShortName: "DOWNLOAD_DIR",
		usage:        "Directory the download tool saves into; created if missing and probed for writability at startup",
	},
	{
		flagName:     "sources",
		envShortName: "SOURCES",
		usage:        "Comma-separated download sources to enable, in the chain's own fixed order; empty enables all",
	},
	{
		flagName:     "pprof-addr",
		envShortName: "PPROF_ADDR",
		usage:        "Serve Go's profiling handlers (net/http/pprof) on this loopback address, e.g. 127.0.0.1:6060; empty serves nothing. Refused unless the host is loopback, because a heap profile is a copy of this process's memory",
	},
	{
		flagName:     "allow-private-addresses",
		envShortName: "ALLOW_PRIVATE_ADDRESSES",
		usage:        "Permit outbound connections to loopback, link-local, private and carrier-grade-NAT addresses: true or false. A host named in LIBGEN_MIRROR or LIBGEN_MCP_SCIHUB_HOSTS is already exempt without it, and the cloud metadata addresses stay refused either way. Refused at startup on an HTTP listener other machines can reach",
	},
}

// mirrorFlagName and mirrorEnvName are kept apart from the table because
// LIBGEN_MIRROR is the one setting on it that carries no LIBGEN_MCP_ prefix: the
// name is the mirror family's own convention rather than this server's, and
// config.EnvName would spell it wrong.
const (
	mirrorFlagName = "mirror"
	mirrorEnvName  = "LIBGEN_MIRROR"
)

// mirrorFlag holds the parsed --mirror value.
var mirrorFlag *string

// envFileFlagName is the flag naming an additional dotenv file.
const envFileFlagName = "env-file"

// envFileFlag holds the parsed --env-file value.
var envFileFlag *string

// registerEnvBackedFlags declares the flags. Call before flag.Parse.
func registerEnvBackedFlags() {
	for i := range envBackedFlags {
		entry := &envBackedFlags[i]
		entry.value = flag.String(entry.flagName, "", entry.usage)
	}
	mirrorFlag = flag.String(mirrorFlagName, "", "Pin a single mirror, e.g. https://libgen.li, and skip auto-discovery")
	envFileFlag = flag.String(envFileFlagName, "", "Load settings from this dotenv file in addition to ~/"+config.EnvFileName+
		". Give an absolute path: a relative one is resolved against the working directory, which the MCP client chooses and changes with every workspace it opens. A .env in the working directory is never loaded")
}

// applyEnvBackedFlags copies each explicitly passed flag into its environment
// variable. Call after flag.Parse and before anything reads configuration —
// which for --env-file means before the dotenv loader resolves it, since that
// resolution happens once per process and a later write does nothing at all.
//
// Only flags that were actually passed are copied, and that is what preserves
// the precedence: a variable already exported keeps its value unless the
// operator typed the flag as well. An unset flag writes nothing, so it cannot
// clear a variable by accident — which a naive implementation writing the empty
// string would do to every deployment that configures through the environment.
func applyEnvBackedFlags() {
	for _, entry := range envBackedFlags {
		setEnvFromFlag(entry.flagName, config.EnvName(entry.envShortName), entry.value)
	}
	setEnvFromFlag(mirrorFlagName, mirrorEnvName, mirrorFlag)
	setEnvFromFlag(envFileFlagName, config.EnvFileVar, envFileFlag)
}

// setEnvFromFlag writes one flag's value into its variable, and only when the
// flag was typed.
func setEnvFromFlag(flagName, envName string, value *string) {
	if value == nil || !isFlagPassed(flagName) {
		return
	}
	// The error is ignored for the same reason os.Setenv's error exists at all:
	// it can only fail on a name containing NUL or "=", and these names are
	// compile-time constants.
	_ = os.Setenv(envName, *value)
}

// isFlagPassed reports whether the operator typed the named flag, as opposed to
// it holding its default. A flag whose value happens to equal the default is
// still a choice, which is why this asks the flag set rather than comparing
// values.
func isFlagPassed(name string) bool {
	passed := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			passed = true
		}
	})
	return passed
}
