// Command bench_resources measures what this server costs to run.
//
// Everything else measured in this repository is about the tool surface: how
// many tokens it spends, whether every field is described, whether the served
// text is gateway-safe. None of it tells an operator how much memory to give a
// container, how long a client waits before its first tool call answers, or
// what a second caller adds to a shared deployment. This command answers those,
// from the real binary, on both transports, and writes one record every
// downstream artifact is rendered from.
//
// # What it needs
//
// Nothing but a Go toolchain. Library Genesis is stood in for by an in-process
// HTTP server on loopback, so a run is offline and a second machine measures
// this server rather than its own network. The mirror is passed to the binary
// explicitly and never read from the ambient environment, for the reason
// CLAUDE.md gives about the audit tools: a developer machine exporting
// LIBGEN_MIRROR would otherwise publish different numbers than a clean one.
//
// # Why the client is an address
//
// On HTTP this server charges a caller by the address its request arrives from
// (internal/clientid), which is what the rate limit, the in-flight ceiling and
// the user.hash attribute are all keyed on. So a concurrency series here steps
// up the number of distinct client addresses rather than the number of
// credentials: that is what a client is on this surface, and measuring anything
// else would measure a dimension no deployment has.
//
// # Usage
//
//	go run ./cmd/bench_resources/                 # measure, then write the record
//	go run ./cmd/bench_resources/ -quick          # short smoke matrix
//	go run ./cmd/bench_resources/ -json /tmp/x.json
package main
