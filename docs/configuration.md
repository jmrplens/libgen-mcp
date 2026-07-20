# Configuration

`libgen-mcp` is configured entirely through environment variables. Every variable is
optional: an empty or unset value uses the documented default. A variable that is present
but malformed (a bad number, an out-of-range value, an unwritable directory, an unknown
source name) causes the server to **fail fast at startup** with an explanatory error rather
than silently falling back to the default.

Set these in your MCP client's `env` block (see [Getting started](getting-started.md)), in
your shell, or with `-e` flags on `docker run`.

## Reference

| Variable                                | Default                                                  | Range / allowed values                                                  | Meaning                                                                                                                                                                                                                                                                                                                                                          |
| --------------------------------------- | -------------------------------------------------------- | ----------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `LIBGEN_MIRROR`                         | *(auto-discovery)*                                       | `http`/`https` URL with a host                                          | Pin a single mirror, e.g. `https://libgen.li`, and skip auto-discovery. Trailing slashes are trimmed. When empty, mirrors are discovered and cached (see [Architecture](architecture.md)).                                                                                                                                                                       |
| `LIBGEN_MCP_DOWNLOAD_DIR`               | `~/Downloads`                                            | Any writable directory path                                             | Default destination directory for `download`. Created if missing and probed for writability at startup. The `download` tool's `path` argument overrides it per call.                                                                                                                                                                                             |
| `LIBGEN_MCP_TIMEOUT`                    | `30s`                                                    | `(0, 10m]`, Go duration                                                 | Per-request HTTP timeout for search, details, and link resolution (e.g. `45s`, `1m`). Does **not** cap streaming downloads, which are governed by the request context.                                                                                                                                                                                           |
| `LIBGEN_MCP_LOG_LEVEL`                  | `info`                                                   | `debug`, `info`, `warn`, `error`                                        | Minimum log level. Logs go to stderr (stdout is reserved for the stdio transport). Raise to `debug` to trace per-mirror attempts.                                                                                                                                                                                                                                |
| `LIBGEN_MCP_RATE_RPS`                   | `1`                                                      | `(0, 20]`, float                                                        | Allowed outbound requests per second (token-bucket refill rate) across all mirror requests and downloads.                                                                                                                                                                                                                                                        |
| `LIBGEN_MCP_RATE_BURST`                 | `1`                                                      | `[1, 100]`, int                                                         | Maximum burst size of the rate limiter — how many requests may fire back-to-back before throttling to `LIBGEN_MCP_RATE_RPS`.                                                                                                                                                                                                                                     |
| `LIBGEN_MCP_MAX_DOWNLOAD_BYTES`         | `0` (no limit)                                           | `[0, 53687091200]` (0–50 GiB), int                                      | Maximum size of a single download in bytes. `0` disables the cap. Enforced up front from `Content-Length` and again while streaming, so a lying or missing length is still caught.                                                                                                                                                                               |
| `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS`   | `2`                                                      | `[1, 16]`, int                                                          | Number of downloads allowed to run at the same time. Extra downloads queue on a semaphore until a slot frees.                                                                                                                                                                                                                                                    |
| `LIBGEN_MCP_RETRY_ATTEMPTS`             | `3`                                                      | `[1, 10]`, int                                                          | Maximum number of passes over the mirror list for a page request before giving up. Only transient failures (network/timeout/5xx/429) trigger a retry pass; a permanent 4xx fails over without retrying.                                                                                                                                                          |
| `LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS` | `5s,5s,5s,10s,10s,10s,15s`                               | Comma-separated Go durations; each in `(0, 10m]`; at most 20 waits      | Staged wait schedule between attempts to get a download to **begin** (resolve the URL, connect, and pull the first byte). `N` waits means `N+1` attempts; the default spans ~60 s over 8 attempts. Retries cover resolve errors, connection errors, non-2xx statuses, and no-first-byte responses. Once bytes flow, start-retries stop and streaming takes over. |
| `LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT`     | `60s`                                                    | `(0, 1h]`, Go duration                                                  | Progress-resetting stall window while streaming: a download is aborted only when **no** bytes arrive for this long. A slow-but-progressing transfer (e.g. 20–50 kB/s) is never cut. `LIBGEN_MCP_TIMEOUT` does not apply to downloads.                                                                                                                            |
| `LIBGEN_MCP_UNPAYWALL_EMAIL`            | `mail@jmrp.io`                                           | Must contain `@` and a domain with a `.`                                | Contact email sent to the [Unpaywall](https://unpaywall.org) API on every article lookup (the API requires it). Set your own address so requests are attributed to you rather than the maintainer's default.                                                                                                                                                     |
| `LIBGEN_MCP_SCIHUB_HOSTS`               | `sci-hub.ee,sci-hub.se,sci-hub.st,sci-hub.ru,sci-hub.wf` | Comma-separated bare hosts (no scheme, no path); at least one           | Ordered Sci-Hub mirror hosts tried when resolving an article by DOI. The source builds `https://<host>/<doi>` itself, so each entry must be a bare host. Falls through the list until one serves an article page.                                                                                                                                                |
| `LIBGEN_MCP_SOURCES`                    | *(all enabled)*                                          | Comma-separated subset of `unpaywall`, `scihub`, `libgen`, `randombook` | Restrict which download sources are active. Empty means all are enabled. Unknown names are rejected at startup. Order in the variable does not matter — the chain order is fixed (see below).                                                                                                                                                                    |

## Notes on specific variables

### `LIBGEN_MIRROR`

When set, this mirror is used as the sole preferred mirror and is placed first. It must
parse as an `http`/`https` URL with a host, otherwise startup fails. Leave it unset to let
the server discover the current live mirrors and fail over between them automatically.

### `LIBGEN_MCP_DOWNLOAD_DIR`

At startup the directory is created (with mode `0750`) if it does not exist and a temporary
file is written and removed to confirm it is writable. Under Docker, the container runs as
UID `10001`, so a mounted host directory must be writable by that user.

### `LIBGEN_MCP_UNPAYWALL_EMAIL`

This is used only by the `unpaywall` source (article DOIs). The default is the maintainer's
address; setting your own is recommended and, on high-volume use, expected by Unpaywall's
API etiquette. The value is sanity-checked (must contain `@` and a dotted domain) but not
verified for deliverability.

### `LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS` and `LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT`

These two variables make downloads resilient without ever cutting a healthy but slow
transfer:

- **Getting started** is retried on a staged schedule. A download must first *begin* —
  resolve a fresh URL, connect, and yield its first byte. If any of that fails (resolve
  error, connection error, non-2xx status, or a 2xx that yields no bytes), the attempt is
  retried after the next wait in `LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS`. The default
  `5s,5s,5s,10s,10s,10s,15s` makes 8 attempts over roughly 60 seconds; each retry resolves
  afresh, so an expired key is renewed. This wraps a single source; the multi-source chain
  still advances to the next source when one is exhausted. When every source fails to start,
  the tool returns an actionable error telling the model it can retry now, retry later, or
  ask the user.
- **Staying alive** is governed by `LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT`, a *progress-resetting*
  window. Once bytes are flowing, the transfer is aborted only if **no** new bytes arrive
  within the window; every received byte resets the clock. A 30 kB/s download that takes many
  minutes is never killed, while a truly stalled connection is dropped after the window and
  its `.part` is kept for a later resume. The per-request `LIBGEN_MCP_TIMEOUT` never applies
  to a streaming download.

### `LIBGEN_MCP_SOURCES` and the source chain

The download chain always runs in the fixed order `unpaywall → scihub → libgen →
randombook`. Each source declares which items it supports, so in practice:

- **Books** (identified by `md5`) are tried against `libgen`, then `randombook`.
- **Articles** (identified by `doi`) are tried against `unpaywall`, then `scihub`.

`LIBGEN_MCP_SOURCES` only removes sources from that chain; it never reorders it. For
example, `LIBGEN_MCP_SOURCES=libgen` disables the article sources and the `randombook`
fallback, leaving only the primary LibGen book source. See [Architecture](architecture.md)
for how the chain is built and traversed.
