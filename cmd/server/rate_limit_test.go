package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// proxied is the charge policy of a deployment that named the proxy in front.
func proxied() chargePolicy {
	return chargePolicy{header: "X-Real-IP", proxies: mustProxies("127.0.0.1/32")}
}

// mustProxies parses a --trusted-proxies value or panics; the values here are
// this file's own literals.
func mustProxies(value string) trustedProxies {
	proxies, err := parseTrustedProxies(commaSeparated(value))
	if err != nil {
		panic(err)
	}
	return proxies
}

// TestResolveRateLimitLeavesItOnWhereAnAddressMeansSomething is the shape the
// hosted deployment runs and the one both documented recipes use.
//
// A wildcard bind is the undecidable case — it may serve real remote peers and a
// same-host proxy at once, and this process cannot see a port publication — so
// startup does not refuse it and does not turn the limit off. What covers the
// bad half of it is the runtime warning below.
func TestResolveRateLimitLeavesItOnWhereAnAddressMeansSomething(t *testing.T) {
	cases := []struct {
		name   string
		addr   string
		charge chargePolicy
	}{
		{name: "a wildcard bind with no proxy flags", addr: "0.0.0.0:8080"},
		{name: "a wildcard bind with them", addr: "0.0.0.0:8080", charge: proxied()},
		{name: "a bind naming a routable address", addr: "10.1.2.3:8080"},
		{name: "a loopback bind that names a proxy", addr: "127.0.0.1:8080", charge: proxied()},
		{
			name:   "a socket whose peers are trusted proxies",
			addr:   "/run/libgen-mcp.sock",
			charge: chargePolicy{header: "X-Real-IP", proxies: mustProxies(unixPeerEntry)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, err := resolveRateLimit(tc.addr, tc.charge, 10, 40, false)
			if err != nil {
				t.Fatalf("startup was refused: %v", err)
			}
			if limit.off {
				t.Fatalf("the limit is off (%s), but this listener can tell two callers apart", limit.reason)
			}
			if limit.rps != 10 || limit.burst != 40 {
				t.Errorf("limit = %g rps burst %d, want what was configured", limit.rps, limit.burst)
			}
		})
	}
}

// TestResolveRateLimitTurnsItOffWhereAnAddressMeansNothing covers the listener
// every peer of which is this machine.
//
// There a per-caller bucket is one budget for the whole deployment, which is
// worse than no limit: legitimate users refuse each other, and no limit is at
// least honest about what it does. Off is the honest state for an operator who
// never asked for one.
func TestResolveRateLimitTurnsItOffWhereAnAddressMeansNothing(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080", "/run/libgen-mcp.sock"} {
		t.Run(addr, func(t *testing.T) {
			limit, err := resolveRateLimit(addr, chargePolicy{}, 10, 40, false)
			if err != nil {
				t.Fatalf("startup was refused although the flag was not passed: %v", err)
			}
			if !limit.off {
				t.Fatal("the limit is on, and every caller of this listener is charged to one address")
			}
			if !strings.Contains(limit.describe(), "off") {
				t.Errorf("describe() = %q, want it to say the limit is off", limit.describe())
			}
			if limit.newLimiter() != nil {
				t.Error("a bucket was minted although the limit is off")
			}
		})
	}
}

// TestResolveRateLimitRefusesAnExplicitFlagItCannotHonor is the difference
// between a default and a request.
//
// Off by default is honest for somebody who never asked. Silently off for
// somebody who passed --rate-limit-rps is a deployment that believes it is
// bounded and is not — so startup says what is wrong and names the two flags
// that fix it.
func TestResolveRateLimitRefusesAnExplicitFlagItCannotHonor(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "/run/libgen-mcp.sock"} {
		t.Run(addr, func(t *testing.T) {
			_, err := resolveRateLimit(addr, chargePolicy{}, 10, 40, true)
			if err == nil {
				t.Fatal("an explicit --rate-limit-rps was accepted on a listener where it bounds nothing")
			}
			for _, want := range []string{"--rate-limit-rps", "--trusted-proxy-header", "--trusted-proxies"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %s: %v", want, err)
				}
			}
		})
	}
}

// TestResolveRateLimitAcceptsAnExplicitZero keeps "turn it off" from being
// refused as "turn it on where it means nothing".
func TestResolveRateLimitAcceptsAnExplicitZero(t *testing.T) {
	limit, err := resolveRateLimit("127.0.0.1:8080", chargePolicy{}, 0, 40, true)
	if err != nil {
		t.Fatalf("--rate-limit-rps=0 was refused: %v", err)
	}
	if !limit.off {
		t.Error("a zero rate left the limit on")
	}
}

// TestMainWithExitRefusesARateLimitItCannotHonor drives the refusal through the
// flags, which is where an operator meets it.
func TestMainWithExitRefusesARateLimitItCannotHonor(t *testing.T) {
	var code int
	awaitReturn(t, func() {
		code = callMainWithExit(t, "libgen-mcp", "--http", "127.0.0.1:0", "--rate-limit-rps", "10")
	})
	if code != 1 {
		t.Fatalf("mainWithExit(--http 127.0.0.1:0 --rate-limit-rps 10) = %d, want 1", code)
	}
}

// TestTheWarningFiresOnceForAnAddressNoPublicClientCouldHave is the safety net
// for the case no startup rule can decide.
//
// The address that proves it is not always loopback: the hosted deployment binds
// 0.0.0.0 inside a container published on the host's loopback, so what it sees
// is the Docker bridge gateway — RFC 1918. A condition written for 127.0.0.1
// alone would never fire on the very shape it was written for, which is why it
// reuses netguard's classification instead of listing addresses again.
func TestTheWarningFiresOnceForAnAddressNoPublicClientCouldHave(t *testing.T) {
	cases := []struct {
		name    string
		address string
		charge  chargePolicy
		want    int
	}{
		{name: "loopback", address: "127.0.0.1", want: 1},
		{name: "the Docker bridge gateway", address: "172.19.0.1", want: 1},
		{name: "another RFC 1918 address", address: "10.0.0.9", want: 1},
		{name: "carrier-grade NAT", address: "100.64.0.1", want: 1},
		{name: "a routable address", address: "203.0.113.7", want: 0},
		{name: "a unix socket peer", address: "@", want: 0},
		{name: "loopback with the proxy named", address: "127.0.0.1", charge: proxied(), want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := captureCallerWarnings(t)
			var warning callerWarning
			// Twice, because once per request would be a flood: the condition
			// holds for every request a misconfigured deployment serves.
			warning.warn(tc.address, tc.charge)
			warning.warn(tc.address, tc.charge)

			if got := lines(); got != tc.want {
				t.Errorf("%d warnings for %s, want %d", got, tc.address, tc.want)
			}
		})
	}
}

// captureCallerWarnings counts the caller-identity warning while the test runs.
func captureCallerWarnings(t *testing.T) func() int {
	t.Helper()

	counter := &warnCounter{}
	previous := slog.Default()
	slog.SetDefault(slog.New(counter))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return counter.count
}

// warnCounter is a slog.Handler that counts one specific message.
type warnCounter struct {
	mu sync.Mutex
	n  int
}

func (c *warnCounter) Enabled(context.Context, slog.Level) bool { return true }

func (c *warnCounter) Handle(_ context.Context, record slog.Record) error {
	if strings.Contains(record.Message, "callers cannot be told apart") {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
	return nil
}

func (c *warnCounter) WithAttrs([]slog.Attr) slog.Handler { return c }

func (c *warnCounter) WithGroup(string) slog.Handler { return c }

func (c *warnCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
