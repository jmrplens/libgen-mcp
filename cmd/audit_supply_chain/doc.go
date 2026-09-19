// Command audit_supply_chain audits the release supply chain's configuration
// invariants.
//
// Four properties, each of which was false in this repository at some point and
// each of which is invisible to every other gate here:
//
//  1. Every uses: in .github/workflows is pinned to a 40-character commit SHA.
//     A mutable tag is resolved by the runner at job start, so a hijacked v7 is
//     consumed with no pull request, no cooldown and no review.
//  2. A job holding contents: write or id-token: write runs no code resolved at
//     run time. That means no npx, no @latest, no curl piped into a shell and
//     no unhashed pip install — in its own run: blocks or in any scripts/ file
//     those blocks invoke — and a tool such a job downloads is pinned to an
//     exact version, because SHA-pinning the action that fetches a binary does
//     not pin the binary.
//  3. Dependabot states a cooldown on every ecosystem instead of inheriting a
//     platform default that GitHub can change under us.
//  4. SECURITY.md carries a supported-versions table, and no row in it marks a
//     superseded major as supported.
//
// Usage:
//
//	go run ./cmd/audit_supply_chain/ [--root <dir>]
//
// Exits non-zero and prints one line per violation.
//
// The auditor is deliberately split in two: pinning is decided on the raw file
// text, so a uses: inside a commented-out block still counts, while job
// structure comes from the parsed YAML. A document with a duplicated mapping key
// is refused rather than audited, because its meaning is ambiguous and the
// parser's choice of which value wins is not the one a reader would make.
package main
