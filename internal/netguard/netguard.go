// Package netguard builds HTTP clients that refuse to connect to addresses only
// this machine can reach.
//
// Every download source and every discovery provider fetches a URL it was handed
// by a third party: a publisher's link deposited with Crossref, a repository's
// download URL republished by Unpaywall, OpenAlex or CORE, a citation_pdf_url
// scraped off a publisher page. Nothing about that pipeline stops one of those
// URLs from naming the loopback interface, the operator's LAN, or a cloud
// instance-metadata endpoint — and if it does, the server becomes a proxy into a
// network the depositor could never reach directly. That is server-side request
// forgery (CWE-918), and this package is where it is stopped.
//
// The check runs in the dialer's Control hook rather than on the URL, because
// only the dialer sees what was actually resolved. A URL check is defeated by a
// public hostname with a private A record (localtest.me resolves to 127.0.0.1)
// and by DNS rebinding, where the name resolves differently between validation
// and connection; the Control hook is handed the concrete IP microseconds before
// connect, so neither is possible. Installing it on the shared Transport also
// means every source, probe, mirror lookup and redirect hop inherits it at once,
// rather than each caller having to remember.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned when a connection is refused because its
// destination is an address reachable only from inside this host or its network.
// It is exported so a caller can tell this refusal apart from an ordinary dial
// failure with errors.Is.
var ErrBlockedAddress = errors.New("refusing to connect to a private or local address")

// ErrTooManyRedirects is returned when a response chain exceeds maxRedirects.
var ErrTooManyRedirects = errors.New("too many redirects")

// maxRedirects caps how many hops a redirect chain may take. Go's own default is
// 10; the lower bound here is deliberate, since a legitimate publisher link
// reaches its file in one or two hops and a long chain is either a loop or an
// attempt to walk somewhere.
const maxRedirects = 5

// cgnatPrefix is the RFC 6598 shared address space used by carrier-grade NAT.
// netip's IsPrivate does not cover it, but a host inside such a network is no
// more reachable from the public internet than an RFC 1918 one.
var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// sensitiveHeaders are stripped when a redirect changes host. net/http already
// drops them when the redirect leaves the DOMAIN, but it keeps them for any
// subdomain of the original — so a redirect from example.com to
// internal.example.com still carries the Authorization header. Any host change
// is treated as reason enough here.
var sensitiveHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Cookie2", "Www-Authenticate"}

// Blocked reports whether addr is one this server must not be talked into
// reaching on someone else's behalf.
//
// The address is unmapped first, so an IPv4 address wearing an IPv6 coat
// (::ffff:169.254.169.254) is judged as the IPv4 address it is — the standard way
// past a filter that only reasons about one family.
//
// Blocked covers: loopback, the unspecified address and the whole 0.0.0.0/8
// "this network" block, IPv4 broadcast, link-local unicast and multicast
// (169.254.0.0/16, which contains the cloud instance-metadata address
// 169.254.169.254, and fe80::/10), every other multicast scope, RFC 1918 private
// space and RFC 4193 IPv6 unique-local addresses, and RFC 6598 carrier-grade NAT
// space. An address that fails to parse is blocked too: an unparseable
// destination is not one to take a chance on.
func Blocked(addr netip.Addr) bool {
	a := addr.Unmap()
	if !a.IsValid() {
		return true
	}
	switch {
	case a.IsLoopback(), a.IsUnspecified(),
		a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(),
		a.IsInterfaceLocalMulticast(), a.IsMulticast(),
		a.IsPrivate():
		return true
	}
	if a.Is4() {
		v4 := a.As4()
		// 0.0.0.0/8 ("this network") and the 255.255.255.255 broadcast address.
		if v4[0] == 0 || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			return true
		}
	}
	return cgnatPrefix.Contains(a)
}

// control returns the dialer Control hook that enforces Blocked, or nil when
// private destinations are permitted (see Client).
func control(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	if allowPrivate {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: unparseable destination %q", ErrBlockedAddress, address)
		}
		// Control is always handed a resolved IP literal, never a name, which is
		// exactly why the check belongs here.
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("%w: unparseable destination %q", ErrBlockedAddress, host)
		}
		if Blocked(addr) {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, addr)
		}
		return nil
	}
}

// Transport builds an http.Transport whose connections are screened by the
// address policy. It starts from a clone of http.DefaultTransport so the
// connection pooling, proxy support, HTTP/2 upgrade and timeouts the standard
// library tunes are kept, and replaces only the dialer.
func Transport(allowPrivate bool) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	t := base.Clone()
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   control(allowPrivate),
	}
	t.DialContext = dialer.DialContext
	return t
}

// CheckRedirect returns the http.Client redirect policy that complements the
// dialer: it bounds the chain, refuses a hop whose target is a literal private
// address, and strips credentials when the host changes.
//
// The address check is deliberately belt-and-braces — a redirect to a private
// host is dialed through the same guarded Transport, so the Control hook would
// refuse it anyway. What this adds is a legible error naming the redirect instead
// of an opaque dial failure, and the one protection the dialer genuinely cannot
// give: removing the Authorization header before it follows a redirect to a
// different host.
//
// It takes allowPrivate for the same reason the dialer does, and getting that
// wrong is not theoretical: while a package-level policy, this refused redirects
// into private space even for a client explicitly built to permit them, so an
// operator pointing LIBGEN_MIRROR at a mirror on their own network would have had
// every redirect that mirror issues rejected. The hop cap and the credential
// strip are not address policy and apply either way.
func CheckRedirect(allowPrivate bool) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("%w: stopped after %d", ErrTooManyRedirects, len(via))
		}
		if !allowPrivate {
			if addr, err := netip.ParseAddr(req.URL.Hostname()); err == nil && Blocked(addr) {
				return fmt.Errorf("%w: redirect to %s", ErrBlockedAddress, req.URL.Redacted())
			}
		}
		if len(via) > 0 && !sameHost(via[len(via)-1].URL, req.URL) {
			for _, h := range sensitiveHeaders {
				req.Header.Del(h)
			}
		}
		return nil
	}
}

// sameHost reports whether two URLs address the same host, compared exactly:
// a subdomain is a different host, because "is a subdomain of" is not "is
// trusted by".
func sameHost(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Hostname() == b.Hostname()
}

// Client builds an http.Client with the guarded Transport and redirect policy.
// A non-positive timeout leaves the client without one, for the streaming
// download path whose lifetime is governed by its context instead.
//
// allowPrivate disables the address policy entirely. It is the escape hatch for
// the one legitimate case — an operator who points the server at a mirror on
// their own network — and for the test suites, which serve every fixture from
// loopback. It must never be set from anything but explicit configuration.
func Client(timeout time.Duration, allowPrivate bool) *http.Client {
	// One decision, applied to both halves of the policy: the dialer and the
	// redirect check must never disagree about what this client may reach.
	allow := allowPrivate || privateAllowedForTest.Load()
	c := &http.Client{
		Transport:     Transport(allow),
		CheckRedirect: CheckRedirect(allow),
	}
	if timeout > 0 {
		c.Timeout = timeout
	}
	return c
}

// privateAllowedForTest lifts the address policy for every client built
// afterwards. It is package state so a whole test binary can be switched once,
// rather than every construction site having to remember.
var privateAllowedForTest atomic.Bool

// SetAllowPrivateForTest permits private destinations for clients built after the
// call, and returns a function restoring the previous setting.
//
// It exists because the unit suites serve every fixture from an httptest server,
// which listens on loopback — the exact address family this package refuses. With
// the policy in force, those suites would spend their retry schedules failing to
// dial their own fixtures. Rather than thread an allowance through the fifty-odd
// places a test builds a Config, each affected package flips it once in TestMain,
// so a test added later inherits the setting instead of having to know about it.
//
// The consequence is that the policy is not exercised by those suites, so it is
// tested here instead, against real dials: see TestClientRefusesLoopbackServer.
// NEVER call this from production code.
func SetAllowPrivateForTest(allow bool) (restore func()) {
	previous := privateAllowedForTest.Swap(allow)
	return func() { privateAllowedForTest.Store(previous) }
}
