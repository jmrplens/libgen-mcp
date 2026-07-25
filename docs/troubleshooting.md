# Troubleshooting

This page collects the failures you are most likely to hit and how to resolve them. When in
doubt, raise the log level (see [below](#raising-the-log-level)) and re-run — most errors
carry an explanatory message.

## All mirrors unreachable

**Symptom.** Searches and downloads fail with `all libgen mirrors unreachable (network
block? try a VPN or different DNS)`.

**Meaning.** At least one mirror failed transiently (network error, timeout, HTTP 5xx, or
429) on every retry pass — a genuine connectivity problem, not a missing resource.

**Fixes.**

- Check basic connectivity and DNS resolution to the `libgen.li` family. Some ISPs and
  networks block these domains; a VPN or an alternative resolver (e.g. `1.1.1.1`) often
  helps.
- If discovery itself is blocked, pin a mirror you can reach with
  `LIBGEN_MIRROR=https://libgen.li` (or another live host) to bypass auto-discovery.
- Increase `LIBGEN_MCP_TIMEOUT` (e.g. `45s` or `1m`) and/or `LIBGEN_MCP_RETRY_ATTEMPTS` on
  slow links.
- Mirrors that fail are put in a 45-second cooldown; wait a moment and retry — the server
  fails over automatically once a mirror recovers.

A related but distinct error, `request rejected by all mirrors`, means every mirror returned
a *permanent* error (e.g. 404/403). That is a normal "not found / rejected", not a network
problem — re-check the query or identifier rather than your connection.

## Download failed / MD5 mismatch

**Symptom.** `download` returns an error, or `integrity check failed: MD5 mismatch`.

**Meaning and fixes.**

- **MD5 mismatch** — the downloaded bytes did not match the requested `md5` (corrupt or
  tampered transfer, or a stale mirror). The partial file is deleted automatically; simply
  retry. If it persists on one mirror, the `randombook` fallback will try freshly discovered
  mirrors.
- **"mirror returned an HTML page instead of the file"** — the download key expired or the
  mirror served an error/challenge page. Retry; the pipeline resolves a fresh key each time
  and falls over to the next source.
- **"download exceeds the configured size limit"** — the file is larger than
  `LIBGEN_MCP_MAX_DOWNLOAD_BYTES`. Raise the cap (up to 50 GiB) or set it to `0` to disable
  it.
- **"truncated download"** — the connection dropped mid-stream. The `.part` file is kept, so
  re-running `download` resumes from where it stopped (the result's `resumed` field reports
  `true` when it does).
- **All sources failed** — the tool returns the joined per-source errors, one line per
  source it tried. Read them to see whether the item was simply not found or every provider
  was unreachable.

## Article not found (open access vs Sci-Hub)

**Symptom.** A `download` with a `doi` fails, or returns nothing useful.

**Meaning.** Articles are fetched by DOI through several sources in order — the legal
open-access providers first, then the shadow-library fallbacks:

1. **`unpaywall`** — returns a PDF only when the article is genuinely open access. A paywalled
   DOI, or one with no PDF link, produces `no open-access PDF for "<doi>"` and the chain
   advances. (In the chain when `LIBGEN_MCP_UNPAYWALL_EMAIL` is set; otherwise an
   elicitation-capable client is asked for a one-off contact email, and declining just
   moves on to the next provider.)
2. **`europepmc`** — serves the PDF when Europe PMC holds the article's open-access full text;
   reports whether the DOI is simply not indexed or indexed without an OA full text.
3. **`biorxiv`** — only for `10.1101` preprint DOIs; returns the latest version's full-text PDF
   from bioRxiv or medRxiv.
4. **`fatcat`** — returns a preserved copy from the Internet Archive when Scholar's release page
   advertises one that still serves a PDF; it distinguishes a DOI the catalog does not hold, a
   release with nothing preserved, and a release whose preserved captures have all gone bad.
5. **`core`** — only in the chain when `LIBGEN_MCP_CORE_KEY` is set; returns CORE's open-access
   download URL when CORE hosts a live copy.
6. **`oapen`** — for a monograph DOI, returns the open-access book OAPEN hosts under it. Most
   journal DOIs are simply not in its catalog, so it usually reports a clean miss and the
   chain advances.
7. **`sci-hub`** — tries each configured host (`LIBGEN_MCP_SCIHUB_HOSTS`) until one serves an
   article page with an extractable PDF.
8. **`scidb`** — the Anna's Archive SciDB viewer, tried last when Sci-Hub yields nothing; it
   covers papers published after Sci-Hub stopped indexing.

**Fixes.**

- Confirm the DOI is correct (copy it exactly from the article search result).
- If the open-access providers all failed, the article likely is not open access; Sci-Hub and
  then SciDB are the fallbacks.
- Sci-Hub mirrors rotate and go down often. Update `LIBGEN_MCP_SCIHUB_HOSTS` with a currently
  working host list if all defaults fail.
- Set your own `LIBGEN_MCP_UNPAYWALL_EMAIL` — until you do, Unpaywall is disabled (the other
  open-access sources and Sci-Hub are still tried). The API expects a real contact address.
- Set `LIBGEN_MCP_CORE_KEY` (a free CORE API key) to add CORE to the open-access chain.
- Note that DOI downloads are **not** MD5-verified (`verified` is `false`) — there is no
  LibGen digest for them.

## Book not found by ISBN (open access only)

**Symptom.** A `download` with an `isbn` fails with something like `no catalog entry states
"9780141439518"` or `no freely downloadable scan ... (every candidate is lending-restricted
or holds no book file)`.

**Meaning.** The `isbn` route reaches only the two **open-access** book sources — `oapen` and
`archive` — and neither will serve a book it may not redistribute:

- `oapen` holds openly licensed scholarly monographs. It confirms the record it found really
  states the ISBN (or DOI) you asked for before serving anything, because its search is free
  text and would otherwise return an unrelated monograph. A trade book is simply not there.
- `archive` serves an Internet Archive scan only when OpenLibrary reports the book as
  `ebook_access: public` **and** the individual scan is neither flagged `access-restricted-item`
  nor filed in a lending collection. A book that is borrowable-only on the Archive is
  reported as a miss rather than downloaded, because a lending item's files either refuse the
  request or arrive DRM-wrapped and unusable.

**Fixes.**

- For an in-copyright book, use the `md5` route instead: search the catalog and download by
  the result's `md5`.
- Check the ISBN itself — a typo, or the ISBN of a different edition, is the common cause. Both
  the 10- and 13-character forms work, with or without hyphens.
- If the book is a public-domain classic, search again and look for a `gutenberg` hit in
  `open_access`: its `full_text_url` is the ebook file itself.

## A source is missing from the errors of a repeated download

**Symptom.** A `download` fails, and a second attempt reports fewer sources than the first —
one that failed a moment ago is not mentioned at all.

**Meaning.** That source is in **cooldown**. When a source fails because it is unavailable (a
transport error, a timeout, a 5xx or a 429) it is set aside for 5 minutes, so the next
download does not spend its resolve budget on a provider that just proved unreachable. It is
skipped, not removed: the cooldown expires on its own, nothing is written to disk (a restart
clears it), and when every source able to serve the item is in cooldown they are all tried
anyway. A source that merely reported it does not hold the item is never set aside.

The server log says which sources were skipped and why — `source in cooldown, skipping` with
the instant it becomes eligible again, or `every capable source is in cooldown, trying them
anyway`. Run with `LIBGEN_MCP_LOG_LEVEL=info` (the default) to see them.

**Fixes.**

- Nothing is needed: retry later, or immediately with `source: "<name>"` to address that
  provider directly — an explicit source is always tried, cooldown or not.
- If a source is repeatedly cooled down, it is genuinely unreachable from this host. Check it
  by hand before suspecting the server.

## Truncated search results

**Symptom.** A search response has `truncated: true` and a `hint`, and paging past a certain
point returns nothing.

**Meaning.** The mirror reports more matches (`total_files`) than it will actually serve
across pages (`reachable`). Pages beyond `reachable` are empty.

**Fix.** Refine rather than page deeper, as the `hint` suggests:

- Add distinguishing terms (author, year).
- Constrain the fields with `search_in` (e.g. `["title"]`).
- Narrow `topics` to the relevant collection(s).

See [Tools](tools.md#pagination-and-truncation) for the exact fields.

## A search result that will not download (`origin: "annas"`)

**Symptom.** A search returned a result labeled `origin: "annas"`, but `download` fails on its
md5 with an error about no IPFS CID or no gateway serving it.

**Meaning.** That result came from [an escalated search](how-search-works.md): the Library
Genesis catalog does not carry the file, so the only route to it is Anna's Archive. The
keyless route reads the item's IPFS address from Anna's record page — and **most Anna's
records publish no IPFS address at all**. When there is none, there is nothing to fetch
keylessly, and this is expected rather than a fault.

**Fix.** In order of effort:

- Set `LIBGEN_MCP_ANNAS_KEY` to an Anna's Archive membership key. The member fast-download
  API serves items with no IPFS address, and the keyless IPFS route stays as the fallback.
  See [Configuration](configuration.md).
- Search again with different terms; another edition of the same work may be in the catalog,
  which downloads through the ordinary sources.
- Call `get_details` on the md5 anyway. It falls back to Anna's record, so you still get the
  title, author, year and language even when the bytes are out of reach — plus ISBNs when
  that record carries them, which a minority do — enough to
  find the item elsewhere.

A gateway that is merely slow reports a timeout instead; retrying later often succeeds, since
the public IPFS gateways vary in how quickly they locate an item.

## Raising the log level

Most problems are easier to diagnose at `debug`, which traces each mirror attempt, cooldown,
and failover:

```bash
LIBGEN_MCP_LOG_LEVEL=debug libgen-mcp
```

Or in your MCP client's `env` block:

```json
{
  "mcpServers": {
    "libgen": {
      "command": "libgen-mcp",
      "env": { "LIBGEN_MCP_LOG_LEVEL": "debug" }
    }
  }
}
```

Logs go to **stderr** (stdout is reserved for the stdio MCP transport), so check your
client's server-log view or the terminal where the process runs. Valid levels are `debug`,
`info` (default), `warn`, and `error`.

## Disk space

**Symptom.** `not enough free disk space in <dir>: need ~<n> bytes, have <m>`.

**Meaning.** Before streaming a download whose size is known, the server checks that the
destination has room for the file plus an ~8 MiB margin, and refuses rather than filling the
disk.

**Fixes.**

- Free space on the target volume, or point `LIBGEN_MCP_DOWNLOAD_DIR` (or the per-call
  `path`) at a volume with more room.
- Remove stale `.part` files left by interrupted downloads if you do not intend to resume
  them (they live in the download directory, named `.libgen-mcp-*.part`).
- Under Docker, make sure the mounted download volume is large enough and writable by UID
  `10001`.

This check only applies to a local stdio/Docker server saving to disk. A remote/hosted
server (started with `--http`) never writes a file at all — `download` always returns a
link (a `resource_link` plus a `resolved` object) instead — so this error cannot occur
there. See [Tools](tools.md#where-the-file-goes-local-vs-remote) for details.

If you're hosting the server as **stdio** behind a proxy (e.g. `mcp-proxy`) on a remote or
ephemeral machine, set `LIBGEN_MCP_REMOTE_DOWNLOADS=1` — its disk is just as unreachable by
the client as an HTTP deployment's, so downloads should come back as links too, not land on
disk there.
