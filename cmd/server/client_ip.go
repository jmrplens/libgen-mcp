// client_ip.go decides which address a request is charged to.
//
// Nothing in this server tells two HTTP callers apart today: a grep for
// RemoteAddr or any forwarded-address header finds nothing at all. Every
// per-caller thing that follows — a rate bucket, a per-caller ceiling, a
// pseudonym in a telemetry attribute — needs one answer to "who is this", and
// this is it, so there is one rule rather than one per feature.
//
// Behind a reverse proxy every connection arrives from the proxy, so
// --trusted-proxy-header names the header the proxy fills with the address it
// heard from. A header is believed only from a peer the operator listed in
// --trusted-proxies: read from any other peer it is caller-supplied text, and a
// caller who can choose the address their traffic is charged to can choose
// somebody else's, or a fresh one per request. The two flags are therefore
// required together, and startup refuses either one alone.

package main

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// unixPeerEntry is the one --trusted-proxies spelling that is not an address.
//
// On a unix-socket listener the peer is a path rather than an address, so no
// range can ever name it and the header could never be believed there. This
// says "every peer of this socket is a trusted proxy", which is defensible
// because the socket is created 0660: its peers are already the owner and the
// group the operator put the proxy in.
//
// It is explicit rather than implicit so the trust is stated rather than
// inherited — and so a socket deployment without it is honestly one key for
// every caller rather than silently believing whatever arrives.
const unixPeerEntry = "unix"

// trustedProxies is the set of peers whose client-address header is believed.
type trustedProxies struct {
	prefixes []netip.Prefix
	// unixPeers trusts every peer of a unix-socket listener; see unixPeerEntry.
	unixPeers bool
}

// parseTrustedProxies reads addresses, CIDR ranges and the literal "unix",
// comma-separated, with whitespace and empty entries ignored. A plain address
// becomes the range that holds only it.
func parseTrustedProxies(entries []string) (trustedProxies, error) {
	var t trustedProxies
	for _, entry := range entries {
		switch entry = strings.TrimSpace(entry); entry {
		case "":
			continue
		case unixPeerEntry:
			t.unixPeers = true
			continue
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			t.prefixes = append(t.prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return trustedProxies{}, fmt.Errorf("%q is neither an address, a CIDR range, nor %q", entry, unixPeerEntry)
		}
		addr = addr.Unmap()
		t.prefixes = append(t.prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return t, nil
}

// commaSeparated splits a comma-separated flag value into its non-empty
// entries, with surrounding whitespace removed. An empty or whitespace-only
// value yields no entries at all, which is what "the operator did not pass this
// flag" has to look like to the checks below.
func commaSeparated(value string) []string {
	var entries []string
	for part := range strings.SplitSeq(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			entries = append(entries, part)
		}
	}
	return entries
}

// empty reports whether nobody is trusted.
func (t trustedProxies) empty() bool { return len(t.prefixes) == 0 && !t.unixPeers }

// contains reports whether addr is one of the trusted proxies. IPv4 addresses
// that arrived mapped into IPv6, which a dual-stack listener produces, are
// compared as IPv4.
func (t trustedProxies) contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range t.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP returns the address a request is charged to.
//
// Without a trusted header, or from a peer that is not a trusted proxy, it is
// the peer itself: the header is then caller-supplied text and is ignored
// rather than believed. From a trusted proxy the header is read from the RIGHT,
// since every proxy appends the peer it heard from: the first hop that is not
// itself a trusted proxy is the client, and if every hop is trusted the leftmost
// one is. A hop that does not parse as an address is a proxy sending something
// other than what the flag promised, and the request is charged to the peer
// rather than to text nobody vouched for.
//
// Reading from the right is what makes a two-proxy deployment count per client.
// Read leftmost — the obvious way, and what most examples show — the value is
// whatever the first proxy was told, which is the caller's own text again.
//
// A single-valued header such as X-Real-IP needs no special case: the walk over
// one value returns that value. Nothing may be removed for it either, because
// the same proxy forwards X-Forwarded-For and an operator may name that instead.
func clientIP(r *http.Request, trustedHeader string, proxies trustedProxies) string {
	peer := remoteHost(r)
	if trustedHeader == "" || proxies.empty() {
		return peer
	}
	if !peerIsTrusted(peer, proxies) {
		return peer
	}
	value := r.Header.Get(trustedHeader)
	if value == "" {
		return peer
	}
	if forwarded, ok := rightmostUntrustedHop(value, proxies); ok {
		return forwarded
	}
	return peer
}

// peerIsTrusted reports whether the connection's peer may set the header.
//
// A peer that does not parse as an address is a unix-socket peer, and is
// trusted only when the operator said so with the "unix" entry.
func peerIsTrusted(peer string, proxies trustedProxies) bool {
	addr, err := netip.ParseAddr(peer)
	if err != nil {
		return proxies.unixPeers
	}
	return proxies.contains(addr)
}

// rightmostUntrustedHop walks a forwarded-address header from the right and
// returns the first hop that is not itself a trusted proxy, or the leftmost hop
// when every one of them is. The second result is false when the header carried
// nothing usable, which charges the request to the peer instead.
func rightmostUntrustedHop(value string, proxies trustedProxies) (string, bool) {
	candidate := ""
	for _, part := range slices.Backward(strings.Split(value, ",")) {
		hop := strings.TrimSpace(part)
		if hop == "" {
			continue
		}
		addr, ok := parseHop(hop)
		if !ok {
			return "", false
		}
		candidate = addr.String()
		if !proxies.contains(addr) {
			return candidate, true
		}
	}
	return candidate, candidate != ""
}

// parseHop reads one entry of a forwarded-address header: an address, or an
// address with a port as some proxies write them, IPv6 bracketed either way.
func parseHop(hop string) (netip.Addr, bool) {
	if addr, err := netip.ParseAddr(strings.Trim(hop, "[]")); err == nil {
		return addr.Unmap(), true
	}
	if addrPort, err := netip.ParseAddrPort(hop); err == nil {
		return addrPort.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// remoteHost is the connection's peer address without its port. A listener on a
// unix socket reports no address, so every caller shares one key there unless
// the operator trusts the socket's peers with the "unix" entry.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// validateTrustedProxyConfig holds the two proxy flags to each other, and the
// "unix" entry to the listener it can mean something on.
//
// A header without a list of who may set it is a header anybody may set, and
// with it the address a caller's traffic is charged to: that is the whole key of
// every per-caller budget, handed to the caller. A list without a header names
// proxies whose word is never asked for. Both are refused at startup rather than
// left to mean something the operator did not write.
func validateTrustedProxyConfig(entries []string, header, listenAddr string) error {
	header = strings.TrimSpace(header)
	if header != "" && len(entries) == 0 {
		return fmt.Errorf("--trusted-proxy-header %q names a header nobody is trusted to set: pass --trusted-proxies with the addresses or CIDR ranges of the proxies that reach this listener, or drop the header", header)
	}
	if header == "" && len(entries) > 0 {
		return fmt.Errorf("--trusted-proxies %q names proxies whose header is never read: pass --trusted-proxy-header with the header they set, or drop the list", strings.Join(entries, ","))
	}
	proxies, err := parseTrustedProxies(entries)
	if err != nil {
		return fmt.Errorf("--trusted-proxies: %w", err)
	}
	// "unix" on a TCP listener matches no peer that will ever connect, so it
	// would leave the operator believing the header is trusted when nothing
	// reads it. Refused rather than ignored, for the reason the pair above is.
	if proxies.unixPeers && listenAddr != "" && !isUnixSocketAddr(listenAddr) {
		return fmt.Errorf("--trusted-proxies %q is for a unix-socket listener, but --http %s is a TCP address: name the proxy's address or CIDR range instead",
			unixPeerEntry, listenAddr)
	}
	return nil
}
