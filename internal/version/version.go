// Package version carries the build's version string and the User-Agent derived
// from it, so every outbound request identifies the binary that actually made it.
//
// The version reaches this package at runtime rather than at compile time. The
// release ldflags stamp `main.version` — the Makefile and .goreleaser.yml both do,
// and they are the single place a release number is injected — so a second
// `-X` target here would be a second thing to keep in step, which is exactly how
// the user agent came to advertise 1.0.0 while VERSION said 1.3.4. Instead each
// command hands its stamped value to Set during startup, before any request is
// made.
package version

// fallback is the version reported by a build nobody stamped: a `go run`, a test,
// or a command that forgot to call Set. It is deliberately not a number. A wrong
// number is worse than an honest "unknown", because a server operator reading our
// User-Agent in their logs cannot tell a stale release from an unstamped build.
const fallback = "dev"

// current is the stamped version. It is written once by Set during startup and
// only read afterwards, so it needs no synchronization; Set documents that rule.
var current = fallback

// Set records the version this binary was stamped with. It must be called from a
// command's startup path, before any goroutine issues a request, and never after.
// An empty or unset value leaves the honest fallback in place rather than
// advertising a blank version.
func Set(v string) {
	if v != "" {
		current = v
	}
}

// Current returns the version this binary reports, which is the stamped release
// number or "dev" when nobody stamped one.
func Current() string { return current }

// UserAgent returns the User-Agent every outbound request carries: the binary's
// name, its version and a link to the project, so an operator who sees the
// traffic can tell what it is and who to contact about it. Several sources are
// served only because this string is honest — one of them, ETSI, refuses a bare
// `curl` User-Agent and answers this one.
//
// It is built per call rather than cached in a package variable, because a
// variable would be initialized before a command's startup path reaches Set and
// would pin the fallback forever.
func UserAgent() string {
	return "libgen-mcp/" + current + " (+https://github.com/jmrplens/libgen-mcp)"
}
