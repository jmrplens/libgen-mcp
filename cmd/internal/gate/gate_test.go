package gate

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// TestParse_ReadsTheTwoFlagsEveryGateTakes verifies the defaults and both
// spellings, since the Makefile writes one and a person at a terminal writes
// the other.
func TestParse_ReadsTheTwoFlagsEveryGateTakes(t *testing.T) {
	testCases := []struct {
		name      string
		args      []string
		wantDir   string
		wantCheck bool
	}{
		{name: "no arguments", args: nil, wantDir: ".", wantCheck: false},
		{name: "one dash", args: []string{"-dir", "/tmp", "-check"}, wantDir: "/tmp", wantCheck: true},
		{name: "two dashes", args: []string{"--dir", "/tmp", "--check"}, wantDir: "/tmp", wantCheck: true},
		{name: "check with an explicit value", args: []string{"-check=false"}, wantDir: ".", wantCheck: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer
			parsed, err := Parse("audit_x", tc.args, &errOut, "refuse a finding", nil)
			if err != nil {
				t.Fatalf("Parse(%v) = %v", tc.args, err)
			}
			if parsed.Dir != tc.wantDir || parsed.Check != tc.wantCheck {
				t.Errorf("Parse(%v) = %+v, want dir %q and check %v", tc.args, parsed, tc.wantDir, tc.wantCheck)
			}
		})
	}
}

// TestParse_ReturnsTheTwoFailuresApart pins why the error is returned rather
// than acted on: usage is a clean exit and a bad flag is not, and each gate
// writes those statuses at its own call site where the three read together.
func TestParse_ReturnsTheTwoFailuresApart(t *testing.T) {
	testCases := []struct {
		name       string
		args       []string
		wantHelped bool
	}{
		{name: "usage", args: []string{"-h"}, wantHelped: true},
		{name: "an unknown flag", args: []string{"-nonsense"}, wantHelped: false},
		{name: "a flag with no value", args: []string{"-dir"}, wantHelped: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer
			_, err := Parse("audit_x", tc.args, &errOut, "refuse a finding", nil)
			if err == nil {
				t.Fatalf("Parse(%v) accepted the command line", tc.args)
			}
			if got := Helped(err); got != tc.wantHelped {
				t.Errorf("Helped(%v) = %v, want %v", err, got, tc.wantHelped)
			}
		})
	}
}

// TestHelped_IsFalseForNoError verifies the helper answers about an error
// rather than about the absence of one, so a caller that asks first does not
// exit 0 on a run that never failed.
func TestHelped_IsFalseForNoError(t *testing.T) {
	if Helped(nil) {
		t.Error("Helped(nil) = true, want false")
	}
}

// TestParse_UsageNamesWhatCheckRefuses verifies the one sentence each gate
// supplies reaches its own -h output, which is the part of the usage that
// differs between them.
func TestParse_UsageNamesWhatCheckRefuses(t *testing.T) {
	var errOut bytes.Buffer
	if _, err := Parse("audit_x", []string{"-h"}, &errOut, "exit non-zero when a name does not resolve", nil); !Helped(err) {
		t.Fatalf("Parse(-h) = %v, want the usage request", err)
	}
	if !strings.Contains(errOut.String(), "exit non-zero when a name does not resolve") {
		t.Errorf("usage = %q, want it to name what -check refuses", errOut.String())
	}
}

// TestParse_RegistersACommandsOwnFlags verifies the hook, so a gate that needs
// a third flag is not pushed back into writing the whole preamble again.
func TestParse_RegistersACommandsOwnFlags(t *testing.T) {
	var (
		errOut  bytes.Buffer
		verbose bool
	)
	register := func(flags *flag.FlagSet) { flags.BoolVar(&verbose, "v", false, "list everything") }

	parsed, err := Parse("audit_x", []string{"-v", "-dir", "/tmp"}, &errOut, "refuse a finding", register)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if !verbose {
		t.Error("the command's own flag was not registered")
	}
	if parsed.Dir != "/tmp" {
		t.Errorf("Dir = %q, want the shared flag still read", parsed.Dir)
	}
}
