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

import (
	"os"
	"testing"
)

// EnvVar is the harness setting that defers a committed-artifact comparison.
//
// It is developer-only, which is why it is read here with [os.Getenv] rather
// than through internal/config: that package resolves the settings the server
// itself defines, and every one of them carries the LIBGEN_MCP_ prefix because
// it is part of the server's configuration surface. This is not.
const EnvVar = "LIBGEN_MCP_TEST_SNAPSHOT_PARITY"

// deferredValue is the one value that defers. Anything else compares —
// "checked" and an unset variable included — because the failure modes are not
// symmetric: a run that compares when it did not have to costs a red check on
// a layer nobody merges, and a run that defers when it should have compared
// lets a stale artifact reach main.
const deferredValue = "deferred"

// SkipReason says what was deferred and on whose authority, so a reader of the
// log sees a decision rather than a gap.
//
// The setting is spelled out rather than built from the two constants above: a
// constant expression is not executable, so nothing could exercise the
// concatenation and no analysis could tell a correct one from a wrong one.
// What keeps this honest is TestSkipReason_NamesTheSettingAndTheValue, which
// asserts both names appear in it.
const SkipReason = EnvVar + "=" + deferredValue +
	": the committed artifacts are refreshed and compared where they land, at the top of a stack," +
	" on a pull request to main, and on every push to main"

// Deferred reports whether this run defers committed-artifact comparisons.
func Deferred() bool {
	return os.Getenv(EnvVar) == deferredValue
}

// SkipIfDeferred skips the test when this run defers the comparison. It is
// called first in a test that reads a committed artifact and compares it with
// what the tree would generate now.
func SkipIfDeferred(t *testing.T) {
	t.Helper()
	if Deferred() {
		t.Skip(SkipReason)
	}
}
