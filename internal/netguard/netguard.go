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
	"log/slog"
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
// netip's IsPrivate does not cover it — it implements RFC 1918 and RFC 4193 only,
// and its own documentation warns it "does not describe a security property of
// addresses, and should not be used for access control" — but a host inside such
// a network is no more reachable from the public internet than an RFC 1918 one.
//
// The literal is the control rather than a risk, which is why the trailing
// NOSONAR is there: S1313 asks whether a hardcoded address is safe to rely on,
// and here relying on it is the point. This is a destination the server refuses
// to reach, never one it dials. There is no library form to use instead — the
// standard library carries this prefix in a test file and nowhere else — and
// making it configurable would let the thing being defended against switch the
// defense off. Suppressed at the line rather than for the file, so a genuinely
// hardcoded destination added here later is still reported.
var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10") // NOSONAR

// sensitiveHeaders are stripped when a redirect leaves the origin. net/http
// already drops them when the redirect leaves the DOMAIN, but it keeps them for
// any subdomain of the original — so a redirect from example.com to
// internal.example.com still carries the Authorization header. Any change of
// scheme, host or port is treated as reason enough here.
//
// Referer is on the list for a reason the other five do not share: it is not a
// credential header, and net/http sets it itself on every redirect it follows,
// carrying the previous request's full URL. Two of this server's outbound URLs
// put a secret in that URL's query string — Anna's Archive's member key and
// Unpaywall's contact address — and a resolved file URL on the member path is a
// presigned URL, which is a working credential in its own right. So a redirect
// off-origin would hand the next host the previous one's query string, and this
// is the only place that can be stopped: net/http has already decided to send
// it by the time any of our code sees the request.
var sensitiveHeaders = []string{
	"Authorization", "Proxy-Authorization", "Cookie", "Cookie2", "Www-Authenticate", "Referer",
}

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

// metadataAddresses are the link-local and shared-space addresses cloud
// providers answer instance credentials on. Reaching one of these on somebody
// else's behalf hands out the credentials of the machine this server runs on,
// which is a different and worse thing than reaching the operator's LAN.
//
// They are named one by one rather than derived from their enclosing ranges
// because this rule holds for every client and every hop, including a
// deployment that has deliberately opened private space, and a rule that broad
// must be as narrow as it can be. Nothing legitimate serves a book, an article
// or a presigned download URL from one of these, so the false-positive cost is
// as close to zero as a guard of this kind gets.
var metadataAddresses = map[netip.Addr]string{
	netip.MustParseAddr("169.254.169.254"): "the cloud instance metadata address",
	netip.MustParseAddr("169.254.170.2"):   "the AWS container credentials address",
	netip.MustParseAddr("fd00:ec2::254"):   "the AWS instance metadata address over IPv6",
	netip.MustParseAddr("100.100.100.200"): "the Alibaba Cloud instance metadata address",
}

// addressLiteral parses host as an IP address, reporting whether it was spelled
// as one at all. A host that is a name is nobody's decision to make before the
// dialer: what it resolves to is the only thing worth judging.
func addressLiteral(host string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(host)
	return addr, err == nil
}

// metadataEndpoint returns what an address is the metadata endpoint of, and
// whether it is one at all.
//
// The address is unmapped first for the same reason [Blocked] does it: an IPv4
// address wearing an IPv6 coat (::ffff:169.254.169.254) is judged as the IPv4
// address it is, which is the standard way past a filter that only reasons
// about one family.
func metadataEndpoint(addr netip.Addr) (string, bool) {
	what, ok := metadataAddresses[addr.Unmap()]
	return what, ok
}

// control returns the dialer Control hook that enforces the address policy.
//
// There are two tiers and only one of them answers to allowPrivate. The
// metadata endpoints above are refused whatever the configuration says: the
// escape hatch exists so an operator can point this server at a mirror on their
// own network, and nothing about that intent extends to letting a URL deposited
// in an open-access index fetch the host's cloud credentials. Everything
// [Blocked] covers is the second tier, and that is what the flag opens.
//
// The hook is therefore installed unconditionally, where it used to be nil when
// the flag was set. One consequence is deliberate: a destination that cannot be
// parsed is now refused under the flag too, where before it was dialed. Control
// is always handed a resolved IP literal, so that path is defensive rather than
// reachable, and refusing is what the rest of this package already does with an
// address it cannot judge.
func control(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
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
		if what, ok := metadataEndpoint(addr); ok {
			return fmt.Errorf("%w: %s is %s", ErrBlockedAddress, addr, what)
		}
		if allowPrivate {
			return nil
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
// different origin.
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
		if err := redirectAddressAllowed(req, allowPrivate); err != nil {
			return err
		}
		if len(via) > 0 && !sameOrigin(via[len(via)-1].URL, req.URL) {
			stripSensitiveHeaders(req, via[len(via)-1].URL)
		}
		return nil
	}
}

// redirectAddressAllowed applies the dialer's two tiers to a hop whose target is
// spelled as an address literal, which is the only case this check can decide on
// its own: a hop named by hostname is judged by the dialer, on what it resolves
// to.
//
// The tiers are the dialer's, deliberately: a metadata endpoint is refused
// whatever the flag says, and everything else is refused only when private
// destinations are not permitted. A redirect is the cheaper half of the attack,
// since a URL deposited in an index need only bounce once, so the two halves of
// one client must not disagree about what is reachable.
func redirectAddressAllowed(req *http.Request, allowPrivate bool) error {
	addr, spelledAsAddress := addressLiteral(req.URL.Hostname())
	if !spelledAsAddress {
		return nil
	}
	if what, ok := metadataEndpoint(addr); ok {
		return fmt.Errorf("%w: redirect to %s, %s", ErrBlockedAddress, addr, what)
	}
	if !allowPrivate && Blocked(addr) {
		return fmt.Errorf("%w: redirect to %s", ErrBlockedAddress, req.URL.Redacted())
	}
	return nil
}

// stripSensitiveHeaders removes the headers that must not follow a redirect off
// the origin they were set for, and logs which ones were actually dropped.
func stripSensitiveHeaders(req *http.Request, previous *url.URL) {
	dropped := make([]string, 0, len(sensitiveHeaders))
	for _, h := range sensitiveHeaders {
		if req.Header.Get(h) != "" {
			dropped = append(dropped, h)
		}
		req.Header.Del(h)
	}
	if len(dropped) == 0 {
		return
	}
	// Scheme and host only, on both sides. Naming either URL would undo the
	// strip in the log: the previous one is what Referer carries, and it is the
	// URL with the secret in its query string.
	slog.Info("dropped request headers on an off-origin redirect",
		"headers", dropped,
		"from", previous.Scheme+"://"+previous.Hostname(),
		"to", req.URL.Scheme+"://"+req.URL.Hostname())
}

// sameOrigin reports whether two URLs address the same service, comparing scheme,
// host AND port. All three matter for a credential:
//
//   - a subdomain is a different host, because "is a subdomain of" is not "is
//     trusted by";
//   - a different port on the same host is a different service, and there is no
//     reason the one on :8080 should receive the key meant for the one on :443;
//   - a change of scheme is the worst of the three, because https → http puts the
//     credential on the wire in cleartext.
//
// Ports are compared after defaulting, so https://example.org and
// https://example.org:443 are correctly one origin rather than two.
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Scheme == b.Scheme && a.Hostname() == b.Hostname() && effectivePort(a) == effectivePort(b)
}

// effectivePort returns the URL's port, filling in the scheme's default when it is
// left implicit so an explicit :443 and an omitted one compare equal.
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

// Client builds an http.Client with the guarded Transport and redirect policy.
// A non-positive timeout leaves the client without one, for the streaming
// download path whose lifetime is governed by its context instead.
//
// allowPrivate opens private destinations. It is the escape hatch for the one
// legitimate case — an operator who points the server at a mirror on their own
// network — and for the test suites, which serve every fixture from loopback. It
// must never be set from anything but explicit configuration.
//
// It does not open everything. The cloud metadata endpoints are refused under it
// too: the hatch exists so this server can reach a machine the operator owns,
// and nothing in that intent covers handing out the credentials of the machine
// it is running on.
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
//
// It lifts the private-address tier only. A fixture cannot be served from a
// cloud metadata endpoint, so there is nothing for it to lift there, and a test
// seam that could switch off the tier that holds unconditionally would be a way
// to reach production with it off.
func SetAllowPrivateForTest(allow bool) (restore func()) {
	previous := privateAllowedForTest.Swap(allow)
	return func() { privateAllowedForTest.Store(previous) }
}
