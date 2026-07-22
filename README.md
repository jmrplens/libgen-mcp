<p align="center">
  <img src="assets/banner.png" alt="libgen-mcp" width="100%">
</p>

<p align="center">

[![GitHub Release](https://img.shields.io/github/v/release/jmrplens/libgen-mcp?style=flat&logo=github&label=Release)](https://github.com/jmrplens/libgen-mcp/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Platform](https://img.shields.io/badge/Windows%20%7C%20Linux%20%7C%20macOS-amd64%20%26%20arm64-lightgrey?style=flat&logo=windows-terminal&logoColor=white)
[![Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=jmrplens_libgen-mcp2&metric=alert_status)](https://sonarcloud.io/summary/overall?id=jmrplens_libgen-mcp2)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=jmrplens_libgen-mcp2&metric=coverage)](https://sonarcloud.io/summary/overall?id=jmrplens_libgen-mcp2)
[![Go Reference](https://pkg.go.dev/badge/github.com/jmrplens/libgen-mcp.svg)](https://pkg.go.dev/github.com/jmrplens/libgen-mcp)

</p>

<p align="center">

[![Cursor Directory](https://img.shields.io/badge/Cursor-Directory-1f9cf0?style=flat&logo=cursor&logoColor=white)](https://cursor.directory/plugins/libgen-mcp)
[![libgen-mcp MCP server](https://glama.ai/mcp/servers/jmrplens/libgen-mcp/badges/score.svg)](https://glama.ai/mcp/servers/jmrplens/libgen-mcp)
[![smithery badge](https://smithery.ai/badge/jmrp/libgen-mcp)](https://smithery.ai/servers/jmrp/libgen-mcp)
[![MCP Badge](https://lobehub.com/badge/mcp/jmrplens-libgen-mcp)](https://lobehub.com/mcp/jmrplens-libgen-mcp)
[![Live on Fly.io](https://img.shields.io/badge/Live-Fly.io-8b5cf6?style=flat&logo=flydotio&logoColor=white)](https://libgen-mcp.fly.dev/health)

</p>

**An [MCP](https://modelcontextprotocol.io) server, written in Go, that lets your AI assistant search and download from [Library Genesis](https://en.wikipedia.org/wiki/Library_Genesis) — books, research papers, magazines, comics, and standards.** It ships as one static binary (or a container) with three focused tools: `search`, `get_details`, and `download`. It works with Claude, Cursor, VS Code, and any MCP client.

You talk to your AI assistant; it does the searching and fetching. You don't need to track mirrors, MD5 hashes, or download URLs. Mirrors are discovered automatically and cached, with transparent failover, so the server keeps working as individual mirrors go up and down.

> "Find me the latest edition of _Clean Code_." · "Download that paper by its DOI." · "Search comics for _Watchmen_ and grab the CBR."

**📖 Full documentation, install guides & configuration reference → [jmrplens.github.io/libgen-mcp](https://jmrplens.github.io/libgen-mcp/)** (also in [Español](https://jmrplens.github.io/libgen-mcp/es/)). Light context footprint: the three tools add **~1,900 tokens** to a request (`make audit-tokens`), and no account, API key, or token is required. It's also verified against a **real LLM** — see the [eval results](https://jmrplens.github.io/libgen-mcp/eval-results/).

---

## Install

The recommended install is a **prebuilt static binary** — no Docker, no Go, no dependencies to manage. Download the asset for your platform from the [latest release](https://github.com/jmrplens/libgen-mcp/releases/latest):

```bash
# Example: Linux amd64 (macOS, Windows and arm64 builds are on the releases page)
curl -L -o libgen-mcp \
  https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-linux-amd64
chmod +x libgen-mcp && sudo mv libgen-mcp /usr/local/bin/
```

The binary is fully static (`CGO_ENABLED=0`), so it runs anywhere for that OS/arch with nothing else installed. Each release ships a `checksums.txt` to verify the download. Then register the binary with your MCP client — see [Claude Code](#claude-code-claude-mcp-add) below, or the [getting-started guide](docs/getting-started.md) for every client. **No token or account is required** — Library Genesis needs no credentials.

### Prefer a one-click button? (Docker)

Each button below registers the **Docker**-based server instead (auto-pulls `ghcr.io/jmrplens/libgen-mcp:latest` on first run; you need [Docker](https://www.docker.com/) installed).

<table>
  <tr>
    <th align="left">Client</th>
    <th align="left">One-click button</th>
  </tr>
  <tr>
    <td><b>VS Code</b></td>
    <td><a href="https://insiders.vscode.dev/redirect/mcp/install?name=libgen&amp;config=%7B%22command%22%3A%22docker%22%2C%22args%22%3A%5B%22run%22%2C%22-i%22%2C%22--rm%22%2C%22ghcr.io%2Fjmrplens%2Flibgen-mcp%3Alatest%22%5D%7D"><img alt="Install in VS Code" src="https://img.shields.io/badge/Install_in-VS_Code-0098FF?style=flat-square&amp;logo=visualstudiocode&amp;logoColor=white" /></a></td>
  </tr>
  <tr>
    <td><b>VS Code Insiders</b></td>
    <td><a href="https://insiders.vscode.dev/redirect/mcp/install?name=libgen&amp;config=%7B%22command%22%3A%22docker%22%2C%22args%22%3A%5B%22run%22%2C%22-i%22%2C%22--rm%22%2C%22ghcr.io%2Fjmrplens%2Flibgen-mcp%3Alatest%22%5D%7D&amp;quality=insiders"><img alt="Install in VS Code Insiders" src="https://img.shields.io/badge/Install_in-VS_Code_Insiders-24bfa5?style=flat-square&amp;logo=visualstudiocode&amp;logoColor=white" /></a></td>
  </tr>
  <tr>
    <td><b>Cursor</b></td>
    <td><a href="https://cursor.com/install-mcp?name=libgen&amp;config=eyJjb21tYW5kIjoiZG9ja2VyIiwiYXJncyI6WyJydW4iLCItaSIsIi0tcm0iLCJnaGNyLmlvL2ptcnBsZW5zL2xpYmdlbi1tY3A6bGF0ZXN0Il19"><img alt="Install in Cursor" src="https://cursor.com/deeplink/mcp-install-dark.svg" height="28" /></a></td>
  </tr>
  <tr>
    <td><b>LM Studio</b></td>
    <td><a href="https://lmstudio.ai/install-mcp?name=libgen&amp;config=eyJjb21tYW5kIjoiZG9ja2VyIiwiYXJncyI6WyJydW4iLCItaSIsIi0tcm0iLCJnaGNyLmlvL2ptcnBsZW5zL2xpYmdlbi1tY3A6bGF0ZXN0Il19"><img alt="Add to LM Studio" src="https://files.lmstudio.ai/deeplink/mcp-install-dark.svg" height="28" /></a></td>
  </tr>
  <tr>
    <td><b>Kiro</b></td>
    <td><a href="https://kiro.dev/launch/mcp/add?name=libgen&amp;config=%7B%22command%22%3A%22docker%22%2C%22args%22%3A%5B%22run%22%2C%22-i%22%2C%22--rm%22%2C%22ghcr.io%2Fjmrplens%2Flibgen-mcp%3Alatest%22%5D%7D"><img alt="Add to Kiro" src="https://kiro.dev/images/add-to-kiro.svg" height="28" /></a></td>
  </tr>
  <tr>
    <td><b>Claude Desktop</b></td>
    <td><a href="https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp.mcpb"><img alt="Download .mcpb extension" src="https://img.shields.io/badge/Download-.mcpb_extension-d97757?style=flat-square&amp;logo=claude&amp;logoColor=white" /></a></td>
  </tr>
</table>

The **Claude Desktop** row instead downloads a native [`.mcpb` desktop extension](https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp.mcpb) (macOS universal + Windows, no Docker) — open it with Claude Desktop and confirm the settings.

## Claude Code (`claude mcp add`)

Native binary (install it first — grab the prebuilt binary from [Install](#install) / the [latest release](https://github.com/jmrplens/libgen-mcp/releases/latest), or build from [source](#building) — then register it):

```bash
claude mcp add libgen -- /usr/local/bin/libgen-mcp
```

Or Docker (no install — pulls the image on first run):

```bash
claude mcp add libgen -- docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest
```

**Then just ask:** open your AI client and try _"Search Library Genesis for the Rust book."_

## Tools

Every result is returned on two channels: the structured JSON output (fields below) and a human-readable Markdown rendering in the text content — for `search`, a results table with each result's clickable download links. Both channels lead with a `next_steps` guidance list, and the search guidance tells the model to include the download links when presenting results.

### `search`

Search the Library Genesis catalog. Returns a page of file results with metadata, MD5 hashes, and download options, plus pagination metadata.

| Parameter          | Type     | Required | Description                                                                                                                  |
| ------------------ | -------- | -------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `query`            | string   | yes      | Search text.                                                                                                                 |
| `topics`           | string[] | no       | Collections to search: `nonfiction`, `fiction`, `articles`, `magazines`, `comics`, `standards`, `fiction_rus`. Omit for all. |
| `search_in`        | string[] | no       | Fields to match: `title`, `author`, `series`, `year`, `publisher`, `isbn`. Omit for all.                                     |
| `results_per_page` | int      | no       | Results per page: `25`, `50`, or `100` (default `25`).                                                                       |
| `page`             | int      | no       | Result page, starting at `1`.                                                                                                |
| `order`            | string   | no       | Sort by: `id`, `time_added`, `title`, `author`, `year`, `size`.                                                              |
| `order_mode`       | string   | no       | `asc` or `desc`.                                                                                                             |

The response includes pagination metadata so the model can decide whether to page or refine:

| Field              | Type   | Description                                                                                                                                                                                                |
| ------------------ | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `next_steps`       | array  | Model-facing follow-up suggestions with a ready-to-run example (e.g. a `get_details`/`download` call using a result's `md5`/`doi`, or how to broaden a no-match query). Every tool output leads with this. |
| `results`          | array  | The file records on this page.                                                                                                                                                                             |
| `page`             | int    | The page number returned.                                                                                                                                                                                  |
| `results_per_page` | int    | Page size in effect.                                                                                                                                                                                       |
| `total_files`      | string | Total matches reported by the mirror for the query.                                                                                                                                                        |
| `reachable`        | int    | How many of those matches were actually parsed/reachable.                                                                                                                                                  |
| `truncated`        | bool   | `true` when only the first slice of matches is reachable.                                                                                                                                                  |
| `hint`             | string | When truncated, a suggestion to refine the query (add author/year, title-only columns, topics).                                                                                                            |
| `has_more`         | bool   | `true` when a full page was returned (more results likely on the next page).                                                                                                                               |
| `mirror`           | string | The mirror that served the query.                                                                                                                                                                          |

### `get_details`

Full metadata for a record (description, identifiers, DOI, cover, related edition) via the libgen JSON API. Look up by `md5` **or** by `id`, not both.

| Parameter | Type   | Required | Description                                                          |
| --------- | ------ | -------- | -------------------------------------------------------------------- |
| `md5`     | string | one of   | File MD5 hash from a search result (returns file + related edition). |
| `id`      | string | one of   | Edition or file id.                                                  |
| `object`  | string | no       | With `id`: `edition` (default) or `file`.                            |

### `download`

Download a file to a local directory. Provide `md5` for a book **or** `doi` for an article (at least one is required); the server resolves the appropriate source chain and, for book (`md5`) downloads, verifies the result against the expected hash (DOI/article downloads are not MD5-verified). Returns the saved path, size, and the source that served it.

| Parameter      | Type   | Required | Description                                                                                                                                                                                    |
| -------------- | ------ | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `md5`          | string | one of   | File MD5 hash from a book search result.                                                                                                                                                       |
| `doi`          | string | one of   | DOI from an article search result; articles are fetched by DOI.                                                                                                                                |
| `path`         | string | no       | Destination directory (default: `LIBGEN_MCP_DOWNLOAD_DIR` or `~/Downloads`).                                                                                                                   |
| `filename`     | string | no       | Destination filename (default: a clean name from the record metadata or the mirror).                                                                                                           |
| `source`       | string | no       | Restrict the download to one source: `libgen`/`randombook` (books) or `unpaywall`/`scihub` (articles). Omit to try all with failover.                                                          |
| `resolve_only` | bool   | no       | Return the direct download **URL** as a link instead of downloading. Use for a remote/hosted server (it can't write to your machine) or to fetch the file with your own tool. Default `false`. |

> **Where the file goes — local vs. remote.** By default `download` fetches the file to the machine **running the server**. With a **local** stdio/Docker server that is your own machine, so files land in your download dir (great for autonomous local agents). A **remote/hosted** server runs elsewhere and cannot write to your disk, so there `download` **always returns a link** instead — a `resource_link` + a `resolved` object with any required `headers` — and you don't need to set `resolve_only`; it's implied. A server is in remote mode when it is started with `--http`, **or** when `LIBGEN_MCP_REMOTE_DOWNLOADS=1` is set (for a hosted **stdio** deployment — e.g. behind `mcp-proxy` on a catalog like Glama — whose disk is ephemeral/unreachable). You (or your agent's fetch tool) retrieve that URL, so the file ends up where the fetch runs. On a local server you can still pass `resolve_only: true` to opt into the same link behavior per call.

If both `md5` and `doi` are given, article sources are tried first, then book sources.

## Configuration

**It works out of the box — zero configuration, no account.** Every variable below is optional. Common setups (add these as `env` entries in your MCP client config, or `-e NAME=value` with Docker):

- **Enable open-access articles (Unpaywall):** `LIBGEN_MCP_UNPAYWALL_EMAIL=you@example.com` — disabled by default; the Unpaywall API needs a contact email. Without it, DOIs still resolve via Sci-Hub.
- **Choose where files land:** `LIBGEN_MCP_DOWNLOAD_DIR=/path/to/downloads` (default `~/Downloads`; the `download` tool's `path` argument overrides per call).
- **Pin one mirror** (skip auto-discovery): `LIBGEN_MIRROR=https://libgen.li`.
- **Restrict sources** (e.g. books only, no article sources): `LIBGEN_MCP_SOURCES=libgen,randombook`.
- **Cap download size / concurrency:** `LIBGEN_MCP_MAX_DOWNLOAD_BYTES=1073741824` (1 GiB), `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS=1`.

Full reference and tuning knobs (rate limits, retry/stall schedules, Sci-Hub hosts) are in the **[configuration guide](https://jmrplens.github.io/libgen-mcp/configuration/)**.

### Environment variables

Every variable is optional; an empty or unset value uses the default. A present-but-invalid numeric value is an error rather than a silent fallback.

| Variable                                | Default                                                  | Description                                                                                                                                                                                                                                                                                                                                             |
| --------------------------------------- | -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `LIBGEN_MIRROR`                         | _(auto-discovery)_                                       | Force a specific mirror, e.g. `https://libgen.li`. Skips auto-discovery. Must be an `http(s)` URL.                                                                                                                                                                                                                                                      |
| `LIBGEN_MCP_DOWNLOAD_DIR`               | `~/Downloads`                                            | Default destination directory for `download` (created if missing, checked for writability).                                                                                                                                                                                                                                                             |
| `LIBGEN_MCP_TIMEOUT`                    | `30s`                                                    | Per-request HTTP timeout (Go duration, e.g. `45s`, `1m`). Range `(0, 10m]`.                                                                                                                                                                                                                                                                             |
| `LIBGEN_MCP_LOG_LEVEL`                  | `info`                                                   | Log level: `debug`, `info`, `warn`, or `error`.                                                                                                                                                                                                                                                                                                         |
| `LIBGEN_MCP_RATE_RPS`                   | `1`                                                      | Allowed outbound requests per second. Range `(0, 20]`.                                                                                                                                                                                                                                                                                                  |
| `LIBGEN_MCP_RATE_BURST`                 | `1`                                                      | Maximum rate-limiter burst. Range `[1, 100]`.                                                                                                                                                                                                                                                                                                           |
| `LIBGEN_MCP_MAX_DOWNLOAD_BYTES`         | `0` _(no limit)_                                         | Maximum download size in bytes. Range `[0, 50 GiB]`; `0` disables the ceiling.                                                                                                                                                                                                                                                                          |
| `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS`   | `2`                                                      | Simultaneous downloads allowed. Range `[1, 16]`.                                                                                                                                                                                                                                                                                                        |
| `LIBGEN_MCP_RETRY_ATTEMPTS`             | `3`                                                      | Passes over the mirror list for page requests (search / details / link resolution), with backoff; does not govern file downloads. Range `[1, 10]`.                                                                                                                                                                                                      |
| `LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS` | `5s,5s,5s,10s,10s,10s,15s`                               | Staged waits between attempts to get a download to _begin_ (resolve / connect / first byte). `N` waits = `N+1` attempts (~60 s by default). Comma-separated Go durations; each in `(0, 10m]`; at most 20.                                                                                                                                               |
| `LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT`     | `60s`                                                    | Progress-resetting stall window while streaming: a download is cut only if _no_ bytes arrive for this long, so a slow-but-progressing transfer is never killed. Range `(0, 1h]`.                                                                                                                                                                        |
| `LIBGEN_MCP_UNPAYWALL_EMAIL`            | _(unset — unpaywall disabled)_                           | Contact email for the Unpaywall API (DOI lookups). The API rejects requests without one, so an unset value disables the `unpaywall` source entirely (and hides it from the download tool's `source` schema). Set your own address to enable it.                                                                                                         |
| `LIBGEN_MCP_SCIHUB_HOSTS`               | `sci-hub.ee,sci-hub.se,sci-hub.st,sci-hub.ru,sci-hub.wf` | Ordered, comma-separated Sci-Hub mirror hosts (bare host, no scheme). Tried in order until one serves.                                                                                                                                                                                                                                                  |
| `LIBGEN_MCP_SOURCES`                    | _(all enabled)_                                          | Comma-separated allow-list of download sources: `unpaywall`, `scihub`, `libgen`, `randombook`.                                                                                                                                                                                                                                                          |
| `LIBGEN_MCP_REMOTE_DOWNLOADS`           | `false`                                                  | Force `download` to always return a link (a `resource_link` + `resolved` object) instead of saving a file — the same behavior `--http` uses. Set it for a **hosted stdio** deployment (e.g. behind `mcp-proxy` on a catalog) whose disk the client can't reach. `--http` implies it; this covers the stdio-hosted case. Accepts `1`/`true`/`0`/`false`. |

## Multi-source downloads

`download` runs an ordered fallback chain and stops at the first source that delivers a valid file:

- **Books (by `md5`):** `libgen` (mirror `ads.php` key + CDN redirect) → `randombook` (fresh-mirror discovery).
- **Articles (by `doi`):** `unpaywall` (open-access PDF, only when `LIBGEN_MCP_UNPAYWALL_EMAIL` is set) → `scihub` (rotating Sci-Hub hosts).
- **Both `md5` and `doi` given:** article sources (`unpaywall`, `scihub`) are tried first, then book sources (`libgen`, `randombook`).

You can restrict or reorder which sources participate with `LIBGEN_MCP_SOURCES`. Additional guarantees:

- **MD5 verification** — book downloads are checked against the expected hash so a corrupt or wrong file is rejected, not saved.
- **Resumable downloads** — interrupted transfers resume via HTTP range requests instead of restarting.
- **Clean filenames** — with no explicit `filename`, book downloads are named `Author - Title (Year).ext` from the record metadata, falling back to the mirror-announced name.

## Robustness

- **Mirror failover** — mirrors are auto-discovered, cached, and rotated; a failed request transparently retries the next live mirror.
- **Retry with backoff** — transient HTTP failures are retried up to `LIBGEN_MCP_RETRY_ATTEMPTS` times with exponential backoff.
- **Rate limiting** — outbound requests are throttled (`LIBGEN_MCP_RATE_RPS` / `LIBGEN_MCP_RATE_BURST`) to stay polite to mirrors.
- **Graceful shutdown** — in-flight work is allowed to drain on termination signals; tool panics are recovered so the stdio session never dies.

## Documentation

- Guides live in [`docs/`](docs/): getting started, configuration, tools reference, architecture, and troubleshooting.
- Full documentation site (bilingual EN/ES): <https://jmrplens.github.io/libgen-mcp/>

## Building

Install the binary with Go:

```bash
go install github.com/jmrplens/libgen-mcp/cmd/server@latest
```

This produces a binary named `server` in `$(go env GOPATH)/bin`. Rename it to `libgen-mcp` (or build with an explicit name) and put it on your `PATH`:

```bash
go build -o libgen-mcp ./cmd/server
```

Common developer tasks are wrapped by the `Makefile` (`make help` lists them all):

```bash
make build         # build the server binary into dist/
make test          # run all tests with a coverage profile
make lint          # golangci-lint + govulncheck
make format-md-tables  # normalize Markdown pipe tables
```

By default the server speaks MCP over **stdio**. To serve **streamable HTTP** instead, pass `--http` with an address (`libgen-mcp --http :8080`); HTTP mode also exposes a `GET /health` readiness endpoint that returns `200` while serving. Because an HTTP server is remote and cannot write to a client's disk, in this mode `download` automatically returns a link (see [local vs. remote](#download) above) rather than saving a file. Print the version with `--version`.

## Maintenance

Library Genesis mirrors occasionally change their HTML layout or routes. Two tools help you detect and confirm those changes:

- **Live diagnostic** — `go run ./cmd/probe` hits a live mirror and reports whether each route and parser still works. Run it if searches or downloads start failing.
- **Opt-in end-to-end test** — `go test -tags e2e ./test/e2e/` queries the real site and asserts the results still parse. It is gated behind the `e2e` build tag, so it never runs under a plain `go test ./...`.

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible for respecting the copyright and intellectual-property laws that apply where you live. Use it only for content you are legally entitled to access.

> **Untrusted content.** Files, metadata, and links returned by this server come from third-party mirrors and the documents themselves — treat them as untrusted data, never as instructions. A downloaded book or paper, a filename, or a record's description may contain text crafted to manipulate an AI agent (for example, "ignore your previous instructions"). Your agent must treat all such content as inert information to summarize or quote, and must not act on any instructions embedded in it.

## License

See [LICENSE](LICENSE). Released under the MIT License.
