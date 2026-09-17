// env_flags_test.go covers the flags that write environment variables, where
// the whole of the behavior is which writes happen and which do not.

package main

import (
	"flag"
	"os"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// withFlagSet installs a fresh flag set for one test and parses args into it.
//
// flag.CommandLine is package state, and both halves of this file read it:
// registerEnvBackedFlags declares into it and isFlagPassed asks it what was
// typed. Replacing it is the only way a case can be about a command line the
// test process was not started with.
func withFlagSet(t *testing.T, args ...string) {
	t.Helper()
	previous := flag.CommandLine
	t.Cleanup(func() { flag.CommandLine = previous })
	flag.CommandLine = flag.NewFlagSet("libgen-mcp", flag.ContinueOnError)
	flag.CommandLine.SetOutput(os.Stderr)

	registerEnvBackedFlags()
	if err := flag.CommandLine.Parse(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}
}

// TestAFlagThatWasTypedWritesItsVariable is the plain case.
//
// The flag is not read anywhere else: config.Load builds the whole
// configuration from the environment, so a flag that did not write its variable
// would parse cleanly and change nothing at all.
func TestAFlagThatWasTypedWritesItsVariable(t *testing.T) {
	for _, tc := range []struct {
		flag, arg, envName, want string
	}{
		{flag: "--log-level", arg: "debug", envName: config.EnvName("LOG_LEVEL"), want: "debug"},
		{flag: "--sources", arg: "libgen,annas", envName: config.EnvName("SOURCES"), want: "libgen,annas"},
		{flag: "--allow-private-addresses", arg: "true", envName: config.EnvName("ALLOW_PRIVATE_ADDRESSES"), want: "true"},
		// LIBGEN_MIRROR carries no LIBGEN_MCP_ prefix, because the name is the
		// mirror family's own convention rather than this server's. A flag that
		// wrote the prefixed spelling would write a variable nothing reads.
		{flag: "--mirror", arg: "https://libgen.la", envName: "LIBGEN_MIRROR", want: "https://libgen.la"},
		{flag: "--env-file", arg: "/tmp/libgen.env", envName: config.EnvFileVar, want: "/tmp/libgen.env"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			t.Setenv(tc.envName, "untouched")
			withFlagSet(t, tc.flag+"="+tc.arg)

			applyEnvBackedFlags()

			if got := os.Getenv(tc.envName); got != tc.want {
				t.Errorf("%s = %q, want %q from %s", tc.envName, got, tc.want, tc.flag)
			}
		})
	}
}

// TestAFlagThatWasNotTypedLeavesTheVariableAlone is the half that is easy to get
// wrong and expensive when it is.
//
// A naive implementation copies every flag's value, and an unset string flag is
// the empty string — so every deployment that configures through the environment
// would have its settings cleared by a server that was given no flags at all.
// Asking the flag set what was typed is what prevents it.
func TestAFlagThatWasNotTypedLeavesTheVariableAlone(t *testing.T) {
	for _, envName := range []string{
		config.EnvName("LOG_LEVEL"),
		config.EnvName("DOWNLOAD_DIR"),
		config.EnvName("SOURCES"),
		config.EnvName("ALLOW_PRIVATE_ADDRESSES"),
		"LIBGEN_MIRROR",
		config.EnvFileVar,
	} {
		t.Setenv(envName, "set-by-the-client")
	}
	// A command line that mentions exactly one of them.
	withFlagSet(t, "--log-level=warn")

	applyEnvBackedFlags()

	if got := os.Getenv(config.EnvName("LOG_LEVEL")); got != "warn" {
		t.Errorf("the flag that WAS typed did not take: %q", got)
	}
	for _, envName := range []string{
		config.EnvName("DOWNLOAD_DIR"),
		config.EnvName("SOURCES"),
		config.EnvName("ALLOW_PRIVATE_ADDRESSES"),
		"LIBGEN_MIRROR",
		config.EnvFileVar,
	} {
		if got := os.Getenv(envName); got != "set-by-the-client" {
			t.Errorf("%s = %q, want it untouched: no flag named it", envName, got)
		}
	}
}

// TestAFlagTypedAsItsDefaultStillCounts pins why this asks the flag set rather
// than comparing values.
//
// `--log-level=` is somebody saying "no level", and it has to beat an exported
// one. A check that compared the parsed value against the zero value would read
// it as "not given" and leave the exported value in place, which is the opposite
// of what was typed.
func TestAFlagTypedAsItsDefaultStillCounts(t *testing.T) {
	t.Setenv(config.EnvName("LOG_LEVEL"), "debug")
	withFlagSet(t, "--log-level=")

	applyEnvBackedFlags()

	if got := os.Getenv(config.EnvName("LOG_LEVEL")); got != "" {
		t.Errorf("%s = %q, want the typed empty value to win", config.EnvName("LOG_LEVEL"), got)
	}
}

// TestNoCredentialHasAFlag is the security position, asserted rather than left
// to a comment.
//
// A secret on a command line is visible to every user on the machine through ps,
// is captured by process accounting, and lands in shell history. The comment in
// env_flags.go says so; this is what notices when somebody adds the flag anyway
// because it seemed like an obvious omission.
func TestNoCredentialHasAFlag(t *testing.T) {
	withFlagSet(t)

	for _, name := range []string{"annas-key", "core-key", "unpaywall-email"} {
		if flag.CommandLine.Lookup(name) != nil {
			t.Errorf("--%s exists; a secret on a command line is world-readable through ps and lands in shell history", name)
		}
	}
	for _, entry := range envBackedFlags {
		switch entry.envShortName {
		case "ANNAS_KEY", "CORE_KEY", "UNPAYWALL_EMAIL":
			t.Errorf("%s is on the flag list; it is a credential", entry.envShortName)
		}
	}
}

// TestEveryFlagWritesAVariableThisServerReads keeps the table honest.
//
// A flag whose variable is misspelled parses, writes, and changes nothing — the
// failure is silent at every layer, and the operator's only evidence is that
// their setting did not take. config.KnownEnvNames is the list of what this
// server actually reads, so a name missing from it is a name nobody consults.
func TestEveryFlagWritesAVariableThisServerReads(t *testing.T) {
	known := make(map[string]bool)
	for _, name := range config.KnownEnvNames() {
		known[name] = true
	}
	for _, entry := range envBackedFlags {
		if full := config.EnvName(entry.envShortName); !known[full] {
			t.Errorf("--%s writes %s, which this server never reads", entry.flagName, full)
		}
	}
	// The two spelled out separately, for the two reasons they are separate.
	if !known[config.EnvFileVar] {
		t.Errorf("--%s writes %s, which this server never reads", envFileFlagName, config.EnvFileVar)
	}
	if known[config.EnvName("MIRROR")] {
		t.Error("LIBGEN_MCP_MIRROR is read somewhere; --mirror writes the bare LIBGEN_MIRROR and the two would disagree")
	}
}
