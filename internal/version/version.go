package version

import libgenmcp "github.com/jmrplens/libgen-mcp"

// current is the version this binary reports. It starts as the number compiled in
// from the repository's VERSION file — the same file the release manifests are
// gated against — so a build nobody stamped is already honest. Set overwrites it
// once during startup when release ldflags carried something more specific.
//
// It is written once by Set and only read afterwards, so it needs no
// synchronization; Set documents that rule.
var current = libgenmcp.Version

// Set records the version this binary was stamped with. It must be called from a
// command's startup path, before any goroutine issues a request, and never after.
// An empty value leaves the compiled-in VERSION in place rather than advertising a
// blank version.
func Set(v string) {
	if v != "" {
		current = v
	}
}

// Current returns the version this binary reports: the release ldflags' value when
// one was stamped, otherwise the number compiled in from VERSION.
func Current() string { return current }

// UserAgent returns the User-Agent every outbound request carries: the binary's
// name, its version and a link to the project, so an operator who sees the
// traffic can tell what it is and who to contact about it. Several sources are
// served only because this string is honest — one of them, ETSI, refuses a bare
// `curl` User-Agent and answers this one.
//
// It is built per call rather than cached in a package variable, because a
// variable would be initialized before a command's startup path reaches Set and
// would pin the pre-Set value forever.
func UserAgent() string {
	return "libgen-mcp/" + current + " (+https://github.com/jmrplens/libgen-mcp)"
}
