# HTTP server mode

**How-to guide** — for an operator deploying this centrally.

Running `libgen-mcp` over streamable HTTP turns a one-client-per-process stdio server into a
shared service: several clients, one outbound budget, one download semaphore, one cache. This
page is the deployment guide for that mode — what you have to decide, in the order the
decisions depend on each other, and what each one costs when it is left out.

Three documents share the subject and do not repeat each other:

- **This page** says how to deploy it, and owns the operational prose.
- [Behind a reverse proxy](deploy/reverse-proxy.md), [Run as a service](deploy/service.md) and
  [Containers and orchestration](deploy/containers.md) have the complete, tested
  configurations: every common proxy, systemd units, Compose and Kubernetes.
- [Architecture → Transports](architecture.md#transports) explains how the transport is built
  and carries the **[full flag table](architecture.md#stateless-mode)**. Every flag named here
  is defined there, together with its `LIBGEN_MCP_*` variable.

## Pick a listener first

`--http` takes a TCP address or a unix socket path, and the rule is the path separator: a value
containing `/` is a path, anything else is an address. So `--http :8080` and
`--http 127.0.0.1:8080` bind TCP, `--http /run/mcp-libgen.sock` binds a socket, and a bare
`--http mcp.sock` binds **TCP** on purpose, because a bare name cannot be told apart from a
hostname. The details are in [Where the server listens](#where-the-server-listens) below; the
short version is that a proxy on the same machine wants the socket and a proxy on another host
wants TCP, with TLS either in this process or in front of it.

Everything between here and there depends on which one you picked, because they give this
server two different answers to "who is calling".

## The name clients use, and why a proxied request is refused without it

Every MCP server is asked to refuse a `Host` header it does not serve, against DNS rebinding:
an attacker resolves a name they control to the address this server listens on, and a browser
they have already loaded then reaches it under that name. The rule is that a connection
**accepted on a loopback address** may carry only a loopback `Host`.

That is every reverse-proxy recipe on this page. The proxy forwards the client's `Host` —
`mcp.example.org` — and connects to this server over loopback, so the request arrives with a
public name on a loopback connection and is refused with `403`.

**The condition is the accepted connection's local address, not the bind**, so it catches a
wildcard bind exactly as it catches a loopback one: a container that binds `0.0.0.0:8080` and
is published with `-p 127.0.0.1:8811:8080` is reached over loopback and is refused the same
way. A unix socket is exempt — no name resolves to a file on disk, so it answers any `Host`.

Two flags each fix it, and either alone is enough:

```bash
libgen-mcp --http 127.0.0.1:8080 --public-url https://mcp.example.org/libgen
```

`--public-url` states the origin clients reach this deployment at. Its host becomes a name this
server answers to, and it is also what puts a `remotes` entry on the discovery card, so it is
the one to reach for when the deployment is public. `--trusted-proxies` has the same effect by
a different route — anything a trusted peer forwards is believed — and is the one to reach for
when the name is internal or when there are several.

A request carrying no `Host` at all is served rather than refused: no browser omits it, and
what does is a health check. HAProxy's `option httpchk` sends none unless one is configured,
and refusing it would mark a working instance down.

## What identity this deployment has

Three things in this server ask *which caller is this* — the inbound rate limit, the in-flight
ceiling, and the `user.hash` telemetry attribute. All three read one answer: **the address the
connection came from**, unless a trusted proxy was named and told this server otherwise.

There are three deployment shapes and they give three different answers. Find yours before
reading the rest of this page, because two of them make a per-caller limit into a
deployment-wide one.

### A TCP listener behind a proxy

This is the common shape, it is what [Behind a reverse proxy](deploy/reverse-proxy.md)
configures, and it is what the hosted endpoint at `mcp.jmrp.io/libgen` runs.

Without `--trusted-proxies`, **every request arrives from the proxy**, so every caller is the
same caller: the rate limit is one bucket for the whole world, the in-flight ceiling is a
second process-wide ceiling wearing a per-caller name, and `user.hash` is one constant. Nothing
about that is subtle or rare — it is the default outcome of the recommended shape.

The fix is two flags plus the proxy actually setting the header:

```bash
libgen-mcp --http 127.0.0.1:8080 \
  --trusted-proxies 127.0.0.1/32 \
  --trusted-proxy-header X-Real-IP
```

```nginx
proxy_set_header X-Real-IP $remote_addr;
```

Both flags or neither — either one alone fails startup — because a header read from an
untrusted peer is text the caller wrote, and a caller who can pick the address their traffic is
charged to can pick somebody else's, or a fresh one per request.

**Find the address rather than copying one.** `--trusted-proxies` names the peer this server
*accepts connections from*, which is not always the address the proxy's own upstream line uses:

| Where this server runs                                       | The peer it sees                                  |
| ------------------------------------------------------------ | ------------------------------------------------- |
| A bare host, proxy on the same machine                       | The proxy's own address, usually `127.0.0.1`      |
| A container published on loopback (`-p 127.0.0.1:8811:8080`) | The container bridge gateway, an RFC 1918 address |
| A container on a shared user-defined network                 | The proxy container's address on that network     |

Get it wrong and nothing breaks loudly: the header is simply ignored and you are back to one
bucket for the world. The startup log names the listener, and the one-time warning described
below names the address a request was actually charged to — that address is what belongs in
`--trusted-proxies`.

**`X-Real-IP` rather than `X-Forwarded-For`** is the recommendation here only because it is what
the nginx recipes already set and what the sibling deployment on the same domain already reads.
Either works: a multi-hop `X-Forwarded-For` is read from the right, stopping at the first hop
that is not itself a trusted proxy, and a single-valued header is that same walk over one
value.

### A unix socket

A socket behind a same-host proxy has the same problem by a different route: the peer of a unix
connection is not an address at all, so without help every caller is again one caller. The
trust is stated with the literal `unix`, which means *every peer of this socket is a trusted
proxy* — an assertion the socket's `0660` mode already limits to its owner and group:

```bash
libgen-mcp --http /run/mcp-libgen.sock \
  --trusted-proxies unix \
  --trusted-proxy-header X-Real-IP
```

`unix` is refused on a TCP address, and an address is refused on a socket.

### A directly exposed TCP listener

No proxy, no flags: the peer is the client, and every limit on this page works as written. It
is the rarest shape of the three, because a public MCP endpoint usually wants TLS termination,
a name and a log in front of it.

### What the identity decides

Once you know which shape you are in, three behaviours follow from it:

- **The inbound rate limit** (`--rate-limit-rps`) is off by default on a listener whose every
  peer is this machine — a loopback bind or a unix socket — unless `--trusted-proxies` names the
  proxy. Passing `--rate-limit-rps` explicitly there is a **startup refusal** naming both proxy
  flags rather than a limit that quietly does something else. On a wildcard bind it stays on,
  because this process cannot see how the port was published; instead the first request charged
  to an address no public client could have (loopback, RFC 1918, CGNAT, link-local,
  unique-local) logs **one** warning naming both flags.
- **The in-flight ceiling** (`--max-inflight-per-client`) degenerates into a second process-wide
  ceiling under a per-caller name. It still bounds the process; it stops telling two callers
  apart.
- **`user.hash`**, exported when `LIBGEN_MCP_TELEMETRY_IDENTITY` is `pseudonymous`, becomes one
  digest for everybody. That is a configuration symptom and **not** a privacy property — see
  [Telemetry → A constant `user.hash` is a configuration symptom](telemetry.md#a-constant-userhash-is-a-configuration-symptom).

A reader who deploys behind a proxy and reads only the socket paragraph above will conclude
none of this applies to them, which is the opposite of true.

## The hosted endpoint, written out

The public instance at `https://mcp.jmrp.io/libgen` is the first shape: three containers each
binding `0.0.0.0:8080`, published on the host's loopback, reached by an nginx on another
machine, with `--public-url`, both proxy flags naming its Docker bridge gateway and
`X-Real-IP`, and `--rate-limit-rps 60`. The exact command, why the gateway address is not one
to copy, and the one environment variable that sets its tool surface apart from the default
are in [Hosted endpoint → Run the same setup yourself](hosted.md#run-the-same-setup-yourself).

## What one caller may ask for

`tools/call`, `prompts/get` and `subscriptions/listen` draw on a token bucket per charged
address; `tools/list` draws on a second bucket of its own. `--rate-limit-rps` (default `10`)
is the refill rate and `--rate-limit-burst` (default `40`) is how many requests one address may
make at once before that rate applies. `0` or less turns the limit off.

The two buckets are separate so that draining the call bucket never costs a caller their
discovery: a refused listing is worse than a refused call, because no model is in the loop to
read the message and back off. The methods that reach nothing are not metered at all —
`initialize`, `prompts/list`, the notifications, and the resource methods.

**The record table is a bounded LRU, not a fixed set of keys.** At the cap it evicts rather
than refusing: refusing at the cap would lock out every new legitimate client once a few
thousand addresses had been seen, which is trivial over IPv6, and refusing to *track* at the
cap would make the next address unlimited, which is the bypass. Eviction costs at most one full
burst, prefers a record with no request in flight, and sweeps lapsed records first.

**Which number is the deployment's contract, and mind the two spellings.** The inbound limit is
what you promise a caller, and its variables are `LIBGEN_MCP_RATE_LIMIT_RPS` and
`LIBGEN_MCP_RATE_LIMIT_BURST`. The **outbound** budget is a different setting with a
confusingly similar name — `LIBGEN_MCP_RATE_RPS` and `LIBGEN_MCP_RATE_BURST`, one bucket shared
by the whole process, defaulting to one request per second — and it is what the mirrors get from
you whatever the inbound limit says.

The extra searchers are paced the same way, each against its own upstream and each for the
whole process: arXiv at one request every three seconds, PubMed at three per second, OpenLibrary
at one per second (three with a contact address), Crossref and OpenAlex at one per second with a
burst of two, and dblp, ERIC and Gutenberg at one per second. Concurrent searches share those buckets rather than each starting with a full one, so a
provider whose token cannot arrive within the search's budget is skipped for that search, and
the upstream never sees more than its stated rate from this process.

They are not the same guarantee and neither derives from the other: raise the inbound limit and
callers queue on the outbound one instead of being refused, which is slower rather than faster.
The inbound defaults are the sibling project's HTTP figures; what they should be against *this*
server's outbound budget is a measurement nobody has taken yet.

A refused `tools/call` comes back as a **successful** JSON-RPC result flagged `isError`, so the
model gets a structured diagnostic and an agent loop can back off. The other three have no
error flag of their own, so their refusal is a JSON-RPC error with code `-42900`. Refusals are
logged at most once every ten seconds with a count of what the line stands for, because their
rate is the arrival rate minus the limit and one line each would be a flood.

## What one caller may hold

The bucket bounds how fast requests arrive. It says nothing about how long one holds something,
and here that is the expensive part: `download` and `read` occupy a slot of
`LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS` for as long as the transfer takes, so a caller who starts
four of them has taken every slot on the replica while their bucket still shows them well
inside their rate.

`--max-inflight-per-client` bounds that per charged address. Left unset it is the configured
`LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS`, so the bound starts at the whole semaphore and changes
nothing about what one caller can do today — what it changes is that a *second* caller can
still get a slot once somebody measures what a download costs and tightens it. `0` or less
turns the per-caller bound off. A separate, non-configurable ceiling of **64** bounds the
process whatever this says, because the per-caller one multiplies by however many callers there
are.

**The wall-clock cap is the other half of the same resource.** `LIBGEN_MCP_ACTION_TIMEOUT`
(default `1h`, `0` disables it) ends a call that is merely slow — a mirror trickling below the
stall window holds a slot indefinitely otherwise. The ceiling ends a call whose client is still
waiting for too many things at once; the cancellation path ends a call whose client went away.
Three different failures, three different bounds, and a deployment wants all three.

Every refusal here uses the same `isError` result shape the rate limit uses, so a model meets
one answer for "this is your side to fix" rather than reading a ceiling as a mirror failure and
retrying into it forever.

## What the whole process may hold

A `tools/call` holds its `POST` open for as long as the call runs, and on this server that is
mostly time spent **queued**: every catalog request waits for a token of the one outbound bucket
(`LIBGEN_MCP_RATE_RPS`, one a second by default), so sixteen searches in flight take fifteen
seconds to drain and a thousand take over a quarter of an hour. The rate limit bounds how often
a caller asks, and the ceiling above bounds `download` and `read`, but nothing bounded how many
`search` and `get_details` calls were waiting, and a process out of descriptors accepts no
connection at all, **`/health` included**.

So the process holds at most a number of calls **derived from its descriptor limit**, read once
at startup: an eighth of the limit is left spare, one descriptor is reserved for each of the 64
byte-moving slots (a download writes a file and a read opens one), and each held call is
counted at **eleven**. That is the widest fan-out one call has: an escalated `search` holds the
caller's connection, the catalog request and one connection for each of the nine searchers
beyond the catalog, all at once, because the federation runs every provider concurrently and
none of them waits on the outbound bucket. The figure is counted from the provider list, so a
provider added there lowers the ceiling with it, and each provider's connection closes as soon
as its response is read, so none outlives the call. The Go runtime raises the soft limit to the
hard one before `main`, so the figure follows the **hard** limit:

| Hard `RLIMIT_NOFILE`               | Held calls | Stateful sessions |
| ---------------------------------- | ---------: | ----------------: |
| 1024                               |         75 |                37 |
| 4096                               |        320 |               160 |
| 524288 (a default systemd service) |      41698 |             20849 |
| 1048576 (a common container limit) |      83403 |             41701 |

Eleven is the bound, not the typical cost: a search still queued for its catalog token holds only
the caller's connection. The ceiling is sized so that it cannot be outrun, which is why it is
counted at the worst case.

Elsewhere than Linux and macOS (Windows, the BSDs) the server reads no limit and sizes the
process as 1024. **No flag moves it**, for the reason the
ceiling of 64 has none: an operator who could raise it could configure away the one bound that
keeps the process answering. Raising the descriptor limit (`ulimit -n`, `LimitNOFILE=` on a
systemd unit, `--ulimit nofile=` for a container) raises it together with what it protects.
The startup line `process ceilings` says what this process was given, as
`held_calls_per_process`, `descriptor_limit` and `descriptor_limit_source`.

**What counts.** A `tools/call` or a `prompts/get` that arrived on an HTTP `POST`. A listing,
`initialize`, a notification or a `ping` never does, and neither does anything on stdio, which
serves one caller over one pipe. A `download` or `read` counts here **and** against the ceiling
above. A slot is held for one of two lifetimes, depending on where it was taken:

- **On protocol `2026-07-28` or later** the headers name the method, so the slot is taken in
  front of the SDK, **before the body is read**, and given back when the HTTP handler returns.
  A client that sends its body slowly therefore holds a slot for as long as the upload takes.
- **On an older revision** the body has to be read to know what it carries, so each call is
  counted where the SDK dispatches it and holds its slot **from dispatch until its handler
  returns**. Each call of a batch counts on its own.

**How it refuses.** Always with the words `This server is busy. Retry later.`, naming no bound
and no other caller. On protocol `2026-07-28` or later, where the `Mcp-Method` header proves
what a `POST` carries, the refusal happens before the SDK reads the body: `503`, a JSON-RPC
error with code `-50300` (the status times -100, like the other transport refusals) and the
request's id, and `Retry-After: 30`. On an older revision the body has to be read first, so the
refusal is the rate limit's shape: a `tools/call` comes back as a result flagged `isError`, and
a `prompts/get` as a JSON-RPC error with code `-50300`. Either way the **connection is closed**
with the reply, because a refused caller that kept it would hold a descriptor of the very limit
that refused it. A refused call spends nothing of its caller's rate: the process being full is
nothing that caller did. The operator's line is
`request refused: too many calls held across the process`, with `scope=process` and
`limit_held_calls`, written at most once every ten seconds.

**One caller can fill it.** There is no per-caller partner, because an address is something a
caller can have many of, and a per-caller number multiplies with every one. Where the limit is
small that is quick: at the default inbound limit (a burst of 40, then ten a second) one address
reaches 75 held calls in about four seconds. The inbound limit is what slows it, and a proxy
that charges the real client address is what keeps it per caller.

**What `/health` still needs.** It takes no call slot, so a full ceiling does not refuse it, and
the spare eighth is what it, the idle process and the connections being refused live in. But it
still needs a free descriptor to be accepted on, and the ceiling bounds **calls**, not
connections: an idle kept-alive connection holds its descriptor between requests, and with
`--http-idle-timeout` at its default of `0` nothing ever closes one. Enough idle connections can
therefore exhaust the descriptors this ceiling leaves free, and `/health` with them. Set
`--http-idle-timeout` on a listener exposed to clients you do not control. The other thing not
bounded here is memory, which a held call costs whatever the descriptor limit says: under a
large limit the container's memory limit (or `MemoryMax=` on a systemd unit) is the bound that
arrives first.

### Stateful sessions

On `--stateless=false` a session lives until its client deletes it or `--session-timeout`
closes it, and `initialize` is metered to no bucket, so sessions were bounded by nothing. Each
may also hold its standalone stream, a `GET` the SDK serves for the session's whole life, and
enough of those take every descriptor too.

So the process keeps at most **half the held-call figure** in sessions, and each session
**reserves one held-call slot for its stream when it opens**, whether the stream is ever opened
or not. That keeps the descriptor budget above whole, leaves calls at least half the held slots
when every session is open, and means a session the process keeps is never refused its stream —
which matters, because a client refused its standalone stream does not ask again and silently
loses everything the server sends outside a response.

A `POST` with no `Mcp-Session-Id` is what opens a session, and it is refused past the ceiling
in the words and the shape of the `2026-07-28` refusal above: `503`, `-50300`, `Retry-After: 30`,
the connection closed, and no session id. A request on a session that is already open is never
refused by this ceiling. The line is `request refused: too many stateful sessions across the
process`, with `limit_stateful_sessions`, and the startup line adds
`stateful_sessions_per_process`.

**Filling it costs a caller nothing**, since `initialize` spends no rate and an idle session
holds no connection, so one caller can hold every session for up to `--session-timeout`. With
`--session-timeout=0` a session nobody deletes keeps its slot until the process restarts, and
once every slot is held that way every new client is refused; startup warns about exactly that.

## Draining before the listener closes

On shutdown, `GET /health` flips to `503` with `{"status":"draining"}` and `--drain-delay`
holds the listener open that long before closing it. The default is `0`, which closes at once.

**Size it against the probe interval of whatever is in front.** Below one interval the delay
does nothing: the balancer learns this instance is going by a connection failing, after it has
already sent work to it. There is no safe default, because only the deployment knows what polls
it — the image's own `HEALTHCHECK` runs every 30 seconds, an nginx upstream check and a
Kubernetes readiness probe each have their own. It is capped at five minutes; past that it is
not a handover but a shutdown that appears to hang, and every supervisor kills the process long
before it elapses.

The graceful phase after the listener closes is bounded at **8 seconds**, and the bracket is
tight from both sides. Below it sit the things a request may legitimately still be doing — a
resolve inside `LIBGEN_MCP_RESOLVE_BUDGET`, a transfer inside
`LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT`. Above it sits the supervisor's own grace: ten seconds for
`docker stop`, thirty for a Kubernetes pod, after which the process is killed and the budget is
academic. Eight leaves room for closing the remaining connections and exiting inside the
shortest of those, and a `--drain-delay` adds to the same budget, so raise the supervisor's
grace with it. It is a ceiling and never a floor, so a process told it has less closes its listener
instead of being killed mid-drain.

**The first SIGINT or SIGTERM starts the drain, and a second one after it ends the process at
once**, drain and all — the way out for an operator who has decided not to wait. A repeat that
arrives **within one second** of the first is not that: it is the same stop delivered twice, which
is how a stop usually arrives. A terminal's Ctrl+C reaches the whole foreground process group,
systemd's default `KillMode=control-group` signals every process in the unit, and the npm
launcher relays the stop it received, so the copies land milliseconds apart. The server logs
such a repeat at `DEBUG` and keeps draining, and logs a forced exit at `WARN`. A supervisor that
signals the whole group therefore gets the drain either way; up to 2.1.0 the second copy ended
the server before it had begun, and `KillMode=mixed` was the way around it.

## Closing what has gone idle

Two timeouts, at two layers, and the distinction is the whole of it: one is about a TCP
connection between requests, the other about an MCP session between calls.

**`--http-idle-timeout` closes a kept-alive connection** that has gone that long with no request
on it. The default is `0`, which closes none — the behaviour this server had before the flag
existed, so upgrading moves nothing. What it bounds is the gap *between* requests, never a
response being written, so **an SSE stream is not what it reclaims**: a stream is a response in
progress for as long as it lasts. Set it when something in front holds connections open longer
than it uses them and the file descriptors matter; leave it at `0` otherwise.

**`--session-timeout` closes a stateful MCP session** whose client has stopped calling, and it
exists only under `--stateless=false`. The default transport ends each POST's session with its
own response, so there is nothing there to idle — which is why passing this flag with the
default transport **fails startup** rather than being quietly ignored. Where sessions do
survive, the only other thing that ends one is a `DELETE` the client may never send, so a client
that crashed holds its session for the life of the process. The default is 30 minutes, `0`
closes none, and it is capped at 24 hours; past a day the setting is not reclaiming anything a
deployment outlives. A session held that way also holds one of the process's
[session slots](#stateful-sessions), which is why `0` is warned about at startup.

## The configuration digest

`GET /health` answers `200` with `status`, `version`, `commit`, `build`, `config_digest`,
`started_at` and `uptime_seconds` while the server is serving. Two of those are worth knowing
about before you run more than one replica.

`started_at` is the stable fact — byte-identical across probes, so a monitor can cache it and
detect a restart by noticing it moved. `uptime_seconds` is the derived convenience, and it is
also why `/health` carries no `ETag`: the body differs on every probe.

`config_digest` is twelve hex characters fingerprinting the settings that decide the served
surface and the answers it can give: the enabled sources, `extra_sources`, `server_fetch`
(which decides whether `read` is registered at all), `remote_downloads`, `enrich`,
`confirm_downloads`, the base path, statelessness, and the three limits that change what an
identical call gets back — `max_download_bytes`, `read_max_chars` and `read_default_pages`.

**"Enabled" means in the chain, not listed.** `LIBGEN_MCP_SOURCES` is half the decision:
`core` joins the chain only when `LIBGEN_MCP_CORE_KEY` is set, and `unpaywall` only when
`LIBGEN_MCP_UNPAYWALL_EMAIL` is. Either one changes the `source` enum `download` advertises, so
the digest covers the resulting set of source names. Two replicas with the same list and a CORE
key on only one of them report different digests, and naming `core` without a key reads the
same as not naming it, because the chain is the same.

**Replicas behind one balancer must agree on every one of them**, or a client gets a different
catalog depending on which node it reaches and nothing else notices. Comparing the digest
across replicas is the whole point of publishing it, and it is order-free wherever the setting
is a set.

**It is a fingerprint for comparison, not a secret.** The settings it covers are few and
public, so whoever reads it can work out which combination produced it. That is exactly why no
credential is in it. A key or an email reaches the digest only as the name of the source it
switches on — never its value, and never a hash of it, which would let anyone reading `/health`
test a guessed key offline. Two replicas holding different CORE keys report the same digest,
because they serve the same surface. An Anna's Archive key changes how `annas` resolves, not
whether it is in the chain, so it is not in the digest at all. What is deliberately out:
counters, credential values, and anything needing an upstream round-trip. That is also why the
endpoint needs no credential.

**Changing what the digest covers changes its value.** Upgrading to a release that adds an
input moves every deployment's digest once, whatever its configuration. Upgrade the replicas
together, or expect a mixed fleet to read as mismatched until the last one is replaced.

## Two cards, and which one a scanner wants

An HTTP deployment publishes two server cards, at two paths, because two specifications
describe two documents. Which one to point something at depends on what it is:

| If it wants                                            | Point it at                             | It gets                                                                                             |
| ------------------------------------------------------ | --------------------------------------- | --------------------------------------------------------------------------------------------------- |
| To discover the deployment: name, version, where it is | `GET /server-card`                      | The SEP-2127 discovery card, plus a `remotes` entry when `--public-url` names one                   |
| The whole surface without connecting                   | `GET /.well-known/mcp/server-card.json` | The older enumerating card: `serverInfo`, capabilities, and the full `tools` and `prompts` listings |

A registry or directory wants the first. A scanner enumerating what this endpoint exposes wants
the second, and that is the one to keep reachable through a proxy that filters `.well-known`.

Both are unauthenticated, both answer `Access-Control-Allow-Origin: *` so a browser-based
directory can read them, and both carry a strong `ETag` over their own bytes — so two replicas
that rendered the same document publish the same validator and a revalidation after the hour
their `Cache-Control` allows costs a `304`. Both move with `--http-path`.

**The discovery card answers after every form of the endpoint.** The server-card extension
reserves `<streamable-http-url>/server-card`: the suffix goes on the endpoint's URL, not on the
host. The endpoint answers at the mount and at its [`/mcp` alias](#the-mcp-alias), so the card
does too — `GET /server-card` and `GET /mcp/server-card`, or `GET /libgen/server-card` and
`GET /libgen/mcp/server-card` under `--http-path=/libgen` — with the same bytes and the same
`ETag` on each. Whichever form `--public-url` publishes, the card URL a client derives from it
answers.

**`remotes` is absent unless `--public-url` is given**, and that absence is deliberate: a listen
address is frequently loopback or a socket behind a proxy, and publishing it would send a client
somewhere it cannot go. If a directory reports this deployment as having no remote endpoint,
that flag is the answer. What each document contains, and why the discovery card lists no tools,
is in [Architecture](architecture.md#stateless-mode).

### Publishing an AI Catalog

A client doing domain-level discovery starts at `https://<host>/.well-known/ai-catalog.json`, an
[AI Catalog](https://github.com/Agent-Card/ai-catalog) listing what the host publishes, and
follows the entries whose `type` is `application/mcp-server-card+json` to their server cards.
That is the discovery mechanism the
[server-card extension](https://github.com/modelcontextprotocol/experimental-ext-server-card/blob/main/docs/discovery.md)
describes.

**The binary does not serve a catalog, and that is deliberate.** `GET
/.well-known/ai-catalog.json` answers the same `404` as any other path the server does not
serve. A catalog describes everything a host publishes, which only whoever runs the host knows:
this server is often one entry among several, behind a prefix, on a host that serves other things
too. The catalog belongs to the deployment, and the proxy in front of the server is the natural
place to serve it from.

The entry for this server is three members:

```json
{
  "specVersion": "1.0",
  "entries": [
    {
      "identifier": "urn:air:example.org:mcp:libgen",
      "type": "application/mcp-server-card+json",
      "url": "https://mcp.example.org/libgen/server-card"
    }
  ]
}
```

- **`url` is `<--public-url>/server-card`.** With `--public-url=https://mcp.example.org/libgen`
  that is `https://mcp.example.org/libgen/server-card`, the same URL a client derives from the
  card's own `remotes[0].url`.
- **`identifier`** follows the `urn:air:{publisher}:{namespace}:{name}` form the catalog
  specification requires for open systems, where `{publisher}` is the domain of the organization
  publishing the artifact. The artifact this entry lists is your deployment's card, which you
  publish, so `{publisher}` is your domain rather than this project's. Deriving it from the card's
  `name` instead would name the project, and two deployments doing so would list one identifier
  for two different endpoints.
- **No `displayName` and no `description`.** The card carries `title` and `description` itself,
  and the catalog specification says an entry pointing at an artifact that names itself should
  omit both: a copy in the catalog drifts out of step with the card at the next upgrade, and when
  present it wins over the card's.

Serve the file from the proxy under its own media type. With nginx that needs `types { }` as well
as `default_type`: nginx picks the type from the file's extension first, and `mime.types` maps
`.json` to `application/json`, so `default_type` alone is never reached. Clearing the extension
map for this one location is what lets it apply:

```nginx
location = /.well-known/ai-catalog.json {
    alias /etc/nginx/ai-catalog.json;
    types { }
    default_type application/ai-catalog+json;
    add_header Access-Control-Allow-Origin "*" always;
    add_header Cache-Control "public, max-age=3600" always;
}
```

This is the one location where the proxy answers CORS itself, because it is the one document the
proxy serves rather than forwards. Every path it forwards gets its CORS headers from the server,
and a second set from the proxy produces a response a browser rejects. The catalog may also live
on another domain entirely: an entry's `url` can name a card on any host.

## Where the server listens

The path-separator rule is above. What follows is what each kind of listener needs.

### A unix socket, for a proxy on the same machine

Prefer a socket to TLS when the only thing that talks to this server is a proxy on the same
host. Both make the hop unreadable, but not in the same way: TLS encrypts the network segment,
a socket removes it. There is no bridge to read, no `docker-proxy` hop, no port another local
process can reach or race for, and no certificate to issue, distribute or rotate — the
permission bits on one inode are the whole access-control story.

One thing surprises people: a unix socket is still a non-empty `--http` value, so `download`
stays in remote (link-only) mode and `read` is not registered. That is correct — a socket behind
a proxy serves clients that are not on this machine — but "unix socket" reads as "local", and it
is not.

### Socket permissions

`--http-socket-mode` sets the mode as octal with or without a leading `0`, and defaults to
`0660`: read and write for the owner and the group, nothing for anyone else. Group-only is the
default because a socket is reached through the filesystem, so the alternative to it is not
"localhost only" but "every local account". The proxy joins the socket's group instead.

In nginx that means the *worker* processes: nginx calls `initgroups()` when it drops privileges,
so on a host it picks up a group its user was added to in `/etc/group`, and in a container it
discards a container-level `group_add`, so in a container it is simpler to run the server with
the proxy's group, as [Containers](deploy/containers.md#nginx-in-front-over-a-shared-unix-socket)
does. [Behind a reverse proxy](deploy/reverse-proxy.md#unix-socket-upstream) has the host
recipe and the `user` directive mistake to avoid. The blunter alternative is `--http-socket-mode 0666`,
reasonable only when the socket's own directory is already restricted, since traversal
permission on the directory gates access before the socket's mode is consulted.

The mode is applied twice and both halves matter. The kernel creates the inode as
`0777 &^ umask`, so the bind runs under a narrowed umask to keep the socket from ever existing
world-connectable, and a `chmod` right after makes the mode exact, since a umask can only clear
bits. An explicit value is refused at startup when `--http` is a TCP address, and on a platform
with no file permission modes, rather than accepted and quietly ignored.

### A file already at that path

Three cases, deliberately different. A path that exists and is **not** a socket is refused and
never removed — the operator pointed at something, and replacing it is not this program's call.
A socket a live process still answers on, probed with a 200 ms connect, is refused too, so a
second instance cannot silently steal the endpoint the first one is serving. A socket nothing
answers is what a crashed predecessor leaves behind, so it is removed with a warning saying so.
A clean shutdown unlinks the socket itself.

### Terminating TLS in this process

When the proxy is on another host there is a network segment that has to exist, and this server
can terminate TLS itself:

```bash
libgen-mcp --http 0.0.0.0:8443 --tls-cert /etc/ssl/mcp.crt --tls-key /etc/ssl/mcp.key
```

Both flags or neither: a certificate without a key is a deployment that believes it is serving
TLS and is not, so the half-pair is refused at startup. The pair is also *loaded* at startup
rather than at the first handshake, so an unreadable file or a key that does not match its
certificate is a named startup error instead of a handshake failure days later, by which time
nobody is watching the log.

The listener pins TLS 1.2 as the floor and advertises `h2` and `http/1.1` over ALPN. This is
also the only case in which the server sends `Strict-Transport-Security`: behind a proxy that
terminates TLS, that proxy is the layer that knows its own certificate lifetime and sends its
own.

A certificate from a private CA needs the proxy in front to be told to trust it. In nginx that
is `proxy_ssl_verify on` **plus** `proxy_ssl_trusted_certificate` naming the CA file:
verification is off by default there, and turning it on without a CA file fails every
connection instead.

### A renewal is two file writes, not a restart

The pair rides behind `tls.Config.GetCertificate`, and each handshake first stats both files:
unchanged, the certificate already in memory is presented and nothing is read; changed, the
pair is re-read and served from that handshake on, while connections already open keep the old
one until they are replaced. So certbot, Vault's agent and a remounted Kubernetes secret are
all writes this server picks up on its own.

A restart would do the same thing at a much higher price: every download in flight is cut, and
the temp cache that would have served the retry goes with the process.

The check is a stat rather than a timer or a signal — a timer needs an interval to explain and
leaves a window, and a signal needs somewhere to send it, which a container whose renewal is an
updated mounted secret does not have. **A reload that fails keeps the previous certificate** and
logs one warning per distinct broken state, because refusing the handshake would turn a routine
renewal into an outage lasting as long as one file write. The first load is the exception and
stays strict: startup is the one moment there is nothing to fall back to and someone is
watching.

## Probing an instance that is already running

`libgen-mcp --healthcheck` finds the running instance on this machine, reads `--http`,
`--http-path`, `--tls-cert` and `--transport` off its command line, derives where `/health` is
served and asks — exiting `0` when it answers, `1` when it does not, `2` for a target that does
not parse. The image's `HEALTHCHECK` runs exactly that, every 30 seconds with a five-second
timeout; one attempt is bounded at three seconds and the whole run at four, so the check answers
rather than being killed without a verdict.

**It reads the instance rather than restating its configuration**, which is what makes it work
on a deployment configured entirely through the environment: a `curl` or `wget` line in the
image would have to repeat the flags, and gets four of the five listener shapes wrong — another
port, a unix socket, TLS this process terminates, and a mount under `--http-path` all serve
perfectly while such a probe reports unhealthy, and an orchestrator then restarts a container
whose restart changes nothing.

An `https` listener is verified against the certificate `--tls-cert` names, as the only trusted
root, asking for a name that certificate carries — so a self-signed certificate on a loopback
address verifies the standard way. An instance serving stdio has no listener to probe and is
reported healthy while it runs. The one listener it cannot discover is one bound to port `0`,
whose real port is known only to the kernel and to the server's own log; pass the target
outright there, which is also what a probe run from outside the container does:

```bash
libgen-mcp --healthcheck http://127.0.0.1:8080/health
libgen-mcp --healthcheck unix:/run/mcp-libgen.sock
libgen-mcp --healthcheck 127.0.0.1:8080
```

This is **not** `cmd/probe`, which is a maintainer's live diagnostic against the real mirrors.

Its neighbour is `libgen-mcp --shutdown`, which asks every other instance of this binary on the
machine to exit and kills what is left after five seconds, or after its drain plus the shutdown
phase when it has a `--drain-delay` ([`--shutdown`](cli.md#operations)). It exists for the upgrade: swapping
the binary under an npm launcher, a `.mcpb` bundle or a plain `cp` leaves the old process
holding a download slot, a temp-cache entry and the listener the new one wants. Binary names are
compared with the platform suffix stripped, so `libgen-mcp-linux-amd64` matches `libgen-mcp` and
neither matches somebody else's process on a shared host.

## One image, two transports

The published image decides its transport from what standard input is, because one image has to
serve both an MCP client and a published port:

```dockerfile
CMD ["--transport", "auto", "--http", "0.0.0.0:8080"]
```

`docker run -i` connects a pipe and gets a stdio server, which is what an MCP client starts. A
run without `-i` — a compose file with no `stdin_open`, a Kubernetes pod, anything an
orchestrator starts — connects `/dev/null` and gets the HTTP listener the image's `EXPOSE 8080`
advertises. Before `auto` it got a stdio server reading `/dev/null`, which is a process that is
up, healthy and unreachable.

The inference asks "did anybody hand this process a stdin at all", not "is this a terminal": a
terminal and `/dev/null` are both character devices. Only `/dev/null` means HTTP; a pipe, a
terminal, a regular file and a socket all mean stdio, and so does an unrecognized shape, because
that error is visible in seconds while the other one is a client hanging with no output.

**Any argument replaces `CMD` wholesale**, so `docker run <image> --http :9000` drops
`--transport auto` with it — which is fine, since naming a listener is deciding the transport.
`--transport stdio` and `--transport http` state the choice outright for a deployment that
would rather not depend on how it was started; `--transport stdio` alongside a `--http` value
warns rather than ignoring it, because an address that quietly does nothing is the hardest kind
of misconfiguration to notice. The other HTTP-only flags are named the same way when the
transport resolves to stdio, one line each on stderr, and the server serves anyway (see
[Troubleshooting](troubleshooting.md#a-stdio-server-says-a-flag-has-no-effect)).

## The `/mcp` alias

The MCP endpoint is the mount itself — `POST /` by default — and `POST /mcp` is an alias for
it. Enough clients and enough guides assume that path that a base URL pasted with or without it
should reach the same place rather than a `404` that reads as "this server does not speak MCP".

It travels with `--http-path`, so under `--http-path=/libgen` the endpoint is `POST /libgen` and
the alias is `POST /libgen/mcp`. Both `/mcp` and `/mcp/` are mounted as exact matches. The
canonical spelling stays the mount itself: it is what every snippet here uses. The discovery card
follows both forms, so `GET /mcp/server-card` answers the same card as `GET /server-card`.

## What the server refuses to start with

A startup refusal is deliberate in every case below. A deployment that does not match its own
configuration must not reach production looking healthy, and each of these would otherwise be a
setting that appears to be in force and is not:

| Refused                                                                       | Because                                                                                 |
| ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES` on a listener other machines can reach   | Any caller that can POST could aim this server at addresses only your network can reach |
| `--tls-cert` without `--tls-key`, or the reverse                              | A deployment that believes it is serving TLS and is not                                 |
| `--trusted-proxies` without `--trusted-proxy-header`, or the reverse          | A header from an unnamed peer is text the caller wrote                                  |
| `--rate-limit-rps` on a loopback bind or socket with no trusted proxy         | The bound cannot do what the operator asked for, so saying so beats downgrading it      |
| `--session-timeout` under the default stateless transport                     | There is no session outliving a request for it to close                                 |
| A negative `--http-idle-timeout`                                              | It is not a duration, and `0` already spells "close nothing"                            |
| `--http-socket-mode` with a TCP `--http`, or on a platform with no file modes | A guarantee the platform cannot give                                                    |
| `--trusted-proxies unix` on a TCP address                                     | There is no unix peer to trust                                                          |
| `--http-path` carrying a query, fragment, `..` segment or percent-escape      | A server mounted on a path it can never match answers `404` to everything               |
| `--public-url` that is not an `http`/`https` URL with a host                  | It would advertise an endpoint nothing can reach                                        |
| A malformed `--trusted-origins` entry                                         | A dropped entry is an origin that silently stops working                                |
| A negative `--max-request-body-bytes`                                         | It would lift the cap entirely                                                          |
| A `LIBGEN_MCP_*` value that does not parse                                    | Falling back to the default in silence is how a mismatched deployment survives          |
| A non-socket file, or a live socket, at the `--http` path                     | Replacing it is not this program's call, and stealing a live endpoint is worse          |

Every refusal of a configuration exits with status 1, and the last record the server writes is the reason, at
`ERROR`, with nothing written to stdout. A command line the flag parser cannot read, such as an
unknown flag or `--drain-delay 5` with no unit, is refused the same way after the parser prints
its usage, and exits with the parser's own status, 2. Up to 2.0.1 every one of these records went
out at `INFO`, and up to 2.1.0 the refusals of a flag or of an HTTP variable still did, so a log
filter for errors showed nothing about why the process died.

The first one deserves a note, because the container case is easy to miss: the image's default
listener binds `0.0.0.0:8080`, which is exactly the shape the refusal is about, even when the
port is published on the host's loopback. Bind a loopback address or a unix socket, or unset the
variable — a mirror named in `LIBGEN_MIRROR` or `LIBGEN_MCP_SCIHUB_HOSTS` is already exempt from
the address guard without it.

## Where the rest of it is documented

| For                                                       | See                                                             |
| --------------------------------------------------------- | --------------------------------------------------------------- |
| Every flag and its `LIBGEN_MCP_*` variable, with defaults | [Architecture → Stateless mode](architecture.md#stateless-mode) |
| Routes, security headers, CORS, cancellation, cache hints | [Architecture → Transports](architecture.md#transports)         |
| Complete proxy configurations                             | [Behind a reverse proxy](deploy/reverse-proxy.md)               |
| systemd units, launchd and Windows services               | [Run as a service](deploy/service.md)                           |
| Compose files and Kubernetes manifests                    | [Containers and orchestration](deploy/containers.md)            |
| Every environment variable this server reads              | [Configuration](configuration.md#http-listener)                 |
| What a remote deployment does to `download` and `read`    | [Tools](tools.md#where-the-file-goes-local-vs-remote)           |
| What an exported signal may say about a caller            | [Telemetry](telemetry.md)                                       |
| A symptom you are looking at right now                    | [Troubleshooting](troubleshooting.md)                           |
