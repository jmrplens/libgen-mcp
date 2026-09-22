package main

import (
	"flag"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// overlayFlagSet declares the HTTP flags on a set of its own, with the same
// names, types and defaults the binary uses.
//
// A set of its own rather than flag.CommandLine because the overlay's whole
// subject is which flags were passed, and the process's own command line is the
// test binary's — full of -test.* and empty of everything here.
//
// The defaults are the binary's on purpose: a case asserting that a variable
// changed a setting has to start from the value the operator would otherwise
// have got.
func overlayFlagSet(t *testing.T) *flag.FlagSet {
	t.Helper()

	fs := flag.NewFlagSet("overlay", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("http", "", "")
	fs.String("http-path", "/", "")
	fs.String("http-socket-mode", "0660", "")
	fs.String("transport", "", "")
	fs.String("public-url", "", "")
	fs.String("trusted-origins", "", "")
	fs.String("trusted-proxies", "", "")
	fs.String("trusted-proxy-header", "", "")
	fs.String("tls-cert", "", "")
	fs.String("tls-key", "", "")
	fs.Bool("stateless", true, "")
	fs.Bool("json-response", false, "")
	fs.Int64("max-request-body-bytes", 0, "")
	fs.Float64("rate-limit-rps", defaultRateLimitRPS, "")
	fs.Int("rate-limit-burst", defaultRateLimitBurst, "")
	fs.Int("max-inflight-per-client", 0, "")
	fs.Duration("drain-delay", 0, "")
	fs.Duration("session-timeout", defaultSessionTimeout, "")
	fs.Duration("http-idle-timeout", defaultHTTPIdleTimeout, "")
	return fs
}

// overlayValue reads one flag's value back as a string.
func overlayValue(t *testing.T, fs *flag.FlagSet, name string) string {
	t.Helper()

	f := fs.Lookup(name)
	if f == nil {
		t.Fatalf("the test's flag set has no --%s", name)
	}
	return f.Value.String()
}

// TestEveryOverlayEntryNamesARealFlag is the gate that keeps the table honest.
//
// An entry naming a flag that does not exist is inert in the worst way: the
// variable is documented, the operator sets it, and it configures nothing. The
// error only surfaces when that variable happens to be set, which for most of
// them is never on a developer's machine.
func TestEveryOverlayEntryNamesARealFlag(t *testing.T) {
	fs := overlayFlagSet(t)
	for _, entry := range httpEnvOverlay {
		if fs.Lookup(entry.flagName) == nil {
			t.Errorf("%s fills --%s, which no flag declares", config.EnvName(entry.envShortName), entry.flagName)
		}
	}
}

// TestEveryOverlayVariableIsKnown keeps the names inside the one list that says
// what this server reads, so a misspelling is a failure here rather than a
// setting that silently does nothing.
func TestEveryOverlayVariableIsKnown(t *testing.T) {
	known := make(map[string]bool)
	for _, name := range config.KnownEnvNames() {
		known[name] = true
	}
	for _, entry := range httpEnvOverlay {
		if full := config.EnvName(entry.envShortName); !known[full] {
			t.Errorf("the overlay reads %s, which is not in config.KnownEnvNames", full)
		}
	}
}

// TestOverlayFillsAFlagNobodyPassed is the case the whole file exists for: a
// deployment that configures the listener through its `environment:` block and
// never touches the command line.
func TestOverlayFillsAFlagNobodyPassed(t *testing.T) {
	t.Setenv(config.EnvName("HTTP_ADDR"), "127.0.0.1:9123")
	t.Setenv(config.EnvName("DRAIN_DELAY"), "12s")
	t.Setenv(config.EnvName("STATELESS"), "0")
	t.Setenv(config.EnvName("RATE_LIMIT_RPS"), "2.5")

	fs := overlayFlagSet(t)
	used, err := applyHTTPEnvOverlayTo(fs)
	if err != nil {
		t.Fatalf("applyHTTPEnvOverlayTo() error = %v", err)
	}

	for _, tc := range []struct{ flagName, want string }{
		{flagName: "http", want: "127.0.0.1:9123"},
		{flagName: "drain-delay", want: "12s"},
		// The house grammar, which is strconv.ParseBool: an operator typing 0
		// is right everywhere else on this surface.
		{flagName: "stateless", want: "false"},
		{flagName: "rate-limit-rps", want: "2.5"},
	} {
		t.Run(tc.flagName, func(t *testing.T) {
			if got := overlayValue(t, fs, tc.flagName); got != tc.want {
				t.Errorf("--%s = %q, want %q from its variable", tc.flagName, got, tc.want)
			}
		})
	}

	// The names come back so the startup log can say where the listener came
	// from, which is the only local evidence an operator has.
	for _, want := range []string{"HTTP_ADDR", "DRAIN_DELAY", "STATELESS", "RATE_LIMIT_RPS"} {
		t.Run(want, func(t *testing.T) {
			if !slices.Contains(used, config.EnvName(want)) {
				t.Errorf("%s configured a flag but is missing from the reported list %q", config.EnvName(want), used)
			}
		})
	}
	if got := describeOverlay(used); !strings.Contains(got, config.EnvName("HTTP_ADDR")) {
		t.Errorf("describeOverlay() = %q, want it to name the variables that were used", got)
	}
}

// TestOverlayLeavesAPassedFlagAlone is the precedence, and it is the half a
// naive implementation gets wrong by writing the variable over everything.
func TestOverlayLeavesAPassedFlagAlone(t *testing.T) {
	t.Setenv(config.EnvName("HTTP_ADDR"), "127.0.0.1:9123")
	t.Setenv(config.EnvName("HTTP_PATH"), "/from-the-environment")

	fs := overlayFlagSet(t)
	if err := fs.Parse([]string{"--http", "127.0.0.1:7777"}); err != nil {
		t.Fatalf("parsing the command line: %v", err)
	}
	if _, err := applyHTTPEnvOverlayTo(fs); err != nil {
		t.Fatalf("applyHTTPEnvOverlayTo() error = %v", err)
	}

	if got := overlayValue(t, fs, "http"); got != "127.0.0.1:7777" {
		t.Errorf("--http = %q, want the command line's value: a flag that was typed wins", got)
	}
	// And the setting nobody typed still comes from the environment, so this is
	// precedence rather than the overlay being off altogether.
	if got := overlayValue(t, fs, "http-path"); got != "/from-the-environment" {
		t.Errorf("--http-path = %q, want the variable's value", got)
	}
}

// TestOverlayLeavesAFlagPassedAtItsDefaultAlone is why the question is "was it
// passed" and not "does it differ from the default".
//
// Both settings here are given the value they already had, which is exactly the
// shape of a deployment that spells its configuration out in full. A comparison
// against the default would read them as unset and hand both to the
// environment, reversing the documented precedence on the two settings most
// likely to be written out explicitly.
func TestOverlayLeavesAFlagPassedAtItsDefaultAlone(t *testing.T) {
	t.Setenv(config.EnvName("STATELESS"), "0")
	t.Setenv(config.EnvName("HTTP_PATH"), "/from-the-environment")

	fs := overlayFlagSet(t)
	if err := fs.Parse([]string{"--stateless=true", "--http-path", "/"}); err != nil {
		t.Fatalf("parsing the command line: %v", err)
	}
	if _, err := applyHTTPEnvOverlayTo(fs); err != nil {
		t.Fatalf("applyHTTPEnvOverlayTo() error = %v", err)
	}

	if got := overlayValue(t, fs, "stateless"); got != "true" {
		t.Errorf("--stateless = %q, want true: it was passed, at its default value, and that is still a choice", got)
	}
	if got := overlayValue(t, fs, "http-path"); got != "/" {
		t.Errorf("--http-path = %q, want /: it was passed, at its default value", got)
	}
}

// TestOverlayRefusesAValueThatDoesNotParse keeps a bad variable a startup error.
//
// Falling back to the default in silence is how a deployment that does not match
// its configuration reaches production: the container starts, the probe answers,
// and the setting the operator wrote is simply not in effect. The message names
// the variable rather than the flag, because the variable is what they would go
// and fix.
func TestOverlayRefusesAValueThatDoesNotParse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		envShort  string
		value     string
		wantFlag  string
		wantInErr string
	}{
		{name: "a duration", envShort: "DRAIN_DELAY", value: "soon", wantFlag: "drain-delay", wantInErr: "DRAIN_DELAY"},
		{name: "a boolean", envShort: "STATELESS", value: "yes please", wantFlag: "stateless", wantInErr: "STATELESS"},
		{name: "a number", envShort: "RATE_LIMIT_BURST", value: "lots", wantFlag: "rate-limit-burst", wantInErr: "RATE_LIMIT_BURST"},
		{name: "a byte count", envShort: "MAX_REQUEST_BODY_BYTES", value: "4MiB", wantFlag: "max-request-body-bytes", wantInErr: "MAX_REQUEST_BODY_BYTES"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.EnvName(tc.envShort), tc.value)

			fs := overlayFlagSet(t)
			used, err := applyHTTPEnvOverlayTo(fs)
			if err == nil {
				t.Fatalf("applyHTTPEnvOverlayTo() = %v, nil for %s=%q, want a refusal",
					used, config.EnvName(tc.envShort), tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("the refusal does not name %s: %v", config.EnvName(tc.wantInErr), err)
			}
			if !strings.Contains(err.Error(), "--"+tc.wantFlag) {
				t.Errorf("the refusal does not name --%s, so the operator cannot tell which setting it is: %v", tc.wantFlag, err)
			}
		})
	}
}

// TestOverlayTreatsABlankVariableAsUnset covers what a compose file writes for a
// key it has no value for.
//
// `LIBGEN_MCP_TLS_CERT=` is how `${TLS_CERT}` expands when the variable is not
// set in the shell, and it appears in the process environment as an empty
// string. Taking it as a deliberate value would be indistinguishable from the
// default anyway — but a whitespace-only value would not be, and reading that
// as a path is a startup failure for a setting nobody set.
func TestOverlayTreatsABlankVariableAsUnset(t *testing.T) {
	t.Setenv(config.EnvName("TLS_CERT"), "")
	t.Setenv(config.EnvName("HTTP_PATH"), "   ")

	fs := overlayFlagSet(t)
	used, err := applyHTTPEnvOverlayTo(fs)
	if err != nil {
		t.Fatalf("applyHTTPEnvOverlayTo() error = %v", err)
	}
	if len(used) != 0 {
		t.Errorf("a blank variable configured %q; want nothing", used)
	}
	if got := overlayValue(t, fs, "http-path"); got != "/" {
		t.Errorf("--http-path = %q, want the default", got)
	}
}

// TestOverlayTrimsSurroundingWhitespace covers the value a YAML block scalar or
// a copied line leaves a stray space on. A path with a trailing space is a file
// that does not exist, and the failure names the file rather than the space.
func TestOverlayTrimsSurroundingWhitespace(t *testing.T) {
	t.Setenv(config.EnvName("HTTP_ADDR"), "  127.0.0.1:9123\t")

	fs := overlayFlagSet(t)
	if _, err := applyHTTPEnvOverlayTo(fs); err != nil {
		t.Fatalf("applyHTTPEnvOverlayTo() error = %v", err)
	}
	if got := overlayValue(t, fs, "http"); got != "127.0.0.1:9123" {
		t.Errorf("--http = %q, want the trimmed value", got)
	}
}

// TestOverlayMarksTheFlagAsPassed pins the half of the precedence that the
// refusals downstream depend on.
//
// resolveRateLimit refuses an explicitly asked-for limit on a listener that
// cannot tell two callers apart, rather than downgrading it, and
// resolveHeavyCeiling reads the same "was this asked for" question. An operator
// who exported the variable asked for it exactly as much as one who typed the
// flag, so a variable that configured the value without recording the choice
// would turn a refusal into a silent downgrade.
func TestOverlayMarksTheFlagAsPassed(t *testing.T) {
	t.Setenv(config.EnvName("RATE_LIMIT_RPS"), "3")

	fs := overlayFlagSet(t)
	if flagPassedIn(fs, "rate-limit-rps") {
		t.Fatal("the flag is recorded as passed before anything set it")
	}
	if _, err := applyHTTPEnvOverlayTo(fs); err != nil {
		t.Fatalf("applyHTTPEnvOverlayTo() error = %v", err)
	}
	if !flagPassedIn(fs, "rate-limit-rps") {
		t.Error("a flag filled from its variable is not recorded as passed, so the refusals downstream will read it as a default")
	}
	// A setting nobody configured either way stays unpassed, so the check above
	// is the overlay recording its own writes rather than the set reporting
	// everything as passed.
	if flagPassedIn(fs, "drain-delay") {
		t.Error("--drain-delay is recorded as passed although nothing set it")
	}
}

// TestOverlayCoversEverySettingAnHTTPDeploymentNeeds is the drift gate in the
// other direction: a flag added to the listener without a variable leaves a
// deployment configured through `environment:` unable to set it, which is the
// exact hole this step exists to close.
//
// The list is written out rather than derived from the binary's flag set,
// because deriving it would make the test agree with whatever the code does.
func TestOverlayCoversEverySettingAnHTTPDeploymentNeeds(t *testing.T) {
	want := []string{
		"drain-delay", "http", "http-idle-timeout", "http-path",
		"http-socket-mode", "json-response", "max-inflight-per-client",
		"max-request-body-bytes", "public-url", "rate-limit-burst",
		"rate-limit-rps", "session-timeout", "stateless", "tls-cert", "tls-key",
		"transport", "trusted-origins", "trusted-proxies", "trusted-proxy-header",
	}
	got := make([]string, 0, len(httpEnvOverlay))
	for _, entry := range httpEnvOverlay {
		got = append(got, entry.flagName)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the overlay covers %q, want %q", got, want)
	}
}

// TestOverlayDurationRoundTrip guards the one type whose flag parser and whose
// documented range disagree about what is acceptable: --drain-delay is capped at
// maxDrainDelay by a check of its own, after parsing.
//
// The overlay must not enforce that cap itself. The refusal belongs where it
// already is, so a value out of range is refused identically however it arrived.
func TestOverlayDurationRoundTrip(t *testing.T) {
	t.Setenv(config.EnvName("DRAIN_DELAY"), (2 * maxDrainDelay).String())

	fs := overlayFlagSet(t)
	if _, err := applyHTTPEnvOverlayTo(fs); err != nil {
		t.Fatalf("applyHTTPEnvOverlayTo() error = %v: an out-of-range duration parses, and is refused by the range check downstream", err)
	}
	f := fs.Lookup("drain-delay")
	got, ok := f.Value.(flag.Getter).Get().(time.Duration)
	if !ok {
		t.Fatalf("--drain-delay holds %T, want a duration", f.Value.(flag.Getter).Get())
	}
	if got != 2*maxDrainDelay {
		t.Errorf("--drain-delay = %s, want %s", got, 2*maxDrainDelay)
	}
}
