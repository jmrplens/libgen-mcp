// http_only_flags.go names the flags only the HTTP transport reads, and says so
// at startup when a stdio run was given one.
//
// Every flag is registered for the whole process, so a stdio run accepts and
// parses all of them. Most of what mainWithExit builds from them lands in the
// listener specification and the transport options, which a stdio run never
// reads: serveStdio takes its configuration from config.Load and nothing else.
// So `libgen-mcp --rate-limit-rps 5` in an MCP client's configuration switched
// no limiter on and said nothing about it, and the same was true of a
// LIBGEN_MCP_RATE_LIMIT_RPS left in a shared dotenv file, since the environment
// overlay writes those variables through the same flags.
//
// None of these is dangerous to ignore on stdio. Each configures a listener, a
// per-caller budget keyed on an address, a browser origin, a proxy or TLS, and a
// stdio server has one caller on the other end of a pipe and none of those
// things. So the run is not refused: it names each flag once, on stderr, and
// carries on.

package main

import (
	"context"
	"flag"
	"log/slog"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
)

// httpOnlyFlag describes one flag only the HTTP transport reads.
type httpOnlyFlag struct {
	// stdioVariable is the short name of the variable a stdio server reads for
	// the same setting, or empty where stdio has no such setting at all, which
	// is every flag here but one.
	stdioVariable string
}

// httpOnlyFlags names every flag only the HTTP transport reads, keyed by the
// name the operator types.
//
// TestHTTPOnlyFlags_ClassifyEveryRegisteredFlag reads this package's source for
// every flag registration and fails on one that is in neither this table nor
// [stdioFlags], so a flag added to the HTTP half cannot be ignored on stdio in
// silence, and a flag that stops being HTTP-only cannot stay here.
//
// --tls-cert is also read by --healthcheck, which answers and exits before a
// transport is chosen, so for a run that serves it is HTTP-only.
var httpOnlyFlags = map[string]httpOnlyFlag{
	"http-path":               {},
	"http-socket-mode":        {},
	"public-url":              {},
	"trusted-origins":         {},
	"trusted-proxies":         {},
	"trusted-proxy-header":    {},
	"tls-cert":                {},
	"tls-key":                 {},
	"stateless":               {},
	"json-response":           {},
	"rate-limit-rps":          {},
	"rate-limit-burst":        {},
	"max-inflight-per-client": {},
	"drain-delay":             {},
	"session-timeout":         {},
	"http-idle-timeout":       {},
	// The largest inbound JSON-RPC message is bounded on both transports, by a
	// body ceiling on HTTP and a line ceiling on stdio, and the two share a
	// 4 MiB default so they refuse the same messages.
	"max-request-body-bytes": {stdioVariable: "STDIO_MAX_LINE_BYTES"},
}

// answeredBeforeTransport is why a flag that answers and exits is not one a
// stdio run ignores.
const answeredBeforeTransport = "answered and exited before a transport is chosen"

// stdioFlags names every other flag, each with why a stdio run is not ignoring
// it. The flags [envBackedFlags] registers belong here too and are not repeated:
// each writes its variable before anything reads configuration, on either
// transport.
var stdioFlags = map[string]string{
	"transport":     "decides the transport",
	"http":          "decides the transport, and an address given to a run that serves stdio is already warned about by decide",
	"version":       answeredBeforeTransport,
	"healthcheck":   answeredBeforeTransport,
	"shutdown":      answeredBeforeTransport,
	mirrorFlagName:  "writes LIBGEN_MIRROR, which config.Load reads on either transport",
	envFileFlagName: "names a dotenv file config.Load reads on either transport",
}

// ignoredHTTPFlag is one HTTP-only flag a stdio run was given.
type ignoredHTTPFlag struct {
	// name is the flag without its dashes.
	name string
	// fromEnvironment records that the environment overlay set it rather than
	// the command line.
	fromEnvironment bool
}

// The two lines [transportDecision.explain] writes about an ignored flag, one
// for each place it came from. test/e2e/stdio reads them off the real binary's
// stderr and spells them there, since it cannot import this package.
const (
	stdioIgnoredFlagLine     = "this flag is read by the HTTP transport only, so it has no effect on a stdio server"
	stdioIgnoredVariableLine = "this variable configures the HTTP transport only, so it has no effect on a stdio server"
)

// flagsPassedIn returns the names of the flags fs holds a value for.
//
// mainWithExit asks it once right after flag.Parse, before the environment
// overlay writes into the same set, which is the only moment the answer is
// "what the command line said" rather than "what anything said".
func flagsPassedIn(fs *flag.FlagSet) map[string]bool {
	passed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { passed[f.Name] = true })
	return passed
}

// httpOnlyFlagsIgnored returns the HTTP-only flags fs holds a value for, in name
// order, each marked with whether the environment overlay set it, or nothing
// when the process serves HTTP and so reads them all. typed is what
// [flagsPassedIn] returned before the overlay ran.
func httpOnlyFlagsIgnored(useHTTP bool, fs *flag.FlagSet, typed map[string]bool) []ignoredHTTPFlag {
	if useHTTP {
		return nil
	}
	var given []ignoredHTTPFlag
	fs.Visit(func(f *flag.Flag) {
		if _, httpOnly := httpOnlyFlags[f.Name]; httpOnly {
			given = append(given, ignoredHTTPFlag{name: f.Name, fromEnvironment: !typed[f.Name]})
		}
	})
	return given
}

// overlayVariable is the variable the environment overlay reads for flagName,
// spelled in full, or empty when there is none.
func overlayVariable(flagName string) string {
	for _, entry := range httpEnvOverlay {
		if entry.flagName == flagName {
			return config.EnvName(entry.envShortName)
		}
	}
	return ""
}

// level is the severity an ignored flag is named at.
//
// WARN by default: an operator who chose stdio, or took it by default, and typed
// an HTTP flag asked for something this process is not going to do. Two sources
// are INFO instead, for a flag stdio has no setting for at all:
//
//   - one given under --transport=auto, since that command line was written for
//     either transport. The container image's own command is `--transport auto
//     --http 0.0.0.0:8080`, and a warning on every `docker run -i` session of it
//     would teach whoever reads that log to skip warnings.
//   - one set through the environment, which is ambient rather than typed for
//     this invocation: a dotenv file in the home directory is read by every
//     process of this binary, an HTTP deployment's included.
//
// A flag stdio does have a variable for stays at WARN either way, since a
// setting written once for either transport was plausibly meant for both.
func (f ignoredHTTPFlag) level(inferred bool) slog.Level {
	if httpOnlyFlags[f.name].stdioVariable == "" && (inferred || f.fromEnvironment) {
		return slog.LevelInfo
	}
	return slog.LevelWarn
}

// logIgnoredHTTPFlag writes the line naming one ignored flag: the flag, the
// variable the overlay reads for it, and the variable stdio reads instead where
// there is one. The line goes through slog, which is stderr: on this transport
// stdout carries JSON-RPC and nothing else.
func logIgnoredHTTPFlag(ctx context.Context, f ignoredHTTPFlag, inferred bool) {
	line, source := stdioIgnoredFlagLine, "command line"
	if f.fromEnvironment {
		line, source = stdioIgnoredVariableLine, "environment"
	}
	attrs := []any{"flag", "--" + f.name, "source", source}
	if variable := overlayVariable(f.name); variable != "" {
		attrs = append(attrs, "variable", variable)
	}
	if stdio := httpOnlyFlags[f.name].stdioVariable; stdio != "" {
		attrs = append(attrs, "on_stdio_set", config.EnvName(stdio))
	}
	slog.Log(ctx, f.level(inferred), line, attrs...)
}
