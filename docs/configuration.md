# Configuration

`libgen-mcp` is configured entirely through environment variables. Every variable is
optional: an empty or unset value uses the documented default. A variable that is present
but malformed (a bad number, an out-of-range value, an unwritable directory, an unknown
source name) causes the server to **fail fast at startup** with an explanatory error rather
than silently falling back to the default.

Set these in your MCP client's `env` block (see [Getting started](getting-started.md)), in
your shell, or with `-e` flags on `docker run`.

## Reference

| Variable                                | Default                                                  | Range / allowed values                                                  | Meaning                                                                                                                                                                                                                                                                                                                                                                                             |
| --------------------------------------- | -------------------------------------------------------- | ----------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `LIBGEN_MIRROR`                         | *(auto-discovery)*                                       | `http`/`https` URL with a host                                          | Pin a single mirror, e.g. `https://libgen.li`, and skip auto-discovery. Trailing slashes are trimmed. When empty, mirrors are discovered and cached (see [Architecture](architecture.md)).                                                                                                                                                                                                          |
| `LIBGEN_MCP_DOWNLOAD_DIR`               | `~/Downloads`                                            | Any writable directory path                                             | Default destination directory for `download`. Created if missing and probed for writability at startup. The `download` tool's `path` argument overrides it per call.                                                                                                                                                                                                                                |
| `LIBGEN_MCP_TIMEOUT`                    | `30s`                                                    | `(0, 10m]`, Go duration                                                 | Per-request HTTP timeout for search, details, and link resolution (e.g. `45s`, `1m`). Does **not** cap streaming downloads, which are governed by the request context.                                                                                                                                                                                                                              |
| `LIBGEN_MCP_LOG_LEVEL`                  | `info`                                                   | `debug`, `info`, `warn`, `error`                                        | Minimum log level. Logs go to stderr (stdout is reserved for the stdio transport). Raise to `debug` to trace per-mirror attempts.                                                                                                                                                                                                                                                                   |
| `LIBGEN_MCP_RATE_RPS`                   | `1`                                                      | `(0, 20]`, float                                                        | Allowed outbound requests per second (token-bucket refill rate) across all mirror requests and downloads.                                                                                                                                                                                                                                                                                           |
| `LIBGEN_MCP_RATE_BURST`                 | `1`                                                      | `[1, 100]`, int                                                         | Maximum burst size of the rate limiter — how many requests may fire back-to-back before throttling to `LIBGEN_MCP_RATE_RPS`.                                                                                                                                                                                                                                                                        |
| `LIBGEN_MCP_MAX_DOWNLOAD_BYTES`         | `0` (no limit)                                           | `[0, 53687091200]` (0–50 GiB), int                                      | Maximum size of a single download in bytes. `0` disables the cap. Enforced up front from `Content-Length` and again while streaming, so a lying or missing length is still caught.                                                                                                                                                                                                                  |
| `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS`   | `2`                                                      | `[1, 16]`, int                                                          | Number of downloads allowed to run at the same time. Extra downloads queue on a semaphore until a slot frees.                                                                                                                                                                                                                                                                                       |
| `LIBGEN_MCP_RETRY_ATTEMPTS`             | `3`                                                      | `[1, 10]`, int                                                          | Maximum number of passes over the mirror list for a page request before giving up. Only transient failures (network/timeout/5xx/429) trigger a retry pass; a permanent 4xx fails over without retrying.                                                                                                                                                                                             |
| `LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS` | `5s,5s,5s,10s,10s,10s,15s`                               | Comma-separated Go durations; each in `(0, 10m]`; at most 20 waits      | Staged wait schedule between attempts to get a download to **begin** (resolve the URL, connect, and pull the first byte). `N` waits means `N+1` attempts; the default spans ~60 s over 8 attempts. Retries cover resolve errors, connection errors, non-2xx statuses, and no-first-byte responses. Once bytes flow, start-retries stop and streaming takes over.                                    |
| `LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT`     | `60s`                                                    | `(0, 1h]`, Go duration                                                  | Progress-resetting stall window while streaming: a download is aborted only when **no** bytes arrive for this long. A slow-but-progressing transfer (e.g. 20–50 kB/s) is never cut. `LIBGEN_MCP_TIMEOUT` does not apply to downloads.                                                                                                                                                               |
| `LIBGEN_MCP_UNPAYWALL_EMAIL`            | *(unset — disabled)*                                     | Empty, or an email with `@` and a dotted domain                         | Contact email sent to the [Unpaywall](https://unpaywall.org) API on every article lookup (the API rejects requests without one). Empty by default, which **disables the `unpaywall` source** and hides it from the download tool's `source` schema; set your own address to enable it.                                                                                                              |
| `LIBGEN_MCP_SCIHUB_HOSTS`               | `sci-hub.ee,sci-hub.se,sci-hub.st,sci-hub.ru,sci-hub.wf` | Comma-separated bare hosts (no scheme, no path); at least one           | Ordered Sci-Hub mirror hosts tried when resolving an article by DOI. The source builds `https://<host>/<doi>` itself, so each entry must be a bare host. Falls through the list until one serves an article page.                                                                                                                                                                                   |
| `LIBGEN_MCP_SOURCES`                    | *(all enabled)*                                          | Comma-separated subset of `unpaywall`, `scihub`, `libgen`, `randombook` | Restrict which download sources are active. Empty means all are enabled. Unknown names are rejected at startup. Order in the variable does not matter — the chain order is fixed (see below).                                                                                                                                                                                                       |
| `LIBGEN_MCP_REMOTE_DOWNLOADS`           | `false`                                                  | `strconv.ParseBool` values (`1`/`true`/`0`/`false`, etc.)               | Forces the `download` tool into remote mode: it always returns a link (a `resource_link` plus a `resolved` object with any required `headers`) instead of saving a file — the same behavior `--http` already triggers. Set it for a hosted stdio deployment (e.g. behind `mcp-proxy` on a catalog) whose disk is remote/ephemeral and unreachable by the client. A non-boolean value fails startup. |
| `LIBGEN_MCP_READ_MAX_CHARS`             | `6000`                                                   | `[500, 200000]`, int                                                    | Max characters the `read` tool returns per call, used when a call omits `max_chars`.                                                                                                                                                                                                                                                                                                                |
| `LIBGEN_MCP_READ_DEFAULT_PAGES`         | `5`                                                      | `[1, 200]`, int                                                         | Default max PDF pages per `read` call, used when a call omits `max_pages`.                                                                                                                                                                                                                                                                                                                          |
| `LIBGEN_MCP_READ_CACHE_BYTES`           | `536870912` (512 MiB)                                    | `[1048576, 53687091200]` (1 MiB–50 GiB), int                            | Total-size cap of the `read` tool's server-side temp-file cache (built by `FetchToTemp`): downloaded read files past this aggregate size are evicted, least-recently-used first, never while a `read` call holds a reference.                                                                                                                                                                       |
| `LIBGEN_MCP_READ_CACHE_TTL`             | `10m`                                                    | `[1s, 24h]`, Go duration                                                | How long an unreferenced `read` temp file lingers before eviction, so successive pages of one read reuse a single fetch while idle files are reclaimed.                                                                                                                                                                                                                                             |
| `LIBGEN_MCP_ENRICH`                     | `true`                                                   | `strconv.ParseBool` values (`1`/`true`/`0`/`false`, etc.)               | Deployment kill-switch for `get_details`' opt-in `enrich` metadata (Crossref/OpenLibrary). Default `true` means enrichment is *allowed*; set `false` to *forbid* it entirely, regardless of the per-call `enrich` flag. A non-boolean value fails startup.                                                                                                                                          |
| `LIBGEN_MCP_OPEN_ACCESS`                | `false`                                                  | `strconv.ParseBool` values (`1`/`true`/`0`/`false`, etc.)               | Deployment default for `search`'s opt-in open-access discovery (arXiv/Crossref/OpenLibrary). Default `false` means a call only federates open-access hits when it sets `include_open_access: true`; set `true` to make discovery on by default while a call can still opt out with `include_open_access: false`. A non-boolean value fails startup.                                                 |

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

This is used only by the `unpaywall` source (article DOIs). It is **unset by default, which
disables the `unpaywall` source**: the Unpaywall API requires a contact email on every lookup,
so without one there is nothing to send and the source is skipped (and hidden from the download
tool's `source` schema). Set your own contact address to enable it. A non-empty value is
sanity-checked (must contain `@` and a dotted domain) but not verified for deliverability; an
empty value is allowed and simply leaves the source disabled.

When it is unset and a `download` call requests an article by `doi` (with no `source` pinned),
a client that advertises the MCP **elicitation** capability may be asked on demand for a
contact email to use for that single request — it is used only for that call and is never
stored or written back to this variable. A client without elicitation, or a decline/empty/
invalid answer, leaves this behavior unchanged: `unpaywall` stays disabled and `scihub` is
tried. See [Tools](tools.md#interactive-prompts-elicitation).

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
- **Articles** (identified by `doi`) are tried against `unpaywall` (only when `LIBGEN_MCP_UNPAYWALL_EMAIL` is set), then `scihub`.

`LIBGEN_MCP_SOURCES` only removes sources from that chain; it never reorders it. For
example, `LIBGEN_MCP_SOURCES=libgen` disables the article sources and the `randombook`
fallback, leaving only the primary LibGen book source. See [Architecture](architecture.md)
for how the chain is built and traversed.

### `LIBGEN_MCP_REMOTE_DOWNLOADS`

The `--http` transport already implies remote-download mode, since an HTTP server runs
elsewhere and cannot write to the client's disk. This variable covers the other case where
that's also true: a **stdio** server hosted on a remote/ephemeral machine (for example,
running behind `mcp-proxy` so it can be listed on a catalog like Glama) whose disk the
client cannot reach either. Set `LIBGEN_MCP_REMOTE_DOWNLOADS=1` on such a deployment and
`download` always returns a link instead of attempting to save a file nobody can retrieve.
See [Tools](tools.md#where-the-file-goes-local-vs-remote) for the resulting output shape.

### `LIBGEN_MCP_READ_MAX_CHARS` and `LIBGEN_MCP_READ_DEFAULT_PAGES`

These bound how much text a single `read` call returns by default: `LIBGEN_MCP_READ_MAX_CHARS`
caps EPUB/TXT chunks by character count, `LIBGEN_MCP_READ_DEFAULT_PAGES` caps PDF chunks by page
count. Both are only fallbacks — a call's own `max_chars`/`max_pages` argument always takes
precedence when set to a positive value. Raise them to return more text per call (fewer round
trips, more tokens per response) or lower them to keep individual responses small. See
[Tools](tools.md#read) for the full `read` reference.

### `LIBGEN_MCP_READ_CACHE_BYTES` and `LIBGEN_MCP_READ_CACHE_TTL`

`read` fetches an `md5`/`doi`-identified file to a server-side temp file once, then serves
successive pages/chunks from that same file as the model pages through it with `cursor` —
without `LIBGEN_MCP_READ_CACHE_TTL`, every page would re-download the whole file.
`LIBGEN_MCP_READ_CACHE_TTL` is how long an unreferenced temp file lingers after its last use
before eviction; `LIBGEN_MCP_READ_CACHE_BYTES` is the cache's total-size ceiling, past which the
least-recently-used files are evicted first (a file a `read` call is actively using is never
evicted). Neither variable affects `download`, which always writes directly to
`LIBGEN_MCP_DOWNLOAD_DIR` and keeps no server-side temp cache.

### `LIBGEN_MCP_ENRICH`

Controls whether `get_details`' opt-in `enrich: true` argument is honored at all. The default
`true` means a caller *may* request enrichment (it still stays off unless a call sets
`enrich: true`); setting it to `false` makes the server ignore `enrich` entirely, so no
Crossref/OpenLibrary calls are ever made on this deployment. Use this to fully disable outbound
calls to those two third-party APIs, for example in a locked-down or offline-preferring
deployment. See [Tools](tools.md#metadata-enrichment) for what enrichment adds and its 6-second
best-effort budget.

### `LIBGEN_MCP_OPEN_ACCESS`

Controls the *deployment default* for `search`'s opt-in `include_open_access` argument — it
does not force discovery on or off unconditionally, since a call's own `include_open_access`
always wins when set. The default `false` means open-access discovery (arXiv, Crossref,
OpenLibrary) only runs on a call that explicitly passes `include_open_access: true`; setting
this variable to `true` flips the deployment default so discovery runs on every `search` call
unless one explicitly passes `include_open_access: false` to opt out. All three providers are
keyless (no account or API key) and best-effort, each bounded by its own short per-request
budget so a slow or failing provider never delays or fails the core Library Genesis search.
See [Tools](tools.md#open-access-discovery) for the merged `open_access` output shape.
