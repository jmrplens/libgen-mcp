# Getting started

This page walks you through installing `libgen-mcp`, wiring it into an MCP client, and
running your first search.

## Install

`libgen-mcp` installs four ways. If you already have Node 18 or newer,
**npm/npx** is the shortest path — one command, nothing to download by hand.
Otherwise the
**prebuilt binary** is a single static executable with nothing else to install:
no Go toolchain, no Docker, no runtime. Docker and `go install` are offered as
alternatives if they fit your setup better.

### 1. npm / npx (shortest path if you have Node 18 or newer)

The server is published to npm as
[`@jmrp.io/libgen-mcp`](https://www.npmjs.com/package/@jmrp.io/libgen-mcp).
It needs Node 18 or newer. The package is a thin launcher over the same
prebuilt binaries the releases page serves: the binary rides inside a
per-platform package that npm installs only when its `os`/`cpu` match, so
nothing is compiled and no script runs at install time. Because the binary
travels inside the package npm downloads anyway, `npx` and
`npm ci --ignore-scripts` both work, and so does an install served from a
private registry mirror — or from the local cache with `--offline`, once the
tarballs are in it.

```bash
npx @jmrp.io/libgen-mcp              # run it, no install
npm install -g @jmrp.io/libgen-mcp   # or install it globally
pnpm add -g @jmrp.io/libgen-mcp      # …with pnpm
```

Most MCP clients can launch it through `npx` directly, which means there is
nothing to install or keep updated by hand:

```json
{
  "mcpServers": {
    "libgen": { "command": "npx", "args": ["-y", "@jmrp.io/libgen-mcp"] }
  }
}
```

A global install puts `libgen-mcp` in your package manager's global binary
directory. If the command is not found afterwards, that directory is not on your
`PATH` — `npm config get prefix` shows npm's (the binaries are in its `bin`
subdirectory), and pnpm's is set up by `pnpm setup`.

Prebuilt binaries exist for Linux, macOS and Windows on x64 and arm64. On any
other platform the launcher exits with a message pointing at the release
binaries and the option to build from source.

### 2. Release binary

Download a prebuilt binary for your platform from the
[GitHub releases](https://github.com/jmrplens/libgen-mcp/releases) page. Assets are named
`libgen-mcp-<os>-<arch>` (for example `libgen-mcp-linux-amd64` or `libgen-mcp-darwin-arm64`),
covering Linux, macOS, and Windows on both amd64 and arm64.

```bash
# Example: Linux amd64
curl -L -o libgen-mcp \
  https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-linux-amd64
chmod +x libgen-mcp
sudo mv libgen-mcp /usr/local/bin/
```

The binary is fully static (built with `CGO_ENABLED=0`), so it depends on nothing on
the host and runs straight away. Each release also ships a `checksums.txt`; verify your
download against it before running.

### 3. Docker

Prefer containers, or want a zero-install command your client pulls on first run? A
multi-arch image is published to the GitHub Container Registry:

```bash
docker pull ghcr.io/jmrplens/libgen-mcp:latest
```

The image runs the server on **stdio by default** — the correct mode for MCP clients, so
`docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest` (as the one-click buttons and
`claude mcp add` use it) works out of the box. Mount a writable volume for downloads and
point `LIBGEN_MCP_DOWNLOAD_DIR` at it; the container runs as a non-root user, so the host
directory must be writable by UID `10001`:

```bash
docker run -i --rm \
  -v "$HOME/Downloads:/downloads" \
  -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads \
  ghcr.io/jmrplens/libgen-mcp:latest
```

For the streamable HTTP transport instead, pass `--http 0.0.0.0:8080` and publish the port;
HTTP mode also exposes a `GET /health` readiness endpoint:

```bash
docker run --rm -p 8080:8080 \
  -v "$HOME/Downloads:/downloads" \
  -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads \
  ghcr.io/jmrplens/libgen-mcp:latest --http 0.0.0.0:8080
```

### 4. `go install` (from source)

If you already have Go 1.27 or newer and prefer building from source:

```bash
go install github.com/jmrplens/libgen-mcp/cmd/server@latest
```

This produces a binary named `server` in `$(go env GOPATH)/bin`. If you prefer to invoke
it as `libgen-mcp`, build it with an explicit name instead:

```bash
git clone https://github.com/jmrplens/libgen-mcp
cd libgen-mcp
go build -o libgen-mcp ./cmd/server
```

Make sure the resulting binary is on your `PATH`.

## Configure an MCP client

Point your client at the binary. The command is `libgen-mcp` (or the absolute path to the
`server` binary if you kept the default `go install` name). Over stdio no extra arguments
are needed.

### Claude Code

Add the server to your project's `.mcp.json` (or run `claude mcp add`):

```json
{
  "mcpServers": {
    "libgen": {
      "command": "libgen-mcp"
    }
  }
}
```

### Claude Desktop

The easiest path is the one-click **`.mcpb`** desktop extension from the
[latest release](https://github.com/jmrplens/libgen-mcp/releases/latest) (macOS universal +
Windows, no Docker): download it and open it with Claude Desktop, then confirm the settings.

To wire it up by hand instead, edit `claude_desktop_config.json`
(`~/Library/Application Support/Claude/` on macOS,
`%APPDATA%\Claude\` on Windows) and add the same `mcpServers` block, then restart Claude
Desktop:

```json
{
  "mcpServers": {
    "libgen": {
      "command": "/absolute/path/to/libgen-mcp",
      "env": {
        "LIBGEN_MCP_DOWNLOAD_DIR": "/absolute/path/to/downloads"
      }
    }
  }
}
```

Desktop clients do not inherit your shell `PATH`, so use an absolute `command` path.

### VS Code

VS Code's MCP support (or the Continue / Cline extensions) reads an `mcp.json` with the
same shape. In VS Code's own format:

```json
{
  "servers": {
    "libgen": {
      "command": "libgen-mcp",
      "type": "stdio"
    }
  }
}
```

### Hosted endpoint (no install)

A public instance runs at **`https://mcp.jmrp.io/libgen`**. It needs no account, no key and
no local process — point any HTTP-capable MCP client at it:

```json
{
  "mcpServers": {
    "libgen": { "type": "http", "url": "https://mcp.jmrp.io/libgen" }
  }
}
```

It is the fastest way to try the server. Running it **locally** — Docker or a binary, above —
remains the better way to keep using it, for two concrete reasons rather than as a
disclaimer:

- **Your queries travel through someone else's machine.** Locally, what you search for never
  leaves your computer. The hosted instance stores nothing, but "stores nothing" is a promise;
  "never sent" is a fact.
- **`download` cannot write to your disk from a remote server**, so it returns a link (a
  `resource_link` plus a `resolved` object) instead of saving a file. That is inherent to
  remote MCP rather than a property of this endpoint — see
  [Where the file goes](tools.md#where-the-file-goes-local-vs-remote).

The endpoint speaks the same stateless streamable HTTP described below: `POST` carries the
protocol, `GET` on the endpoint itself answers `405` by design — a path the server does not
serve answers `404` — and `https://mcp.jmrp.io/libgen/health`
answers `{"status":"ok","version":"…","commit":"…","started_at":"…","uptime_seconds":…}`. Its version tracks the releases of this repository, so it can briefly lag a fresh
tag.

It is one of the servers listed at **[mcp.jmrp.io](https://mcp.jmrp.io/)**, a directory of the
MCP servers the author maintains, each reachable at its own endpoint under the same domain;
[`servers.json`](https://mcp.jmrp.io/servers.json) is the same list for automated clients.

### Remote (streamable HTTP)

To run the server centrally and connect over HTTP instead of stdio, start it with an
address and point HTTP-capable clients at it:

```bash
libgen-mcp --http :8080
```

The endpoint is the mount itself — `POST /` here — and `POST /mcp` is an alias for it, because
enough clients and guides assume that path that a base URL pasted with or without it should
reach the same place. Under `--http-path` the alias moves with the mount (`/libgen/mcp`).

**Behind a reverse proxy, declare the name clients use.** A proxy forwards the client's `Host`
and connects over loopback, and a server that never heard that name refuses the request with
`403` — the DNS-rebinding check every MCP server is asked to make. Tell it the name with
`--public-url`, or vouch for the proxy with `--trusted-proxies`; either is enough, and neither
is optional on a wildcard bind, which declares no name of its own:

```bash
libgen-mcp --http :8080 --public-url https://mcp.example.org
```

In HTTP mode the server also exposes a `GET /health` readiness endpoint that returns `200`
while serving, handy for container and load-balancer health checks. It answers whatever `Host`
a prober sends, including none at all — HAProxy's `option httpchk` sends no `Host` unless one
is configured, and a refused probe marks a working instance down.

In HTTP mode the server also publishes **two server cards**, one per location, because the
two specifications that reserve those paths describe different documents:

- `GET /server-card`, served as `application/mcp-server-card+json`, is the **discovery card**
  SEP-2127 describes. It carries identity and nothing else — the registry name
  `io.github.jmrplens/libgen-mcp`, the running version, the description, the website and the
  repository — plus, when `--public-url` names one, a `remotes` entry giving that URL, the
  `streamable-http` type and the protocol versions *this* deployment negotiates. It lists no
  tools and no prompts: what a server exposes can vary per session, so a card cannot answer
  that honestly.
- `GET /.well-known/mcp/server-card.json`, served as `application/json`, is the **enumerating
  card** the earlier SEP-1649 draft described, kept because scanners already fetch it there.
  It carries `serverInfo`, the `capabilities` the handshake negotiates, an `authentication`
  block (this server takes none), and the full `tools` and `prompts` listings, so a directory
  can read the whole surface — the four prompts included — without opening an MCP session.

Both are served unauthenticated, answer CORS preflight and carry `Access-Control-Allow-Origin:
*` so a browser-based directory can read them, and both are unaffected by stateless mode's
`405` on the MCP endpoint. Both also carry a strong `ETag` derived from the document's own
bytes, so the revalidation after their one-hour `Cache-Control` costs a `304` instead of the
whole document — and because the tag depends on the bytes alone, two replicas behind one
balancer publish the same one.

The transport is **stateless by default**: there is no `Mcp-Session-Id`, each POST is a
complete request, and `GET`/`DELETE` on the MCP endpoint return `405` (`/health` is
unaffected). That is what MCP protocol `2026-07-28` requires over HTTP, and it lets any
number of replicas sit behind a plain load balancer with no sticky routing. Two more flags
tune the transport — `--json-response` returns `application/json` instead of SSE, and
`--max-request-body-bytes` tightens the 4 MiB body cap:

```bash
libgen-mcp --http :8080 --json-response --max-request-body-bytes 1048576
```

Those routes — the MCP endpoint, `/health` and the two card paths — are the whole HTTP
surface. **Every other path answers `404`**, with a JSON body naming the endpoint it should
have asked for; a `405` now means only what it says, that the MCP endpoint was reached with
the wrong method. If a reverse proxy forwards its prefix to this server instead of stripping
it, mount the whole set under that prefix with `--http-path`:

```bash
libgen-mcp --http :8080 --http-path /libgen
```

The endpoint is then `POST /libgen`, the probe `GET /libgen/health`, and the cards live under
`/libgen` too; `libgen`, `/libgen` and `/libgen/` all mean the same mount, while a value with
a query, a fragment, a `..` segment or a percent-escape is refused at startup. Every response
— the `404` and the `405` included — carries `X-Content-Type-Options: nosniff`,
`X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store` and
`Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`. The card overrides the
cache header with its own one-hour lifetime. `Strict-Transport-Security` joins them only when
this process terminates TLS itself (`--tls-cert`, below); behind a proxy that terminates it,
the proxy is the layer that sends it.

If a client of yours still needs the old session-based transport, pass `--stateless=false`;
it then negotiates MCP protocol `2025-11-25` or older. See
[Architecture](architecture.md#stateless-mode) for what else changes, including the
elicitation note.

Because an HTTP server answers clients whose disk it cannot write to, `download` returns a
link (a `resource_link` plus a `resolved` object) instead of saving a file — you don't need
to set `resolve_only` in this mode. See [Tools](tools.md#where-the-file-goes-local-vs-remote)
for details.

An HTTP server also serves **three** tools rather than four by default: `read` is not registered, because
returning a page of text means fetching the whole file over an egress IP shared by everyone
using your deployment. Clients fetch the link `download` gives them and read the file
themselves. If the egress is yours to spend, `LIBGEN_MCP_SERVER_FETCH=1` turns the tool back
on — see [Configuration](configuration.md#libgen_mcp_server_fetch).

Hosting a **stdio** server remotely instead (e.g. behind `mcp-proxy` so it can be listed on a
catalog like Glama) puts you in the same situation without `--http`: the disk is remote and
the client can't reach it. Set `LIBGEN_MCP_REMOTE_DOWNLOADS=1` to put that stdio server into
the same remote-download mode.

#### Behind a proxy on the same machine: a unix socket

If the only thing that talks to this server is a reverse proxy on the same host, `--http` also
takes a filesystem path and binds a unix socket there instead of a port:

```bash
libgen-mcp --http /run/mcp-libgen.sock
```

The rule is that a value containing `/` is a path: `/run/mcp-libgen.sock` and `./mcp.sock` are
sockets, while `:8080`, `127.0.0.1:8080` and — deliberately — a bare `mcp.sock` are addresses,
because a bare name cannot be told apart from a hostname. Prefer this over TLS for a same-host
proxy: it does not encrypt the hop, it removes it. There is no bridge to read, no `docker-proxy`
hop, no port another local process can reach, and no certificate to issue or rotate.

The socket is created `0660` — owner and group only — so the proxy reaches it by sharing the
group rather than by being any local account at all; `--http-socket-mode 0600` (or any other
octal mode) changes that. nginx points an upstream straight at the path:

```nginx
upstream libgen {
    server unix:/run/mcp-libgen.sock;
}

server {
    listen 443 ssl;
    server_name mcp.example.org;

    location / {
        proxy_pass         http://libgen;
        proxy_http_version 1.1;
        proxy_buffering    off;   # the POST response is a real SSE stream
        proxy_read_timeout 1h;    # downloads emit progress for minutes
        proxy_set_header   X-Real-IP $remote_addr;
    }
}
```

That last header is only worth sending if the server is told to read it. A unix-socket peer is
a path rather than an address, so the trust is stated with the literal `unix`:
`--trusted-proxy-header X-Real-IP --trusted-proxies unix`. Both flags or neither — either one
alone fails startup — and without them every caller is told apart by the socket, which is to
say not at all. Over TCP, name the address the server accepts connections from instead of
`unix`; behind a published container port that is the bridge gateway, not the `127.0.0.1` the
upstream line uses.

Under Docker the two containers share the socket's directory and publish nothing:

```yaml
services:
  libgen-mcp:
    image: ghcr.io/jmrplens/libgen-mcp:latest
    command: ["--http", "/run/mcp/libgen.sock"]
    user: "10001:10001"
    volumes:
      - ./run:/run/mcp # the socket lives here; no ports are published
  proxy:
    image: nginx:1.29-alpine
    ports:
      - "443:443"
    volumes:
      - ./run:/run/mcp
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
```

The proxy's worker processes have to run in the socket's group (GID `10001` here) to open a
`0660` socket — in nginx that is the second argument of the `user` directive, `user nginx
10001;`, since nginx re-initializes its groups when it drops privileges.

One thing that surprises people: a unix socket is still a non-empty `--http` value, so
`download` stays in remote (link-only) mode. That is correct — a socket behind a proxy serves
clients that are not on this machine — but "unix socket" reads like "local", and it is not.

#### Configuring the listener without a command line

Every HTTP flag also has a variable, so the same deployment can be written with no `command:`
at all — which is what a Kubernetes ConfigMap, a systemd unit's `Environment=` or an image
somebody else's platform runs for you has to work with:

```yaml
services:
  libgen-mcp:
    image: ghcr.io/jmrplens/libgen-mcp:latest
    environment:
      LIBGEN_MCP_HTTP_ADDR: "/run/mcp/libgen.sock"
      LIBGEN_MCP_TRUSTED_PROXY_HEADER: "X-Real-IP"
      LIBGEN_MCP_TRUSTED_PROXIES: "unix"
      LIBGEN_MCP_DRAIN_DELAY: "10s"
    user: "10001:10001"
    volumes:
      - ./run:/run/mcp
```

The name is the flag in upper case with dashes as underscores, under `LIBGEN_MCP_`. The one
exception is `--http` itself, whose variable is `LIBGEN_MCP_HTTP_ADDR`: `LIBGEN_MCP_HTTP` would
read as a switch rather than as an address. The full list is in
[Configuration → HTTP listener](configuration.md#http-listener).

**A flag you type still wins**, so a base image that sets its defaults in `environment:` and a
`command:` that overrides one of them behaves the way it looks. Typing a flag counts even when
you type its default value: `--stateless=true` beats `LIBGEN_MCP_STATELESS=0`, because the
question is whether you asked, not whether the answer differs. A variable that does not parse
fails startup naming both the variable and the flag, rather than silently leaving the default
in place — and the startup log names the variables that configured the listener, which is the
quickest answer to "why is it on that port".

#### Terminating TLS in this process

When the proxy is on **another** host, there is a network segment that has to exist, and this
server can terminate TLS itself:

```bash
libgen-mcp --http 0.0.0.0:8443 --tls-cert /etc/ssl/mcp.crt --tls-key /etc/ssl/mcp.key
```

Both flags or neither — a certificate without a key fails startup, as does a file that cannot
be read or a key that does not match its certificate, because the pair is loaded at startup
rather than at the first handshake. **A renewal written to the same two paths needs no
restart**: each handshake checks whether the files have moved and re-reads them when they have,
so certbot, Vault's agent or a remounted Kubernetes secret is picked up on the next connection,
and a half-written pair keeps the previous certificate rather than failing the handshake. The
listener requires TLS 1.2 or better and negotiates HTTP/2 with clients that offer it. Every response then carries
`Strict-Transport-Security: max-age=31536000; includeSubDomains`, which only the endpoint that
actually terminated the connection is in a position to claim.

If the certificate comes from a private CA, the proxy in front has to be pointed at that CA —
in nginx, `proxy_ssl_verify on;` **together with** `proxy_ssl_trusted_certificate /etc/nginx/ca.pem;`
and a `proxy_ssl_name` matching the certificate. nginx does not verify upstream certificates by
default, so leaving `proxy_ssl_verify` off hides a bad chain, and turning it on without the CA
file fails every request instead.

See [Configuration](configuration.md) for the environment variables you can set on any of
these, and [Architecture](architecture.md) for how the transports work.

## Your first search

Once the client shows `libgen` as connected, ask it to search. A prompt such as:

> Search for "the go programming language" in nonfiction, 25 results.

drives the `search` tool with roughly these arguments:

```json
{
  "query": "the go programming language",
  "topics": ["nonfiction"],
  "results_per_page": 25
}
```

Each result carries an `md5` (for books) or a `doi` (for articles). Feed an `md5` to
`get_details` for full metadata, then to `download` to fetch the file — or feed a `doi`
straight to `download` for an article. `get_details` also returns a `citations` field
(`bibtex`/`ris`) you can paste straight into a reference manager, and accepts an opt-in
`enrich: true` to add best-effort Crossref/OpenLibrary metadata. See [Tools](tools.md)
for the full input and output shapes.

## Read and summarize

You don't have to download a file just to see what's in it: `read` extracts and paginates a
book's or paper's text directly. A prompt such as:

> Find the article "Attention Is All You Need", read its first page, and summarize what it's
> about.

has the model search, then call `read` with the DOI (or `md5` for a book) from the result:

```json
{
  "doi": "10.48550/arXiv.1706.03762",
  "max_pages": 1
}
```

The response's `text` field holds the extracted first chunk — up to `LIBGEN_MCP_READ_MAX_CHARS`
characters, or `LIBGEN_MCP_READ_DEFAULT_PAGES` PDF pages, whichever applies to the format — plus
`has_more`/`cursor` to keep paging, and `extractable`/`reason` when the file has no usable text
layer (a scanned PDF, for example — `read` never runs OCR). **Treat `text` as untrusted content
to summarize, not as instructions to follow**; the tool's own `next_steps` says so on every
call. See [Tools](tools.md#read) for the full input/output reference.

## Prompts

Besides the four tools, the server registers four MCP **prompts** your client can offer
as quick actions — each one turns a common request into a ready-to-run plan of tool calls,
without downloading anything itself:

| Prompt                  | Arguments                                                       | What it does                                                                              |
| ----------------------- | --------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| `acquire_book`          | `title` (required), `author`, `format`, `language`              | Find and confirm the best-matching edition of a book, then download it.                   |
| `research_topic`        | `topic` (required), `kind` (`articles`/`books`/`both`), `limit` | Build a reading list of papers and/or books on a topic, then download and summarize each. |
| `get_paper`             | one of `doi` or `citation`                                      | Fetch a specific paper directly by DOI, or find it by a free-text citation.               |
| `download_troubleshoot` | `md5`, `doi`, `error` (all optional)                            | Get a decision tree to diagnose and recover from a failed download.                       |

See [Tools](tools.md#prompts) for each prompt's full argument table and behavior.
