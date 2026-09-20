// Package freshness reads the one harness setting that decides whether a test
// comparing a committed, generated artifact runs that comparison now or leaves
// it to the run where the artifact is refreshed.
//
// # Why the question exists
//
// A freshness gate holds a committed file against what the source tree would
// generate now. In a stack of pull requests the artifacts are refreshed once,
// at the top, so every layer below carries them stale on purpose and would
// fail on drift the top overwrites. CI computes that answer once, as FRESHNESS
// in .github/workflows/ci.yml, and gates its own generated-artifact steps on
// it; this package is how the same answer reaches a test in the unit suite,
// which runs in a job of its own.
//
// # What reads it
//
// Test files only, and never the server: a setting that changes what a test
// asserts has no business being reachable from a binary somebody deploys.
//
// # What deferring does not mean
//
// It never means the artifact goes unchecked. The comparison runs wherever the
// refresh lands: at the top of a stack, on a pull request to main, and on
// every push to main, where CI answers "checked" and the matching make check-
// gate runs as well.
package freshness
