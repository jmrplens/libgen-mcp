# libgen-mcp

An [MCP](https://modelcontextprotocol.io) server, written in Go, for searching and
downloading from **Library Genesis** (the `libgen.li` mirror family). It exposes three
tools — `search`, `get_details`, and `download` — to any MCP-compatible client such as
Claude Code, Claude Desktop, or your own agent.

Mirrors are discovered automatically and cached, with transparent failover, so the server
keeps working as individual mirrors go up and down.

## Installation

```bash
go install github.com/jmrplens/libgen-mcp/cmd/server@latest
```

This produces a binary named `server` in `$(go env GOPATH)/bin`. Rename it (or build with an
explicit name) if you prefer to invoke it as `libgen-mcp`:

```bash
go build -o libgen-mcp ./cmd/server
```

Make sure the resulting binary is on your `PATH`.

## Client configuration

Point your MCP client at the binary. Example for Claude Code (`.mcp.json` or the equivalent
`mcpServers` block):

```json
{
  "mcpServers": {
    "libgen": {
      "command": "libgen-mcp"
    }
  }
}
```

If you kept the default `server` name from `go install`, use the absolute path to that
binary as the `command` instead.

## Tools

### `search`

Search the Library Genesis catalog. Returns a page of file results.

| Parameter          | Type       | Required | Description |
| ------------------ | ---------- | -------- | ----------- |
| `query`            | string     | yes      | Search text. |
| `topics`           | string[]   | no       | Collections to search: `nonfiction`, `fiction`, `articles`, `magazines`, `comics`, `standards`, `fiction_rus`. Omit for all. |
| `search_in`        | string[]   | no       | Fields to match: `title`, `author`, `series`, `year`, `publisher`, `isbn`. Omit for all. |
| `results_per_page` | int        | no       | Results per page: `25`, `50`, or `100`. |
| `page`             | int        | no       | Result page, starting at `1`. |
| `order`            | string     | no       | Sort by: `id`, `time_added`, `title`, `author`, `year`, `size`. |
| `order_mode`       | string     | no       | `asc` or `desc`. |

### `get_details`

Full metadata for a record (description, identifiers, DOI, cover, related edition) via the
libgen JSON API. Look up by `md5` **or** by `id`, not both.

| Parameter | Type   | Required | Description |
| --------- | ------ | -------- | ----------- |
| `md5`     | string | one of   | File md5 hash from a search result (returns file + related edition). |
| `id`      | string | one of   | Edition or file id. |
| `object`  | string | no       | With `id`: `edition` (default) or `file`. |

### `download`

Download a file by `md5` to a local directory, resolving the mirror download chain
(`ads.php` key + CDN redirect). Returns the saved path and size.

| Parameter  | Type   | Required | Description |
| ---------- | ------ | -------- | ----------- |
| `md5`      | string | yes      | File md5 hash from a search result. |
| `path`     | string | no       | Destination directory (default: `LIBGEN_MCP_DOWNLOAD_DIR` or `~/Downloads`). |
| `filename` | string | no       | Destination filename (default: the name announced by the mirror). |

## Environment variables

| Variable                  | Default       | Description |
| ------------------------- | ------------- | ----------- |
| `LIBGEN_MIRROR`           | *(auto)*      | Force a specific mirror, e.g. `https://libgen.li`. Skips auto-discovery. |
| `LIBGEN_MCP_DOWNLOAD_DIR` | `~/Downloads` | Default destination directory for `download`. |
| `LIBGEN_MCP_TIMEOUT`      | `30s`         | Per-request HTTP timeout (Go duration, e.g. `45s`, `1m`). |

## Mirrors

Mirrors are discovered automatically from
[shadowlibraries](https://shadowlibraries.github.io/DirectDownloads/libgen/) and cached for
24 hours in your OS cache directory (`~/.cache/libgen-mcp/mirrors.json` on Linux,
`~/Library/Caches/libgen-mcp/mirrors.json` on macOS). `libgen.li` is preferred, and the server
fails over to the next live mirror automatically when a request fails. Set `LIBGEN_MIRROR`
to pin a single mirror and bypass discovery.

## Transports

By default the server speaks MCP over **stdio**. To serve **streamable HTTP** instead, pass
`--http` with an address:

```bash
libgen-mcp --http :8080
```

Print the version and exit with `--version`.

## Maintenance

Library Genesis mirrors occasionally change their HTML layout or routes. Two tools help you
detect and confirm those changes:

- **Live diagnostic** — `go run ./cmd/probe` hits a live mirror and reports whether each
  route and parser still works. Run it if searches or downloads start failing.
- **Opt-in end-to-end test** — `go test -tags e2e ./...` runs a test that queries the real
  site and asserts the results still parse. It is gated behind the `e2e` build tag, so it
  never runs under a plain `go test ./...`.

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible for
respecting the copyright and intellectual-property laws that apply where you live. Use it
only for content you are legally entitled to access.

## License

See [LICENSE](LICENSE).
