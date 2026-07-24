# Configuration

`libgen-mcp` is configured entirely through environment variables. Every variable is
optional: an empty or unset value uses the documented default. A variable that is present
but malformed (a bad number, an out-of-range value, an unwritable directory, an unknown
source name) causes the server to **fail fast at startup** with an explanatory error rather
than silently falling back to the default.

Set these in your MCP client's `env` block (see [Getting started](getting-started.md)), in
your shell, or with `-e` flags on `docker run`.

## Reference

| Variable                                | Default                                                  | Range / allowed values                                                                    | Meaning                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| --------------------------------------- | -------------------------------------------------------- | ----------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `LIBGEN_MIRROR`                         | *(auto-discovery)*                                       | `http`/`https` URL with a host                                                            | Pin a single mirror, e.g. `https://libgen.li`, and skip auto-discovery. Trailing slashes are trimmed. When empty, mirrors are discovered and cached (see [Architecture](architecture.md)).                                                                                                                                                                                                                                                               |
| `LIBGEN_MCP_DOWNLOAD_DIR`               | `~/Downloads`                                            | Any writable directory path                                                               | Default destination directory for `download`. Created if missing and probed for writability at startup. The `download` tool's `path` argument overrides it per call.                                                                                                                                                                                                                                                                                     |
| `LIBGEN_MCP_TIMEOUT`                    | `30s`                                                    | `(0, 10m]`, Go duration                                                                   | Per-request HTTP timeout for search, details, and link resolution (e.g. `45s`, `1m`). Does **not** cap streaming downloads, which are governed by the request context.                                                                                                                                                                                                                                                                                   |
| `LIBGEN_MCP_LOG_LEVEL`                  | `info`                                                   | `debug`, `info`, `warn`, `error`                                                          | Minimum log level. Logs go to stderr (stdout is reserved for the stdio transport). Raise to `debug` to trace per-mirror attempts.                                                                                                                                                                                                                                                                                                                        |
| `LIBGEN_MCP_RATE_RPS`                   | `1`                                                      | `(0, 20]`, float                                                                          | Allowed outbound requests per second (token-bucket refill rate) across all mirror requests and downloads.                                                                                                                                                                                                                                                                                                                                                |
| `LIBGEN_MCP_RATE_BURST`                 | `1`                                                      | `[1, 100]`, int                                                                           | Maximum burst size of the rate limiter — how many requests may fire back-to-back before throttling to `LIBGEN_MCP_RATE_RPS`.                                                                                                                                                                                                                                                                                                                             |
| `LIBGEN_MCP_MAX_DOWNLOAD_BYTES`         | `0` (no limit)                                           | `[0, 53687091200]` (0–50 GiB), int                                                        | Maximum size of a single download in bytes. `0` disables the cap. Enforced up front from `Content-Length` and again while streaming, so a lying or missing length is still caught.                                                                                                                                                                                                                                                                       |
| `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS`   | `2`                                                      | `[1, 16]`, int                                                                            | Number of downloads allowed to run at the same time. Extra downloads queue on a semaphore until a slot frees.                                                                                                                                                                                                                                                                                                                                            |
| `LIBGEN_MCP_RETRY_ATTEMPTS`             | `3`                                                      | `[1, 10]`, int                                                                            | Maximum number of passes over the mirror list for a page request before giving up. Only transient failures (network/timeout/5xx/429) trigger a retry pass; a permanent 4xx fails over without retrying.                                                                                                                                                                                                                                                  |
| `LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS` | `5s,5s,5s,10s,10s,10s,15s`                               | Comma-separated Go durations; each in `(0, 10m]`; at most 20 waits                        | Staged wait schedule between attempts to get a download to **begin** (resolve the URL, connect, and pull the first byte). `N` waits means `N+1` attempts; the default spans ~60 s over 8 attempts. Retries cover resolve errors, connection errors, non-2xx statuses, and no-first-byte responses. Once bytes flow, start-retries stop and streaming takes over.                                                                                         |
| `LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT`     | `60s`                                                    | `(0, 1h]`, Go duration                                                                    | Progress-resetting stall window while streaming: a download is aborted only when **no** bytes arrive for this long. A slow-but-progressing transfer (e.g. 20–50 kB/s) is never cut. `LIBGEN_MCP_TIMEOUT` does not apply to downloads.                                                                                                                                                                                                                    |
| `LIBGEN_MCP_UNPAYWALL_EMAIL`            | *(unset — disabled)*                                     | Empty, or an email with `@` and a dotted domain                                           | Contact email sent to the [Unpaywall](https://unpaywall.org) API on every article lookup (the API rejects requests without one). Empty by default, which **disables the `unpaywall` source** and hides it from the download tool's `source` schema; set your own address to enable it.                                                                                                                                                                   |
| `LIBGEN_MCP_SCIHUB_HOSTS`               | `sci-hub.ee,sci-hub.se,sci-hub.st,sci-hub.ru,sci-hub.wf` | Comma-separated bare hosts (no scheme, no path); at least one                             | Ordered Sci-Hub mirror hosts tried when resolving an article by DOI. The source builds `https://<host>/<doi>` itself, so each entry must be a bare host. Falls through the list until one serves an article page.                                                                                                                                                                                                                                        |
| `LIBGEN_MCP_ANNAS_KEY`                  | *(unset — keyless)*                                      | Empty, or an Anna's Archive account secret key                                            | Optional Anna's Archive account secret (from your account page) enabling the member fast-download API used by the `annas` source. Empty by default, which keeps `annas` **fully keyless**, resolving books through public IPFS gateways. When set, the member API is tried first and IPFS remains the fallback, so an expired or rejected key never makes the source worse than keyless. Requires an active paid membership; a free account is rejected. |
| `LIBGEN_MCP_SOURCES`                    | *(all enabled)*                                          | Comma-separated subset of `unpaywall`, `scihub`, `scidb`, `libgen`, `randombook`, `annas` | Restrict which download sources are active. Empty means all are enabled. Unknown names are rejected at startup. Order in the variable does not matter — the chain order is fixed (see below).                                                                                                                                                                                                                                                            |
| `LIBGEN_MCP_REMOTE_DOWNLOADS`           | `false`                                                  | `strconv.ParseBool` values (`1`/`true`/`0`/`false`, etc.)                                 | Forces the `download` tool into remote mode: it always returns a link (a `resource_link` plus a `resolved` object with any required `headers`) instead of saving a file — the same behavior `--http` already triggers. Set it for a hosted stdio deployment (e.g. behind `mcp-proxy` on a catalog) whose disk is remote/ephemeral and unreachable by the client. A non-boolean value fails startup.                                                      |
| `LIBGEN_MCP_READ_MAX_CHARS`             | `6000`                                                   | `[500, 200000]`, int                                                                      | Max characters the `read` tool returns per call, used when a call omits `max_chars`.                                                                                                                                                                                                                                                                                                                                                                     |
| `LIBGEN_MCP_READ_DEFAULT_PAGES`         | `5`                                                      | `[1, 200]`, int                                                                           | Default max PDF pages per `read` call, used when a call omits `max_pages`.                                                                                                                                                                                                                                                                                                                                                                               |
| `LIBGEN_MCP_READ_CACHE_BYTES`           | `536870912` (512 MiB)                                    | `[1048576, 53687091200]` (1 MiB–50 GiB), int                                              | Total-size cap of the `read` tool's server-side temp-file cache (built by `FetchToTemp`): downloaded read files past this aggregate size are evicted, least-recently-used first, never while a `read` call holds a reference.                                                                                                                                                                                                                            |
| `LIBGEN_MCP_READ_CACHE_TTL`             | `10m`                                                    | `[1s, 24h]`, Go duration                                                                  | How long an unreferenced `read` temp file lingers before eviction, so successive pages of one read reuse a single fetch while idle files are reclaimed.                                                                                                                                                                                                                                                                                                  |
| `LIBGEN_MCP_ENRICH`                     | `true`                                                   | `strconv.ParseBool` values (`1`/`true`/`0`/`false`, etc.)                                 | Deployment kill-switch for `get_details`' opt-in `enrich` metadata (Crossref/OpenLibrary). Default `true` means enrichment is *allowed*; set `false` to *forbid* it entirely, regardless of the per-call `enrich` flag. A non-boolean value fails startup.                                                                                                                                                                                               |
| `LIBGEN_MCP_EXTRA_SOURCES`              | `auto`                                                   | `auto`, `always`, `never`                                                                 | When the extra searchers (Anna's Archive, arXiv, Crossref, OpenLibrary) are consulted. `auto`: only when the Library Genesis catalog returns nothing or fails. `always`: on every search, alongside the catalog. `never`: catalog only, even on a miss. A per-call `extra_sources` argument overrides this default in either direction — except `never`, which is a lock no call can lift; an unrecognized value fails startup.                          |

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

### `LIBGEN_MCP_ANNAS_KEY`

Unset by default, and the `annas` source works without it: Anna's book pages publish
each item's IPFS CID, which public gateways serve with no account, key or CAPTCHA.
That keyless path is genuinely usable but often slow, because public IPFS gateway
availability varies by item and by network.

Setting this variable to your Anna's Archive **account secret key** makes `annas` try
the member fast-download API first, which is far faster and more reliable; IPFS stays
as the fallback. The API requires an **active paid membership** — a free account is
rejected — and enforces a per-day download quota. A missing, expired or rejected key
costs a single request and then falls through to IPFS, so it never degrades the source
below its keyless behavior.

The key is a credential: keep it out of version control, and prefer your MCP client's
`env` block or a gitignored env file over a shell history entry.

### `LIBGEN_MCP_SOURCES` and the source chain

The download chain always runs in the fixed order `unpaywall → scihub → scidb →
libgen → randombook → annas`. Each source declares which items it supports, so in
practice:

- **Books** (identified by `md5`) are tried against `libgen`, then `randombook`, then `annas`.
- **Articles** (identified by `doi`) are tried against `unpaywall` (only when `LIBGEN_MCP_UNPAYWALL_EMAIL` is set), then `scihub`, then `scidb`.

`scidb` serves scholarly articles through Anna's Archive's SciDB viewer. It is fully
keyless and sits after `scihub` because it reaches papers published after Sci-Hub
stopped indexing, so it covers that gap rather than replacing it.

`annas` is the last book rescue. By default it is **keyless**: Anna's book pages
publish each item's IPFS CID, and the source streams from the first public IPFS
gateway that serves it. Gateway availability varies widely, so this path is a genuine
fallback rather than a fast one — expect it to be slow, or to time out, for items that
are not well seeded. Setting [`LIBGEN_MCP_ANNAS_KEY`](#libgen_mcp_annas_key) makes it
try the much faster member API first, with IPFS still the fallback.

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

### `LIBGEN_MCP_EXTRA_SOURCES`

Controls when the extra searchers (Anna's Archive, arXiv, Crossref, OpenLibrary) are
consulted. The default `auto` consults them only when the Library Genesis catalog returns
nothing or fails; `always` consults them on every search, concurrently with the catalog;
`never` restricts every search to the catalog, even on a miss. A per-call `extra_sources`
argument overrides this default in either direction, with one exception: a deployment set to
`never` is a **lock**, not a default — no call can re-enable the extras, because a policy an
individual caller can overrule is not a policy. All four providers are keyless (no
account or API key) and best-effort, each bounded by its own short per-request budget so a
slow or failing provider never delays or fails the core Library Genesis search. Anna's
md5-keyed hits merge into `results` (labeled by `origin`); arXiv/Crossref/OpenLibrary hits
appear in the separate `open_access` array. See [Tools](tools.md#open-access-discovery)
for the output shape.
