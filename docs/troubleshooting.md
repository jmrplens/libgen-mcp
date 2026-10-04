# Troubleshooting

**How-to guide** — for someone looking at a symptom right now.

The entries below are grouped by where the symptom shows up: in the client, at startup, on the
wire, behind a proxy, in a search, in a record lookup, in a download, in `read`, in a
collector, or during an install. Each one quotes what you see, says what it means here, says what to do, and links the
page that explains the mechanism. Every message in backticks is the server's own text, so
searching the log for it works.

Three checks answer most questions before any entry does:

- **Read the server's log.** It goes to **stderr**, never to stdout, and every startup
  refusal ends with one `ERROR` record that names the problem. Where a client keeps it is in
  [Raising the log level](#raising-the-log-level).
- **Run the client's command in a terminal.** Copy `command` and `args` from the client's
  configuration and run them. A stdio server that works sits there waiting for input and
  prints a note saying so. An error there is the server's; no error points at the client's
  configuration.
- **Check the version.** `libgen-mcp --version` prints the build. Several entries below
  depend on it.

## Quick index

| What you see                                                                   | Go to                                                                                                   |
| ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------- |
| The client shows the server as failed, or lists no tools                       | [The server does not appear](#the-server-does-not-appear-or-its-tools-are-missing)                      |
| `search`, `get_details` and `download`, but no `read`                          | [`read` is missing](#the-client-lists-three-tools-not-four-read-is-missing)                             |
| The process exits at once, the last line is an `ERROR`                         | [The server will not start](#what-a-refused-start-looks-like)                                           |
| `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES is set and --http …`                       | [The private-address hatch](#the-server-will-not-start-the-private-address-hatch-on-an-open-listener)   |
| `this flag is read by the HTTP transport only …`                               | [A flag "has no effect"](#a-stdio-server-says-a-flag-has-no-effect)                                     |
| HTTP `400`, `403`, `404`, `405`, `413`, `429` or `503`                         | [HTTP status codes](#http-status-codes)                                                                 |
| `502 Bad Gateway` from the proxy on a unix socket                              | [`502` on a unix socket](#the-proxy-gets-502-on-a-unix-socket-permission-denied)                        |
| Responses arrive all at once, or a stream is cut                               | [Buffered or cut streams](#responses-arrive-all-at-once-or-a-stream-is-cut)                             |
| A browser client reports a CORS error                                          | [CORS behind a proxy](#a-browser-client-reports-a-cors-error)                                           |
| `callers cannot be told apart …`                                               | [One budget for every caller](#every-caller-shares-one-rate-limit-budget)                               |
| `unable to get local issuer certificate`                                       | [TLS against a private CA](#tls-handshake-fails-against-a-private-ca)                                   |
| Clients still get the old certificate                                          | [A renewed certificate](#a-renewed-certificate-is-not-being-served)                                     |
| `refusing to connect to a private or local address`                            | [Behind an outbound proxy](#behind-an-outbound-proxy-refusing-to-connect-to-a-private-or-local-address) |
| `all libgen mirrors unreachable …`                                             | [All mirrors unreachable](#all-mirrors-unreachable)                                                     |
| Searches take seconds each, or queue behind one another                        | [Slow searches](#searches-are-slow-or-queue-behind-each-other)                                          |
| `truncated: true` and a `hint`                                                 | [Truncated search results](#truncated-search-results)                                                   |
| A result with `origin: "annas"` will not download                              | [Anna's Archive results](#a-search-result-that-will-not-download-origin-annas)                          |
| A provider never appears in `open_access`                                      | [A provider contributes nothing](#an-open-access-provider-contributes-nothing)                          |
| `OpenAlex rejected the configured API key …`                                   | [A provider contributes nothing](#an-open-access-provider-contributes-nothing)                          |
| `dblp search: the SPARQL service answered with a bot check …`                  | [A provider contributes nothing](#an-open-access-provider-contributes-nothing)                          |
| `year_filtered` and few or no catalog results                                  | [A year range empties the page](#a-year-range-empties-the-page)                                         |
| `citation_match` with `status: "unresolved"`                                   | [A citation that does not resolve](#a-pasted-citation-does-not-resolve)                                 |
| A `cite_as` style with `source: "local"` or `"unavailable"`                    | [Citation styles](#cite_as-comes-back-local-or-unavailable)                                             |
| `related` with no works and a note                                             | [Related works](#related-lists-nothing)                                                                 |
| `integrity check failed: MD5 mismatch`, or another download error              | [Download failed](#download-failed--md5-mismatch)                                                       |
| A DOI that will not download                                                   | [Article not found](#article-not-found-open-access-vs-sci-hub)                                          |
| `… is a retracted publication, so it is not served`                            | [Article not found](#article-not-found-open-access-vs-sci-hub)                                          |
| An ISBN that will not download                                                 | [Book not found by ISBN](#book-not-found-by-isbn-open-access-only)                                      |
| A source missing from a second attempt's errors                                | [A source in cooldown](#a-source-is-missing-from-the-errors-of-a-repeated-download)                     |
| `not enough free disk space in …`                                              | [Disk space](#disk-space)                                                                               |
| `download` returns a link instead of a file                                    | [Links instead of files](#download-returns-a-link-instead-of-saving-a-file)                             |
| `… is outside the allowed directories …`                                       | [A path outside the allowed directories](#a-path-is-outside-the-allowed-directories)                    |
| `read` answers `extractable: false`                                            | [A file `read` cannot extract](#a-file-read-cannot-extract)                                             |
| `section "…" matches … entries`, or another `section` refusal                  | [`section` refused](#section-is-refused)                                                                |
| Nothing reaches the OpenTelemetry collector                                    | [Telemetry not arriving](#nothing-reaches-the-collector)                                                |
| An install through npm, PyPI, NuGet, Homebrew, Docker, `.mcpb` or `go install` | [Install problems by channel](#install-problems-by-channel)                                             |

## Raising the log level

Most problems are easier to diagnose at `debug`, which traces each mirror attempt, cooldown,
and failover:

```bash
LIBGEN_MCP_LOG_LEVEL=debug libgen-mcp
```

Or in your MCP client's `env` block:

```json
{
  "mcpServers": {
    "libgen": {
      "command": "libgen-mcp",
      "env": { "LIBGEN_MCP_LOG_LEVEL": "debug" }
    }
  }
}
```

Logs go to **stderr** (stdout is reserved for the stdio MCP transport) as one JSON record per
line. Valid levels are `debug`, `info` (default), `warn`, and `error`. Where stderr ends up
depends on what started the server:

- **Claude Desktop** writes it to `mcp-server-libgen.log` in its log directory
  (`~/Library/Logs/Claude` on macOS, `%APPDATA%\Claude\logs` on Windows).
- **Most editors** show it in an output panel or a server-log view named after the server.
- **Docker** keeps it as the container's log: `docker logs <container>`.
- **systemd** keeps it in the journal: `journalctl -u <unit>`.

These are the server's own diagnostic logs, written straight to stderr. They are not the MCP
`logging` capability — that capability is deprecated (SEP-2577), this server declares no
handler for it, and no log line ever reaches a client as an MCP notification.

## The client does not see the server

### The server does not appear, or its tools are missing

**Symptom.** The client marks the server as failed or disconnected, lists no tools for it, or
its log says the command was not found (`spawn npx ENOENT`, `command not found`, `The system
cannot find the file specified`).

**Meaning.** One of three things, and the terminal check above tells them apart:

- **The client could not start the command.** A desktop client started from a dock or a start
  menu does not read your shell's `PATH`, so a bare `npx`, `uvx`, `dnx` or `libgen-mcp` that
  works in a terminal is not found there. Node installed through a version manager (nvm,
  fnm, volta) and binaries in `~/.local/bin`, `~/.dotnet/tools` or `/opt/homebrew/bin` are
  the usual cases.
- **The server started and refused its configuration.** It exits with status `1` and its
  last stderr line is the reason, at `ERROR`. See
  [What a refused start looks like](#what-a-refused-start-looks-like).
- **The configuration never reached the client.** A JSON error in the file, the wrong file,
  or a client that reads its configuration only at startup.

**Fixes.**

- Give `command` an absolute path: `which npx`, `which libgen-mcp` (or `where` on Windows)
  prints the one to use.
- On Windows, `npx` is a batch file, and a client that starts processes without a shell
  cannot run it. Wrap it: `"command": "cmd"` with `"args": ["/c", "npx", "-y",
  "@jmrp.io/libgen-mcp"]`.
- Quit the client completely and start it again after changing its configuration. Closing
  the window often leaves it running.
- Run the same command in a terminal. Started there, the server prints `libgen-mcp <version>
  is a Model Context Protocol server, not an interactive program.` and waits; that is the
  working state, and `Ctrl+C` ends it.

**How it works.** [Connect a client](clients.md) has every client's configuration, and
[Install problems by channel](#install-problems-by-channel) covers each launcher.

### The client lists three tools, not four (`read` is missing)

**Symptom.** A remote server shows `search`, `get_details` and `download`, and no `read`. A
`tools/call` for `read` is refused by the protocol as an unknown tool. (The public
[hosted endpoint](hosted.md) is the exception: its operator turned `read` on.)

**Meaning.** This is deliberate, and it is the default for a remote deployment. `read` cannot
return a single page without first pulling the whole file over the **server's** connection,
and a hosted server's egress IP is shared by every user it serves — one caller's transfers can
get that address throttled or blocked for everybody. So `LIBGEN_MCP_SERVER_FETCH` defaults to
off under `--http`, on a unix socket, and with `LIBGEN_MCP_REMOTE_DOWNLOADS=1`, and with it off
the tool is not registered at all rather than listed and failing every call.

**Fixes.**

- Call `download` instead: it returns a direct link (a `resource_link` plus a `resolved`
  object). Fetch that link with your own HTTP tool and read the file where it lands.
- `search`, `get_details` and `download`'s link resolution are unaffected — only the file body
  stays on your side of the connection.
- If you run the deployment and its egress is yours to spend (a private instance, or one that
  is not shared), set `LIBGEN_MCP_SERVER_FETCH=1` to get the tool back.
- On a local stdio server `read` is present by default. If it is missing there, something has
  set that variable to a false value.

**How it works.** [Configuration](configuration.md#libgen_mcp_server_fetch) and
[The hosted endpoint](hosted.md).

## The server will not start

### What a refused start looks like

**Symptom.** The process exits within a second of starting. A client reports the server as
failed, a container restarts in a loop, and the last line on stderr is an `ERROR` record.

**Meaning.** Every setting is checked before anything is served, and a value that is invalid
or contradicts another one stops startup with status `1` rather than being dropped or clamped.
A deployment that does not match its own configuration must not reach production looking
healthy. Status `2` is different: the command line did not parse, an unknown flag or a value
its type rejects. The most common refusals:

| The last line says                                                          | What to change                                                                                      |
| --------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `LIBGEN_MCP_TIMEOUT: time: missing unit in duration "30"`                   | Durations are Go durations: `30s`, `1m`                                                             |
| `LIBGEN_MCP_SERVER_FETCH: strconv.ParseBool: parsing "yes": invalid syntax` | Booleans are `1`/`0`, `t`/`f`, `true`/`false`                                                       |
| `LIBGEN_MCP_RATE_RPS must be in (0, 20], got 50`                            | Every range is in [Configuration](configuration.md#reference)                                       |
| `LIBGEN_MCP_SOURCES has unknown source "…" (allowed: …)`                    | Use a name from the list the message prints                                                         |
| `LIBGEN_MCP_DOWNLOAD_DIR "…" is not writable: …`                            | Point it at a directory this user can write, or fix the mount ([Docker](#docker))                   |
| `LIBGEN_MIRROR must use http or https, got scheme "…"`                      | Write the mirror as a full URL, `https://libgen.li`                                                 |
| `listen on 0.0.0.0:8080: … bind: address already in use`                    | Another process holds the port: an older server, so run `libgen-mcp --shutdown`, or another program |
| `--session-timeout … cannot apply under the default stateless transport …`  | Drop it, or pass `--stateless=false` if you really want sessions                                    |
| `--trusted-proxy-header "…" names a header nobody is trusted to set …`      | Pass `--trusted-proxies` with it, or neither                                                        |
| `--rate-limit-rps cannot bound a caller on 127.0.0.1:8080 …`                | Name the proxy with `--trusted-proxy-header` and `--trusted-proxies`, or drop the flag              |
| `--public-url "…" needs an http:// or https:// scheme …`                    | Give the origin clients type, scheme included                                                       |

**Fixes.**

- Read the last line; it names the setting. A variable may come from a dotenv file rather
  than the client: the server names the env file it loaded at startup, before the refusal.
- Up to 2.0.1 that last record went out at `INFO`, so a log filter for errors showed nothing
  about why the process died. Upgrade, or read the whole stream.

**How it works.** [HTTP server mode](http-server-mode.md#what-the-server-refuses-to-start-with)
lists every refusal and why each one is a refusal rather than a warning, and the
[command-line reference](cli.md#exit-codes) has the exit codes.

### The server will not start: the private-address hatch on an open listener

**Symptom.** After an upgrade, a deployment that had been running for months exits immediately
instead of serving, saying `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES is set and --http 0.0.0.0:8080
binds a listener other machines can reach`. Nothing about the deploy changed; the health check
never goes green because the process is gone.

**Meaning.** Two settings that were independent are now paired. The variable lets this server
dial loopback, your LAN and carrier-grade-NAT space; an HTTP listener that is anything but a
loopback address or a unix socket can be reached by whoever else is on that network.
Together, a tool call naming a URL is enough to have the server fetch an address the caller
could not reach and hand back the answer — a request-forgery proxy into your network, turned on
by a variable that says nothing about who may reach the endpoint. It is a startup refusal
rather than a warning because a warning is read once by whoever deployed it while the exposure
lasts for the life of the process.

A wildcard bind is judged open **even when the container port is published on loopback**
(`-p 127.0.0.1:8811:8080`): nothing inside the process distinguishes that from the same
container published on every interface.

**Fixes**, best first.

- **Drop the variable.** It is very probably no longer doing anything. The destination guard
  exempts the hosts you named yourself — `LIBGEN_MIRROR`, `LIBGEN_MCP_SCIHUB_HOSTS` and each
  mirror family's own hosts — so a mirror on your own network is reachable without it, and so
  is a redirect from that mirror to a private sibling. See
  [the operator-named-host ADR](decisions/2026-09-17-an-operator-named-host-is-exempt-from-the-destination-guard.md).
- **Bind loopback instead**, `--http 127.0.0.1:8080`, and let the reverse proxy in front reach
  it there. Under Docker that means the proxy must share the container's network namespace;
  if it cannot, a unix socket is the better shape and is accepted too.
- **Keep the variable only if you have measured that you need it**: a mirror you configured
  that resolves publicly and then redirects into private space is the one case the exemption
  above does not cover. Then bind loopback or a socket, which is what the refusal is asking
  for — not a reason to keep a wildcard bind.

**How it works.** [Security](security.md) and
[Architecture](architecture.md#outbound-address-policy).

### A stdio server says a flag "has no effect"

**Symptom.** A stdio server logs `this flag is read by the HTTP transport only, so it has no
effect on a stdio server`, or the same about a variable, with a `flag` such as
`--rate-limit-rps` and the `LIBGEN_MCP_*` variable that fills it.

**Meaning.** The listener, the per-caller budgets, the origin list, the proxy settings, TLS
and the session settings configure the HTTP transport, and a stdio server has none of them:
one client on the other end of a pipe. A valid value is ignored and the server serves anyway,
since none of these is dangerous to ignore, and names each one once at startup so a setting
that does nothing is not also a setting nobody hears about. The value is still validated first:
one that is invalid or contradicts another setting stops startup on stdio exactly as on HTTP,
before any such line, as with `--session-timeout` under the default stateless mode,
`--trusted-proxy-header` without `--trusted-proxies`, or a `--tls-cert` naming a missing file.

The line is a `WARN` for a flag typed on a stdio run, and an `INFO` under `--transport auto` (a
command line written for either transport, as the container image's is) and for a variable set
in the environment (which a shared dotenv file carries to every process). The exception is
`--max-request-body-bytes`, which stays a `WARN` from any source, because stdio has a setting
of its own for the same bound and a value written for either transport was plausibly meant for
both.

**Fixes.**

- Remove the flag from the client's `args`, or the variable from its `env`, if it was carried
  over from an HTTP deployment.
- `--max-request-body-bytes` is the one with a stdio counterpart: the line names it as
  `on_stdio_set`, `LIBGEN_MCP_STDIO_MAX_LINE_BYTES`.
- If you meant to serve HTTP, give `--http` an address or pass `--transport http`.

**How it works.** [Command-line reference](cli.md#http-flags-on-a-stdio-server).

## HTTP status codes

These apply to a server started with `--http`. Every refusal on the MCP endpoint itself is
written as a JSON-RPC error carrying the id of the request it refuses, because a client handed
an unreadable `4xx` concludes it is talking to an older server and downgrades its transport
instead of reporting the problem. So the body of the response, not only its status, says what
went wrong.

| Status | What it means on this server                                                                                        | Entry                                                                      |
| ------ | ------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------- |
| `400`  | An `MCP-Protocol-Version` this server does not speak (code `-32022`, with the list it does), or a malformed request | [400](#400-bad-request)                                                    |
| `403`  | A `Host` header this deployment does not serve, or a browser origin it does not vouch for                           | [403](#403-forbidden)                                                      |
| `404`  | A path nothing is mounted on, or, with sessions on, a session that no longer exists                                 | [404](#every-http-path-answers-404---http-deployments)                     |
| `405`  | The right path with the wrong method: the endpoint takes `POST`                                                     | [405](#405-method-not-allowed)                                             |
| `413`  | A request body over `--max-request-body-bytes` (4 MiB by default)                                                   | [413](#413-request-entity-too-large)                                       |
| `429`  | Never sent by this server: a rate-limited call comes back as a tool error instead                                   | [429](#429-too-many-requests)                                              |
| `503`  | The process is holding as many calls or sessions as it may, or `/health` while draining                             | [503](#calls-come-back-this-server-is-busy-retry-later---http-deployments) |

### 400 Bad Request

**Symptom.** A request is answered `400`. The JSON-RPC body says `unsupported protocol
version` with code `-32022`, or the plain-text body names a header: `Accept must contain both
'application/json' and 'text/event-stream'`, `Content-Type must be 'application/json'` (the
last one with `415`).

**Meaning.** The protocol-version answer lists the versions this server negotiates in its
`data.supported` member, so a client can retry with one of them. A stateful deployment
(`--stateless=false`) lists only revisions older than `2026-07-28`, because the newest
revision has no sessions. The header refusals come from the MCP SDK and mean a client, or a
hand-written `curl`, that does not speak Streamable HTTP.

**Fixes.**

- Upgrade the client, or let it negotiate: a request with no `MCP-Protocol-Version` header at
  all is never refused on this ground.
- A `curl` test needs both `-H 'Content-Type: application/json'` and
  `-H 'Accept: application/json, text/event-stream'`.

**How it works.** [Architecture](architecture.md#transports).

### 403 Forbidden

**Symptom.** Every call through a reverse proxy is answered `403`, and the JSON-RPC message
reads `the Host header names a host this deployment does not serve. Behind a reverse proxy,
pass --public-url with the origin clients use, or --trusted-proxies with the proxy's address.`
The server logs `request refused: the Host header names a host this deployment does not
serve`. Or a browser-based client gets `cross-origin request refused: this deployment vouches
for no browser origin, or not for this one. Name it with --trusted-origins.`

**Meaning.** The first is the guard against DNS rebinding. Every proxy recipe forwards the
client's `Host` and connects over loopback, which is exactly the shape a rebinding attack
takes, so the server needs to be told the public name. The second is the origin check the
transport specification requires: a state-changing `POST` sent by a browser from another
origin is refused unless the origin is named. Non-browser clients send no `Origin` and are
never refused by it.

**Fixes.**

- Pass `--public-url https://mcp.example.org/libgen` with the origin clients use, or
  `--trusted-proxies` with the address the proxy connects from. Either one is enough.
- `/health` and the server cards are not behind the guard, so a probe that answers while the
  endpoint refuses points at exactly this.
- For a browser client, name its origin: `--trusted-origins https://app.example.org`.

**How it works.** [HTTP server mode](http-server-mode.md#the-name-clients-use-and-why-a-proxied-request-is-refused-without-it)
and [Reverse proxy](deploy/reverse-proxy.md).

### Every HTTP path answers 404 (`--http` deployments)

**Symptom.** A server started with `--http` answers `404` and
`{"error":"not found","mcp_endpoint":"/"}` to the MCP client, to `/health`, or to both — on a
path that used to work.

**Meaning.** The MCP endpoint is mounted on one path rather than as a catch-all, so a request
that matches no mounted route is a `404` instead of the misleading `405` a catch-all used to
return. Two configurations produce it:

- **A reverse proxy that forwards its prefix.** If `https://example.org/libgen` is proxied to
  this server without `/libgen` being stripped, the server sees `POST /libgen`, and by
  default its routes live at the root.
- **`--http-path` set to a prefix requests do not actually arrive with.** Under
  `--http-path=/libgen` the routes are `POST /libgen` and `GET /libgen/health`, and `/health`
  at the root is a `404` like anything else.

**Fixes.**

- Read the `mcp_endpoint` field in the 404 body: it names the path this process serves the
  MCP endpoint on. Compare it with the path the request actually arrived with — the proxy's
  access log has that, your client's URL may not.
- Then pick one layer, never both: either strip the prefix at the proxy (in nginx, a
  `proxy_pass` whose URI ends in `/`), or mount the server under it with
  `--http-path=/libgen`.
- Point container and load-balancer probes at the mounted health route, which moves with
  `--http-path` (`/libgen/health` in the example above).
- If the process exits at startup complaining about `--http-path`, the value carries a query,
  a fragment, a `..` segment or a percent-escape. That is refused deliberately — a server
  mounted on a path it can never match would answer `404` to everything, which reads as a
  proxy fault.
- A plain-text `session not found` with `404` is a different case: with `--stateless=false`,
  the session the client names was closed by `--session-timeout` or by a restart. A client
  recovers by initializing again.

**How it works.** [HTTP server mode](http-server-mode.md#the-mcp-alias): the endpoint also
answers on `/mcp` under the mount, so a URL pasted with or without it reaches the same place.

### 405 Method Not Allowed

**Symptom.** A request is answered `405` with `Allow: POST`, often a `GET` the client sent
right after another refusal.

**Meaning.** The path is right and the method is wrong. The endpoint takes `POST` only; a
stateless deployment (the default) has no stream for a `GET` to open and no session for a
`DELETE` to end. A client that sends a `GET` first is usually one that fell back to the old
HTTP+SSE transport, and the request before it is the one to look at.

**Fixes.**

- Find the response before the `GET` in the client's log or the proxy's: a `400` or `403`
  there is the real fault, and its entry above is the one to follow.
- A client that only speaks the withdrawn HTTP+SSE transport cannot use this server. Run it
  over stdio instead.

**How it works.** [Architecture](architecture.md#stateless-mode).

### 413 Request Entity Too Large

**Symptom.** A `POST` is answered `413` with `request body exceeds 4194304 bytes`.

**Meaning.** The body is larger than `--max-request-body-bytes`, 4 MiB by default. No tool on
this server takes an argument anywhere near that size, so a `413` is usually a client
sending something it should not, or a proxy limit set lower than this one (nginx's own
`client_max_body_size` defaults to 1 MiB, and its refusal is an HTML page, not this text).

**Fixes.**

- Check which layer answered: this server's text is the one above, plain text.
- Raise the bound only with a reason: `--max-request-body-bytes 8388608`. A negative value
  is refused at startup, because it would lift the cap entirely.
- The stdio equivalent is a JSON-RPC error, `Invalid Request: message exceeds the … byte
  limit; send a smaller message or raise LIBGEN_MCP_STDIO_MAX_LINE_BYTES`, and the session
  carries on.

**How it works.** [Command-line reference](cli.md#limits).

### 429 Too Many Requests

**Symptom.** A tool call returns an error result reading `rate limit exceeded for search;
retry after a short backoff`, or a `prompts/get` or `tools/list` gets a JSON-RPC error with
code `-42900` and the same text. The server logs `request refused: rate limit exceeded` at
most once every ten seconds, with the count of what it refused since. Or the client reports a
real HTTP `429`.

**Meaning.** This server never answers HTTP `429` itself. Its inbound limit, `--rate-limit-rps`
(10 per second, burst 40, per charged address), refuses inside MCP, so the model reads the
refusal and backs off rather than the transport giving up. A real `429` on the wire was written
by something in front of the server: a proxy, a gateway, or a platform's own limit.

A second per-caller bound exists for the calls that move files: `you already have … download
or read calls in flight, which is this deployment's limit per caller; let one finish before
starting another` comes from `--max-inflight-per-client`.

**Fixes.**

- For the in-band refusal, wait and retry; the model is told to.
- If every caller is refused at once, the limit is probably counting all of them as one: see
  [Every caller shares one rate-limit budget](#every-caller-shares-one-rate-limit-budget).
- For a real `429`, look at the proxy's or platform's limits, not this server's flags.
- Upstream mirrors answering `429` are a different matter: the server treats them as a
  transient failure and fails over, see [All mirrors unreachable](#all-mirrors-unreachable).

**How it works.** [HTTP server mode](http-server-mode.md#what-one-caller-may-ask-for).

### Calls come back `This server is busy. Retry later.` (`--http` deployments)

**Symptom.** A `tools/call` returns an `isError` result with that text, a `prompts/get` or an
`initialize` gets a JSON-RPC error with code `-50300`, or a request is answered `503` with
`Retry-After: 30`. The log carries `request refused: too many calls held across the process`
or `request refused: too many stateful sessions across the process`.

**Meaning.** The process is holding as many calls, or keeping as many stateful sessions, as its
descriptor limit allows, and refuses the next one rather than run out of descriptors and stop
answering altogether. Calls queue behind the outbound bucket (`LIBGEN_MCP_RATE_RPS`), so a
burst of searches is held for as long as that queue takes to drain.

A `503` from `GET /health` with `"status":"draining"` is not this: it is an instance shutting
down under `--drain-delay`, telling the balancer to stop sending it work.

**Fixes.**

- Read the startup line `process ceilings`: `held_calls_per_process` and `descriptor_limit`
  say what this process was given. A `descriptor_limit` of `1024` is a small hard limit, and
  raising it (`ulimit -n`, `LimitNOFILE=`, `--ulimit nofile=`) raises both ceilings. No flag of
  this server moves them.
- If one caller is holding everything, the inbound rate limit is what slows it, and it only
  works per caller behind a proxy named with `--trusted-proxy-header` and `--trusted-proxies`.
- On `--stateless=false`, sessions nobody deletes hold their slots until `--session-timeout`
  closes them. With `--session-timeout=0` they never do: set a timeout.

**How it works.** [HTTP server mode](http-server-mode.md#what-the-whole-process-may-hold) has
the figures and the refusal shapes, and [Scaling](deploy/scaling.md) what to do when one
process is not enough.

## Proxies, in front and behind

### The proxy gets `502` on a unix socket (`permission denied`)

**Symptom.** The server logs `listening on unix socket /run/mcp-libgen.sock (http)` and stays
up, but every request through the reverse proxy comes back `502 Bad Gateway`; the proxy's error
log names the socket and says `Permission denied` (nginx: `connect() to unix:/… failed (13:
Permission denied)`).

**Meaning.** The socket exists and the proxy found it, but the process that tried to open it is
neither the socket's owner nor in its group. The socket is created `0660` on purpose — a unix
socket is reached through the filesystem, so anything wider than owner+group means every local
account can talk to the MCP endpoint.

**Fixes.**

- Put the proxy's **worker** processes in the socket's group. On a host, add the proxy's user
  to the group and restart the proxy. In a container, run the server with the proxy's group
  instead (`user: "10001:101"` for the official nginx images): nginx calls `initgroups()` when
  it drops privileges, so a Docker `group_add` is discarded before the first connect. Both
  setups are in [Run as a service](deploy/service.md#a-unix-socket-with-a-dedicated-user) and
  [Containers](deploy/containers.md#nginx-in-front-over-a-shared-unix-socket), and
  [Behind a reverse proxy](deploy/reverse-proxy.md#unix-socket-upstream) says why the `user`
  directive is not the place to name the group.
- Check the socket's own directory as well. A `0750` directory the proxy cannot traverse
  produces the same `permission denied` no matter what the socket's mode is.
- Widen the mode only if you must: `--http-socket-mode 0666` makes it reachable by every
  account in that namespace, which is reasonable only when the directory above it is already
  restricted.
- Under Docker, both containers must see the **same** directory — one bind mount or one named
  volume mounted in both. Two containers each writing to their own copy of `/run/mcp` produce
  a server that binds happily and a proxy that gets `No such file or directory`, not
  `permission denied`.
- If the process instead exits at startup saying `--http-socket-mode … applies to a unix
  socket, but --http … is an address`, the mode was given alongside a TCP `--http`; drop one of
  the two. On Windows an explicit mode is refused as well, since `os.Chmod` there cannot honor
  it.
- Two more startup refusals name themselves: `exists and is not a socket` means the path
  already holds a regular file or a directory, which is never removed for you, and
  `is already served by another process` means a live server is answering on it. A socket left
  by a crashed run is removed automatically, with a warning in the log.

**How it works.** [HTTP server mode](http-server-mode.md#socket-permissions).

### Responses arrive all at once, or a stream is cut

**Symptom.** Through the proxy, a tool call's progress arrives in one piece at the end instead
of as it happens, or a long call is cut off with a gateway timeout while it works directly.

**Meaning.** Responses are Server-Sent Events by default, and a proxy that buffers responses
holds the stream until it ends. The server sets `X-Accel-Buffering: no` on every SSE response,
which nginx honors without configuration; a proxy that does not read that header buffers
anyway. A call that waits on the mirrors can also outlast a proxy's read timeout.

**Fixes.**

- Turn response buffering off for the MCP route in the proxy (for Apache's `mod_proxy`,
  `flushpackets=on` on the `ProxyPass` line).
- Raise the proxy's read timeout above the longest call you expect. A download that resolves
  through several sources can take minutes.
- Where the proxy cannot stream at all, `--json-response` answers each request with one
  `application/json` body instead of a stream.

**How it works.** [Reverse proxy](deploy/reverse-proxy.md).

### A browser client reports a CORS error

**Symptom.** A browser-based client fails with a CORS error, often _"the header contains
multiple values … but only one is allowed"_, while `curl` reports `200` for the same request.

**Meaning.** One layer must answer CORS, never two. With `--trusted-origins` set the server
answers the preflight itself and echoes the origin in `Access-Control-Allow-Origin`; a proxy
that adds its own header on top sends two, and a browser rejects the response outright. A
proxy that answers the preflight itself, with the origin not named on the server, tells the
browser the request is allowed and the `POST` that follows is then refused with `403`.

**Fixes.**

- Drop the CORS block from the proxy in the same change that sets `--trusted-origins`.
- If the two cannot be coordinated, drop the proxy's block first: that returns the endpoint
  to refusing browsers, which is at least consistent.
- The server cards are the exception: a proxy may answer CORS for that one document, since it
  serves it rather than forwards it.

**How it works.** [Architecture](architecture.md#transports).

### Every caller shares one rate-limit budget

**Symptom.** The log carries `callers cannot be told apart: the address requests are charged
to is one no public client could have, so everyone reaching this server through the proxy in
front shares a single rate-limit budget`, once, and then every caller is refused together.

**Meaning.** The per-caller limits are keyed on the address a request came from. Behind a
proxy that is the proxy's address, so without being told which header carries the client's
address, the server charges every caller to the same key.

**Fixes.**

- Pass `--trusted-proxy-header` with the header the proxy sets (`X-Real-IP`,
  `X-Forwarded-For`) and `--trusted-proxies` with the address the proxy connects from. Both
  are needed, and either alone is refused at startup.
- On a loopback bind or a unix socket the limit is off unless those two name the proxy,
  for the same reason, and passing `--rate-limit-rps` there is refused rather than ignored.

**How it works.** [HTTP server mode](http-server-mode.md#what-identity-this-deployment-has).

### TLS handshake fails against a private CA

**Symptom.** With `--tls-cert`/`--tls-key` set, a client or proxy cannot complete the
handshake: nginx logs `upstream SSL certificate verify error: (20:unable to get local issuer
certificate)`, `curl` reports `SSL certificate problem: unable to get local issuer
certificate`, and the server's own log shows nothing wrong — it is serving.

**Meaning.** The certificate is fine; nothing on the client side trusts the CA that issued it.
A private or internal CA is not in the system trust store, and nginx does not verify upstream
certificates at all unless told to.

**Fixes.**

- In nginx, turn verification on **and** supply the CA in the same change:
  `proxy_ssl_verify on;`, `proxy_ssl_trusted_certificate /etc/nginx/ca.pem;` and a
  `proxy_ssl_name` that matches a name in the certificate. Turning on the first without the
  second fails every request; supplying neither means the hop is encrypted but unauthenticated,
  which is the state this symptom usually starts from.
- Make sure the certificate actually carries the name the proxy connects by, as a
  subjectAltName. A CN-only certificate is rejected outright by modern clients, whatever the
  trust store says.
- For a client rather than a proxy, add the CA to its trust store (`curl --cacert ca.pem` to
  confirm the chain before changing anything system-wide).
- A handshake that fails with a protocol-version error is the TLS 1.2 floor: this listener
  refuses TLS 1.0 and 1.1.
- If the process never got as far as serving and exited with `loading the TLS certificate and
  key`, the pair itself is the problem — a missing file, an unreadable one, or a key that does
  not match the certificate. That check runs at startup on purpose, so it is not discovered at
  a handshake nobody is watching. `--tls-cert was given without --tls-key` (or the reverse) is
  the other half of the same guard.
- **Prefer a unix socket when the proxy is on the same machine.** It removes the segment
  instead of encrypting it, so there is no certificate to issue, trust or rotate, and this
  whole class of failure disappears.

**How it works.** [HTTP server mode](http-server-mode.md#terminating-tls-in-this-process).

### A renewed certificate is not being served

**Symptom.** Certbot or a secret projection has written a new certificate, the files on disk
are current, and clients are still presented the old one — or the log carries
`the TLS certificate on disk could not be loaded; serving the previous one`.

**Meaning.** The pair is re-read when the files change, and "changed" means the size or the
modification time of either file moved. Two situations produce this symptom, and the log tells
them apart.

**Fixes.**

- A **silently old** certificate with nothing in the log means the files never looked changed.
  A renewal that writes through a symlink the server was not started on, or a copy made with
  `cp -p` that preserves the original timestamps, both leave the paths the server is watching
  exactly as they were. Point `--tls-cert`/`--tls-key` at the paths the renewal actually
  rewrites — for certbot that is `live/<name>/fullchain.pem` and `privkey.pem` — or drop the
  timestamp-preserving flag.
- A **warning** means the pair on disk was read and refused. Mid-rotation that is expected and
  self-correcting: the certificate lands before its key, the pair does not match for as long as
  the second write takes, and the previous certificate is served meanwhile. One warning is
  logged per distinct state of the files, so a line that keeps repeating with a new timestamp
  is a rotation that keeps failing rather than a busy server. If it persists, the two files do
  not belong together — check that the key matches the certificate with
  `openssl pkey -in key.pem -pubout` against `openssl x509 -in cert.pem -pubkey -noout`.
- Connections **already open keep the certificate they handshook with**, which is by design.
  A client holding a keep-alive connection sees the new certificate when it reconnects, not
  before; a proxy in front with a long-lived upstream pool is the usual reason a rotation looks
  half-applied from outside.

**How it works.** [HTTP server mode](http-server-mode.md#a-renewal-is-two-file-writes-not-a-restart).

### Behind an outbound proxy: `refusing to connect to a private or local address`

**Symptom.** With `HTTP_PROXY` or `HTTPS_PROXY` set, a download, an enrichment or an
open-access search fails with `refusing to connect to a private or local address`.

**Meaning.** It depends on the address the message names.

- **The proxy's own address, for every host** (Crossref, arXiv, Unpaywall, every download
  URL): the server is 2.0.1 or older, which judged the dial to a proxy on a private address as
  if the proxy were the destination. From 2.1.0 the dial to the proxy answers only to the
  cloud-metadata rule, so a corporate proxy on `10.x` works without
  `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES`.
- **The destination**: the URL a record supplied names a private or metadata address, and it
  is refused behind a proxy exactly as it is without one. That includes every numeric spelling
  a C resolver accepts (`2852039166` and `0xa9fea9fe` are both `169.254.169.254`), and
  `a numeric host this server cannot classify` means a host whose last label only looks
  numeric. `resolves to 169.254.169.254, the cloud instance metadata address` (or another
  metadata address) means a hostname this machine resolves to a cloud metadata
  endpoint; the server resolves names locally for that check alone, and a lookup that fails
  does not refuse.

**Fixes.**

- Upgrade if the message names the proxy.
- A refused destination is the guard doing its job on a third-party URL, not a configuration
  problem. If it is a mirror of yours on a private address, name it in `LIBGEN_MIRROR` or
  `LIBGEN_MCP_SCIHUB_HOSTS`: a host you named is exempt behind a proxy too.
- A host in `NO_PROXY` is dialed directly and judged by the dialer, as without a proxy.
- The proxy variables have to reach the server's own environment: a desktop client passes
  only what its `env` block names, so a proxy that works for `curl` in your shell may not be
  set for the server at all.

**How it works.** [Architecture](architecture.md#outbound-address-policy).

## Searches

### All mirrors unreachable

**Symptom.** Searches and downloads fail with `all libgen mirrors unreachable (network
block? try a VPN or different DNS)`.

**Meaning.** At least one mirror failed transiently (network error, timeout, HTTP 5xx, or
429) on every retry pass — a genuine connectivity problem, not a missing resource.

**Fixes.**

- Check basic connectivity and DNS resolution to the `libgen.li` family. Some ISPs and
  networks block these domains; a VPN or an alternative resolver (e.g. `1.1.1.1`) often
  helps.
- If discovery itself is blocked, pin a mirror you can reach with
  `LIBGEN_MIRROR=https://libgen.li` (or another live host). It is tried first, and discovery
  stays the fallback.
- Increase `LIBGEN_MCP_TIMEOUT` (e.g. `45s` or `1m`) and/or `LIBGEN_MCP_RETRY_ATTEMPTS` on
  slow links.
- Mirrors that fail are put in a 45-second cooldown; wait a moment and retry — the server
  fails over automatically once a mirror recovers.

A related but distinct error, `request rejected by all mirrors`, means every mirror returned
a _permanent_ error (e.g. 404/403). That is a normal "not found / rejected", not a network
problem — re-check the query or identifier rather than your connection.

**How it works.** [Architecture](architecture.md#failover-retry-and-cooldown).

### Searches are slow, or queue behind each other

**Symptom.** Each search takes several seconds, a search that finds nothing takes longer
than one that does, or several searches started together finish one after another.

**Meaning.** Three things set the pace, and none of them is the server's own work:

- **The outbound bucket.** Every request to a mirror waits for a token from one bucket the
  whole process shares, `LIBGEN_MCP_RATE_RPS`, which ships at one request per second with a
  burst of one. A search is several requests, and calls in flight together queue for the same
  tokens: measured, sixteen searches in flight took fifteen seconds to drain.
- **Escalation.** A search the catalog answers with nothing is, under the default
  `extra_sources: "auto"`, sent on to Anna's Archive and the open-access providers, and waits
  for them.
- **Discovery.** The first call after a start, or after the cached mirror list expires,
  fetches the list of mirrors and probes them.

**Fixes.**

- On a deployment that serves several callers, raise the bucket: `LIBGEN_MCP_RATE_RPS` up to
  `20` and `LIBGEN_MCP_RATE_BURST` up to `100`. The low default is deliberate politeness
  towards volunteer-run mirrors, so raise it as far as your load needs and no further.
- Pass `extra_sources: "never"` (or set `LIBGEN_MCP_EXTRA_SOURCES=never`) when only the
  catalog matters.
- Pin a mirror with `LIBGEN_MIRROR` to skip discovery.
- An inbound limit above the outbound bucket only moves the queue: on a busy HTTP
  deployment, the bucket is the setting that changes throughput.

**How it works.** [How search works](how-search-works.md) and
[Configuration](configuration.md#libgen_mcp_extra_sources).

### Truncated search results

**Symptom.** A search response has `truncated: true` and a `hint`, and paging past a certain
point returns nothing.

**Meaning.** The mirror reports more matches (`total_files`) than it will actually serve
across pages (`reachable`). Pages beyond `reachable` are empty.

**Fixes.** Refine rather than page deeper, as the `hint` suggests:

- Add distinguishing terms (author, year).
- Constrain the fields with `search_in` (e.g. `["title"]`).
- Narrow `topics` to the relevant collection(s).

**How it works.** [Tools](tools.md#pagination-and-truncation).

### A search result that will not download (`origin: "annas"`)

**Symptom.** A search returned a result labeled `origin: "annas"`, but `download` fails on its
md5 with an error about no IPFS CID or no gateway serving it.

**Meaning.** That result came from an escalated search: the Library Genesis catalog does not
carry the file, so the only route to it is Anna's Archive. The keyless route reads the item's
IPFS address from Anna's record page — and **most Anna's records publish no IPFS address at
all**. When there is none, there is nothing to fetch keylessly, and this is expected rather
than a fault.

**Fixes**, in order of effort:

- Set `LIBGEN_MCP_ANNAS_KEY` to an Anna's Archive membership key. The member fast-download
  API serves items with no IPFS address, and the keyless IPFS route stays as the fallback.
- Search again with different terms; another edition of the same work may be in the catalog,
  which downloads through the ordinary sources.
- Call `get_details` on the md5 anyway. It falls back to Anna's record, so you still get the
  title, author, year and language — plus ISBNs when that record carries them, which a
  minority do — even when the bytes are out of reach, enough to find the item elsewhere.

A gateway that is merely slow reports a timeout instead; retrying later often succeeds, since
the public IPFS gateways vary in how quickly they locate an item.

**How it works.** [How search works](how-search-works.md) and
[Sources](sources.md#annas).

### An open-access provider contributes nothing

**Symptom.** A search reaches beyond the catalog, but one provider never shows up in
`open_access`: no `openalex`, `dblp` or `annas` rows, or `arxiv` rows on only some of several
searches run together. The log may hold one of these lines:

- `OpenAlex rejected the configured API key, so OpenAlex search and the openalex download
  source fail until it is fixed or unset`, at `WARN`, with `variable=LIBGEN_MCP_OPENALEX_KEY`
  and the status.
- `dblp search: the SPARQL service answered with a bot check or a rate limit instead of JSON,
  asking nothing for 15m0s`, at `INFO`.
- `annas search: mirror is serving a browser challenge, asking nothing for 15m0s`, at `WARN`.

**Meaning.** Every provider beyond the catalog is best-effort: one that cannot answer
contributes nothing rather than failing the search. Most of the reasons are deliberate:

- **Pacing.** Each provider is paced for the whole process at the rate its operator asks for,
  arXiv at one request every three seconds and dblp at one every ten. A search whose next token
  for a provider is more than a second away skips that provider instead of waiting, so under
  concurrent searches only some of them get arXiv or dblp. A skip is not logged.
- **A refusal.** When dblp answers with a bot check or a rate limit, or an Anna's Archive
  mirror with a browser challenge, that provider is asked nothing for fifteen minutes and the
  log says so once.
- **OpenAlex's daily credits.** Without a key, OpenAlex allows 1000 credits a day per address,
  and a search costs 10. The server stops sending searches when one would leave fewer than 100,
  the reserve kept for the one-credit lists `get_details` `related` asks for, and starts again
  when the window resets at midnight UTC. A rate-limit refusal that asks for a minute or less
  pauses keyless searches and `related` for that long only, since a search sent with a key
  does not consult the budget. Neither is logged.
- **A rejected OpenAlex key.** OpenAlex answering `401` or `403` to a request sent with
  `LIBGEN_MCP_OPENALEX_KEY` is the `WARN` above, written once per process.

**Fixes.**

- Usually nothing: the other providers still answered, and a later search gets the skipped one
  back.
- For OpenAlex, set `LIBGEN_MCP_OPENALEX_KEY`. A search sent with a key is not held back by the
  keyless reserve. After the `WARN`, fix the key or unset it.
- A dblp or Anna's Archive refusal is the upstream's own decision, and no setting changes it.
  Wait out the fifteen minutes. A configured Anna's Archive member key still serves `download`
  through the member API.

**How it works.** [How search works](how-search-works.md#when-a-source-sits-a-search-out) and
[Configuration](configuration.md#libgen_mcp_openalex_key).

### A year range empties the page

**Symptom.** A search with `year_from` or `year_to` returns few or no catalog results, and the
response carries `year_filtered` with a line such as `The year range left out 17 catalog
records on this page (outside it or undated).` Or the call is refused with
`year_from must be a year between 1000 and 2100, got 990`, or
`year_from (2020) is after year_to (2010): swap them, or drop one to leave that side open`.

**Meaning.** Library Genesis has no year filter, so the server filters the page after it
arrives, and a record without a year is left out too, since nothing shows it is in range.
`year_filtered` counts this page only. `total_files`, `reachable` and `has_more` still describe
the unfiltered search, so a page with nothing in range is not proof the range is empty.

**Fixes.**

- Request the next page when `has_more` is true. The next steps say so before they say nothing
  was found.
- Add the year to the query text as well, or widen the range.
- Let the search reach beyond the catalog (`extra_sources: "always"`). Most providers there
  apply the range in their own query rather than after the fact.

**How it works.** [Tools](tools.md#narrowing-by-year) and
[How search works](how-search-works.md#narrowing-by-year).

## Records and citations

### A pasted citation does not resolve

**Symptom.** `get_details` with `citation` returns no record, and `citation_match` has
`status: "unresolved"`, a `reason` and up to five candidates. The reason is one of:

- `Crossref offered no candidate for this citation.`
- `No candidate stands out: the best scores 41.2 against 40.9 for the next, and a match needs a
  lead of 20%.`
- `The best candidate leads, but only 67% of its title appears in the citation, so it may be a
  different work that shares some of its words.`

Or the call is refused with `a citation is resolved through Crossref, which this server has
turned off (LIBGEN_MCP_ENRICH=false). Pass the work's doi instead, or search for its title`, or
`citation is 1450 characters long, and the most accepted is 1000. Paste one reference, trimmed
to its authors, title, venue and year`.

**Meaning.** A citation resolves only when Crossref's best candidate scores at least 20% above
the next one **and** at least 90% of its title's words appear in the citation. Anything less
hands the candidates back unchosen, because a confident wrong DOI puts the wrong work in a
bibliography. A work with no Crossref DOI (an arXiv-only preprint, many books) cannot resolve
this way at all.

**Fixes.**

- Pick the right candidate and call again with its `doi`.
- Paste the title in full, with the year and the venue.
- For a work without a DOI, `search` for its title instead.

**How it works.** [Citations](citations.md#get-a-citation) and
[Tools](tools.md#get_details-input).

### `cite_as` comes back `local` or `unavailable`

**Symptom.** A style asked for in `cite_as` has `source: "local"` and a note, or
`source: "unavailable"` and no text. The note is one of:

- `doi.org gave no usable answer for this style, so it was built from the record's fields.`
- `The record's DOI was not confirmed to name this work, so it was not sent to doi.org and the
  style was built from the record's fields.`
- `The record has no DOI, so the style was built from its fields.`
- `This server does not ask doi.org (LIBGEN_MCP_ENRICH=false), so the style was built from the
  record's fields.`
- `Neither doi.org nor the record's own fields could produce it: the record has no title.`,
  for `unavailable`.

**Meaning.** A style is formatted by the DOI's registration agency through doi.org only when the
record carries a DOI that names this work, and doi.org gives a usable answer within eight
seconds. Otherwise it is built here, by the same rules as the BibTeX entry. A local style is a
fallback, not an error, but it is only as good as the catalog's fields: a name written
"Given Family" is read given name first.

**Fixes.**

- Check a local style against the work before you publish it.
- Call again with a `doi` that `citations.doi_status` reports as confirmed to get the registry's
  text.
- For a doi.org that did not answer in time, retry later.

**How it works.** [Citations](citations.md#other-citation-styles).

### `related` lists nothing

**Symptom.** `get_details` with `related` returns a `related` object with no works and a note,
such as:

- `Not available: the record has no DOI, and OpenAlex is asked by DOI.`
- `Not available: the record's DOI was not confirmed to name this work, and OpenAlex is asked
  by DOI.`
- `Not available: this server does not reach OpenAlex for metadata (LIBGEN_MCP_ENRICH=false).`
- `OpenAlex asked this server to slow down, so the list was not fetched. Try again shortly.`
- `The OpenAlex daily allowance shared by this server is spent, so the list was not fetched. It
  resets at midnight UTC.`, followed by `LIBGEN_MCP_OPENALEX_KEY raises it.` on a server with
  no key.
- `OpenAlex refused the API key this server is configured with, so the list was not fetched.
  The operator's log names the variable to fix.`
- `OpenAlex has no work with this DOI.`, or `OpenAlex did not answer, so the list is not
  available now.`

**Meaning.** The list comes from OpenAlex, asked by the record's DOI, and only by a DOI that
names this work: a catalog DOI that failed corroboration belongs to another work, whose
references would be listed under this one. The lookup of the work is free, and the list costs
one credit of the daily allowance every OpenAlex request from this server shares, the search
provider's included. An empty answer that OpenAlex gave (`OpenAlex lists no references for this
work.`, `OpenAlex knows no work that cites this one.`) is a fact about the record, not a
failure.

**Fixes.**

- For a missing or unconfirmed DOI, find the work's DOI (a `citation` lookup, or a `search`)
  and call again with it.
- For a slow-down note, retry in a minute. For a spent allowance, wait for midnight UTC or set
  `LIBGEN_MCP_OPENALEX_KEY`.
- For a refused key, the server's log has the `WARN` naming the variable. Fix the key or unset
  it.

**How it works.** [Tools](tools.md#related-works).

## Downloads

### Download failed / MD5 mismatch

**Symptom.** `download` returns an error, or `integrity check failed: MD5 mismatch`.

**Meaning and fixes.**

- **MD5 mismatch** — the downloaded bytes did not match the requested `md5` (corrupt or
  tampered transfer, or a stale mirror). The partial file is deleted automatically; simply
  retry. If it persists on one mirror, the `randombook` fallback will try freshly discovered
  mirrors.
- **"mirror returned an HTML page instead of the file"** — the download key expired or the
  mirror served an error/challenge page. Retry; the pipeline resolves a fresh key each time
  and falls over to the next source.
- **"download exceeds the configured size limit"** — the file is larger than
  `LIBGEN_MCP_MAX_DOWNLOAD_BYTES`. Raise the cap (up to 50 GiB) or set it to `0` to disable
  it.
- **"truncated download"** — the connection dropped mid-stream. The `.part` file is kept, so
  re-running `download` resumes from where it stopped (the result's `resumed` field reports
  `true` when it does).
- **All sources failed** — the tool returns the joined per-source errors, one line per
  source it tried. Read them to see whether the item was simply not found or every provider
  was unreachable.

The `download_troubleshoot` prompt walks a model through the same diagnosis from the error
text.

**How it works.** [Architecture](architecture.md#download-flow) and
[Tools](tools.md#behavior-and-errors).

### Article not found (open access vs Sci-Hub)

**Symptom.** A `download` with a `doi` fails, or returns nothing useful.

**Meaning.** Articles are fetched by DOI through a chain of sources, in order: the open-access
resolvers (`unpaywall`, `openalex`, `europepmc`), the publisher-direct sources that each serve
one DOI prefix (`biorxiv`, `rfc`, `nist`, `dagstuhl`, `acl`, `zenodo`, `scielo`, `fao`), the
preservation copies (`fatcat`, `core`), the link the publisher deposited with Crossref
(`crossref`), open-access monographs (`oapen`), and last the shadow-library fallbacks
(`scihub`, then `scidb`). Each source either serves the article or reports a clean miss, and
the chain advances. Two are only in the chain when configured: `unpaywall` needs
`LIBGEN_MCP_UNPAYWALL_EMAIL` (an elicitation-capable client is asked for one instead, and
declining just moves on), and `core` needs `LIBGEN_MCP_CORE_KEY`.

**Fixes.**

- Confirm the DOI is correct (copy it exactly from the article search result).
- If the open-access providers all failed, the article likely is not open access; Sci-Hub and
  then SciDB are the fallbacks.
- Sci-Hub mirrors rotate and go down often. Update `LIBGEN_MCP_SCIHUB_HOSTS` with a currently
  working host list if all defaults fail.
- Set your own `LIBGEN_MCP_UNPAYWALL_EMAIL` — until you do, Unpaywall is disabled (the other
  open-access sources and Sci-Hub are still tried). The API expects a real contact address.
- Set `LIBGEN_MCP_CORE_KEY` (a free CORE API key) to add CORE to the open-access chain.
- A `crossref` error naming the browser as the remaining route means every link the publisher
  deposited refused an anonymous client; the large commercial publishers answer `403`
  whatever the link says.
- A `europepmc` error reading `… is a retracted publication, so it is not served` or `the PMC
  dataset marks … retracted, so it is not served` means the article was retracted. That source
  declines it and the chain moves on, so check the retraction notice before relying on any
  copy another source serves.
- Note that DOI downloads are **not** MD5-verified (`verified` is `false`) — there is no
  LibGen digest for them.

**How it works.** [Sources](sources.md) has each source's corpus, what it does not cover and
the errors it reports, and [Download a paper](https://jmrp.io/docs/libgen-mcp/download-a-paper/) walks through the route.

### Book not found by ISBN (open access only)

**Symptom.** A `download` with an `isbn` fails with something like `no catalog entry states
"9780141439518"` or `no freely downloadable scan ... (every candidate is lending-restricted
or holds no book file)`.

**Meaning.** The `isbn` route reaches only the two **open-access** book sources — `oapen` and
`archive` — and neither will serve a book it may not redistribute:

- `oapen` holds openly licensed scholarly monographs. It confirms the record it found really
  states the ISBN (or DOI) you asked for before serving anything, because its search is free
  text and would otherwise return an unrelated monograph. A trade book is simply not there.
- `archive` serves an Internet Archive scan only when OpenLibrary reports the book as
  `ebook_access: public` **and** the individual scan is neither flagged `access-restricted-item`
  nor filed in a lending collection. A book that is borrowable-only on the Archive is
  reported as a miss rather than downloaded, because a lending item's files either refuse the
  request or arrive DRM-wrapped and unusable.

**Fixes.**

- For an in-copyright book, use the `md5` route instead: search the catalog and download by
  the result's `md5`.
- Check the ISBN itself — a typo, or the ISBN of a different edition, is the common cause. Both
  the 10- and 13-character forms work, with or without hyphens.
- If the book is a public-domain classic, search again and look for a `gutenberg` hit in
  `open_access`: its `full_text_url` is the ebook file itself.

**How it works.** [Sources](sources.md#open-access-and-public-domain-books).

### A source is missing from the errors of a repeated download

**Symptom.** A `download` fails, and a second attempt reports fewer sources than the first —
one that failed a moment ago is not mentioned at all.

**Meaning.** That source is in **cooldown**. When a source fails because it is unavailable (a
transport error, a timeout, a 5xx or a 429) it is set aside for 5 minutes, so the next
download does not spend its resolve budget on a provider that just proved unreachable. It is
skipped, not removed: the cooldown expires on its own, nothing is written to disk (a restart
clears it), and when every source able to serve the item is in cooldown they are all tried
anyway. A source that merely reported it does not hold the item is never set aside.

The server log says which sources were skipped and why — `source in cooldown, skipping` with
the instant it becomes eligible again, or `every capable source is in cooldown, trying them
anyway`. Run with `LIBGEN_MCP_LOG_LEVEL=info` (the default) to see them.

**Fixes.**

- Nothing is needed: retry later, or immediately with `source: "<name>"` to address that
  provider directly — an explicit source is always tried, cooldown or not.
- If a source is repeatedly cooled down, it is genuinely unreachable from this host. Check it
  by hand before suspecting the server.

**How it works.** [Architecture](architecture.md#per-source-cooldown).

### Disk space

**Symptom.** `not enough free disk space in <dir>: need ~<n> bytes, have <m>`.

**Meaning.** Before streaming a download whose size is known, the server checks that the
destination has room for the file plus an ~8 MiB margin, and refuses rather than filling the
disk.

**Fixes.**

- Free space on the target volume, or point `LIBGEN_MCP_DOWNLOAD_DIR` (or the per-call
  `path`) at a volume with more room.
- Remove stale `.part` files left by interrupted downloads if you do not intend to resume
  them (they live in the download directory, named `.libgen-mcp-*.part`). A download that
  eventually succeeds tries to clean up after itself, including the partials of the sources
  that failed first — it skips any a concurrent download is still writing to. A download
  where _every_ source failed keeps its partials on purpose, one per source that got far
  enough to write bytes, so a later call can resume from them.
- Under Docker, make sure the mounted download volume is large enough and writable by UID
  `10001`.

This check only applies to a server that saves to disk. A remote server never writes a file
at all, so this error cannot occur there.

**How it works.** [Tools](tools.md#where-the-file-goes-local-vs-remote).

### `download` returns a link instead of saving a file

**Symptom.** `download` succeeds but returns a `resource_link` and a `resolved` object, and no
file appears anywhere.

**Meaning.** The server is running in remote mode: started with `--http`, on a unix socket, or
with `LIBGEN_MCP_REMOTE_DOWNLOADS=1`. Its disk is not one the client can reach, so writing a
file there would hand the caller nothing, and the server resolves the link instead.
`resolve_only` is implied and need not be set.

**Fixes.**

- Fetch the link with your own HTTP tool, or run the server locally over stdio, where files
  land in `LIBGEN_MCP_DOWNLOAD_DIR` (default `~/Downloads`).
- A stdio server hosted on a remote or ephemeral machine (behind `mcp-proxy`, for example)
  should set `LIBGEN_MCP_REMOTE_DOWNLOADS=1`: its disk is just as unreachable by the client as
  an HTTP deployment's.

**How it works.** [Tools](tools.md#where-the-file-goes-local-vs-remote) and
[Configuration](configuration.md#libgen_mcp_remote_downloads).

## `read` refused

### A path is outside the allowed directories

**Symptom.** `read` fails with `path /home/me/notes.txt is outside the allowed directories;
use the working directory, the OS temp directory, the download directory, or name it in
LIBGEN_MCP_ALLOWED_READ_DIRS`, or `download` with a `path` fails the same way about its
`output path`. On a remote server `read` says `path is not available on a remote server; use
md5 or doi` instead.

**Meaning.** A caller-supplied local path is confined to the working directory, the OS temp
directory and the download directory, after symlinks are resolved. A client that starts the
server in `/` or in your home directory would otherwise let any prompt read any file there. A
remote server takes no local path at all, because the path would be on the server's disk.

**Fixes.**

- Name the directory you want readable in `LIBGEN_MCP_ALLOWED_READ_DIRS`.
- Or read by `md5` or `doi`, which reads the downloaded file without a path.
- A symlink that points outside the roots is refused even when the link itself is inside.

**How it works.** [Security](security.md) and [Tools](tools.md#read-input).

### A file `read` cannot extract

**Symptom.** `read` returns `extractable: false` with a reason: `no extractable text layer
(likely a scanned or image-only PDF); OCR is not supported`, `unsupported format .djvu: text
extraction is not available (comic/scanned/proprietary container)`, `unsupported file
extension .env and its bytes match no supported format (unrecognized)`, `cannot read PDF: it is
encrypted in a way this reader cannot decrypt (…)`, `cannot read PDF: the file is damaged (…)`,
or `cannot read the file: the reader did not finish within the time limit (the document is
damaged or pathologically structured)`.

**Meaning.** `read` extracts text from PDF, EPUB and plain text. A scan has no text to
extract and no OCR runs here; DjVu, comic archives and proprietary e-book containers are not
read at all; a PDF encrypted with AES-256 or RC4 under crypt filters, or one that needs a
password, cannot be decrypted, and a damaged PDF is not repaired; a document that takes too
long is given a time limit rather than a thread forever.

**Fixes.**

- Download another edition of the same work: the catalog often has a text PDF or an EPUB
  beside a scan, and another mirror's copy of an encrypted or damaged PDF is often a plain,
  whole one.
- Open the file locally in a reader that does OCR, or that decrypts or repairs the PDF.
- An outline that comes back empty is not a failure: many PDFs carry no table of contents,
  the `reason` says whether it has none or a damaged one, and `find` still searches the text.

**How it works.** [Tools](tools.md#not-extractable).

### `section` is refused

**Symptom.** `read` with `section` is refused with one of:

- `section "Summary" matches 4 entries, pass the number of the one you mean: …`
- `no outline entry matches "…": read the outline and pass an entry number`
- `section 40 does not exist: the outline has 12 entries, numbered from 1`
- `this document has no table of contents, so section cannot address part of it: read by page
  with start_page (PDF) or by character with offset (EPUB/TXT), or search it with find`
- `section cannot be combined with outline: …`, the same with `find`, or `section fixes where
  reading starts, so omit start_page and offset: continue a long section with the cursor`
- `entry 7 "…" points to no page, so it cannot be read as a section: …`

**Meaning.** `section` names one table-of-contents entry, by the number outline mode shows or
by its title. A value of digits only is always a number. A title matches ignoring case and
spacing, an exact match first and then one that contains it, and a title several entries share
is refused with their numbers rather than read as the first one.

**Fixes.**

- Call `read` with `outline: true` and pass the entry's number, the `[n]` in front of it.
- For a document with no outline, read by page or offset, or search it with `find`.
- Drop the argument the refusal names. A long section continues with its `cursor`.
- For an entry that points to no page, pick a neighboring entry or read by page.

**How it works.** [Tools](tools.md#read-one-section).

## Telemetry not arriving

### Nothing reaches the collector

**Symptom.** `LIBGEN_MCP_TELEMETRY` is set and the collector receives nothing, or only some
signals.

**Meaning.** The startup log says which of several things it is:

- **No `telemetry enabled` line at `WARN`.** The switch did not reach the process, or
  `OTEL_SDK_DISABLED=true` vetoed it (`telemetry suppressed by OTEL_SDK_DISABLED`, at
  `INFO`).
- **`telemetry disabled: it could not be started`, at `ERROR`.** The exporter refused its
  configuration, for example `http/json` as the protocol, which this server refuses rather
  than splitting one deployment across two encodings. The server keeps serving.
- **Repeated `opentelemetry sdk error` lines.** Exports are failing. The most common cause is
  an endpoint written without a scheme or not set at all: the Go exporters default to
  `https://localhost:4318`, so a plaintext local collector refuses every batch.
- **Nothing wrong in the log, but timeouts or batch settings have no effect.** `OTEL_*`
  durations are integer milliseconds: `OTEL_EXPORTER_OTLP_TIMEOUT=30s` parses as nothing.

**Fixes.**

- Write the endpoint with its scheme: `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318`.
- Write every `OTEL_*` duration in milliseconds.
- On stdio, never set `OTEL_TRACES_EXPORTER=console`: it writes to stdout, which is the
  protocol.
- A value of this server's own telemetry variables that does not parse stops startup; a
  collector that cannot be reached never does.

**How it works.** [Telemetry](telemetry.md#when-it-goes-wrong).

## Install problems by channel

Each channel's page has the install itself; these are the failures specific to each.

### npm and npx

- **`spawn npx ENOENT` or `command not found` in a desktop client.** The client does not read
  your shell's `PATH`, and Node from a version manager lives somewhere only your shell knows.
  Give the absolute path from `which npx`.
- **Nothing happens on Windows.** `npx` is a batch file: use `"command": "cmd"` with
  `"args": ["/c", "npx", "-y", "@jmrp.io/libgen-mcp"]`.
- **`npm WARN EBADENGINE`, or a launcher that fails on old Node.** The launcher needs Node
  18 or newer.
- **`libgen-mcp: the @jmrp.io/libgen-mcp-<platform> package is not installed.`** The binary
  rides in an optional dependency, and the install skipped it: `--no-optional`,
  `--omit=optional`, or a lockfile written on another operating system. Reinstall without
  the flag, or delete `node_modules` and the lockfile and install again.
- **`libgen-mcp: unsupported platform …`.** Prebuilt binaries exist for Linux, macOS and
  Windows on x64 and arm64. Anything else builds from source.
- **An old version keeps running.** `npx` reuses its cache: `npx -y @jmrp.io/libgen-mcp@latest`
  asks for the newest.

See [npm](install/npm.md).

### uvx and pip

- **`uvx: command not found` in a desktop client.** uv installs to `~/.local/bin`, which a
  desktop client's `PATH` does not include. Give the absolute path from `which uvx`.
- **`No matching distribution found` (pip) or no wheel for the platform (uv).** Wheels exist
  for Linux, macOS 13 or newer and Windows, on x86-64 and arm64. A 32-bit system, another
  operating system, or a pip too old to read the wheel's platform tags finds none: upgrade
  pip first.
- **Alpine and other musl systems.** The Linux wheels carry `musllinux` tags beside
  `manylinux`, and the binary needs no C library, so the same wheel installs there.
- **`libgen-mcp binary not found next to …`.** `python -m libgen_mcp` was run with a different
  interpreter from the one the package was installed into. Run `libgen-mcp` itself, or use
  the matching interpreter.

See [PyPI](install/pypi.md).

### dnx and NuGet

- **`dnx` is not found.** `dnx` ships with the .NET 10 SDK, and the tool's packages need it:
  the runtime-specific layout they use does not exist in earlier SDKs. Install .NET 10, or
  use another channel.
- **A server flag goes to `dnx` instead of the server.** Everything before `--` belongs to
  `dnx`: `dnx libgen-mcp -- --http 127.0.0.1:8080`, and in a client's `args`,
  `["libgen-mcp", "--", "--env-file", "/absolute/path/libgen.env"]`.
- **An installed tool is not found by a desktop client.** `dotnet tool install -g` puts the
  shim in `~/.dotnet/tools`; give that absolute path.

See [NuGet](install/nuget.md).

### Homebrew

- **`libgen-mcp` is not found by a desktop client.** A client started from the dock does not
  have Homebrew's `bin` on its `PATH`. Give the full path the formula's caveats print:
  `$(brew --prefix)/bin/libgen-mcp`, which is `/opt/homebrew/bin/libgen-mcp` on Apple
  silicon, `/usr/local/bin/libgen-mcp` on Intel and `/home/linuxbrew/.linuxbrew/bin/libgen-mcp`
  on Linux.
- **`No available formula`.** The formula is in this project's tap:
  `brew install jmrplens/tap/libgen-mcp`.
- **The old version is still answering after `brew upgrade`.** The running process keeps the
  binary it started with. Run `libgen-mcp --shutdown` and let the client start it again.

See [Homebrew](install/homebrew.md).

### Docker

- **The client waits and never connects.** The `docker run` in its configuration has no
  `-i`. Without it standard input is `/dev/null`, so the image starts its HTTP listener
  instead of stdio, and the log says `transport inferred from stdin` with
  `stdin is /dev/null, so no client is speaking to this process`. Add `-i`.
- **`LIBGEN_MCP_DOWNLOAD_DIR "/downloads" is not writable: … permission denied`.** The
  container runs as UID `10001`, which must be able to write the directory you mount:
  `chown 10001:10001` it on the host, or run with `--user "$(id -u):$(id -g)"` and point
  `LIBGEN_MCP_DOWNLOAD_DIR` at the mount.
- **`… is not usable: … read-only file system` under `--read-only`.** The download directory
  is checked at startup on every transport. Mount a writable volume or a `tmpfs` there, and
  give `/tmp` a `tmpfs` too, since `read` by `md5` or `doi` stages the file it fetches there.
- **`the mirror cache cannot be written, so every start discovers the mirrors again`.** Logged
  once, at `WARN`, the first time the server tries to save the mirror list it discovered, with
  the directory in `dir`. The server works, but every start fetches the mirror catalog again.
  The cache lives under `$HOME/.cache/libgen-mcp` (or `$XDG_CACHE_HOME/libgen-mcp`): give
  `/home/appuser` a volume, as [Containers](deploy/containers.md) does, or point
  `XDG_CACHE_HOME` at a writable mount.
- **The container exits at once with status `0`.** An argument was added without the
  defaults: any argument replaces the image's command wholesale, `--transport auto --http
  0.0.0.0:8080` included, and with no `-i` and no `--http` a stdio server reads end-of-file
  and stops. Write the whole command line, `--transport auto --http 0.0.0.0:8080` and your
  flag.
- **It exits at once with status `1`, naming `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES`.** The
  image's default listener is a wildcard bind. See
  [the private-address hatch](#the-server-will-not-start-the-private-address-hatch-on-an-open-listener).
- **`cosign verify` reports "no signatures found".** Use a cosign 3.x client: 2.x does not
  find the signatures a 3.x client verifies.

See [Docker](install/docker.md) and [Containers](deploy/containers.md).

### Claude Desktop (`.mcpb`)

- **On Linux, opening the file does nothing.** The Linux app registers no handler for `.mcpb`
  files. Install it from **Extensions > Install Extension…**.
- **Claude Desktop refuses the bundle.** Each per-system bundle lists only its own system.
  Download the one for yours, or the universal `libgen-mcp.mcpb`.
- **The extension installs but the server fails.** A setting it was given does not parse, and
  the server log in Claude's log directory ends with the reason: a **Request timeout** of `30`
  instead of `30s` gives `LIBGEN_MCP_TIMEOUT: time: missing unit in duration "30"`. Correct
  the field in the extension's settings.
- **`no Linux binary for machine type …`** comes from the Linux launcher on a machine that is
  neither x86-64 nor arm64. **`is still not executable after chmod; is the filesystem mounted
  noexec?`** means the extensions directory is on a `noexec` mount.
- **A setting has no field.** The bundle's fields cover the common ones. For anything else,
  such as `LIBGEN_MCP_ANNAS_KEY`, use the **Settings file** field or `~/.libgen-mcp.env`.

See [Claude Desktop](install/claude-desktop.md).

### Release binary

- **`Permission denied` when running it.** The file was downloaded without its execute bit:
  `chmod +x libgen-mcp`.
- **`exec format error` or `cannot execute binary file`.** The asset is for another
  architecture. Pick the one `uname -m` names: `amd64` for x86-64, `arm64` for aarch64.
- **macOS refuses to open it because it cannot verify the developer.** A file a browser
  downloads carries the quarantine attribute. Remove it with
  `xattr -d com.apple.quarantine libgen-mcp`, or download with `curl`, which does not set it.

See [Release binaries](install/binary.md).

### `go install`

- **An old version installs, or `module contains a go.mod file, so major version must be
  compatible`.** The module path carries the major version:
  `go install github.com/jmrplens/libgen-mcp/v2/cmd/server@latest`. Without `/v2`, `@latest`
  quietly resolves the newest 1.x release, and a `@v2.x.y` is refused.
- **`requires go >= 1.27.1`.** The toolchain is older than the module needs, and
  `GOTOOLCHAIN=local` stops Go from fetching a newer one. Install Go 1.27 or newer, or let it
  switch toolchains.
- **The command is called `server`.** `go install` names the binary after its package,
  `cmd/server`. Build it with `go build -o libgen-mcp ./cmd/server` from a clone to get the
  usual name.
- **`command not found` after a successful install.** `$(go env GOPATH)/bin` is not on your
  `PATH`.

See [Release binaries](install/binary.md).

## Frequently asked questions

### Why does libgen-mcp say all mirrors are unreachable?

The error `all libgen mirrors unreachable (network block? try a VPN or different DNS)` means at
least one mirror failed transiently — a network error, timeout, HTTP 5xx, or 429 — on every
retry pass, which is a genuine connectivity problem rather than a missing resource. Check
connectivity and DNS to the `libgen.li` family; some ISPs block these domains, so a VPN or a
different resolver such as `1.1.1.1` often helps. If discovery itself is blocked, pin a
reachable mirror with `LIBGEN_MIRROR=https://libgen.li`. You can also raise
`LIBGEN_MCP_TIMEOUT` and `LIBGEN_MCP_RETRY_ATTEMPTS` on slow links. Failed mirrors get a
45-second cooldown and the server fails over automatically once one recovers.

### Why did my libgen-mcp download fail an MD5 check?

For book (`md5`) downloads libgen-mcp hashes the whole downloaded file and compares it to the
requested MD5. An `integrity check failed: MD5 mismatch` means the bytes did not match —
usually a corrupt or tampered transfer or a stale mirror. The partial file is deleted
automatically, so simply retry; if it persists on one mirror, the `randombook` fallback tries
freshly discovered mirrors. DOI (article) downloads are not MD5-verified, because there is no
LibGen digest for them.

### Why are some articles missing from libgen-mcp results?

Articles are fetched by DOI through a chain of sources, legal open-access providers first:
`unpaywall` (when `LIBGEN_MCP_UNPAYWALL_EMAIL` is set), `openalex` and `europepmc`, then the
publisher-direct sources that each serve one DOI prefix, then preserved copies (`fatcat`, and
`core` when `LIBGEN_MCP_CORE_KEY` is set), the link the publisher deposited with `crossref`,
and `oapen` for monographs. The shadow-library fallbacks come last: `scihub`, then `scidb`. A
paywalled DOI with no copy anywhere in that chain is not downloadable. Confirm the DOI is
exactly correct, set your own `LIBGEN_MCP_UNPAYWALL_EMAIL`, and update
`LIBGEN_MCP_SCIHUB_HOSTS` with currently working hosts if all defaults fail, since Sci-Hub
mirrors rotate and go down often.

### Where does libgen-mcp save downloaded files?

Downloads go to the directory set by `LIBGEN_MCP_DOWNLOAD_DIR`, which defaults to
`~/Downloads`. The `download` tool's `path` argument overrides it per call. The directory is
created if missing and checked for writability at startup, and the server verifies there is
enough free disk space (the file plus an ~8 MiB margin) before streaming. Interrupted
downloads leave a `.part` file named `.libgen-mcp-*.part` in that directory so a later call can
resume. Under Docker the container runs as UID `10001`, so the mounted download volume must be
writable by that user.

That applies to a local stdio/Docker server. A remote/hosted server (started with `--http`)
cannot write to disk at all, so no file ever lands anywhere on it: `download` automatically
returns a link (a `resource_link` plus a `resolved` object) instead of saving a file —
`resolve_only` is implied and need not be set. A hosted **stdio** server — for example running
behind `mcp-proxy` on a catalog like Glama — is on the same remote/ephemeral footing even
without an HTTP listener; set `LIBGEN_MCP_REMOTE_DOWNLOADS=1` on it so its downloads come back
as links too.
