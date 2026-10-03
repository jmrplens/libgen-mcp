// env_shadow_test.go covers the warning a typed flag overriding a variable
// leaves in the startup log.

package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
)

// TestOverlayShadowedInNamesAFlagThatOverrodeItsVariable is the deployment the
// warning exists for: the image's command carries --http, the compose file sets
// LIBGEN_MCP_HTTP_ADDR, and the flag wins.
func TestOverlayShadowedInNamesAFlagThatOverrodeItsVariable(t *testing.T) {
	t.Setenv(config.EnvName("HTTP_ADDR"), " 127.0.0.1:9000 ")
	fs := overlayFlagSet(t)
	if err := fs.Parse([]string{"--http", "0.0.0.0:8080"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	got := overlayShadowedIn(fs)

	want := []shadowedVariable{{
		variable: config.EnvName("HTTP_ADDR"), envValue: "127.0.0.1:9000",
		flagName: "http", flagValue: "0.0.0.0:8080",
	}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("overlayShadowedIn() = %+v, want %+v", got, want)
	}
}

// TestOverlayShadowedInIsQuietWhenNothingIsOverridden covers every way the
// flag and the variable do not conflict, each of which a warning would make
// noise of.
func TestOverlayShadowedInIsQuietWhenNothingIsOverridden(t *testing.T) {
	for _, tc := range []struct {
		name     string
		envName  string
		envValue string
		args     []string
	}{
		{name: "the variable is unset", envName: "HTTP_PATH", args: []string{"--http", ":8080"}},
		{name: "the variable is blank", envName: "HTTP_ADDR", envValue: "  ", args: []string{"--http", ":8080"}},
		{name: "the flag was not typed", envName: "HTTP_ADDR", envValue: ":9000"},
		{name: "the same address", envName: "HTTP_ADDR", envValue: ":8080", args: []string{"--http", ":8080"}},
		{name: "a boolean spelled two ways", envName: "STATELESS", envValue: "1", args: []string{"--stateless=true"}},
		{name: "a duration spelled two ways", envName: "DRAIN_DELAY", envValue: "1m", args: []string{"--drain-delay", "60s"}},
		{name: "an int spelled two ways", envName: "RATE_LIMIT_BURST", envValue: "0x10", args: []string{"--rate-limit-burst", "16"}},
		{name: "an int64 spelled two ways", envName: "MAX_REQUEST_BODY_BYTES", envValue: "1024", args: []string{"--max-request-body-bytes", "0x400"}},
		{name: "a float spelled two ways", envName: "RATE_LIMIT_RPS", envValue: "2.50", args: []string{"--rate-limit-rps", "2.5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.EnvName(tc.envName), tc.envValue)
			fs := overlayFlagSet(t)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := overlayShadowedIn(fs); len(got) != 0 {
				t.Errorf("overlayShadowedIn() = %+v, want nothing", got)
			}
		})
	}
}

// TestOverlayShadowedInCountsAValueThatDoesNotParse is the one disagreement
// that is not a difference of spelling: the variable would have been refused,
// and the flag is what the server runs with.
func TestOverlayShadowedInCountsAValueThatDoesNotParse(t *testing.T) {
	for _, tc := range []struct {
		name, envName, envValue, arg string
	}{
		{name: "bool", envName: "STATELESS", envValue: "yes", arg: "--stateless=true"},
		{name: "duration", envName: "DRAIN_DELAY", envValue: "soon", arg: "--drain-delay=1s"},
		{name: "int", envName: "RATE_LIMIT_BURST", envValue: "many", arg: "--rate-limit-burst=3"},
		{name: "int64", envName: "MAX_REQUEST_BODY_BYTES", envValue: "big", arg: "--max-request-body-bytes=3"},
		{name: "float", envName: "RATE_LIMIT_RPS", envValue: "fast", arg: "--rate-limit-rps=3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.EnvName(tc.envName), tc.envValue)
			fs := overlayFlagSet(t)
			if err := fs.Parse([]string{tc.arg}); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := overlayShadowedIn(fs); len(got) != 1 {
				t.Errorf("overlayShadowedIn() = %+v, want the one variable", got)
			}
		})
	}
}

// TestApplyEnvBackedFlagsReportsWhatItOverwrote covers the half found before
// logging is up: after the write the variable holds the flag's value, so the
// comparison has to happen on the way in.
func TestApplyEnvBackedFlagsReportsWhatItOverwrote(t *testing.T) {
	t.Setenv(config.EnvName("LOG_LEVEL"), "debug")
	t.Setenv(config.EnvName("ALLOW_PRIVATE_ADDRESSES"), "1")
	t.Setenv(config.EnvFileVar, "/etc/libgen.env")
	withFlagSet(t, "--log-level=warn", "--allow-private-addresses=true", "--env-file=/srv/libgen.env")

	got := applyEnvBackedFlags()

	// The private-address flag is a string carrying a boolean, and "true"
	// against "1" is one setting, not two.
	want := []shadowedVariable{
		{variable: config.EnvName("LOG_LEVEL"), envValue: "debug", flagName: "log-level", flagValue: "warn"},
		{variable: config.EnvFileVar, envValue: "/etc/libgen.env", flagName: "env-file", flagValue: "/srv/libgen.env"},
	}
	if len(got) != len(want) {
		t.Fatalf("applyEnvBackedFlags() = %+v, want %+v", got, want)
	}
	for i := range want {
		t.Run(want[i].variable, func(t *testing.T) {
			if got[i] != want[i] {
				t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
			}
		})
	}
}

// TestSameSettingOnAStringThatIsNotABoolean pins that the boolean leniency of
// a string flag applies to booleans alone: two different words are two
// settings.
func TestSameSettingOnAStringThatIsNotABoolean(t *testing.T) {
	fs := overlayFlagSet(t)
	if err := fs.Parse([]string{"--transport=http"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sameSetting(fs.Lookup("transport").Value, "auto") {
		t.Error("sameSetting(http, auto) = true, want false")
	}
}

// opaqueValue is a flag.Value that is not a flag.Getter, so it carries no
// parsed value to compare against.
type opaqueValue string

// String returns the value as given.
func (v *opaqueValue) String() string { return string(*v) }

// Set stores the value as given.
func (v *opaqueValue) Set(s string) error { *v = opaqueValue(s); return nil }

// TestSameSettingOnAValueWithNoParsedForm compares by text alone when the flag
// offers nothing else, which is the conservative answer: a difference it
// cannot rule out is reported.
func TestSameSettingOnAValueWithNoParsedForm(t *testing.T) {
	v := opaqueValue("a")
	if !sameSetting(&v, "a") {
		t.Error("sameSetting(a, a) = false, want true")
	}
	if sameSetting(&v, "b") {
		t.Error("sameSetting(a, b) = true, want false")
	}
}

// TestWarnShadowedNamesBothSides reads the record an operator gets: one WARN
// per variable, carrying the variable, the flag and both values.
func TestWarnShadowedNamesBothSides(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	warnShadowed([]shadowedVariable{
		{variable: "LIBGEN_MCP_HTTP_ADDR", envValue: "127.0.0.1:9000", flagName: "http", flagValue: "0.0.0.0:8080"},
		{variable: "LIBGEN_MCP_DRAIN_DELAY", envValue: "30s", flagName: "drain-delay", flagValue: "0s"},
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d records, want one per variable:\n%s", len(lines), buf.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decoding %q: %v", lines[0], err)
	}
	for key, want := range map[string]string{
		"level":          "WARN",
		"msg":            shadowedVariableLine,
		"variable":       "LIBGEN_MCP_HTTP_ADDR",
		"variable_value": "127.0.0.1:9000",
		"flag":           "--http",
		"flag_value":     "0.0.0.0:8080",
	} {
		t.Run(key, func(t *testing.T) {
			if got, _ := record[key].(string); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		})
	}
}
