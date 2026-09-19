package freshness

import (
	"strings"
	"testing"
)

// TestDeferred_OnlyOneValueDefers pins the asymmetry the package exists for: a
// run that compares when it did not have to costs a red check on a layer
// nobody merges, and a run that defers when it should have compared lets a
// stale artifact reach main. So everything but the one value compares.
func TestDeferred_OnlyOneValueDefers(t *testing.T) {
	testCases := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{name: "unset", want: false},
		{name: "deferred", value: "deferred", set: true, want: true},
		{name: "checked", value: "checked", set: true, want: false},
		{name: "empty", value: "", set: true, want: false},
		{name: "a near miss", value: "defer", set: true, want: false},
		{name: "the right word capitalized", value: "Deferred", set: true, want: false},
		{name: "the right word with spacing", value: " deferred ", set: true, want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(EnvVar, tc.value)
			}
			if got := Deferred(); got != tc.want {
				t.Errorf("Deferred() with %s=%q (set=%v) = %v, want %v", EnvVar, tc.value, tc.set, got, tc.want)
			}
		})
	}
}

// TestSkipIfDeferred_SkipsOnlyWhenDeferred drives the helper both ways through
// a sub-test, which is the only way to observe a skip without skipping the
// test that is asserting.
func TestSkipIfDeferred_SkipsOnlyWhenDeferred(t *testing.T) {
	testCases := []struct {
		name     string
		value    string
		wantSkip bool
	}{
		{name: "deferred skips", value: "deferred", wantSkip: true},
		{name: "checked runs", value: "checked", wantSkip: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvVar, tc.value)
			reached := false
			skipped := t.Run("inner", func(inner *testing.T) {
				SkipIfDeferred(inner)
				reached = true
			})
			if !skipped {
				t.Fatalf("the inner test failed, which neither case expects")
			}
			if reached == tc.wantSkip {
				t.Errorf("with %s=%q the body ran = %v, want %v", EnvVar, tc.value, reached, !tc.wantSkip)
			}
		})
	}
}

// TestSkipReason_NamesTheSettingAndTheValue is what keeps the spelled-out
// reason honest. A constant expression is not executable, so nothing else can
// tell a correct one from a wrong one, and a skip message naming a setting
// that does not exist is worse than none: it sends the reader looking for it.
func TestSkipReason_NamesTheSettingAndTheValue(t *testing.T) {
	for _, want := range []string{EnvVar, deferredValue} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(SkipReason, want) {
				t.Errorf("SkipReason = %q, want it to name %q", SkipReason, want)
			}
		})
	}
}
