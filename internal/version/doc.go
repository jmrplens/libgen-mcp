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
