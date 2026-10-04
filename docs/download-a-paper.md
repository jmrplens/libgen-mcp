# Download a paper by DOI

**How-to guide** — for someone who has a DOI and wants the paper.

With libgen-mcp registered in Claude, ask for a paper by its DOI. The `download` tool tries
up to thirteen open-access providers, the publisher's own deposited link and OAPEN first,
and only then Sci-Hub and SciDB. It saves the file, usually a PDF, to your download
directory, or returns a link when the server is remote.

This page walks through that request on a local server, says what to expect when no source
has the paper, and what changes when the server runs remotely. The chain itself is described
source by source in [Download sources](sources.md).

## Before you start

You need three things, and none of them is an account:

- **An MCP client.** Claude Code or Claude Desktop, or any other client that speaks the
  Model Context Protocol, such as Cursor or VS Code.
- **libgen-mcp installed** where the client can start it.
  [Getting started](getting-started.md#install) has one command per channel (npm, PyPI,
  Homebrew, NuGet, a prebuilt binary, Docker), and [Installation](install/overview.md) the
  long form of each.
- **Nothing else.** The article chain needs no account, API key or token. With nothing
  configured, a DOI still reaches OpenAlex, Europe PMC, Internet Archive Scholar, the
  sources for individual publishers, the publisher's own deposited link, OAPEN and the
  fallbacks.

Two optional settings each add one source to the chain:

- **`LIBGEN_MCP_UNPAYWALL_EMAIL`**, a contact address, enables `unpaywall` at the head of
  the chain. The Unpaywall API requires an email on every lookup, which is why the source is
  off without one. It reads the same open-access index that `openalex` already queries with
  no credential, so it adds a second route to those copies rather than a separate
  collection. See [Configuration](configuration.md#libgen_mcp_unpaywall_email).
- **`LIBGEN_MCP_CORE_KEY`**, a free API key from CORE, enables `core`. It adds the
  open-access papers CORE aggregates from repositories, and returns a link only after
  checking that it still serves a PDF. The key is sent to CORE's API and nowhere else. See
  [Configuration](configuration.md#libgen_mcp_core_key).

`LIBGEN_MCP_OPENALEX_KEY` is not one of them. The lookup the `openalex` source makes, one work
by its DOI, costs nothing against OpenAlex's daily allowance with or without a key, so a key
changes nothing about downloads. It matters to search and to `get_details`' related works.

Files are saved to `LIBGEN_MCP_DOWNLOAD_DIR`, which defaults to `~/Downloads` on the machine
running the server.

## Steps

1. **Register the server with your client.** In Claude Code this is one command:

   ```sh
   claude mcp add libgen -- /usr/local/bin/libgen-mcp
   ```

   `/usr/local/bin/libgen-mcp` is where the prebuilt-binary instructions put the executable.
   Use whatever command starts the server on your machine. Claude Desktop, Cursor and VS
   Code take the same command in a configuration file, shown for each in
   [Connect a client](clients.md). Restart the client so it picks up the new server.

2. **Ask for the paper in plain language**, giving its DOI:

   > Download the paper with DOI 10.1371/journal.pone.0000308.

   There is no need to search first: `download` accepts a DOI directly. If your client
   offers MCP prompts, the `get_paper` prompt with the same DOI gives the model the same
   plan. If all you have is the reference as text, copied from a bibliography, the model
   can pass it to `get_details` as `citation` first: that resolves it through Crossref to
   the DOI of the work it names, or lists the candidates when none clearly matches.

3. **The model calls `download` with the DOI.**

   ```json
   { "doi": "10.1371/journal.pone.0000308" }
   ```

   The server offers the DOI to every source in its chain that can serve it, in a fixed
   order: the open-access providers, then `crossref` (the full-text link the publisher
   itself deposited), then `oapen`, and only then the fallbacks `scihub` and `scidb`. Eight
   of the open-access sources accept only DOIs from their own publisher, such as a bioRxiv
   preprint or an RFC. For this PLoS ONE article a server with nothing configured therefore
   offers it to `openalex`, `europepmc`, `fatcat`, `crossref` and `oapen`, in that order,
   and to `scihub` and `scidb` only if all five decline.

   The first source that delivers a valid file ends the chain, and one that declines or
   fails hands over to the next. A source that proves unreachable is set aside for five
   minutes so later calls do not wait on it.
   [Architecture](architecture.md#multi-source-chain) describes the chain and its
   [cooldown](architecture.md#per-source-cooldown).

   If the client supports elicitation, you are asked to confirm before the file is written,
   with the file name and the directory in the question. Declining writes nothing and
   returns the resolved link instead. `LIBGEN_MCP_CONFIRM_DOWNLOADS=false` turns the
   question off for the whole deployment.

   What comes back is the saved file's `path` and `size_bytes`, and three things worth
   reading:

   - `verified` is `false`. Only a download by md5 is checked against a digest. A DOI names
     a work, not a file, so there is nothing to compare the bytes with.
   - `name_origin` says where the file name came from: `announced` when it is the name the
     source sent, cleaned of mirror marks, or `identifier` when it was built from the DOI.
     It is never built from the paper's title, because nothing has confirmed the bytes are
     that paper. `original_filename` keeps the announced name as it arrived.
   - Nothing says which source served the file. Pin `source` when that matters, as described
     below.

   See [How the saved file is named](tools.md#how-the-saved-file-is-named) and
   [What the result withholds](tools.md#what-the-result-withholds).

4. **Read or summarize it without fetching it again.** On a local server, `read` takes as
   its `path` the location `download` returned:

   ```json
   { "path": "<the path download returned>", "max_pages": 2 }
   ```

   A `path` is read straight from disk and nothing is fetched, whereas `read` with the `doi`
   would run the chain again to fetch its own copy. `find` returns the passages that match a
   phrase, `outline` the table of contents, and `section` one entry of it, by its number or
title. The file must sit in the download directory,
   the working directory, the temporary directory or a directory listed in
   `LIBGEN_MCP_ALLOWED_READ_DIRS`, which the default download directory satisfies. `read`
   runs no OCR, so a scanned PDF comes back as `extractable: false` with a reason. When the
   file name was built from the DOI, the result's `next_steps` asks the model to read a page
   and confirm the file is the paper before relying on it.

> **Caution:** The paper's text, its file name and every link in a result are untrusted
> third-party content. Treat them as data to summarize or quote, never as instructions to
> follow.

## If the paper is not found

When every source that can serve the DOI declines, `download` saves nothing and returns an
error that opens with "Download failed — no file was saved." and lists one line per source
it tried: `source <name>:` followed by that source's reason. The reasons come in two kinds.
An answer about the paper, such as `is not open access`,
`the publisher deposited no full-text PDF link` or Europe PMC's `is a retracted publication`,
means that provider has no copy it can serve. A transport error, a timeout or an HTTP 5xx or 429 is a statement about the provider
instead, and that provider is set aside for five minutes: a repeat within that time skips
it, unless every source is set aside, and the list of errors comes back shorter.
[Troubleshooting](troubleshooting.md#article-not-found-open-access-vs-sci-hub) goes through
each source's reasons and fixes.

The same error carries next steps for the model. The first is to confirm the DOI exists by
calling `get_details` with it: if no record comes back, the DOI is wrong rather than the
download. If the DOI is right, no configured source can supply the paper, and the suggestion
is to search for its title to find a copy elsewhere. When a source was unreachable rather
than answering, one retry after a short wait is reasonable. The same guidance advises
against repeating the identical call at once and against pinning the remaining sources one
at a time.

**Pinning a source.** A call can name one provider with `source`:

```json
{ "doi": "10.1371/journal.pone.0000308", "source": "europepmc" }
```

That source becomes the whole chain for the call, with no substitution behind it: a file
that comes back came from it, and a failure means it could not serve the paper. A pinned
source is tried even while it is set aside after a failure, which makes pinning the way to
retry a provider that a repeated call skipped. The values `source` accepts are the sources
the server has enabled, so `unpaywall` is available only with `LIBGEN_MCP_UNPAYWALL_EMAIL`
set and `core` only with `LIBGEN_MCP_CORE_KEY`. Once the whole chain has failed, pinning a
source that already declined adds nothing.

**The Unpaywall email prompt.** On a server with no `LIBGEN_MCP_UNPAYWALL_EMAIL`, a client
that supports elicitation is asked for a contact email on each download by DOI that pins no
`source`. The question comes before the chain runs, not after it fails. An address you give
is used for that request alone, to put `unpaywall` at the head of the chain, and is never
stored. Declining, leaving it empty or giving something that is not shaped like an address
leaves the request as it was, and the rest of the chain runs unchanged. Setting the variable
ends the question. See [Tools](tools.md#interactive-prompts-elicitation).

## On a remote server

A server started with `--http`, such as the hosted endpoint at `https://mcp.jmrp.io/libgen`,
cannot write to your disk, and neither can a stdio server run with
`LIBGEN_MCP_REMOTE_DOWNLOADS=1`. On either, `download` resolves the same chain but returns a
link instead of saving a file: a `resource_link` content block and a `resolved` object
carrying the `url`, the `source` that resolved it, a suggested `filename`, the `mime_type`
and, where the host requires them, `headers` to send with the request, such as a `Referer`
for `scihub` and `scidb`. `resolve_only` is implied, and no confirmation is asked because
nothing is written. Your client, or an agent's own HTTP tool, fetches the URL, so the file
lands wherever that fetch runs. See [Tools](tools.md#where-the-file-goes-local-vs-remote).

The server resolves that link without downloading the file, so the checks a local download
makes on the bytes, rejecting an HTML page in place of the file and enforcing the size cap,
have not been made. Check what arrives before treating it as the paper. A local server
returns the same link for one call with `resolve_only: true`, and for every call with
`LIBGEN_MCP_SERVER_FETCH=0`.

`read` is not registered on a remote server by default. `LIBGEN_MCP_SERVER_FETCH` is off
there, because returning a page of text means fetching the whole file over an egress address
that every user of the deployment shares. A client connected to such a server sees three
tools, not four. Fetch the link and read the file locally instead. An operator whose egress
is not shared can set `LIBGEN_MCP_SERVER_FETCH=1` to restore `read`, which on a remote
server takes the `doi` but not a `path`. See
[Configuration](configuration.md#libgen_mcp_server_fetch).

## Frequently asked questions

### Can Claude download a paper by DOI?

Yes, once libgen-mcp is registered as an MCP server in Claude Code, Claude Desktop or
another MCP client. Ask for the paper and give its DOI, and the model calls the `download`
tool with it. No search is needed first, and no account or API key is required for the
article chain.

On a local server the file is saved to the download directory. On a remote one `download`
returns a link to fetch instead. Whether a copy turns up depends on the paper: the chain
reaches the open-access copies first and the shadow-library fallbacks last, and when no
source has it, the error gives each source's reason.

### Does libgen-mcp use Sci-Hub?

Yes, as a fallback. For a DOI, `scihub` and then `scidb`, Anna's Archive's SciDB viewer, are
tried only after every open-access provider in the chain, the publisher's own deposited link
and OAPEN have declined. The order is fixed in code and held by a test, so a deployment
cannot move them earlier.

An operator who wants neither can leave `scihub` and `scidb` out of `LIBGEN_MCP_SOURCES`,
which lists the sources to keep. It removes sources but cannot reorder them. A single call
stays away from both by pinning `source` to one open-access provider. The result never says
which source served a file, so pinning is also how to know.

### Where is the downloaded PDF saved?

In the directory named by `LIBGEN_MCP_DOWNLOAD_DIR`, which defaults to `~/Downloads` on the
machine running the server. A call can name another directory with `path`, confined to the
working directory, the temporary directory, the download directory and
`LIBGEN_MCP_ALLOWED_DOWNLOAD_DIRS`. The result's `path` field gives the saved file's
location.

That holds for a local server. A remote server saves nothing: `download` returns a link, and
the file lands wherever your client or agent fetches it. The download directory is created
at startup if it does not exist. Under Docker the server runs as UID `10001`, so a host
directory mounted for downloads has to be writable by that user.

### Do I need an API key to download papers?

No. The article chain works with nothing configured: OpenAlex, Europe PMC, Internet Archive
Scholar, the sources for individual publishers, the publisher's own deposited link, OAPEN
and the fallbacks all run without a key. Two optional settings add a source each: an
Unpaywall contact email and a free CORE API key.

`LIBGEN_MCP_UNPAYWALL_EMAIL` enables `unpaywall` and `LIBGEN_MCP_CORE_KEY` enables `core`.
Without them the two sources are left out of the chain rather than failing. A client that
supports elicitation can also be asked for an Unpaywall email on a single download, which is
used for that request and never stored.
