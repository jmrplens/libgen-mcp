// rate_limit.go decides whether an inbound limit can mean anything here.

package main

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/jmrplens/libgen-mcp/v2/internal/netguard"
	"github.com/jmrplens/libgen-mcp/v2/internal/toolutil"
)

// The shipped inbound limit, per charged address.
//
// It bounds what one caller may ask of a process everybody shares: the outbound
// bucket, the download slots, the temp cache and the processor are all one set,
// and without this the noisiest caller spends them for everyone. The figures
// match the sibling project's HTTP defaults; what they should be for this
// server's own upstream budget is a measurement nobody has taken yet, and the
// documentation says which number a deployment is actually running under.
const (
	defaultRateLimitRPS   = 10
	defaultRateLimitBurst = 40
)

// rateLimitDecision is what startup settled on, and why.
type rateLimitDecision struct {
	rps   float64
	burst int
	// off reports that no limit is applied. reason says why, for the startup
	// line an operator reads when they wonder where their limit went.
	off    bool
	reason string
}

// describe renders the decision for that line.
func (d rateLimitDecision) describe() string {
	if d.off {
		return "off (" + d.reason + ")"
	}
	return fmt.Sprintf("%g rps, burst %d, per charged address", d.rps, d.burst)
}

// newLimiter builds one caller's bucket, or nil when the limit is off.
func (d rateLimitDecision) newLimiter() *toolutil.RateLimiter {
	if d.off {
		return nil
	}
	return toolutil.NewRateLimiter(d.rps, d.burst)
}

// resolveRateLimit decides whether a per-address limit can mean anything on this
// listener, and refuses rather than pretending when it cannot.
//
// A limit keyed on the charged address is only a limit where that address can
// tell two callers apart. On a listener every peer of which is this same
// machine — a loopback bind, or a unix socket — it cannot, unless the operator
// has vouched for a proxy that forwards the real one: without that, every caller
// in the world arrives as `127.0.0.1` and the "per-caller" bucket is one budget
// for the whole deployment. That is worse than no limit, because legitimate
// users then refuse each other and no limit is at least honest about what it
// does.
//
// So on such a listener the limit is off by default — the honest state for an
// operator who never asked for one — and passing --rate-limit-rps explicitly is
// a startup refusal rather than a silent downgrade, because an operator who
// asked for a bound deserves to be told it cannot do what they think.
//
// A wildcard bind is the undecidable case and is deliberately not covered here.
// It may serve real remote peers and a same-host proxy at once, and this process
// cannot see a port publication: a container binding 0.0.0.0 and published with
// `-p 127.0.0.1:8080:8080` is host-local in fact and reads as public here. The
// limit stays on, and [callerWarning.warn] is what says so when the first
// request proves the peer is a proxy nobody vouched for.
func resolveRateLimit(addr string, charge chargePolicy, rps float64, burst int, explicit bool) (rateLimitDecision, error) {
	if rps <= 0 {
		return rateLimitDecision{off: true, reason: "--rate-limit-rps is not positive"}, nil
	}
	if !listenerIsHostLocal(addr) || charge.namesAProxy() {
		return rateLimitDecision{rps: rps, burst: burst}, nil
	}
	if explicit {
		return rateLimitDecision{}, fmt.Errorf(
			"--rate-limit-rps cannot bound a caller on %s: every peer of this listener is this machine, so without --trusted-proxy-header and --trusted-proxies naming the proxy in front, every caller in the world is charged to one address and the limit is one budget for the whole deployment. Pass those two flags, or drop --rate-limit-rps",
			addr,
		)
	}
	return rateLimitDecision{
		off:    true,
		reason: "every peer of this listener is this machine and no --trusted-proxies names the proxy in front, so a per-caller limit would be one budget for everybody",
	}, nil
}

// callerWarning says, once, that the addresses being charged are ones no public
// client could have.
type callerWarning struct {
	once sync.Once
}

// unidentifiableCallers is the process-wide warning state. One line, once, for
// the life of the process: the condition holds for every request of a
// misconfigured deployment, so anything per-request would be a flood.
var unidentifiableCallers callerWarning

// warn writes the line the first time a charged address turns out to be one no
// client on the internet could be reaching this server from.
//
// This is the wildcard-bind case that no startup rule can decide. The address
// that proves it is not always loopback: a container published on the host's
// loopback sees its bridge gateway, which is RFC 1918 — so a condition written
// for 127.0.0.1 alone would never fire on the shape it was written for.
// [netguard.Blocked] already classifies exactly the set that matters, the
// addresses this server refuses to *dial* because no legitimate third party
// could have named them, and reusing it is what keeps the two from drifting.
func (w *callerWarning) warn(address string, charge chargePolicy) {
	if charge.namesAProxy() {
		return
	}
	addr, err := netip.ParseAddr(address)
	if err != nil || !netguard.Blocked(addr) {
		return
	}
	w.once.Do(func() {
		slog.Warn("callers cannot be told apart: the address requests are charged to is one no public client could have, so everyone reaching this server through the proxy in front shares a single rate-limit budget",
			"charged_address", address,
			"fix", "pass --trusted-proxy-header with the header the proxy sets and --trusted-proxies with the address it connects from")
	})
}
