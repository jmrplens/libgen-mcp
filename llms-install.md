# libgen-mcp — AI agent installation guide

This guide is for AI agents (Cline, Claude, Cursor, etc.) installing the
libgen-mcp server on behalf of a user. Follow the steps in order. Every step is
deterministic — no build from source is required.

## What this server is

A single-binary MCP server (Go) that lets an AI assistant search and download
from [Library Genesis](https://en.wikipedia.org/wiki/Library_Genesis) — books,
research papers, magazines, comics, and standards. It exposes three tools over
stdio: `search`, `get_details`, and `download`. Books resolve by MD5 (libgen +
randombook); articles resolve by DOI (Unpaywall + Sci-Hub).

**No account, token, or credentials are required.** Unlike many MCP servers,
there is nothing to authenticate — skip straight to installation.

## Step 1 — Choose an install method

### Method A: Native binary (recommended — no dependencies)

A single static executable: no Docker, no Go, nothing else to install. If you
cannot determine the user's OS and architecture, use Docker (Method B) instead.

1. Download the binary for the user's platform from the latest release at
   `https://github.com/jmrplens/libgen-mcp/releases/latest`. Asset names:
   - `libgen-mcp-linux-amd64`
   - `libgen-mcp-linux-arm64`
   - `libgen-mcp-darwin-amd64`
   - `libgen-mcp-darwin-arm64`
   - `libgen-mcp-windows-amd64.exe`
   - `libgen-mcp-windows-arm64.exe`
2. Make it executable (`chmod +x`) on Linux/macOS and place it somewhere stable,
   e.g. `/usr/local/bin/libgen-mcp`. Verify against the release `checksums.txt`.
3. Configure (desktop clients do not inherit your shell `PATH`, so use an
   absolute `command` path) for clients that use the `mcpServers` key (Cursor
   `mcp.json`, Claude Desktop `claude_desktop_config.json`, Cline
   `cline_mcp_settings.json`):

```json
{
  "mcpServers": {
    "libgen": {
      "command": "/usr/local/bin/libgen-mcp"
    }
  }
}
```

For VS Code / GitHub Copilot, use the `servers` key with an explicit
`"type": "stdio"` and the same absolute `command`.

**Claude Code CLI one-liner** with the native binary:

```bash
claude mcp add libgen -- /usr/local/bin/libgen-mcp
```

### Method B: Docker (no download; pulls on first run)

Requires Docker installed and running. Verify with `docker --version`. The image
is pulled automatically on first run.

MCP configuration for clients that use the `mcpServers` key (Cursor `mcp.json`,
Claude Desktop `claude_desktop_config.json`, Cline `cline_mcp_settings.json`):

```json
{
  "mcpServers": {
    "libgen": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "ghcr.io/jmrplens/libgen-mcp:latest"]
    }
  }
}
```

For VS Code / GitHub Copilot, which use the `servers` key with an explicit
`type`:

```json
{
  "servers": {
    "libgen": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "ghcr.io/jmrplens/libgen-mcp:latest"],
      "type": "stdio"
    }
  }
}
```

**Claude Code CLI one-liner** (registers the Docker server directly):

```bash
claude mcp add libgen -- docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest
```

> Downloads happen **inside** the container unless you mount a host directory.
> To save files to the host, add a volume and point the download dir at it:
> `-v "$HOME/Downloads:/downloads" -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads` in the
> `args` (the container runs as UID `10001`, so the host dir must be writable by
> it).

## Step 2 — Optional environment variables

All configuration is optional; the defaults work out of the box. Add entries to
the `env` block (and, for Docker, a matching `-e NAME` in `args`) only when the
user asks for the behavior:

| Variable                     | Default        | Purpose                                                                   |
| ---------------------------- | -------------- | ------------------------------------------------------------------------- |
| `LIBGEN_MCP_DOWNLOAD_DIR`    | `~/Downloads`  | Destination directory for `download`.                                     |
| `LIBGEN_MCP_UNPAYWALL_EMAIL` | maintainer's   | Contact email for the Unpaywall API. Recommended if resolving DOIs.       |
| `LIBGEN_MIRROR`              | auto-discovery | Pin a specific Library Genesis mirror, e.g. `https://libgen.li`.          |
| `LIBGEN_MCP_LOG_LEVEL`       | `info`         | `debug`, `info`, `warn`, or `error`.                                      |
| `LIBGEN_MCP_SOURCES`         | all enabled    | Restrict download sources: `unpaywall`, `scihub`, `libgen`, `randombook`. |
| `LIBGEN_MCP_REMOTE_DOWNLOADS` | `false`       | Set to `1` when hosting the stdio server remotely (e.g. behind `mcp-proxy`): `download` returns a link instead of saving a file to an unreachable disk. |

Full reference: <https://jmrplens.github.io/libgen-mcp/configuration/>

## Step 3 — Verify the installation

1. Restart or reload the MCP client so it picks up the new configuration.
2. The server should appear as connected with three tools: `search`,
   `get_details`, and `download`.
3. Smoke test: call `search` with `{ "query": "the go programming language",
   "topics": ["nonfiction"], "results_per_page": 25 }`. A successful response
   returns a page of file records, each with an `md5` (books) or `doi`
   (articles).

## Troubleshooting

- **Docker: `docker: command not found`** — use Method A (native binary), the
  recommended default anyway.
- **No search results / parse errors** — a mirror may be down or have changed
  layout. Mirrors fail over automatically; retry, or pin a known-good one with
  `LIBGEN_MIRROR`. Set `LIBGEN_MCP_LOG_LEVEL=debug` to trace per-mirror attempts.
- **Downloads not appearing on the host (Docker)** — files land inside the
  container by default; mount a volume and set `LIBGEN_MCP_DOWNLOAD_DIR` (see the
  note under Method A).
- **Article DOI lookups rate-limited** — set `LIBGEN_MCP_UNPAYWALL_EMAIL` to the
  user's own address to be a good API citizen.

## More documentation

- Project README: <https://github.com/jmrplens/libgen-mcp>
- Getting started (full client config): [docs/getting-started.md](docs/getting-started.md)
- Docs site: <https://jmrplens.github.io/libgen-mcp/>
