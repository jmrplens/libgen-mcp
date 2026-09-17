// env_overlay.go applies environment variables underneath the HTTP-mode flags.
//
// Every HTTP setting was flag-only, and everything else this server reads is a
// LIBGEN_MCP_* variable — so a compose file's `environment:` block, a systemd
// unit's `Environment=` or a Kubernetes ConfigMap could configure the whole
// server except the half that decides where it listens and who may reach it.
// Configuring those meant rewriting the command line, which in a container image
// means rebuilding it.
//
// The precedence is the one the rest of this server already documents: an
// explicitly passed flag, then the environment, then the built-in default.
//
// # Why this writes through the flag set
//
// Each value is handed to [flag.Set], which runs the very parser the command
// line runs. A duration is parsed by the duration flag, an octal socket mode by
// the same string flag the mode parser reads afterwards, a boolean by
// strconv.ParseBool — the house grammar, so 1/0/t/f/true/false all work here
// exactly as they do everywhere else. A second parser written beside this one
// would be a second thing to keep in agreement, and the first disagreement would
// be a value accepted on the command line and refused in a ConfigMap.
//
// It also means a value that does not parse is a named startup error rather than
// a silent fallback to the default, which is how a deployment that does not match
// its configuration survives to production.
//
// # Nothing here is reachable per request
//
// These decide the listener, the identity every per-caller budget is keyed on,
// and the origins a browser may use. A request that could reach any of them
// would be a client configuring the server for everybody else. The only per-call
// inputs this server takes are the optional Anna's and Unpaywall credentials,
// which are used once and never stored.

package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// httpEnvOverlay pairs each HTTP flag with the variable that fills it in when
// the flag was not passed.
//
// The variable names are the short form [config.KnownEnvNames] lists, so the
// prefix is applied in one place and a name that forgot it is a missing entry in
// one list rather than a silent collision with another tool's environment.
var httpEnvOverlay = []struct {
	// flagName is what the operator types.
	flagName string
	// envShortName is the setting as knownNames spells it, without the prefix.
	envShortName string
}{
	{flagName: "http", envShortName: "HTTP_ADDR"},
	{flagName: "http-path", envShortName: "HTTP_PATH"},
	{flagName: "http-socket-mode", envShortName: "HTTP_SOCKET_MODE"},
	{flagName: "transport", envShortName: "TRANSPORT"},
	{flagName: "public-url", envShortName: "PUBLIC_URL"},
	{flagName: "trusted-origins", envShortName: "TRUSTED_ORIGINS"},
	{flagName: "trusted-proxies", envShortName: "TRUSTED_PROXIES"},
	{flagName: "trusted-proxy-header", envShortName: "TRUSTED_PROXY_HEADER"},
	{flagName: "tls-cert", envShortName: "TLS_CERT"},
	{flagName: "tls-key", envShortName: "TLS_KEY"},
	{flagName: "stateless", envShortName: "STATELESS"},
	{flagName: "json-response", envShortName: "JSON_RESPONSE"},
	{flagName: "max-request-body-bytes", envShortName: "MAX_REQUEST_BODY_BYTES"},
	{flagName: "rate-limit-rps", envShortName: "RATE_LIMIT_RPS"},
	{flagName: "rate-limit-burst", envShortName: "RATE_LIMIT_BURST"},
	{flagName: "max-inflight-per-client", envShortName: "MAX_INFLIGHT_PER_CLIENT"},
	{flagName: "drain-delay", envShortName: "DRAIN_DELAY"},
}

// applyHTTPEnvOverlay fills in every HTTP flag the operator did not pass, from
// the variable that backs it, and returns the variables that configured
// something. Call after [flag.Parse] and after the dotenv files have been
// loaded, and before anything reads a flag.
//
// A flag that was passed is left alone, whatever it was set to: the question is
// whether the operator typed it, not whether the value differs from the default.
// `--stateless=true` on a deployment exporting LIBGEN_MCP_STATELESS=0 means
// stateless, and comparing values instead would read it as unset.
//
// A variable that is set but blank is treated as unset. An empty string is what a
// compose file writes for a key it has no value for — `LIBGEN_MCP_TLS_CERT=` when
// the certificate is optional — and taking it as a deliberate empty value would
// be indistinguishable from the default anyway.
//
// The names come back rather than being logged here because the answer is only
// useful once, at startup, on the stream the transport decision goes to. Only
// the ones that were used: listing all seventeen on every start is noise, and
// listing none leaves the deployment that is surprised by its own listener
// nothing to read.
func applyHTTPEnvOverlay() (used []string, err error) {
	return applyHTTPEnvOverlayTo(flag.CommandLine)
}

// applyHTTPEnvOverlayTo is [applyHTTPEnvOverlay] against a flag set the caller
// owns, so a test can drive it without the process's own command line.
func applyHTTPEnvOverlayTo(fs *flag.FlagSet) (used []string, err error) {
	for _, entry := range httpEnvOverlay {
		if flagPassedIn(fs, entry.flagName) {
			continue
		}
		value := config.TrimmedGetenv(entry.envShortName)
		if value == "" {
			continue
		}
		full := config.EnvName(entry.envShortName)
		// Set records the flag as set, which is the right answer to the question
		// isFlagPassed asks downstream: an operator who exported
		// LIBGEN_MCP_RATE_LIMIT_RPS chose that value as deliberately as one who
		// typed it, and the refusals that turn on "was this asked for
		// explicitly" must treat the two the same.
		if setErr := fs.Set(entry.flagName, value); setErr != nil {
			return nil, fmt.Errorf("%s=%q is not a valid --%s: %w", full, value, entry.flagName, setErr)
		}
		used = append(used, full)
	}
	return used, nil
}

// flagPassedIn reports whether the named flag of fs holds a value somebody gave
// it, as opposed to its default.
//
// It asks the set rather than comparing values, because a flag set to the same
// value as its default is still a choice: --stateless=true on a deployment
// exporting LIBGEN_MCP_STATELESS=0 means stateless, and a comparison would read
// it as unset and then overwrite it.
func flagPassedIn(fs *flag.FlagSet, name string) bool {
	passed := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			passed = true
		}
	})
	return passed
}

// describeOverlay renders what applyHTTPEnvOverlay reported for the startup log.
func describeOverlay(used []string) string {
	return strings.Join(used, ", ")
}
