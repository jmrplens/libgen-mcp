# Getting started

**Tutorial** — for someone running this for the first time.

This page walks you through installing `libgen-mcp`, wiring it into an MCP client, and
running your first search.

## Install

`libgen-mcp` is published to most of the places you already get software from.
Whichever you pick, you end up with the same single static binary: nothing is
compiled, nothing runs at install time, and no account, API key or token is
needed.

| Channel        | Run it without installing                               | Install it                                                       |
| -------------- | ------------------------------------------------------- | ---------------------------------------------------------------- |
| npm            | `npx @jmrp.io/libgen-mcp`                               | `npm install -g @jmrp.io/libgen-mcp`                             |
| PyPI           | `uvx libgen-mcp`                                        | `pipx install libgen-mcp`                                        |
| Homebrew       | —                                                       | `brew install jmrplens/tap/libgen-mcp`                           |
| NuGet          | `dnx libgen-mcp`                                        | `dotnet tool install -g libgen-mcp`                              |
| Docker         | `docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest` | —                                                                |
| Release binary | —                                                       | download and put it on your `PATH`                               |
| Go             | —                                                       | `go install github.com/jmrplens/libgen-mcp/v2/cmd/server@latest` |

The two shortest paths are spelled out below. **Every channel has its own
section in [Installation](install/overview.md)** — what you get, how to check it is
what this project published, how to upgrade it and how to remove it.

### npm / npx (shortest path if you have Node 18 or newer)

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

### Docker

Prefer containers, or want a zero-install command your client pulls on first run? A
multi-arch image is published to the GitHub Container Registry:

```bash
docker pull ghcr.io/jmrplens/libgen-mcp:latest
```

The image **decides its transport from what standard input is**, so
`docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest` (as the one-click buttons and
`claude mcp add` use it) connects a pipe and gets the stdio server, which is the correct
mode for an MCP client, and works out of the box. A run without `-i` — a Compose service
with no `stdin_open`, a Kubernetes pod, anything an orchestrator starts — connects
`/dev/null` and gets the streamable HTTP listener on port 8080 instead. Mount a writable
volume for downloads and point `LIBGEN_MCP_DOWNLOAD_DIR` at it; the container runs as a
non-root user, so the host directory must be writable by UID `10001`:

```bash
docker run -i --rm \
  -v "$HOME/Downloads:/downloads" \
  -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads \
  ghcr.io/jmrplens/libgen-mcp:latest
```

For the streamable HTTP transport instead, drop the `-i` and publish the port — the image's
default command is `--transport auto --http 0.0.0.0:8080`, so there is nothing to pass.
HTTP mode also exposes a `GET /health` readiness endpoint:

```bash
docker run --rm -p 8080:8080 \
  -v "$HOME/Downloads:/downloads" \
  -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads \
  ghcr.io/jmrplens/libgen-mcp:latest
```

Naming a listener yourself still works — `docker run --rm -p 9000:9000
ghcr.io/jmrplens/libgen-mcp:latest --http 0.0.0.0:9000` — with one thing to know: **an
argument replaces the default command wholesale**, `--transport auto` included. That is
harmless here, because naming a listener is deciding the transport, but it means a flag you
add is the *whole* command line rather than an addition to it.

Two settings that meet in a container and surprise people apart:

- A container binding `0.0.0.0` **with `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES` set is refused
  at startup**, and the default command above is exactly that listener. The refusal is
  deliberate — that variable lets a caller steer the server at addresses only this host can
  reach, which is not a power to hand to whoever finds the published port — so pass
  `--http 127.0.0.1:8080` and put a proxy in front, or leave the variable unset.
- A container on the **host's** network namespace (`--network host`) behind a proxy on that
  same host is reached over loopback, so it looks to the server like a local client. On a
  bridge network — the default — it is not, and the proxy's address is what arrives. See
  [The caller's address](deploy/reverse-proxy.md#the-callers-address) for which flags each
  shape needs.

### The other channels

The release binary, PyPI, Homebrew, NuGet, the Claude Desktop `.mcpb` bundle and
`go install` are each one command, and each puts the same executable on your
machine. They have a section apiece in [Installation](install/overview.md), which is
also where the verification recipes live — the release binary and the image are
signed, and checking that is a step worth taking once.

## Configure an MCP client

Point your client at the binary. The command is `libgen-mcp` (or the absolute path to the
`server` binary if you kept the default `go install` name). Over stdio no extra arguments
are needed. Claude Code is the example here; **[Connect a client](clients.md)** has the
complete entry for every other client — Claude Desktop, VS Code, Cursor, Windsurf, Zed,
JetBrains, Kiro, OpenCode, Cline, Continue, LM Studio, Gemini CLI, Codex and Goose — in
both its local and its remote form, the one-click install buttons, and where optional keys
go.

### Claude Code

Run `claude mcp add libgen -- libgen-mcp`, or add the server to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "libgen": {
      "command": "libgen-mcp"
    }
  }
}
```

On Claude Desktop the one-click [`.mcpb` extension](install/claude-desktop.md) needs no
configuration file at all. Desktop clients do not inherit your shell `PATH`, so a
configuration file there takes an absolute `command` path.

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

It is the fastest way to try the server; running it locally remains the better way to keep
using it, because your queries then never leave your computer and `download` saves the file
instead of returning a link. [Hosted endpoint](hosted.md) says what the public instance
serves, what it limits and logs, and how to run the same setup yourself.

### Remote (streamable HTTP)

To run the server centrally and connect HTTP-capable clients to it instead of starting one
process per client, give it a listener:

```bash
libgen-mcp --http 127.0.0.1:8080
```

The endpoint is the mount itself, `POST /`, with `POST /mcp` as an alias, and `GET /health`
answers while the server runs. The transport is stateless, so any number of replicas can sit
behind one balancer. Over HTTP the server's disk and connection are not the client's:
`download` returns a link instead of saving a file, and `read` is not registered unless
`LIBGEN_MCP_SERVER_FETCH` turns it back on (see
[Where the file goes](tools.md#where-the-file-goes-local-vs-remote)).

A deployment other people reach needs more than that one flag: a proxy that forwards the name
clients use and passes the stream through unbuffered, the server told that name and which peer
is the proxy, and a supervisor that lets it drain when it stops. Each has its own page, with
configurations that were run as written:

- [Behind a reverse proxy](deploy/reverse-proxy.md): what every proxy has to do, and complete
  configurations for nginx, Caddy, Traefik, Apache httpd, HAProxy and Cloudflare Tunnel, over
  TCP, a unix socket or TLS.
- [Run as a service](deploy/service.md): systemd units for a port and a socket, the environment
  file, the descriptor limit, stopping and upgrading, launchd and Windows.
- [Containers and orchestration](deploy/containers.md): Compose files, on their own and behind
  nginx over a shared socket, and a Kubernetes Deployment with its probes and Ingress.
- [HTTP server mode](http-server-mode.md): how the transport behaves, flag by flag, and what the
  server refuses to start with.

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
