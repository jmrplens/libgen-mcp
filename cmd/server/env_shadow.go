// env_shadow.go names the variables a typed flag overrode.
//
// The precedence is deliberate and stays: a flag typed on the command line
// beats the environment, which beats the default. What it cost was silence. The
// container image's own command is `--transport auto --http 0.0.0.0:8080`, so a
// deployment that sets LIBGEN_MCP_HTTP_ADDR in its `environment:` block gets a
// server on 8080 and not one line saying the variable was read and overruled.
// The other direction was already announced ("HTTP settings taken from the
// environment"); this is the half that was not.

package main

import (
	"flag"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
)

// shadowedVariableLine is the message every overridden variable is logged
// under. test/e2e/http reads it off the real binary's stderr and spells it
// there, since it cannot import this package.
const shadowedVariableLine = "a flag on the command line overrides an environment variable, which has no effect"

// shadowedVariable is one variable a typed flag overrode with a different
// value.
//
// Both values are kept because both are printed. That is safe only because no
// variable this file is asked about is a credential: the credentials have no
// flag at all (see envBackedFlags), so nothing can shadow one. LIBGEN_MIRROR is
// left out for the neighboring reason — a mirror URL can carry userinfo, and
// the name is not in this server's namespace anyway.
type shadowedVariable struct {
	// variable is the full name, prefix included.
	variable string
	// envValue is what the environment said, trimmed.
	envValue string
	// flagName is the flag without its dashes.
	flagName string
	// flagValue is what the flag holds, as the flag package prints it.
	flagValue string
}

// shadowedBy reports whether the flag flagName of fs was given a value and
// thereby overrides a non-blank envName that says something different.
//
// Call it before anything writes the flag set from the environment or the
// environment from the flag set: on either side of those writes the two agree
// by construction, and the question has no answer left.
//
// A blank variable is unset here, as it is everywhere this server reads one.
// The same setting spelled two ways — `--stateless=true` against
// LIBGEN_MCP_STATELESS=1, `--drain-delay=60s` against 1m — is not a conflict,
// and warning about it would teach the reader to skip the warning.
func shadowedBy(fs *flag.FlagSet, flagName, envName string) (shadowedVariable, bool) {
	if !flagPassedIn(fs, flagName) {
		return shadowedVariable{}, false
	}
	envValue := strings.TrimSpace(os.Getenv(envName))
	if envValue == "" {
		return shadowedVariable{}, false
	}
	// A flag fs holds a value for is a flag fs declares, so Lookup cannot miss.
	value := fs.Lookup(flagName).Value
	if sameSetting(value, envValue) {
		return shadowedVariable{}, false
	}
	return shadowedVariable{variable: envName, envValue: envValue, flagName: flagName, flagValue: value.String()}, true
}

// sameSetting reports whether envValue says what the flag value v holds,
// parsed the way that flag parses.
//
// A value that does not parse is a different setting by definition: the flag
// won, and the variable would have been refused had it been read.
func sameSetting(v flag.Value, envValue string) bool {
	if strings.TrimSpace(v.String()) == envValue {
		return true
	}
	getter, ok := v.(flag.Getter)
	if !ok {
		return false
	}
	switch held := getter.Get().(type) {
	case bool:
		parsed, err := strconv.ParseBool(envValue)
		return err == nil && parsed == held
	case time.Duration:
		parsed, err := time.ParseDuration(envValue)
		return err == nil && parsed == held
	case int:
		parsed, err := strconv.ParseInt(envValue, 0, strconv.IntSize)
		return err == nil && int(parsed) == held
	case int64:
		parsed, err := strconv.ParseInt(envValue, 0, 64)
		return err == nil && parsed == held
	case float64:
		parsed, err := strconv.ParseFloat(envValue, 64)
		return err == nil && parsed == held
	case string:
		// A string flag carrying a boolean, which is what
		// --allow-private-addresses is: config reads its variable with the
		// house grammar, so "true" and "1" are one setting there as well.
		flagBool, flagErr := strconv.ParseBool(held)
		envBool, envErr := strconv.ParseBool(envValue)
		return flagErr == nil && envErr == nil && flagBool == envBool
	}
	return false
}

// overlayShadowedIn returns the HTTP variables a typed flag overrides. Call
// after the dotenv files are loaded, so a variable a home file set is counted
// too, and before [applyHTTPEnvOverlayTo] sets flags through the same set.
func overlayShadowedIn(fs *flag.FlagSet) []shadowedVariable {
	var shadowed []shadowedVariable
	for _, entry := range httpEnvOverlay {
		if s, ok := shadowedBy(fs, entry.flagName, config.EnvName(entry.envShortName)); ok {
			shadowed = append(shadowed, s)
		}
	}
	return shadowed
}

// warnShadowed logs one WARN line per overridden variable, naming the
// variable, the flag and both values.
func warnShadowed(shadowed []shadowedVariable) {
	for _, s := range shadowed {
		slog.Warn(shadowedVariableLine,
			"variable", s.variable, "variable_value", s.envValue,
			"flag", "--"+s.flagName, "flag_value", s.flagValue,
			"hint", "a typed flag takes precedence over the environment: drop the flag to use the variable, or drop the variable to silence this")
	}
}
