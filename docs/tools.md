# Tools

`libgen-mcp` exposes three MCP tools: [`search`](#search), [`get_details`](#get_details),
and [`download`](#download). All three are annotated with an open-world hint; `search` and
`get_details` are read-only, and `download` is non-destructive and idempotent. Every handler
is panic-safe: an unexpected failure is returned as a tool error, never as a crash of the
session.

Every result is returned on **two channels**: the structured JSON output (fields documented
below) and a human-readable Markdown rendering in the text content — for `search`, a results
table that includes each result's clickable download links. Both channels lead with a
`next_steps` guidance list; the search guidance tells the model to include the download links
when it presents the results to the user.

## search

Search the Library Genesis catalog. Returns a page of file results with metadata, MD5
hashes, and per-result download options, plus pagination metadata.

### search input

| Parameter          | Type     | Required | Description                                                                                                                              |
| ------------------ | -------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `query`            | string   | yes      | Search text.                                                                                                                             |
| `topics`           | string[] | no       | Collections to search: `nonfiction`, `fiction`, `articles`, `magazines`, `comics`, `standards`, `fiction_rus`. Omit for all collections. |
| `search_in`        | string[] | no       | Fields to match: `title`, `author`, `series`, `year`, `publisher`, `isbn`. Omit to match all fields.                                     |
| `results_per_page` | int      | no       | Results per page: `25`, `50`, or `100`. Default `25`.                                                                                    |
| `page`             | int      | no       | Result page, starting at `1`. Default `1`.                                                                                               |
| `order`            | string   | no       | Sort by: `id`, `time_added`, `title`, `author`, `year`, `size`.                                                                          |
| `order_mode`       | string   | no       | Sort direction: `asc` or `desc`.                                                                                                         |

### search output

| Field              | Type   | Description                                                                                                                                                                                                                       |
| ------------------ | ------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `next_steps`       | array  | Model-facing follow-up suggestions with a ready-to-run example (e.g. a `get_details`/`download` call using the first result's `md5`/`doi`, or, on no matches, how to broaden the query). Every tool output leads with this.       |
| `results`          | array  | The file records on this page. Each carries `md5` (books), `doi` (articles), title, authors, publisher, year, language, pages, size, extension, type, ISBNs, ids, and labeled `downloads`. Empty array when there are no matches. |
| `page`             | int    | The page number returned.                                                                                                                                                                                                         |
| `results_per_page` | int    | The page size in effect.                                                                                                                                                                                                          |
| `total_files`      | string | Total matches the mirror reports for the query. May be a capped indicator such as `1000+`.                                                                                                                                        |
| `reachable`        | int    | How many results are actually reachable across all pages (mirrors cap this below `total_files`).                                                                                                                                  |
| `truncated`        | bool   | `true` when `total_files` exceeds `reachable`, i.e. some matches cannot be paged to.                                                                                                                                              |
| `hint`             | string | Present only when `truncated` — advises refining the query (add author/year, use title-only columns, or narrow topics).                                                                                                           |
| `has_more`         | bool   | `true` when this page is full (`len(results) >= results_per_page`), suggesting a next page may exist.                                                                                                                             |
| `mirror`           | string | The mirror base URL that served this search.                                                                                                                                                                                      |

### Pagination and truncation

Library Genesis mirrors advertise a full match count (`total_files`) but only serve the
first `reachable` results across pages. When the advertised total exceeds that cap the
search is `truncated` and `hint` explains how to narrow it, for example:

> Only the first 250 of 38514 results are reachable; refine your query (add author/year,
> use title-only columns, or narrow topics).

Prefer refining a truncated query over deep paging: pages beyond `reachable` return nothing.

### Errors

Invalid input is rejected before any network call: an empty `query`, an unknown `topic`,
`search_in`, or `order`, a `results_per_page` other than 25/50/100, or an `order_mode` other
than `asc`/`desc`. Connectivity and mirror problems surface as described in
[Troubleshooting](troubleshooting.md).

## get_details

Full metadata for a record via the LibGen JSON API — description, identifiers, DOI, cover,
and related edition. Look up by `md5` **or** by `id`, never both.

### get_details input

| Parameter | Type   | Required | Description                                                                                                                    |
| --------- | ------ | -------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `md5`     | string | one of   | File MD5 hash from a search result. Returns the file record plus its first related edition. Must be a 32-character hex string. |
| `id`      | string | one of   | Edition or file id.                                                                                                            |
| `object`  | string | no       | Used with `id`: `edition` (default) or `file`.                                                                                 |

Provide exactly one of `md5` or `id`. Supplying both, neither, an `md5` that is not
32 hex chars, or an `object` other than `edition`/`file` returns an input error.

### get_details output

| Field        | Type   | Description                                                                                                    |
| ------------ | ------ | -------------------------------------------------------------------------------------------------------------- |
| `next_steps` | array  | Model-facing follow-up suggestion, e.g. a `download` call using this record's `md5` (book) or `doi` (article). |
| `file`       | object | The file record (present for an `md5` lookup, or an `id` lookup with `object: file`).                          |
| `edition`    | object | The edition record (present for an `md5` lookup's related edition, or an `id` lookup with `object: edition`).  |

An `md5` lookup returns `file` and, best-effort, its related `edition`. An `id` lookup
returns whichever object was requested. A lookup that matches nothing returns a
"no file found" error.

## download

Download a file to a local directory. Provide `md5` for a book **or** `doi` for an article;
at least one is required. Returns the saved path, size, and the source that served it.

### download input

| Parameter  | Type   | Required | Description                                                                                                                                                                           |
| ---------- | ------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `md5`      | string | one of   | File MD5 hash from a book search result. Must be a 32-character hex string.                                                                                                           |
| `doi`      | string | one of   | DOI from an article search result. Articles are fetched by DOI.                                                                                                                       |
| `path`     | string | no       | Destination directory. Defaults to `LIBGEN_MCP_DOWNLOAD_DIR` (or `~/Downloads`).                                                                                                      |
| `filename` | string | no       | Destination filename. Defaults to a clean name from the record metadata, else the name the mirror announces, else the MD5.                                                            |
| `source`   | string | no       | Restrict the download to a single source: `libgen`/`randombook` (books, `md5`) or `unpaywall`/`scihub` (articles, `doi`). Omit to try every compatible source in order with failover. |

At least one of `md5` or `doi` is required. A malformed `md5` (not 32 hex chars) is
rejected before any work.

### Sources tried

The download runs through a fixed source chain, filtered by what each item supports:

- **Book** (`md5` only) → `libgen` (ads.php key + CDN), then `randombook` (fresh-mirror
  discovery).
- **Article** (`doi` only) → `unpaywall` (open-access PDF), then `scihub`.
- **Both `md5` and `doi`** → article sources first (`unpaywall`, `scihub`), then book
  sources (`libgen`, `randombook`).

The first source that resolves and streams a valid file wins; the `source` field in the
result names it. See [Architecture](architecture.md) for the full chain.

### download output

| Field               | Type   | Description                                                                                                                                      |
| ------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `next_steps`        | array  | Model-facing follow-up suggestion — confirms the file was saved and is ready to open or read.                                                    |
| `path`              | string | Absolute path of the saved file.                                                                                                                 |
| `size_bytes`        | int    | Final file size in bytes.                                                                                                                        |
| `original_filename` | string | The name the mirror/CDN announced (from `Content-Disposition`), if any.                                                                          |
| `mirror`            | string | The `scheme://host` origin that served the bytes.                                                                                                |
| `source`            | string | The source that succeeded: `libgen`, `randombook`, `unpaywall`, or `scihub`.                                                                     |
| `verified`          | bool   | `true` when the downloaded bytes' MD5 matched the requested `md5` (book downloads). `false` for DOI-keyed sources, which carry no LibGen digest. |
| `resumed`           | bool   | `true` when the download continued from a pre-existing partial via an HTTP `Range` request rather than starting from zero.                       |

### Behavior and errors

- **Clean filenames.** With no explicit `filename`, book downloads look up bibliographic
  metadata and land under a name like `Author - Title (Year).ext`, falling back to the
  CDN-announced name and then the MD5. Illegal path characters are stripped.
- **Resume.** An interrupted download leaves a `.part` file; a later call asks the CDN to
  continue from the existing offset. If the server ignores the range it restarts cleanly.
- **MD5 verification.** For book (`md5`) downloads the whole file is hashed and compared to
  the requested digest; a mismatch deletes the partial and fails the download. DOI sources
  skip this check.
- **Guards.** HTML error pages are rejected (by content type and by sniffing the first
  bytes), the size cap (`LIBGEN_MCP_MAX_DOWNLOAD_BYTES`) is enforced, free disk space is
  checked before streaming, and the completed file is atomically renamed into place.
- **Progress.** When the client supplies a progress token, throttled progress notifications
  are emitted while streaming.

If every applicable source fails, the tool returns the joined per-source errors. See
[Troubleshooting](troubleshooting.md) for how to read them.
