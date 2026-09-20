// Command gen_lhm_manifest regenerates the tools and prompts arrays in
// lhm.plugin.json, the manifest published to the LobeHub Marketplace.
//
// LobeHub derives a listing's capability badges from the manifest's own
// tools/resources/prompts arrays — its crawler cannot introspect a server that
// ships as a Go binary or a Docker image, so a manifest without them lists the
// server as having zero tools and zero prompts no matter what the server
// actually registers. The arrays are therefore data we owe the marketplace, and
// this command derives them from a real tools/list + prompts/list round-trip
// against an in-memory server rather than from a hand-written copy that would
// drift on the next release.
//
// Every field the manifest already carries is preserved; only tools and prompts
// are rewritten.
//
// Usage:
//
//	go run ./cmd/gen_lhm_manifest/
//	go run ./cmd/gen_lhm_manifest/ --check
package main
