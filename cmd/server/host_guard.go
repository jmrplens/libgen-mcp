// host_guard.go decides which Host header this deployment answers.
//
// The check exists against DNS rebinding: an attacker resolves a name they
// control to the address a server listens on, and a browser they have already
// loaded then reaches that server with the attacker's name in the Host header.
// Refusing a name the deployment never declared is what stops it.
//
// Every reverse proxy in the getting-started guide forwards the client's Host
// (nginx's `proxy_set_header Host $host`, Apache's `ProxyPreserveHost On`,
// Caddy and Traefik by default) and connects over loopback, which is exactly
// the shape the guard refuses — so the SDK's own copy of this check was
// refusing the documented recipe, in plain text, on a request a Streamable HTTP
// client cannot read as anything but a broken server.
//
// So the operator declares the host instead, with --public-url, or a hop they
// listed in --trusted-proxies forwards whatever host it heard. Both checks live
// here, because only this layer can see either flag: the SDK's is switched off
// in internal/transport and its rule is reproduced below.

package main

import (
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// loggedHostPrefixBytes bounds what a refused Host header contributes to a log
// line. The value is a caller's to choose and a log stream is not the place to
// find that out.
const loggedHostPrefixBytes = 64

// hostGuard is a deployment's policy on the Host header a request may carry.
type hostGuard struct {
	// declared is every host this deployment answers to: the loopback names,
	// the host --http binds when it names one, and the host --public-url
	// advertises. Never nil.
	declared map[string]bool
	// bound reports whether --http named a single host. When it did, a Host
	// outside the declared set is refused outright, whatever address the
	// request arrived on: the operator named the interface, so a request for
	// another name is nobody's health check. A wildcard bind or a unix socket
	// names none, and only the rebinding rule below applies.
	bound bool
	// proxies are the peers whose forwarded Host is believed, from
	// --trusted-proxies.
	proxies trustedProxies
}

// newHostGuard builds the policy from the listen address, the advertised public
// origin and the trusted proxy list.
func newHostGuard(addr, publicURL string, proxies trustedProxies) hostGuard {
	guard := hostGuard{
		declared: map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true},
		proxies:  proxies,
	}
	if bound := allowedHosts(addr); bound != nil {
		guard.bound = true
		maps.Copy(guard.declared, bound)
	}
	// The operator has already told clients to reach this deployment by that
	// name — it is the origin in every configuration snippet they publish — so
	// the name is declared in the strongest sense the flags offer.
	if host := publicURLHost(publicURL); host != "" {
		guard.declared[normalizedHost(host)] = true
	}
	return guard
}

// permits reports whether the request's Host header is one this deployment
// answers.
func (g hostGuard) permits(r *http.Request) bool {
	// A request naming no host at all is not the attack this guards against.
	// DNS rebinding works by making a browser resolve a name the attacker
	// controls to the loopback address, and the browser then puts that name in
	// the Host header; no browser omits it. What does omit it is a health
	// check — HAProxy's `option httpchk` sends no Host unless one is
	// configured — so refusing it marks a working instance permanently down,
	// and buys nothing: anything able to send a header-less request can reach
	// the listener directly without a browser to do it for it.
	if r.Host == "" {
		return true
	}
	if g.declared[normalizedHost(r.Host)] {
		return true
	}
	// A proxy the operator listed is a hop they vouched for, and forwarding the
	// client's Host is what every proxy in the guide is configured to do. A
	// browser carrying out a rebinding attack is not that hop: it reaches the
	// listener itself, from an address nobody listed.
	if g.trustsPeer(r) {
		return true
	}
	if g.bound {
		return false
	}
	// The rule the SDK applies inside its own handler, reproduced so that the
	// two exceptions above reach it as well: a listener reached over loopback
	// is a local server, and a local server is what a rebinding attack aims at.
	// Reached on any other address it is not, and a wildcard bind then keeps
	// answering whatever host a proxy in front of it forwards.
	return !arrivedOnLoopback(r) || isLoopbackHost(normalizedHost(r.Host))
}

// trustsPeer reports whether the request arrived from a peer in
// --trusted-proxies. It is the same trust [clientIP] reads, asked a different
// question, so a deployment cannot have one answer for whose address to charge
// and another for whose Host to believe.
func (g hostGuard) trustsPeer(r *http.Request) bool {
	if g.proxies.empty() {
		return false
	}
	return peerIsTrusted(remoteHost(r), g.proxies)
}

// hostOnly strips the port from a Host header value, leaving a bare host or a
// bracket-free IPv6 literal. A value with no port is returned as it came.
func hostOnly(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

// normalizedHost is a Host header value or a configured host reduced to what the
// declared set is keyed on: no port, lower case.
//
// DNS is case-insensitive and Go is not. Neither url.Hostname nor
// http.Request.Host folds case, so --public-url=https://MCP.Example.com would
// declare one spelling while every browser, which lowercases the authority
// before sending it, asks for another — and the guard would answer 403 to the
// origin the operator published. Both sides go through here so the comparison
// asks what DNS asks. An IPv6 literal is unharmed: its hex digits mean the same
// in either case, and lower is the canonical spelling.
func normalizedHost(value string) string {
	return strings.ToLower(hostOnly(value))
}

// publicURLHost is the host --public-url advertises, without its port. An
// unparseable value yields "": the URL is validated at startup, so this is the
// unreachable half of a check that stays rather than trusting that.
func publicURLHost(publicURL string) string {
	if publicURL == "" {
		return ""
	}
	u, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// isLoopbackHost reports whether a Host names the local machine. It mirrors the
// SDK's own test, which accepts every loopback address rather than the 127.0.0.1
// spelling alone.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// arrivedOnLoopback reports whether the connection was accepted on a loopback
// address. A request whose context carries no local address — a handler driven
// directly rather than through net/http — is not treated as local, which is the
// SDK's answer for the same case.
func arrivedOnLoopback(r *http.Request) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || local == nil {
		return false
	}
	return isLoopbackHost(normalizedHost(local.String()))
}

// allowedHosts is the set of Host values the listen address declares on its own,
// or nil when it declares none: a wildcard bind (0.0.0.0 or ::) and a unix
// socket both name no host.
//
// The socket case is explicit rather than incidental. The check exists against
// DNS rebinding, which needs a browser to reach the listener by name, and no
// name resolves to a file on disk; the Host a socket client sends is whatever
// its HTTP library needs to build a request. Deriving a set from the path
// happened to do nothing on Linux, where SplitHostPort finds no colon — on
// Windows it finds the drive letter's, and a server on C:\...\mcp.sock would
// serve nothing but a client naming host "C".
func allowedHosts(addr string) map[string]bool {
	if isUnixSocketAddr(addr) {
		return nil
	}
	host, _, _ := net.SplitHostPort(addr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		return nil
	}
	return map[string]bool{
		strings.ToLower(host): true,
		"localhost":           true,
		"127.0.0.1":           true,
		"::1":                 true,
	}
}

// hostGuarded refuses a request whose Host names a host this deployment does not
// serve, and is the reason the SDK's own copy of the check is turned off.
//
// It is mounted on the MCP endpoint and nowhere else. /health is what a balancer
// and a container runtime probe, and the server card is a public document; both
// are reached with whatever Host the prober happens to send, and neither
// executes anything a rebinding attack could want.
func hostGuarded(guard hostGuard, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if guard.permits(r) {
			next.ServeHTTP(w, r)
			return
		}
		slog.WarnContext(r.Context(), "request refused: the Host header names a host this deployment does not serve",
			"host", loggedHostPrefix(r.Host), "host_len", len(r.Host))
		http.Error(w, "Forbidden: the Host header names a host this deployment does not serve. "+
			"Behind a reverse proxy, pass --public-url with the origin clients use, or --trusted-proxies with the proxy's address.",
			http.StatusForbidden)
	})
}

// loggedHostPrefix bounds a refused Host header to what is worth writing down.
func loggedHostPrefix(value string) string {
	if len(value) <= loggedHostPrefixBytes {
		return value
	}
	return value[:loggedHostPrefixBytes] + "..."
}

// validatePublicURL refuses a --public-url that cannot be the origin clients
// use, at startup rather than at the first refused request.
//
// The value is a declaration, and a declaration nobody can act on is worse than
// none: the guard would keep refusing the very name the operator wrote, and the
// message would keep telling them to pass the flag they already passed.
func validatePublicURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("--public-url %q is not a URL: %w", value, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("--public-url %q needs an http:// or https:// scheme; it is the origin clients type, not a bare host", value)
	case u.Hostname() == "":
		return fmt.Errorf("--public-url %q names no host", value)
	}
	return nil
}
