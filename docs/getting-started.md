# Getting started

This page walks you through installing `libgen-mcp`, wiring it into an MCP client, and
running your first search.

## Install

The recommended way to install `libgen-mcp` is the **prebuilt binary**: a single
static executable with nothing else to install — no Go toolchain, no Docker, no
runtime. Docker and `go install` are offered as alternatives if they fit your
setup better.

### 1. Release binary (recommended)

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

### 2. Docker

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

### 3. `go install` (from source)

If you already have Go 1.26 or newer and prefer building from source:

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

### Remote (streamable HTTP)

To run the server centrally and connect over HTTP instead of stdio, start it with an
address and point HTTP-capable clients at it:

```bash
libgen-mcp --http :8080
```

In HTTP mode the server also exposes a `GET /health` readiness endpoint that returns `200`
while serving, handy for container and load-balancer health checks.

Because an HTTP server runs elsewhere and cannot write to your disk, `download` returns a
link (a `resource_link` plus a `resolved` object) instead of saving a file — you don't need
to set `resolve_only` in this mode. See [Tools](tools.md#where-the-file-goes-local-vs-remote)
for details.

Hosting a **stdio** server remotely instead (e.g. behind `mcp-proxy` so it can be listed on a
catalog like Glama) puts you in the same situation without `--http`: the disk is remote and
the client can't reach it. Set `LIBGEN_MCP_REMOTE_DOWNLOADS=1` to put that stdio server into
the same remote-download mode.

See [Configuration](configuration.md) for the environment variables you can set on any of
these, and [Architecture](architecture.md) for how the transports work.

## Your first search

Once the client shows `libgen` as connected, ask it to search. A prompt such as:

> Search Library Genesis for "the go programming language" in nonfiction, 25 results.

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
