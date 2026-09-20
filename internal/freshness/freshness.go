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
