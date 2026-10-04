# Scaling and capacity

**Explanation** — for an operator sizing a shared deployment.

One `libgen-mcp` process serving streamable HTTP is a shared service: every caller shares one
outbound budget, one download semaphore and one cache. This page explains what bounds that
process, what changes when you run more than one, and what a balancer or an MCP gateway in front
of them has to get right. It links rather than restates:

- [HTTP server mode](../http-server-mode.md) owns the behaviour of one process: every limit,
  refusal and probe named here is defined there.
- [Behind a reverse proxy](reverse-proxy.md) and [Containers and orchestration](containers.md)
  have the complete configurations.
- [What it costs to run](../benchmarks/resource-benchmark.md) has the measured memory, CPU and
  latency figures.

## The upstream is the bottleneck, not the process

Every catalog request this server makes waits for a token from one bucket per process:
`LIBGEN_MCP_RATE_RPS` (default `1`, accepted in `(0, 20]`) refilled into a bucket
`LIBGEN_MCP_RATE_BURST` deep (default `1`). A `tools/call` that reaches the catalog holds its
`POST` open while it waits, so at the shipped rate sixteen searches in flight take about fifteen
seconds to drain, and a thousand take over a quarter of an hour. Nothing the server does on its
own side comes close to that cost.

**An inbound limit above the outbound bucket only moves the queue.** The inbound limit
(`--rate-limit-rps`, default `10` per charged address) decides whether a caller is refused or
admitted; once admitted, the call queues for the outbound token like every other. Raising the
inbound limit, or adding callers, makes the queue longer without making it drain faster. The
benchmark measures exactly this pair: the `http-8` scenario opens the outbound valve to `20`
requests per second and `http-8-shipped-rate` leaves it at `1`, with the same eight clients
making the same calls. Compare their `tools/call` rows in
[Latency per method](../benchmarks/resource-benchmark.md#latency-per-method): milliseconds in
the first, seconds in the second, and what the second measures is the queue, not the server.

Two things do not wait on that bucket. The searchers beyond the catalog (`extra_sources`) run
concurrently, each paced by a bucket of its own upstream that the whole process shares, and a
download's transfer occupies a slot of `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS` (default `2`) for as
long as the bytes take. A searcher's pace never holds a search back: a search whose token for an
upstream is more than a second away answers without that upstream, so under concurrent load
arXiv (one request every 3 seconds) and dblp (one every 10) contribute to some searches and are
skipped by the rest. The rates are in
[What one caller may ask for](../http-server-mode.md#what-one-caller-may-ask-for).

So before adding a replica, ask which resource is short. If calls are slow because they queue
for the outbound token, a second replica doubles the rate the mirrors see rather than the
capacity they grant (see [What the mirrors see](#what-the-mirrors-see)). A replica adds
capacity only for what is genuinely per process: held connections, memory, CPU and download
slots.

## What bounds one process

### Held calls and sessions, from the descriptor limit

The process holds at most a number of calls derived from its file-descriptor limit, read once
at startup, and no flag moves it. The arithmetic is short:

```text
held calls        = (limit - limit/8 - 64) / (2 + extra searchers)
stateful sessions = held calls / 2
```

An eighth of the limit is left spare for the idle process, `/health` and the connections being
refused. Sixty-four descriptors are reserved for the process-wide ceiling on byte-moving calls
(`download` and `read`), each of which opens a file on top of its connection. What remains is
divided by what one held call can cost at most, which is an escalated `search`: the caller's
connection, the catalog request, and one connection for each of the ten searchers beyond the
catalog, twelve in all. The divisor is counted from the provider list, so a provider added in a
release lowers the ceiling with it. Both results are at least `1`.

A worked example, for a container started with `--ulimit nofile=4096`:

```text
spare       4096 / 8               =  512
reserved    64 byte-moving slots   =   64
left        4096 - 512 - 64        = 3520
held calls  3520 / 12              =  293
sessions    293 / 2                =  146   (counted only with --stateless=false)
```

The Go runtime raises the soft limit to the hard one before `main`, so the figure follows the
**hard** limit: 69 held calls under 1024, 38224 under the 524288 a default systemd service gets.
The startup line `process ceilings` prints what this process was given
(`held_calls_per_process`, `descriptor_limit`, `descriptor_limit_source`), and the full table,
the refusal (`503`, `Retry-After: 30`, `This server is busy. Retry later.`) and what counts as a
held call are in
[HTTP server mode → What the whole process may hold](../http-server-mode.md#what-the-whole-process-may-hold).

Twelve descriptors is the bound, not the typical cost: a search still queued for its catalog token
holds only its caller's connection. That is why the ceiling is rarely what a deployment meets
first.

### Memory and CPU

Under a large descriptor limit, memory arrives first. A held call costs memory whatever the
limit says, so the container's memory limit (or `MemoryMax=` on a systemd unit) is the real
bound on a host with a hard limit in the hundreds of thousands. The measured cost of the idle
process, of each extra caller under load and held, and the CPU per call are in
[What each extra caller costs](../benchmarks/resource-benchmark.md#what-each-extra-caller-costs).
Size the memory limit from those figures and the number of callers you expect at once, not from
the held-call ceiling.

### Downloads

`download` and `read` take a slot of `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS` (default `2`,
accepted in `[1, 16]`) and queue for one when all are taken. `--max-inflight-per-client` bounds
how many one charged address may hold, defaulting to the whole semaphore, and a ceiling of `64`
per process, which no flag changes, sits above both. The details are in
[What one caller may hold](../http-server-mode.md#what-one-caller-may-hold). On an HTTP
deployment `read` is not registered and `download` returns a link unless
`LIBGEN_MCP_SERVER_FETCH` says otherwise, so by default no transfer runs in the process at all.

## What multiplies with replicas

Every limit this server enforces lives in one process. The [one-server-for-every-caller
ADR](../decisions/2026-09-17-one-server-for-every-caller.md) records why: the outbound limiter,
the per-source cooldowns, the mirror cache and the `read` cache are fields of objects built once
per process, and nothing is shared between processes. Run `N` replicas and each limit becomes
`N` independent copies:

| Limit                                                    | Scope                            | With `N` replicas behind one balancer                         |
| -------------------------------------------------------- | -------------------------------- | ------------------------------------------------------------- |
| `LIBGEN_MCP_RATE_RPS` / `LIBGEN_MCP_RATE_BURST`          | Per process, outbound            | `N` times the request rate reaches the mirrors                |
| The pace of each searcher beyond the catalog             | Per process, per upstream        | `N` times the request rate reaches arXiv, PubMed and the rest |
| `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS`                    | Per process                      | `N` times the concurrent transfers                            |
| `--rate-limit-rps` / `--rate-limit-burst`                | Per charged address, per process | Up to `N` times per caller, unless the balancer pins a caller |
| `--max-inflight-per-client`                              | Per charged address, per process | Up to `N` times per caller, unless the balancer pins a caller |
| Held calls, stateful sessions, the 64 byte-moving calls  | Per process                      | `N` times, which is the capacity a replica adds               |
| The `read` cache, the mirror cache, per-source cooldowns | Per process                      | Each replica learns on its own, and fetches on its own        |

### What the mirrors see

The mirrors and providers this server reaches are run by volunteers and public institutions, and
they see the deployment's egress address, not its replicas. Replicas behind one NAT or one
cloud egress are one client to them at `N` times the rate one process would send. A
deployment's egress address shared by every user is also why a remote server returns links
rather than fetching file bodies: a block provoked by one caller lands on everybody
([`LIBGEN_MCP_SERVER_FETCH`](../configuration.md#libgen_mcp_server_fetch)).

**Divide the budget rather than multiplying it.** The variable accepts fractions, so four
replicas that should together send what one process sends at the default run with
`LIBGEN_MCP_RATE_RPS=0.25`. Each replica's queue then drains four times slower, which is the
point: replicas buy you held connections and memory, and the mirrors' tolerance is the same
however many processes ask.

**OpenAlex counts the other way.** Its keyless allowance (1000 credits a day, reset at midnight
UTC) is counted per address by OpenAlex itself, and every process reads what is left from the
headers of OpenAlex's answers. Replicas behind one egress address therefore spend one allowance
between them rather than one each. A search costs 10 credits, and without a key a replica stops
searching OpenAlex once a search would leave fewer than 100, which it keeps for `get_details`'
`related` lists. With
[`LIBGEN_MCP_OPENALEX_KEY`](../configuration.md#libgen_mcp_openalex_key) the allowance is the
key's, and the reserve does not apply.

### What a caller sees

The inbound limits are keyed on the charged address in each process. A balancer that spreads one
caller's requests round-robin gives that caller a full bucket on every replica, so `N` replicas
admit up to `N` times the rate and `N` times the in-flight downloads the flags say. Affinity by
client address (`ip_hash` in nginx, `balance source` in HAProxy) keeps each caller on one
replica, and with it keeps the flags meaning what they say per caller.

## Sessions and affinity

**The default transport needs no affinity.** With `--stateless` at its default of `true`, no
`Mcp-Session-Id` is issued and every `POST` is self-contained, so any replica can serve any
request. That includes a call that has to ask the user something: on protocol `2026-07-28` the
question travels back in the tool result and the client calls again with the answer, so the
retry carries everything the handler needs and may land on another replica. Affinity there is an
optimisation, not a requirement. It keeps a caller's inbound budget in one place and, where
`read` is registered, lets the next page of a document reuse the copy the
[`read` cache](../configuration.md#libgen_mcp_read_cache_bytes-and-libgen_mcp_read_cache_ttl)
already holds instead of downloading the whole file again on another replica.

**`--stateless=false` requires it.** The legacy stateful transport, for clients on protocol
`2025-11-25` or older, keeps each session in the memory of the process that opened it. A request
carrying that session's id on any other replica is answered `404` with `session not found`, and
the client has to initialize again. Pin by client address: a hash of the `Mcp-Session-Id` header
does not work, because the `POST` that opens a session carries no id and is routed somewhere
other than where the id later sends its follow-ups.

Two flags bound what a stateful deployment keeps, and both are defined in
[Closing what has gone idle](../http-server-mode.md#closing-what-has-gone-idle):

- `--session-timeout` (default `30m`, `0` to `24h`) closes a session whose client has stopped
  calling. `0` closes none, and startup warns, because a session nobody deletes keeps one of the
  process's [session slots](../http-server-mode.md#stateful-sessions) until a restart. It is
  refused under the default stateless transport.
- `--http-idle-timeout` (default `0`) closes a kept-alive connection idle between requests. It
  matters for any transport behind a balancer that holds connections open, because an idle
  connection holds a descriptor of the limit the held-call ceiling leaves free for `/health`.

A replica that stops takes its stateful sessions with it, so a rolling update of a stateful
deployment makes every client on the restarted replica initialize again.

## Telling callers apart behind a balancer

Every per-caller limit is keyed on the address a request is charged to, and behind a balancer the
connection's peer is the balancer. Set the trusted-proxy pair on **every** replica, naming the
balancer's address (or every hop, with two layers of proxies) and the header it fills:

```bash
libgen-mcp --http 0.0.0.0:8080 \
  --public-url https://mcp.example.org \
  --trusted-proxies 10.0.0.0/24 \
  --trusted-proxy-header X-Forwarded-For
```

The two flags are required together. The header is believed only from a peer listed in
`--trusted-proxies`, and a multi-valued header such as `X-Forwarded-For` is read **from the
right**: the first hop that is not itself a trusted proxy is the caller, so a caller cannot
choose its own address by sending the header. A single-valued `X-Real-IP` works the same way.

Leave the pair off and every caller is charged to the balancer: one bucket and one in-flight
count for the whole deployment, one `user.hash` in the telemetry, and on a loopback or unix
socket listener no inbound limit at all, since there every peer is the same machine. The
identity rules, and how each listener shape changes them, are in
[What identity this deployment has](../http-server-mode.md#what-identity-this-deployment-has).
`--public-url` belongs on every replica too: it names the `Host` the server answers to behind a
proxy, and the address the discovery card publishes.

## Rolling updates

A stop signal does not close the listener at once. The sequence, defined in
[Draining before the listener closes](../http-server-mode.md#draining-before-the-listener-closes),
is:

1. The first `SIGTERM` or `SIGINT` flips `GET /health` to `503` with `{"status":"draining"}` and
   `Cache-Control: no-store`. The MCP endpoint keeps serving.
2. After `--drain-delay` (default `0`, capped at `5m`) the listener closes.
3. Requests still running get up to **8 seconds** to finish. Streams still open after that are
   closed, and the process exits.

A repeated signal **within one second** of the first is taken as a copy of the same stop (a
process group, a cgroup and the npm launcher all deliver copies milliseconds apart), so the
drain still runs. A signal after that window forces the exit at once.

Three settings have to agree for a replica to leave without failing a request:

- **The balancer has to poll `/health`.** The flip only helps a balancer that asks. nginx open
  source has no active health check: its `max_fails` is passive, so it removes an upstream only
  after real requests to it fail. Use a balancer that polls, a Kubernetes readiness probe, or
  take the replica out of the upstream and reload before signalling it.
- **`--drain-delay` is at least one probe interval** of whatever polls, plus the time it takes
  to act on a failed probe. Below one interval the delay does nothing.
- **The supervisor's grace exceeds `--drain-delay` plus 8 seconds.** `docker stop` waits 10
  seconds by default and a Kubernetes pod 30, after which the process is killed mid-drain. Raise
  `--stop-timeout` or `terminationGracePeriodSeconds` with the drain delay.

`/health` answers any `Host` and takes no call slot, so a full process still answers its probe.
The image's own `HEALTHCHECK` and complete Compose and Kubernetes manifests are in
[Containers and orchestration](containers.md).

**A balancer should retry only what never reached a replica.** Every tool here is annotated
idempotent, so a replayed `tools/call` corrupts nothing, but it spends another outbound token
and, for a `download` on a deployment that fetches, a second transfer. Retry on a connection
failure, not on a timeout after the request was sent.

## Detecting configuration drift

Replicas behind one balancer must serve the same catalog, or a client sees different tools
depending on which one it reaches and nothing else reports it. `GET /health` carries
`config_digest`, twelve hex characters over the settings that decide the served surface; which
settings it covers is in
[HTTP server mode → The configuration digest](../http-server-mode.md#the-configuration-digest).
It is a fingerprint, not a secret. Compare it, and `build`, across the fleet:

```bash
for replica in 10.0.0.11:8080 10.0.0.12:8080 10.0.0.13:8080; do
  curl -fsS "http://$replica/health" | jq -r '[.build, .config_digest] | @tsv'
done | sort -u
```

One line out means the replicas agree. During a rolling update two lines are expected, one per
build.

**The digest sees which credentials are set, never what they are.** `LIBGEN_MCP_UNPAYWALL_EMAIL`
and `LIBGEN_MCP_CORE_KEY` each gate a download source, and a gated source is absent from
`download`'s `source` enum. The digest covers the sources actually enabled, so two replicas where
only one holds the CORE key publish different digests. Two replicas holding different keys
publish the same one, because they serve the same catalog: the values are never in the digest,
hashed or otherwise. A key that is set on every replica but wrong on one is therefore invisible
here, and shows up only as that replica's downloads failing.

The enumerating server card is the second check, and the exact one. It carries the full
`tools/list` output, and its strong `ETag` is computed over its bytes, so replicas that serve the
same catalog publish the same validator whatever produced it.

```bash
for replica in 10.0.0.11:8080 10.0.0.12:8080 10.0.0.13:8080; do
  curl -fsSI "http://$replica/.well-known/mcp/server-card.json" | grep -i '^etag'
done | sort -u
```

What neither covers is configuration that changes behaviour rather than the catalog: the inbound
limits, the trusted-proxy pair, `LIBGEN_MCP_RATE_RPS` and the telemetry settings. Keep those in
one environment file or one manifest that every replica reads, rather than per-host flags. The
field list and its reasoning are in
[The configuration digest](../http-server-mode.md#the-configuration-digest).

## Telemetry across replicas

Each process reports a random `service.instance.id` in its resource, so two replicas sharing a
`service.name` are told apart in the collector without any setting.

With `LIBGEN_MCP_TELEMETRY_IDENTITY=pseudonymous`, `user.hash` is an HMAC of the charged address
under a key. Left unset, `LIBGEN_MCP_TELEMETRY_IDENTITY_KEY` is generated at startup and lost with
the process, so every replica reports one caller under a different pseudonym and a restart starts
over. Give every replica the same key from one secret store and the pseudonym is stable across
the fleet. A `user.hash` that never changes means the trusted-proxy pair is missing, as above.
Both are in
[The key decides whether replicas agree](../telemetry.md#the-key-decides-whether-replicas-agree).

## An MCP gateway in front

A gateway that admits, routes or caches MCP traffic sees the same server a client does. Four
things about this one matter to it.

**The served text is plain ASCII with no semicolon.** Every description, title, schema
description and prompt argument in `tools/list` and `prompts/list`, because a gateway refused a
sibling project over ordinary semicolons and a validator talking about "unsafe characters"
matches a character class. Results are data and are not held to it. See
[Compatibility → Gateways](../compatibility.md#gateways).

**No credential travels in a header.** This server authenticates nobody and reads no
`Authorization` header, so a gateway that injects one changes nothing. A deployment's keys are
environment variables on each replica, which is one more reason to give every replica the same
environment. The per-call alternative, an Anna's Archive key or an Unpaywall contact address for
one download, is asked of the user through elicitation, used for that request and never stored,
so it stays in the client's interface instead of the model's context. A gateway has to pass the
input request and the client's retry through unchanged for that question to reach anyone; where
it does not, the call goes on without the credential.

**There is no argument to route on.** No tool argument carries an `x-mcp-header` annotation, so
no `Mcp-Param-*` header mirrors one. Annotating `md5` would make `download` and `get_details`
uncallable from browser clients, as the
[param-header ADR](../decisions/2026-07-31-no-param-header-routing.md) records. On protocol
`2026-07-28` the `Mcp-Method` header names the method, which is enough to route listings and
calls differently. With the default stateless transport a gateway may send any request to any
replica.

**What a gateway may cache.** `tools/list` and `prompts/list` carry the
[SEP-2549](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549) hint
`ttlMs: 3600000` with `cacheScope: "public"`, since the catalog changes only with a release and
every caller is served the same one. The two server cards answer `Cache-Control: public,
max-age=3600` with a strong `ETag`, so revalidating after the hour costs a `304`. Tool results
carry no hint and are live data: never cache a `tools/call`. A shared cache in front of replicas
that disagree hands one replica's catalog to the clients of another, which is the drift above
made permanent for an hour. Which card a scanner or a registry wants is in
[Two cards, and which one a scanner wants](../http-server-mode.md#two-cards-and-which-one-a-scanner-wants).
