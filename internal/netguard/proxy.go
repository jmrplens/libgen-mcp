// What the guard judges when a request goes through an outbound proxy.

package netguard

import (
	"context"
	"net"
	"net/http"
	"net/url"
)

// guardedDial is the DialContext of every transport this package builds: d,
// told whether the dial is to the proxy the request is sent through.
//
// # Why the proxy gets tier A and nothing else
//
// When HTTP_PROXY or HTTPS_PROXY applies to a request, net/http dials the
// proxy's host and port in place of the destination's, and the Control hook is
// handed the address that resolved to. Judged as the destination, a proxy on a
// private address was refused for every request tier B refuses a private
// address, whatever that request was going to reach: behind an ordinary
// corporate proxy on 10.x, every host the operator did not name (Crossref,
// arXiv, Unpaywall, a repository's download URL) failed on the proxy's own
// address. The proxy is the operator's configuration, as much as LIBGEN_MIRROR
// is, so it is not a destination a third party chose and tier B has nothing to
// say about it. Tier A still has: no proxy is served from a cloud metadata
// address either.
//
// What the proxy is asked to reach is judged apart from the dial, per request,
// by [judgeProxiedDestination].
//
// # Why here
//
// The Control hook sees only what an address resolved to, which cannot say it
// was a proxy. The address before resolution can, and this is the one place it
// is visible. It is compared with the address [policyTransport.RoundTrip]
// stamped for the request's proxy, as a string, so a dial that is not that
// address keeps the request's decision and is judged as a destination. A
// disagreement between the two spellings therefore falls back to judging the
// proxy as the destination, which is the rule every proxy dial got before this
// wrapper existed: it can refuse more than a match does, never permit more,
// since tier A applies either way. An unstamped dial, or one whose request is
// not proxied, carries no proxy address, and net/http never dials an empty one.
func guardedDial(d *net.Dialer) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if decision, _ := dialDecisionFrom(ctx); decision.proxy != "" && decision.proxy == address {
			ctx = withDialDecision(ctx, dialDecision{proxyDial: true})
		}
		return d.DialContext(ctx, network, address)
	}
}

// proxyDialAddress reports the address transport will dial when it sends req
// through a proxy, and "" when it will dial req's own host.
//
// It asks the transport's own Proxy function, which the transport asks again
// for the same request, rather than reading the environment itself, so for a
// deterministic function the two come to one answer. The function every
// transport here carries is [http.ProxyFromEnvironment], which reads the
// environment once per process. A function that fails here refuses the request
// unsent, which is what net/http does when its own call fails. A round tripper
// that is not an [http.Transport] has no proxy this package can see, and none
// is assumed.
//
// The address is spelled as net/http spells the one it hands the dialer: the
// proxy's host, and its port or the default of its scheme. [guardedDial]
// compares the two as strings, so a host net/http would spell differently (a
// name it converts to its IDNA form, which this does not) matches no dial and
// is judged under the request's own decision, the stricter rule.
func proxyDialAddress(base http.RoundTripper, req *http.Request) (string, error) {
	transport, ok := base.(*http.Transport)
	if !ok || transport.Proxy == nil {
		return "", nil
	}
	proxy, err := transport.Proxy(req)
	if err != nil || proxy == nil {
		return "", err
	}
	port := proxy.Port()
	if port == "" {
		port = proxySchemePorts[proxy.Scheme]
	}
	return net.JoinHostPort(proxy.Hostname(), port), nil
}

// proxySchemePorts are the ports net/http dials a proxy on when its URL names
// none, one per proxy scheme it supports.
var proxySchemePorts = map[string]string{
	"http":    "80",
	"https":   "443",
	"socks5":  "1080",
	"socks5h": "1080",
}

// judgeProxiedDestination applies the guard to the destination of a request
// sent through a proxy, which the dialer never sees because it dials the proxy.
//
// ctx carries the request's own decision. Only a destination spelled as an
// address can be judged here, and it is judged by exactly the rule the dialer
// would have applied to it, so a URL naming 169.254.169.254 is refused behind a
// proxy as it is without one, and so is a private address tier B refuses this
// request. A destination spelled as a name is resolved by the proxy, not by
// this server, so what it reaches is the proxy's decision: a deployment that
// sends its outbound traffic through a proxy has made the proxy the place a
// rule about names belongs.
//
// It runs per request rather than per dial for two reasons. Behind any proxy
// the dialer only ever sees the proxy's address. And net/http keys a
// plain-HTTP request sent through an http or https proxy on the proxy alone,
// so one pooled connection carries requests to many destinations, and a
// request served from it is never dialed at all.
func judgeProxiedDestination(ctx context.Context, dest *url.URL, allowPrivate bool) error {
	addr, spelledAsAddress := addressLiteral(dest.Hostname())
	if !spelledAsAddress {
		return nil
	}
	return judge(ctx, addr, allowPrivate)
}

// refuseUnsent closes the body of a request this package declines to send and
// returns err, since a RoundTripper owns the body it is handed, refusals
// included.
func refuseUnsent(req *http.Request, err error) error {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return err
}
