package main

import (
	"strings"
	"testing"
	"time"
)

// TestCheckSessionTimeoutRefusesWhatItCannotServe covers both refusals and the
// case that decides whether the shipped default is usable at all.
//
// The last one is the one worth writing down: --session-timeout carries a
// non-zero default, so every stateless deployment in the world holds a value for
// a setting its transport does not use. Refusing on the value would refuse them
// all, which is why the refusal asks whether the operator typed it.
func TestCheckSessionTimeoutRefusesWhatItCannotServe(t *testing.T) {
	testCases := []struct {
		name      string
		timeout   time.Duration
		stateless bool
		explicit  bool
		want      string
	}{
		{name: "the default under the stateless transport nobody configured", timeout: defaultSessionTimeout, stateless: true},
		{name: "a stateful deployment that set one", timeout: time.Minute, stateless: false, explicit: true},
		{name: "a stateful deployment that turned it off", timeout: 0, stateless: false, explicit: true},
		{name: "the maximum exactly", timeout: maxSessionTimeout, stateless: false, explicit: true},
		{
			name: "a negative duration", timeout: -time.Second, stateless: false, explicit: true,
			want: "must be between 0 and",
		},
		{
			name: "past the maximum", timeout: maxSessionTimeout + time.Second, stateless: false, explicit: true,
			want: "must be between 0 and",
		},
		{
			name: "set under the stateless transport", timeout: time.Minute, stateless: true, explicit: true,
			want: "cannot apply under the default stateless transport",
		},
		{
			// Out of range is judged before the mode, so an operator who is wrong
			// twice is told about the value rather than about the transport: the
			// value is what they have to change either way.
			name: "both wrong at once", timeout: -time.Second, stateless: true, explicit: true,
			want: "must be between 0 and",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSessionTimeout(tc.timeout, tc.stateless, tc.explicit)
			if tc.want == "" {
				if err != nil {
					t.Errorf("checkSessionTimeout(%s, stateless=%t, explicit=%t) = %v, want it accepted",
						tc.timeout, tc.stateless, tc.explicit, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("checkSessionTimeout(%s, stateless=%t, explicit=%t) = %v, want it to say %q",
					tc.timeout, tc.stateless, tc.explicit, err, tc.want)
			}
		})
	}
}

// TestCheckIdleTimeoutsChecksBoth verifies the pair is validated together, and
// that a caller holding one bad value and one good one is still refused.
//
// The order matters and is asserted: the session refusal comes first, so a
// deployment wrong about both is told about the session — which is the setting
// that changes what the server does, where a negative idle timeout is a typo.
func TestCheckIdleTimeoutsChecksBoth(t *testing.T) {
	testCases := []struct {
		name string
		in   idleTimeouts
		want string
	}{
		{
			name: "both usable",
			in:   idleTimeouts{session: time.Minute, sessionExplicit: true, httpIdle: time.Minute},
		},
		{
			name: "both zero",
			in:   idleTimeouts{stateless: true},
		},
		{
			name: "only the idle timeout is wrong",
			in:   idleTimeouts{httpIdle: -time.Second, stateless: true},
			want: "--http-idle-timeout",
		},
		{
			name: "only the session timeout is wrong",
			in:   idleTimeouts{session: time.Minute, sessionExplicit: true, stateless: true},
			want: "--session-timeout",
		},
		{
			name: "both wrong reports the session",
			in:   idleTimeouts{session: time.Minute, sessionExplicit: true, httpIdle: -time.Second, stateless: true},
			want: "--session-timeout",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkIdleTimeouts(tc.in)
			if tc.want == "" {
				if err != nil {
					t.Errorf("checkIdleTimeouts(%+v) = %v, want it accepted", tc.in, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("checkIdleTimeouts(%+v) = %v, want it to say %q", tc.in, err, tc.want)
			}
		})
	}
}

// TestCheckSessionTimeoutNamesTheWayOut verifies the refusal says what to do
// about it, in both spellings.
//
// A message that only states the contradiction leaves an operator to guess which
// half to change, and the answer is genuinely either: keep sessions, or stop
// asking for a timeout. The variable is named too, because a deployment
// configured through `environment:` never typed the flag and would otherwise
// search its compose file for a string that is not in it.
func TestCheckSessionTimeoutNamesTheWayOut(t *testing.T) {
	err := checkSessionTimeout(time.Minute, true, true)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"--stateless=false", "LIBGEN_MCP_SESSION_TIMEOUT"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %q: %v", want, err)
			}
		})
	}
}

// TestSessionTimeoutForIsZeroWhereSessionsDoNotOutliveARequest verifies the
// value handed to the transport.
func TestSessionTimeoutForIsZeroWhereSessionsDoNotOutliveARequest(t *testing.T) {
	testCases := []struct {
		name      string
		timeout   time.Duration
		stateless bool
		want      time.Duration
	}{
		{name: "stateless discards the default", timeout: defaultSessionTimeout, stateless: true, want: 0},
		{name: "stateful keeps the default", timeout: defaultSessionTimeout, stateless: false, want: defaultSessionTimeout},
		{name: "stateful keeps a zero somebody chose", timeout: 0, stateless: false, want: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionTimeoutFor(tc.timeout, tc.stateless); got != tc.want {
				t.Errorf("sessionTimeoutFor(%s, stateless=%t) = %s, want %s",
					tc.timeout, tc.stateless, got, tc.want)
			}
		})
	}
}

// TestTheShippedDefaultsAreTheOnesTheFlagsDocument pins the two numbers the help
// text quotes, because the help text is built from them and a reader who checks
// one against the other would find them agreeing no matter what they were.
func TestTheShippedDefaultsAreTheOnesTheFlagsDocument(t *testing.T) {
	if defaultSessionTimeout != 30*time.Minute {
		t.Errorf("defaultSessionTimeout = %s, want 30m: long enough that no working client is cut off", defaultSessionTimeout)
	}
	if maxSessionTimeout != 24*time.Hour {
		t.Errorf("maxSessionTimeout = %s, want 24h", maxSessionTimeout)
	}
	// Zero rather than a number, so upgrading to a build that has the flag does
	// not move any deployment's connection handling.
	if defaultHTTPIdleTimeout != 0 {
		t.Errorf("defaultHTTPIdleTimeout = %s, want 0: the flag must not change what a deployment already does", defaultHTTPIdleTimeout)
	}
}
