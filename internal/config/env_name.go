package config

import (
	"os"
	"slices"
	"strings"
)

// EnvPrefix is what every variable this server defines is named with.
//
// A stdio MCP server runs in whatever shell its client was started from,
// alongside every other tool that person uses. Names like TIMEOUT, LOG_LEVEL and
// SOURCES are generic enough that another tool may already own them there, and a
// collision is silent: the server reads a value it was never given and behaves
// in a way nobody configured.
//
// No variable this server defines has ever shipped without the prefix, so there
// is no old spelling to keep answering to and no deprecation to warn about. The
// rule is simply the rule.
const EnvPrefix = "LIBGEN_MCP_"

// knownNames is every setting this package reads, spelled without [EnvPrefix].
//
// It is the policy in a form a test can check. TestEveryEnvNameIsKnown walks
// this package's syntax and fails on a read this list does not cover, so adding
// a variable without listing it here is caught rather than remembered — which
// is the only way a list like this stays true.
//
// Two names are deliberately absent, and both are bare on purpose:
//
//   - LIBGEN_MIRROR is the mirror family's own convention, the vendor-shaped
//     counterpart of naming a service's URL. An operator who already has it set
//     must not have to spell it twice.
//   - every OTEL_* name belongs to the OpenTelemetry specification and is read
//     by the exporters themselves, so a prefixed spelling would not be seen.
var knownNames = []string{
	"ALLOWED_DOWNLOAD_DIRS",
	"ALLOWED_READ_DIRS",
	"ALLOW_PRIVATE_ADDRESSES",
	"ANNAS_KEY",
	"CONFIRM_DOWNLOADS",
	"CORE_KEY",
	"DOWNLOAD_DIR",
	"DOWNLOAD_RETRY_EVERY_SOURCE",
	"DOWNLOAD_STALL_TIMEOUT",
	"DOWNLOAD_START_RETRY_WAITS",
	"ENRICH",
	"EXTRA_SOURCES",
	"LOG_LEVEL",
	"MAX_CONCURRENT_DOWNLOADS",
	"MAX_DOWNLOAD_BYTES",
	"RATE_BURST",
	"RATE_RPS",
	"READ_CACHE_BYTES",
	"STDIO_MAX_LINE_BYTES",
	"READ_CACHE_TTL",
	"READ_DEFAULT_PAGES",
	"READ_MAX_CHARS",
	"REMOTE_DOWNLOADS",
	"ENV_FILE",
	"PPROF_ADDR",
	"RESOLVE_BUDGET",
	"RETRY_ATTEMPTS",
	"SCIHUB_HOSTS",
	"SERVER_FETCH",
	"SOURCES",
	"TIMEOUT",
	"UNPAYWALL_EMAIL",
}

// EnvName returns the full variable name a setting is read from, so an error
// message names what an operator would actually set rather than the short name
// the code passes around.
func EnvName(name string) string {
	return EnvPrefix + name
}

// Getenv reads a setting under its prefixed name. name is the short form, as it
// appears in [knownNames].
//
// Every read in this package goes through here rather than through os.Getenv so
// the prefix is applied in one place: a variable added later inherits the rule
// instead of having to restate it, and a name that forgot the prefix is a
// missing entry in one list rather than a silent collision with another tool's
// environment.
func Getenv(name string) string {
	return os.Getenv(EnvName(name))
}

// TrimmedGetenv is [Getenv] with surrounding whitespace removed, for a setting
// where a stray space is a typo rather than a value. A value that is only
// whitespace reads as unset.
func TrimmedGetenv(name string) string {
	return strings.TrimSpace(Getenv(name))
}

// KnownEnvNames returns every variable this package reads, fully spelled and in
// order. It is what an audit or a documentation generator enumerates rather than
// grepping for a prefix.
func KnownEnvNames() []string {
	full := make([]string, 0, len(knownNames))
	for _, name := range slices.Sorted(slices.Values(knownNames)) {
		full = append(full, EnvName(name))
	}
	return full
}
