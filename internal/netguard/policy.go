package netguard

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Policy says which outbound destinations this deployment's operator chose, so
// the guard can constrain the ones they did not.
//
// That axis is the whole point. One boolean governed every client and every
// URL, which forced a single answer to two different questions: may this server
// reach the mirror its operator wrote into a configuration file, and may it
// reach an address some third party put in an open-access index. The first is a
// deployment's own business — a mirror on a LAN is an ordinary setup. The
// second is server-side request forgery.
//
// The usual way out is an exception list the operator maintains, and that is a
// second copy of the configuration they already wrote. This reads the
// configuration instead.
//
// A Policy is safe for concurrent use: one is built per client and shared by
// every request that client makes.
type Policy struct {
	// hosts are the lowercased hostnames the operator named, as a set.
	hosts map[string]struct{}
	// names are the entries of hosts that are not address literals, which are
	// the only ones a resolver has anything to say about.
	names []string
	// privateLiteral records that some named host was spelled as a private
	// address outright, which is decided at construction and never costs a
	// lookup — the shape a mirror on a LAN usually takes.
	privateLiteral bool
	// allowPrivate is the tier B opt-out. It never lifts tier A.
	allowPrivate bool

	// mu guards private.
	mu sync.Mutex
	// private memoizes, per named host, whether that host itself sits in
	// private space. Only a definitive answer is stored; see [Policy.hostIsPrivate].
	private map[string]bool
}

// NewPolicy builds a policy from the hosts the operator named and the tier B
// opt-out.
//
// A host may be given as a bare hostname, as a host:port pair or as a URL; only
// the host part is kept, lowercased and without a port, because that is what a
// destination is compared on. An empty entry is dropped rather than becoming a
// member that matches nothing and looks like it might.
func NewPolicy(operatorHosts []string, allowPrivate bool) *Policy {
	p := &Policy{hosts: make(map[string]struct{}, len(operatorHosts)), allowPrivate: allowPrivate}
	for _, raw := range operatorHosts {
		h := normalizeHost(raw)
		if h == "" {
			continue
		}
		if _, seen := p.hosts[h]; seen {
			continue
		}
		p.hosts[h] = struct{}{}
		if addr, err := netip.ParseAddr(h); err == nil {
			p.privateLiteral = p.privateLiteral || Blocked(addr)
			continue
		}
		p.names = append(p.names, h)
	}
	return p
}

// normalizeHost reduces a configured value to the hostname a destination is
// compared on, accepting a bare host, a host:port pair or a URL.
func normalizeHost(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "//") {
		if u, err := url.Parse(value); err == nil && u.Hostname() != "" {
			return strings.ToLower(u.Hostname())
		}
	}
	// A bare host may still carry a port. SplitHostPort also accepts a bracketed
	// IPv6 literal, which is why the result is preferred over the raw value.
	if host, _, err := net.SplitHostPort(value); err == nil && host != "" {
		return strings.ToLower(host)
	}
	return strings.ToLower(strings.Trim(value, "[]"))
}

// Names reports whether host is one the operator named.
func (p *Policy) Names(host string) bool {
	if p == nil || host == "" {
		return false
	}
	_, ok := p.hosts[strings.ToLower(host)]
	return ok
}

// Hosts returns the operator-named hosts in no particular order. It exists for
// tests and for a diagnostic; the guard reads the set directly.
func (p *Policy) Hosts() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.hosts))
	for h := range p.hosts {
		out = append(out, h)
	}
	return out
}

// AllowsPrivate reports the tier B opt-out this policy carries.
func (p *Policy) AllowsPrivate() bool { return p != nil && p.allowPrivate }

// operatorLookupTimeout bounds the lookups NamesPrivateHost may make, for the
// whole call rather than per host, so a refusal cannot become a hang on a
// resolver that never answers — nor a walk through a dozen of them.
const operatorLookupTimeout = 2 * time.Second

// NamesPrivateHost reports whether any host the operator named sits in private
// space itself.
//
// It exists for one ordinary shape: an operator's own mirror redirecting to a
// sibling host on the same private network. A deployment whose named mirror is
// inside a private network is already inside that network, so the hop is not
// the server being talked into reaching somewhere it could not otherwise.
//
// It deliberately does not ask which named host the chain left. The dialer
// cannot know: net/http hands each redirect hop to the transport as a request
// of its own, carrying the initial request's context, so by the time an address
// is in hand the previous hop is gone. The redirect check does know, and could
// answer more precisely — but two halves of one client that disagree about what
// is reachable are worse than one answer that is slightly broad, and the breadth
// is bounded by the premise above: the deployment named a host on that network.
//
// A resolver that cannot answer leaves the destination refused rather than
// permitted. This decides whether to open a path into somebody's network, and
// not knowing is a no.
func (p *Policy) NamesPrivateHost(ctx context.Context) bool {
	if p == nil {
		return false
	}
	if p.privateLiteral {
		return true
	}
	if len(p.names) == 0 {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, operatorLookupTimeout)
	defer cancel()
	for _, host := range p.names {
		if p.hostIsPrivate(lookupCtx, host) {
			return true
		}
	}
	return false
}

// hostIsPrivate answers, and remembers, whether one named host sits in private
// space.
//
// The memo is per host rather than one answer for the whole policy, and only a
// definitive answer is kept. A deployment may name a dozen hosts, and a resolver
// that failed for one of them has said nothing about it: memoizing that silence
// would turn a single timeout into a permanent answer for a host nobody asked.
func (p *Policy) hostIsPrivate(ctx context.Context, host string) bool {
	p.mu.Lock()
	decided, known := p.private[host]
	p.mu.Unlock()
	if known {
		return decided
	}
	decided, definitive := resolveHostPrivate(ctx, host)
	if !definitive {
		return decided
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.private == nil {
		p.private = make(map[string]bool, len(p.names))
	}
	p.private[host] = decided
	return decided
}

// resolveHostPrivate reports whether a named host sits in private space, and
// whether anything was actually learned about it.
//
// It is only ever asked about names. A host the operator spelled as an address
// literal was decided by [NewPolicy] without a resolver, which is both cheaper
// and the only answer available: there is nothing to look up.
func resolveHostPrivate(ctx context.Context, host string) (private, definitive bool) {
	addrs, err := lookupHostAddrs(ctx, host)
	if err != nil || len(addrs) == 0 {
		return false, false
	}
	// Every address, not the first: a name that answers with one private and
	// one public address is not a private deployment, and taking the first would
	// make the answer depend on resolver ordering.
	for _, addr := range addrs {
		if !Blocked(addr) {
			return false, true
		}
	}
	return true, true
}

// lookupHostAddrs resolves host to addresses. It is a variable so a test can
// answer as a resolver would without needing one.
var lookupHostAddrs = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// dialDecision is what one request stamps on its context for the dialer to read.
//
// It carries the answer rather than only the policy because the question — is
// THIS request's host one the operator named — is settled once per request,
// above the dialer, where the URL is still in hand. By the time the Control hook
// runs there is nothing left but an address.
type dialDecision struct {
	policy        *Policy
	operatorNamed bool
}

// permitsPrivate reports whether the operator's own configuration answers for a
// private destination on this request.
func (d dialDecision) permitsPrivate(ctx context.Context) bool {
	return d.operatorNamed || d.policy.NamesPrivateHost(ctx)
}

// dialDecisionKey is the private context key for [dialDecision].
type dialDecisionKey struct{}

// withDialDecision stamps the destination decision for one request.
func withDialDecision(ctx context.Context, d dialDecision) context.Context {
	return context.WithValue(ctx, dialDecisionKey{}, d)
}

// dialDecisionFrom reads back what [withDialDecision] stamped.
func dialDecisionFrom(ctx context.Context) (dialDecision, bool) {
	d, ok := ctx.Value(dialDecisionKey{}).(dialDecision)
	return d, ok
}

// policyTransport stamps every request with the decision in force for it.
//
// It sits immediately around the guarded transport, so the stamp is
// structurally the last thing that happens before the dial rather than
// something a later wrapper could be inserted in front of.
//
// net/http hands each redirect hop to the transport as a request of its own, so
// a hop that leaves an operator-named host is stamped as not operator-named
// without anything having to track the chain.
type policyTransport struct {
	base   http.RoundTripper
	policy *Policy
}

// RoundTrip stamps the request and delegates.
func (t *policyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	decision := dialDecision{policy: t.policy, operatorNamed: t.policy.Names(req.URL.Hostname())}
	return t.base.RoundTrip(req.WithContext(withDialDecision(req.Context(), decision)))
}
