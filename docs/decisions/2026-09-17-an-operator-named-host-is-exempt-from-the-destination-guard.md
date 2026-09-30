# An operator-named host is exempt from the destination guard

Date: 2026-09-17

`internal/netguard` refuses to dial addresses only this machine or its network
can reach, because almost every URL this server fetches was chosen by somebody
else. That was one boolean, `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES`, and it had to
answer two different questions with the same word: may this server reach the
mirror its operator wrote into a configuration file, and may it reach an address
some third party deposited in an open-access index.

The first is a deployment's own business — a mirror on a LAN is an ordinary
setup. The second is server-side request forgery. An operator who needed the
first had to grant the second, for every source at once, which is a guard that
protects nobody and annoys everybody.

This records the axis that separates them, the line it draws, and what was
deliberately left on the strict side of that line.

## The rule

> A destination the operator named in their own configuration is exempt from the
> private-address tier, whatever it resolves to. Everything else is not.

There is no exception list to maintain, because an exception list is a second
copy of the configuration the operator already wrote. The guard reads the
configuration instead.

## The two tiers

**Tier A, always on.** The cloud instance-metadata addresses — `169.254.169.254`,
`169.254.170.2`, `fd00:ec2::254` and `100.100.100.200` — are refused on every
hop, for every client, whatever the configuration says. Naming one as an
operator host does not open it, and neither does the flag. They are listed one
by one rather than derived from the ranges around them, because a rule that
holds for every deployment must be as narrow as it can be: nothing legitimate
serves a book, an article or a presigned download URL from one of them.

**Tier B, for destinations the operator did not choose.** Loopback, `0.0.0.0/8`,
the IPv4 broadcast address, link-local unicast and multicast, every multicast
scope, RFC 1918, RFC 4193 and RFC 6598 carrier-grade NAT are refused unless one
of three things is true: the request's host is one the operator named, some host
they named is itself private (see below), or
`LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES` is set.

**There is never a tier C.** An address reached because `LIBGEN_MIRROR` named it
is exempt with no further conditions. DNS rebinding is a threat when the
attacker controls the name; here the operator wrote the name into their own
configuration file, and refusing it would mean telling everyone running a mirror
on their own network that it is now a risk to their own server.

## What counts as operator-named

- The host of `LIBGEN_MIRROR`.
- The hosts in `LIBGEN_MCP_SCIHUB_HOSTS`.
- Each mirror family's own constants: its catalog `SourceURL`, its `Preferred`
  mirror and its `Fallback` list. These are this server's choice of where to
  look, which is the same kind of decision `LIBGEN_MIRROR` makes explicitly.

Everything else is third-party, however ordinary it looks: every resolved
download URL, every discovery provider's result, **every mirror hostname scraped
from the shadowlibraries catalog**, and every redirect hop that leaves an
operator-named host. The catalog is the one worth spelling out — it is a page on
the public internet, and a page that started answering with `127.0.0.1` would
otherwise be reading this server's own loopback back to it.

`internal/discovery` names nothing at all, and that is its whole configuration
rather than an omission: every provider there is a baked-in public API, so each
gets the strict tier on every address it reaches.

## Why the policy travels on the request

One client serves both kinds of destination. The same `c.dl` fetches the mirror
the operator configured and the resolved URL a third party deposited, so the
decision cannot be a property of the client.

It is therefore stamped per request by a `RoundTripper` sitting immediately in
front of the guarded transport, and read in `net.Dialer.ControlContext` — after
resolution, once per candidate address, with no window between the check and the
connection because this *is* the connection. `net/http` hands each redirect hop
to the transport as a request of its own, so a hop that leaves an operator-named
host is stamped as not operator-named without anything having to track the
chain.

## The private sibling

One shape falls between the tiers: an operator's own mirror redirecting to a
sibling host on the same private network. The hop is not operator-named, so tier
B would refuse it.

A private destination is therefore also permitted when a host the operator named
is *itself* private — because a deployment whose named mirror is inside a
private network is already inside that network, so the hop reaches nowhere its
operator could not. A host spelled as an address literal is decided at
construction; a name is resolved on the refusal path only, so the common case
pays nothing.

The answer is aggregated over the named set rather than asked about the
particular host a chain left, and that is a deliberate loss of precision. The
dialer cannot know which host the chain left — by the time an address is in hand
the previous hop is gone — and the redirect check, which could answer more
precisely, is the same client's other half. Two halves of one client that
disagree about what is reachable are worse than one answer that is slightly
broad, and the breadth is bounded by the premise: the deployment named a host on
that network.

## Consequences

Positive:

- The ordinary private-mirror deployment needs no configuration at all. It is
  protected by construction rather than by a list somebody has to maintain, and
  cannot be broken by forgetting an entry.
- `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES` stops being the price of running your own
  mirror. It keeps its meaning and its default, and a deployment that set it only
  to reach its own mirror can now drop it — which closes every third-party URL it
  was opening as a side effect.
- Nothing that worked before stops working. The flag is unchanged, so this ships
  without a migration, which is the deliberate difference from the decision it
  is modelled on.
- Each client this package builds gets its own transport and therefore its own
  idle-connection pool, so a connection vetted under one policy is never handed
  to a request governed by another.

Negative:

- A **publicly resolving** operator mirror that redirects to a **private**
  address is refused. That is the one combination with a real false-positive
  cost, nothing here measures how many such deployments exist, and the opt-out is
  documented. The first bug report is the measurement.
- A deployment that names both a public and a private host extends the sibling
  concession to redirects leaving the public one. See the last paragraph of *The
  private sibling* for why that is accepted rather than fixed.
- The family constants are in the exempt set, so a hijacked DNS record for
  `libgen.li` pointing at loopback would be dialed. They are the hosts this
  server exists to talk to; refusing them would refuse the product.
- Tier A has no opt-out at all. The only flag it could be is
  `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES`, whose meaning is "my mirror is on a
  private network", which is not a claim about cloud credential endpoints. One
  flag meaning both would let a deployment that needs the first silently acquire
  the second.
- A proxy named by `HTTP_PROXY` or `HTTPS_PROXY` is what gets dialed, so the
  dialer sees the proxy rather than the destination behind it. A destination
  spelled as a hostname is resolved by the proxy, and a deployment that proxies
  its outbound traffic has delegated the private-address decision to the proxy,
  which is where a rule about names belongs in that topology. *Amended
  2026-10-01*, below: the proxy is no longer judged under the destination's rule,
  a destination spelled as an address is still judged here, and a hostname still
  gets tier A from a local lookup.
- A refusal may cost one DNS lookup. It is bounded at two seconds for the whole
  decision rather than per host, memoized per host, and a resolver that could not
  answer is not memoized at all — silence is not an answer, and remembering it
  would turn one timeout into a permanent refusal for the deployment the
  concession exists for.

## Where this lives

- `internal/netguard/policy.go` — `Policy` holds the named set and the sibling
  concession; `policyTransport` stamps each request; `dialDecision` is what the
  dialer reads.
- `internal/netguard/netguard.go` — `control` applies tier A then `tierB`, and
  `redirectAddressAllowed` applies the same two tiers to a redirect whose target
  is spelled as an address literal.
- `internal/config` — `Config.OperatorHosts` reports what the environment named,
  and nothing else.
- `internal/mirrors` — `OperatorHosts` adds each known family's constants, and is
  the one function both `mirrors` and `libgen` build their policy from.

The tests that hold it: `TestOperatorNamedHostIsExemptThoughItResolvesPrivate`
pins the exemption against a named host that resolves *public*, so nothing but
the exemption can admit the dial;
`TestPrivateSiblingIsAdmittedOnlyWhenTheOperatorHostIsPrivate` drives both
halves of the client and both directions; `TestNamingAMetadataAddressDoesNotOpenIt`
names a metadata endpoint as an operator host with the flag on and still expects
a refusal; and `TestManagerReachesTheMirrorTheOperatorConfigured` and
`TestClientsCarryTheOperatorNamedDestinations` assert the wiring against a real
server on a real private address.

## Amendment — 2026-10-01 (outbound proxies)

The proxy bullet under *Consequences* described what the dialer sees and left
out what it did with it: the proxy's address was judged under the destination's
rule. A proxy on a private address, which is what a corporate `HTTP_PROXY`
usually is, was therefore refused for every request tier B refuses a private
address, whatever that request was going to reach. Every host the operator did
not name failed on the proxy's own address, which is Crossref, arXiv, Unpaywall,
OpenLibrary and every download URL. And under the flag, or behind a proxy on a
public address, the opposite held: nothing judged the destination at all, so
`http://169.254.169.254/` went to the proxy. And a plain-HTTP connection
through a proxy, which `net/http` keys on the proxy alone, carried a request to
a private address nobody named once a request to the operator's own mirror had
opened it.

- **The proxy's own dial answers to tier A alone.** It is the operator's
  configuration, as much as `LIBGEN_MIRROR` is. `policyTransport` asks the base
  transport's `Proxy` function which proxy the request goes through and stamps
  the address `net/http` will dial for it. `guardedDial`, the `DialContext` of
  every transport the package builds, re-stamps a dial to exactly that address as
  the proxy's. The comparison is between two strings, so a spelling they disagree
  about falls back to the older, stricter rule.
- **The destination behind a proxy is judged per request**, before anything is
  sent, by the same two tiers, when the URL spells it as an address.
- **"Spelled as an address" is read the way a C resolver reads it.**
  getaddrinfo parses a host through `inet_aton`, which accepts one to four
  dot-separated parts in decimal, octal or hex, so `2852039166`, `0xa9fea9fe`,
  `0251.0376.0251.0376` and `169.254.43518` are each `169.254.169.254` to the
  proxy's resolver while `netip` rejects all four. `addressLiteral` parses those
  forms and judges the result, and refuses a host whose last label is numeric
  but which is no valid form, since no registered name ends in such a label. The
  redirect check reads hosts the same way.
- **A hostname behind a proxy gets tier A from a local lookup.** Tier B stays the
  proxy's decision, as the bullet above says: the proxy may see a view of DNS
  this machine does not, so a private answer here says nothing about where it
  connects. Tier A holds for every client and every hop, so the name is resolved
  here as well, under the two-second bound the sibling concession uses, and
  refused if any address it answers with is a metadata endpoint. A lookup that
  fails does **not** refuse. A machine behind a proxy is often one whose own
  resolver cannot see the outside world, and refusing on silence would refuse
  every request such a deployment makes, which is the defect this amendment
  exists to remove. What remains is a name this machine cannot resolve and the
  proxy resolves to a metadata address, and there the proxy is the only party
  that sees the answer. The cost is one lookup per proxied request to a name.
- **The metadata table is consulted without an IPv6 zone.** `fd00:ec2::254%eth0`
  is the table's address with an interface named, and an operating system ignores
  the zone of a destination that is not link-local.

Connection reuse without a proxy needed nothing. A connection dialed for one
request is handed only to a request with the same scheme, host and port on the
same client, and the decision reads the host and a policy-wide answer that only
widens, so such a request would have been allowed to dial it.
`TestAPooledConnectionIsOnlyHandedToARequestAllowedToDialIt` holds that with a
real idle connection. The proxy half lives in `internal/netguard/proxy.go`, and
`TestAPrivateProxyIsNotJudgedAsTheDestination`,
`TestTheDestinationBehindAProxyIsStillJudged` and
`TestAPooledProxyConnectionDoesNotCarryTheNextRequestPastTheGuard` drive it
through a real forward proxy on loopback, and
`TestAnHTTPSRequestTunnelsThroughAPrivateProxy` through a CONNECT tunnel. The
host parse lives in `internal/netguard/hostaddr.go`, held by
`TestAddressLiteralReadsHostsAsACResolverDoes` and, on the redirect path, by
`TestCheckRedirectJudgesEveryNumericSpelling`.
