// Package stdioe2e drives the real libgen-mcp binary over pipes the way an MCP
// client does: process lifetime, stdout carrying nothing but JSON-RPC, logs on
// stderr with their severities intact, a handshake answered before the catalog
// is asked for, and the exit status a supervisor reads.
//
// It reaches nothing it did not start itself — the mirror cache is seeded, the
// search is told not to federate, and the sessions run behind a dead proxy that
// exempts loopback — so it runs on every CI push rather than reporting a third
// party's outage as this server's regression.
//
// The tests carry the stdioe2e build tag; this file is what a plain build sees
// of the package.
//
// stdio is this server's primary transport and, until this module existed,
// nothing started the binary and spoke it. The live suite under test/e2e talks
// to real mirrors and answers questions about tool behavior; test/e2e/http
// drives the binary but only over a socket. Neither has a pipe, a separated
// stdout, or a process to outlive a client — which is where a stray Println, a
// misrouted logger and a keepalive that closes an idle session all live.
package stdioe2e
