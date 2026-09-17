# One server for every caller, and the address each one is charged to

Date: 2026-09-17

Over HTTP this server is one process serving everybody. There is no per-caller
anything: `cmd/server`'s `newRegisteredServer` builds one `mirrors.Manager`, one
`libgen.Client` and one `mcp.Server`, registers the catalog on it once, and every
request that arrives — from any client, on any connection — is handled by those
same objects. The outbound rate limiter (`LIBGEN_MCP_RATE_RPS` /
`LIBGEN_MCP_RATE_BURST`), the per-source cooldowns, the discovery providers' own
limiters, the mirror cache and the `read` temp cache are all fields of those
shared objects.

That is the right shape, and it is not an accident of the wiring. But it means
the process cannot tell two callers apart, and until now it had no way to: a
grep for `RemoteAddr` or any forwarded-address header found nothing in `cmd/` or
`internal/` at all.

This records the invariant, and the one function that is allowed to break the
tie when something needs to.

## The invariant

> Isolation here is a client each, not a server each. Every HTTP caller shares
> one server, one outbound budget and one cache, and nothing a caller supplies
> may create state that outlives the call.

A per-call secret — an Anna's Archive membership key, an Unpaywall contact
address — is used for the request it arrived on and is never stored, keyed on,
counted by or logged. That is the existing rule for credentials, and this record
extends it to identity: **a caller's own input may not become a bucket.** A
server that kept a bucket per supplied key would let one caller mint an unlimited
number of them, and would hold a map of other people's credentials to do it.

## What may be keyed on instead

> Per-caller state added later is keyed on `clientIP` (`cmd/server/client_ip.go`)
> and on nothing else.

It is one function so that a rate bucket, a per-caller ceiling and a telemetry
pseudonym cannot disagree about who the caller is — which they would, written
separately, the first time a deployment put a second proxy in front.

`clientIP` answers with the address the connection came from, unless a trusted
proxy has told the server otherwise:

- `--trusted-proxy-header` names the header a proxy fills with the address it
  heard the request from.
- `--trusted-proxies` names the peers whose word is taken for it, as addresses
  and CIDR ranges.

The header is read **only** from a peer on that list. From anybody else it is
text the caller wrote, and a caller who can choose the address their traffic is
charged to can choose somebody else's, or a fresh one per request — which is the
whole budget, handed to the person it was meant to bound.

The two flags are therefore **mutually required**: a header with no list is a
header anybody may set, a list with no header names proxies whose word is never
asked for, and both fail startup rather than run as something the operator did
not write.

## Reading the header from the right

A forwarded-address header is a trail, and each proxy appends the peer it heard
from. So the walk starts at the **rightmost** hop and stops at the first one that
is not itself a trusted proxy; if every hop is trusted, the leftmost is the
client. A hop that does not parse as an address ends the walk and the request is
charged to the peer, because a proxy sending something other than what the flag
promised is not evidence of anything.

Read leftmost — the obvious way, and what most examples show — the value is
whatever the outermost proxy was told, which is the caller's own text again.
Read rightmost with no list, the answer is the inner proxy and every caller
behind it shares one budget. The list is what makes the walk mean anything.

A single-valued header such as `X-Real-IP` needs no special case: the walk over
one value returns that value.

## The deployed shape

The hosted endpoint is plain-TCP replicas in containers, each binding
`0.0.0.0:8080`, published on loopback ports of the host, behind a reverse proxy
on a separate machine that already forwards `Host`, `X-Real-IP`,
`X-Forwarded-For` and `X-Forwarded-Proto`. `X-Real-IP` already carries the real
client address, so that is the header the documentation names.

One detail is worth understanding before copying a value: the address to trust
is **not** the one the proxy's upstream line names. nginx connects to
`127.0.0.1:8811` because that is where the container's port is published on the
host, but what the server accepts is a connection from the container network's
gateway. The flag takes whatever address or CIDR range the operator supplies and
nothing in the code assumes either, so the right value is read off the
deployment rather than copied from here — the repository's own nginx fixture
uses `127.0.0.1` because it has no container in between.

Exactly one origin is ever the peer in that shape, which is why a single `/32`
is the whole of what it needs. A wider range gives away exactly as much as it
covers.

## The same-host alternative, and its own spelling

A unix socket remains the recommendation for a proxy on the same host. There the
peer is a path rather than an address, so no range can ever name it and the rule
above could never believe a header.

`--trusted-proxies` therefore accepts one entry that is not an address, the
literal `unix`: *every peer of a unix-socket listener is a trusted proxy*. It is
defensible because the socket is created `0660`, so its peers are already its
owner and the group the operator put the proxy in. It is explicit rather than
implicit so the trust is stated rather than inherited, and so a socket
deployment that does not pass it is honestly one key for every caller instead of
silently believing whatever arrives. On a TCP listener it matches no peer that
will ever connect and is refused at startup.

## Consequences

- The default, with neither flag, is that every caller is told apart by the
  address their connection came from. Behind a proxy that is the proxy, so every
  caller shares one key — which is correct, and visible, rather than wrong and
  quiet.
- An operator who names the wrong hop gets a configuration that looks identical
  from outside. That is why the accepted rule is stated once in the startup log:
  it is the only place it can be seen.
- Nothing reads `clientIP` yet. It landed before its first consumer on purpose,
  so that the consumers arrive already agreeing on who a caller is.
- Replicas do not share these keys with each other. Anything that must hold
  across replicas — a pseudonym in a telemetry attribute, for instance — has to
  derive from a configured value and not from process state.

## Where this lives

`cmd/server/client_ip.go` holds `clientIP`, `trustedProxies` and the startup
check, with `cmd/server/client_ip_test.go` beside it. The flags are documented in
`docs/architecture.md` § *Stateless mode*, and the proxy recipes in
`docs/getting-started.md`. `test/e2e/http/proxy_test.go` drives the deployed
shape through a real nginx, and `test/e2e/http/unixsocket_test.go` covers the
`unix` entry and its refusal on a TCP address.
