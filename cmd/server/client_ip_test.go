package main

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// realIP is the header the hosted deployment's proxy already fills, and the one
// the docs name. The tests use it rather than X-Forwarded-For wherever the case
// is not specifically about multiple hops.
const realIP = "X-Real-IP"

// requestFrom builds a request that arrived from peer, with the given headers
// set. peer is written as the transport writes it: host:port for TCP, and the
// bare peer name a unix socket reports.
func requestFrom(t *testing.T, peer string, headers map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	r.RemoteAddr = peer
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	return r
}

// mustParseProxies parses a --trusted-proxies value the way mainWithExit does.
func mustParseProxies(t *testing.T, value string) trustedProxies {
	t.Helper()
	proxies, err := parseTrustedProxies(commaSeparated(value))
	if err != nil {
		t.Fatalf("parseTrustedProxies(%q) failed: %v", value, err)
	}
	return proxies
}

// TestClientIPChargesTheForwardedAddressFromATrustedProxy is the deployed shape
// and the case that must never regress: one proxy, one single-valued header,
// one /32.
//
// The peer here is whatever the listener accepts the connection from, which on
// the hosted endpoint is the Docker bridge gateway rather than the address
// nginx's upstream names. Nothing in the code cares which it is — the flag takes
// whatever the operator supplies — so the test uses its own topology's address
// rather than restating the deployment's.
func TestClientIPChargesTheForwardedAddressFromATrustedProxy(t *testing.T) {
	proxies := mustParseProxies(t, "127.0.0.1/32")
	r := requestFrom(t, "127.0.0.1:52144", map[string]string{realIP: "203.0.113.7"})

	if got := clientIP(r, realIP, proxies); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want the forwarded address 203.0.113.7", got)
	}
}

// TestClientIPIgnoresTheHeaderFromAPeerOutsideTheTrustedRange is what a single
// /32 buys and what a wider range would give away.
//
// The caller here sets the very header the operator named. Believed, it would
// let anyone reaching the listener directly choose which budget their traffic
// counts against — somebody else's, or a fresh one per request.
func TestClientIPIgnoresTheHeaderFromAPeerOutsideTheTrustedRange(t *testing.T) {
	proxies := mustParseProxies(t, "127.0.0.1/32")
	r := requestFrom(t, "198.51.100.23:41100", map[string]string{realIP: "203.0.113.7"})

	if got := clientIP(r, realIP, proxies); got != "198.51.100.23" {
		t.Errorf("clientIP = %q, want the peer 198.51.100.23; the header was believed from an untrusted peer", got)
	}
}

// TestClientIPIgnoresTheHeaderWithNoTrustedProxies covers the default shape,
// where the flags were not passed at all. Startup refuses a header without a
// list, so this is the state a direct deployment runs in.
func TestClientIPIgnoresTheHeaderWithNoTrustedProxies(t *testing.T) {
	r := requestFrom(t, "127.0.0.1:52144", map[string]string{realIP: "203.0.113.7"})

	for _, tc := range []struct {
		name    string
		header  string
		proxies trustedProxies
	}{
		{name: "no header named", header: "", proxies: mustParseProxies(t, "127.0.0.1/32")},
		{name: "nobody trusted", header: realIP, proxies: trustedProxies{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientIP(r, tc.header, tc.proxies); got != "127.0.0.1" {
				t.Errorf("clientIP = %q, want the peer 127.0.0.1", got)
			}
		})
	}
}

// TestClientIPWalksForwardedForFromTheRight is the pin for the walk's
// direction, and it is the assertion a leftmost-first implementation fails.
//
// Both subtests use the same header value and differ only in whether the inner
// proxy is trusted. Read leftmost, both would answer with the client's address
// and the second subtest would pass while charging a caller an address nobody
// vouched for; read rightmost, the answer tracks the list.
func TestClientIPWalksForwardedForFromTheRight(t *testing.T) {
	const (
		header      = "X-Forwarded-For"
		value       = "203.0.113.7, 10.0.0.9"
		peer        = "127.0.0.1:52144"
		client      = "203.0.113.7"
		innerProxy  = "10.0.0.9"
		outerProxy  = "127.0.0.1/32"
		bothProxies = outerProxy + ",10.0.0.9/32"
	)
	r := requestFrom(t, peer, map[string]string{header: value})

	t.Run("both hops trusted charges the client", func(t *testing.T) {
		if got := clientIP(r, header, mustParseProxies(t, bothProxies)); got != client {
			t.Errorf("clientIP = %q, want the client %s", got, client)
		}
	})

	t.Run("an untrusted inner hop is as far as the walk gets", func(t *testing.T) {
		if got := clientIP(r, header, mustParseProxies(t, outerProxy)); got != innerProxy {
			t.Errorf("clientIP = %q, want %s: nothing vouches for what it appended", got, innerProxy)
		}
	})
}

// TestClientIPChargesTheLeftmostHopWhenEveryHopIsTrusted covers the walk running
// off the end of the header: there is no untrusted hop to stop at, so the
// leftmost value is the closest thing to a client there is.
func TestClientIPChargesTheLeftmostHopWhenEveryHopIsTrusted(t *testing.T) {
	proxies := mustParseProxies(t, "127.0.0.1/32,10.0.0.0/8")
	r := requestFrom(t, "127.0.0.1:52144", map[string]string{"X-Forwarded-For": "10.1.2.3, 10.0.0.9"})

	if got := clientIP(r, "X-Forwarded-For", proxies); got != "10.1.2.3" {
		t.Errorf("clientIP = %q, want the leftmost hop 10.1.2.3", got)
	}
}

// TestClientIPChargesThePeerWhenAHopIsNotAnAddress covers a proxy sending
// something other than what the flag promised.
//
// The request is charged to the peer rather than to text nobody vouched for:
// skipping the bad hop and walking on would let a caller who can reach the inner
// proxy insert a value that decides where the walk stops.
func TestClientIPChargesThePeerWhenAHopIsNotAnAddress(t *testing.T) {
	proxies := mustParseProxies(t, "127.0.0.1/32")

	for _, value := range []string{"not-an-address", "203.0.113.7, unknown", "_hidden", ""} {
		r := requestFrom(t, "127.0.0.1:52144", map[string]string{realIP: value})
		if got := clientIP(r, realIP, proxies); got != "127.0.0.1" {
			t.Errorf("clientIP with %q = %q, want the peer 127.0.0.1", value, got)
		}
	}
}

// TestClientIPReadsAHopWrittenWithAPort covers the proxies that write one, and
// the IPv6 spellings that come with it.
func TestClientIPReadsAHopWrittenWithAPort(t *testing.T) {
	proxies := mustParseProxies(t, "127.0.0.1/32")

	for _, tc := range []struct{ value, want string }{
		{value: "203.0.113.7:4444", want: "203.0.113.7"},
		{value: "[2001:db8::1]:4444", want: "2001:db8::1"},
		{value: "[2001:db8::1]", want: "2001:db8::1"},
		{value: "::ffff:203.0.113.7", want: "203.0.113.7"},
	} {
		r := requestFrom(t, "127.0.0.1:52144", map[string]string{realIP: tc.value})
		if got := clientIP(r, realIP, proxies); got != tc.want {
			t.Errorf("clientIP with %q = %q, want %q", tc.value, got, tc.want)
		}
	}
}

// TestClientIPComparesAMappedPeerAsIPv4 covers the dual-stack listener, which
// reports an IPv4 peer as ::ffff:127.0.0.1. Compared as written it matches no
// IPv4 range, and the header of a proxy the operator did list would be ignored.
func TestClientIPComparesAMappedPeerAsIPv4(t *testing.T) {
	proxies := mustParseProxies(t, "127.0.0.1/32")
	r := requestFrom(t, "[::ffff:127.0.0.1]:52144", map[string]string{realIP: "203.0.113.7"})

	if got := clientIP(r, realIP, proxies); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want 203.0.113.7: a mapped peer was not recognized as the listed IPv4 proxy", got)
	}
}

// TestClientIPOnAUnixSocketNeedsTheUnixEntry is the socket rule, in both
// directions.
//
// A unix peer is a path rather than an address, so no range can ever name it.
// Without the literal the header is caller-supplied text and every caller shares
// the one key the socket reports; with it the operator has stated that the
// socket's peers — which 0660 already limits to its owner and group — may speak
// for their callers.
func TestClientIPOnAUnixSocketNeedsTheUnixEntry(t *testing.T) {
	r := requestFrom(t, "@", map[string]string{realIP: "203.0.113.7"})

	t.Run("without it every caller is one key", func(t *testing.T) {
		// A header is named, so this is not the "no flags" path: the peer is
		// simply nobody the operator vouched for.
		if got := clientIP(r, realIP, mustParseProxies(t, "127.0.0.1/32")); got != "@" {
			t.Errorf("clientIP = %q, want the socket peer %q", got, "@")
		}
	})

	t.Run("with it the header is believed", func(t *testing.T) {
		if got := clientIP(r, realIP, mustParseProxies(t, unixPeerEntry)); got != "203.0.113.7" {
			t.Errorf("clientIP = %q, want the forwarded address 203.0.113.7", got)
		}
	})
}

// TestUnixEntryAloneIsNotAnEmptyTrustList pins the one thing the literal must
// not break: empty() decides whether the header is read at all, so a list of
// just "unix" reading as empty would make the socket rule a no-op — and the
// subtest above would still pass for the wrong reason.
func TestUnixEntryAloneIsNotAnEmptyTrustList(t *testing.T) {
	if mustParseProxies(t, unixPeerEntry).empty() {
		t.Fatal("a list of just \"unix\" reads as trusting nobody, so the header is never read on a socket")
	}
	if !(trustedProxies{}).empty() {
		t.Error("an unset list does not read as trusting nobody")
	}
}

// TestParseTrustedProxiesReadsEverySpelling covers what the flag accepts, and
// what it refuses rather than ignores.
func TestParseTrustedProxiesReadsEverySpelling(t *testing.T) {
	t.Run("addresses, ranges and the literal", func(t *testing.T) {
		proxies, err := parseTrustedProxies(commaSeparated(" 127.0.0.1 , 10.0.0.0/8 ,, unix , 2001:db8::1 "))
		if err != nil {
			t.Fatalf("parseTrustedProxies failed: %v", err)
		}
		if !proxies.unixPeers {
			t.Error("the unix literal was not read")
		}
		for _, tc := range []struct {
			addr string
			want bool
		}{
			{addr: "127.0.0.1", want: true},
			{addr: "127.0.0.2", want: false}, // a bare address is that address only
			{addr: "10.4.5.6", want: true},
			{addr: "11.4.5.6", want: false},
			{addr: "2001:db8::1", want: true},
			{addr: "2001:db8::2", want: false},
		} {
			if got := containsAddr(t, proxies, tc.addr); got != tc.want {
				t.Errorf("contains(%s) = %v, want %v", tc.addr, got, tc.want)
			}
		}
	})

	t.Run("an entry that is neither is refused", func(t *testing.T) {
		for _, entry := range []string{"localhost", "10.0.0.0/64", "unixx", "127.0.0.1:8080"} {
			if _, err := parseTrustedProxies([]string{entry}); err == nil {
				t.Errorf("parseTrustedProxies(%q) was accepted", entry)
			}
		}
	})
}

// containsAddr is contains, taking the address as it would be written.
func containsAddr(t *testing.T, proxies trustedProxies, addr string) bool {
	t.Helper()
	parsed, err := netip.ParseAddr(addr)
	if err != nil {
		t.Fatalf("the test's own address %q does not parse: %v", addr, err)
	}
	return proxies.contains(parsed)
}

// TestValidateTrustedProxyConfigHoldsTheFlagsToEachOther is the startup rule.
//
// Each half is a configuration that reads as working and is not: a header
// nobody may set is never read, and a header anybody may set is the key of every
// per-caller budget handed to the caller.
func TestValidateTrustedProxyConfigHoldsTheFlagsToEachOther(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		header  string
		addr    string
		wantErr string
	}{
		{
			name:    "a header nobody is trusted to set",
			header:  realIP,
			addr:    "0.0.0.0:8080",
			wantErr: "--trusted-proxies",
		},
		{
			name:    "a list whose header is never read",
			entries: []string{"127.0.0.1/32"},
			addr:    "0.0.0.0:8080",
			wantErr: "--trusted-proxy-header",
		},
		{
			name:    "a header that is only whitespace counts as absent",
			entries: []string{"127.0.0.1/32"},
			header:  "   ",
			addr:    "0.0.0.0:8080",
			wantErr: "--trusted-proxy-header",
		},
		{
			name:    "an entry that is not an address",
			entries: []string{"localhost"},
			header:  realIP,
			addr:    "0.0.0.0:8080",
			wantErr: "--trusted-proxies",
		},
		{
			name:    "the unix literal on a TCP listener",
			entries: []string{unixPeerEntry},
			header:  realIP,
			addr:    "0.0.0.0:8080",
			wantErr: "unix-socket listener",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTrustedProxyConfig(tc.entries, tc.header, tc.addr)
			if err == nil {
				t.Fatalf("validateTrustedProxyConfig(%q, %q, %q) was accepted", tc.entries, tc.header, tc.addr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not name %q, so it does not say which flag to change", err, tc.wantErr)
			}
		})
	}
}

// TestValidateTrustedProxyConfigAcceptsTheShapesThatWork is the negative half:
// the checks above must not refuse a deployment that is configured correctly,
// including the default one that passes neither flag.
func TestValidateTrustedProxyConfigAcceptsTheShapesThatWork(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		header  string
		addr    string
	}{
		{name: "neither flag, the default", addr: "0.0.0.0:8080"},
		{name: "neither flag on stdio"},
		{name: "a proxy on a TCP listener", entries: []string{"127.0.0.1/32"}, header: realIP, addr: "0.0.0.0:8080"},
		{name: "the unix literal on a socket", entries: []string{unixPeerEntry}, header: realIP, addr: "/run/libgen-mcp.sock"},
		{name: "the unix literal beside a range", entries: []string{unixPeerEntry, "127.0.0.1/32"}, header: realIP, addr: "/run/libgen-mcp.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateTrustedProxyConfig(tc.entries, tc.header, tc.addr); err != nil {
				t.Errorf("validateTrustedProxyConfig(%q, %q, %q) = %v, want accepted", tc.entries, tc.header, tc.addr, err)
			}
		})
	}
}

// TestMainWithExitRefusesAnUnusableProxyConfig drives the refusals through the
// flags, which is where an operator meets them.
//
// The unit test above calls the check directly and would keep passing if nothing
// ever called it; this is the assertion that the wiring exists.
func TestMainWithExitRefusesAnUnusableProxyConfig(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "a header nobody is trusted to set", args: []string{"--http", "127.0.0.1:0", "--trusted-proxy-header", realIP}},
		{name: "a list whose header is never read", args: []string{"--http", "127.0.0.1:0", "--trusted-proxies", "127.0.0.1/32"}},
		{name: "an entry that is not an address", args: []string{"--http", "127.0.0.1:0", "--trusted-proxy-header", realIP, "--trusted-proxies", "localhost"}},
		{name: "the unix literal on a TCP listener", args: []string{"--http", "127.0.0.1:0", "--trusted-proxy-header", realIP, "--trusted-proxies", unixPeerEntry}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			awaitReturn(t, func() {
				code = callMainWithExit(t, append([]string{"libgen-mcp"}, tc.args...)...)
			})
			if code != 1 {
				t.Fatalf("mainWithExit(%q) = %d, want 1", tc.args, code)
			}
		})
	}
}

// TestCommaSeparatedDropsEmptyEntries pins what "the operator did not pass this
// flag" looks like to the checks: an empty value must yield no entries, or a
// bare --trusted-proxies= would read as naming a proxy and refuse to start.
func TestCommaSeparatedDropsEmptyEntries(t *testing.T) {
	for _, value := range []string{"", "   ", ",", " , , "} {
		if got := commaSeparated(value); len(got) != 0 {
			t.Errorf("commaSeparated(%q) = %q, want no entries", value, got)
		}
	}
	if got := commaSeparated(" a , b "); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("commaSeparated = %q, want [a b]", got)
	}
}
