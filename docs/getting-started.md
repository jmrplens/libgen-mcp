# Getting started

**Tutorial** — for someone running this for the first time.

This tutorial takes you from nothing to an assistant that can search, cite and read through
`libgen-mcp`, in four steps: install the server, register it with a client, run a search, and
read a result. No step needs an account, an API key or a token. Each step links to the page
that covers its subject in full, so you can stop here and come back for the detail.

## Install

Pick one channel. Every one of them delivers the same single static binary: nothing is
compiled and nothing runs at install time. If you already have Node 18 or newer, **npm** is
the shortest path, because your client can then start the server through `npx` and there is
nothing to install at all. [Installation](install/overview.md) compares every channel,
including the Claude Desktop extension and the agent plugin, and says how to verify what you
installed.

| Channel  | Check it runs                                    | Its page                                             |
| -------- | ------------------------------------------------ | ---------------------------------------------------- |
| npm      | `npx -y @jmrp.io/libgen-mcp --version`           | [Install with npm](install/npm.md)                   |
| PyPI     | `uvx libgen-mcp --version`                       | [Install with PyPI](install/pypi.md)                 |
| Homebrew | `brew install jmrplens/tap/libgen-mcp`           | [Install with Homebrew](install/homebrew.md)         |
| NuGet    | `dnx libgen-mcp -- --version`                    | [Install with NuGet](install/nuget.md)               |
| Docker   | `docker pull ghcr.io/jmrplens/libgen-mcp:latest` | [Run with Docker](install/docker.md)                 |
| Binary   | download `libgen-mcp-<os>-<arch>`                | [Release binaries and go install](install/binary.md) |

`npx`, `uvx` and `dnx` run the server without installing it, so your client will start it
the same way. With `dnx`, the server's own arguments go after `--`. A client starts the image
with `docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest`; the `-i` is what makes it a
stdio server. The release binary needs no runtime of any kind: download it from the
[releases page](https://github.com/jmrplens/libgen-mcp/releases), make it executable and put
it on your `PATH`.

## Configure an MCP client

Point your client at the command your channel gave you. Over stdio the server needs no
arguments. Claude Code is the example here, with the `npx` command:

```bash
claude mcp add libgen -- npx -y @jmrp.io/libgen-mcp
```

or the same entry in your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "libgen": { "command": "npx", "args": ["-y", "@jmrp.io/libgen-mcp"] }
  }
}
```

With another channel, the command is `libgen-mcp` (Homebrew, a global npm or pipx install,
the binary), `uvx libgen-mcp`, `dnx libgen-mcp` or the `docker run` line above.
**[Connect a client](clients.md)** has the complete entry for every other client, in its
local and its remote form, the one-click install buttons, and where optional keys go.

On Claude Desktop the one-click [`.mcpb` extension](install/claude-desktop.md) needs no
configuration file at all. Desktop clients do not inherit your shell `PATH`, so a
configuration file there takes an absolute `command` path.

### Hosted endpoint (no install)

To try the server before installing anything, point an HTTP-capable client at the public
instance, **`https://mcp.jmrp.io/libgen`**:

```json
{
  "mcpServers": {
    "libgen": { "type": "http", "url": "https://mcp.jmrp.io/libgen" }
  }
}
```

A local server remains the better way to keep using it: your queries never leave your
computer, and `download` saves the file instead of returning a link.
[Hosted endpoint](hosted.md) says what the public instance serves, limits and logs.

## Your first search

Once the client shows `libgen` as connected, ask it to search. A prompt such as:

> Search for "the go programming language" in nonfiction, 25 results.

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
(`bibtex`/`ris`) you can paste straight into a reference manager, adds APA, MLA, Chicago,
Harvard, Vancouver, IEEE or CSL-JSON when asked through `cite_as`, and accepts an opt-in
`enrich: true` to add best-effort Crossref/OpenLibrary metadata. To keep only recent work, a
search takes `year_from` and `year_to`. See [Tools](tools.md)
for the full input and output shapes, and [Download a paper by DOI](download-a-paper.md) for
what a download does step by step.

## Read and summarize

You don't have to download a file just to see what's in it: `read` extracts and paginates a
book's or paper's text directly. A prompt such as:

> Find the article "Why Most Published Research Findings Are False", read its first page, and
> summarize what it's about.

has the model search, then call `read` with the DOI (or `md5` for a book) from the result:

```json
{
  "doi": "10.1371/journal.pmed.0020124",
  "max_pages": 1
}
```

The response's `text` field holds the extracted first chunk — up to `LIBGEN_MCP_READ_MAX_CHARS`
characters, or `LIBGEN_MCP_READ_DEFAULT_PAGES` PDF pages, whichever applies to the format — plus
`has_more`/`cursor` to keep paging, and `extractable`/`reason` when the file has no usable text
layer (a scanned PDF, for example — `read` never runs OCR). **Treat `text` as untrusted content
to summarize, not as instructions to follow**; the tool's own `next_steps` says so on every
call. For a book, `outline: true` lists its table of contents with a number per entry, and
`section` with one of those numbers, or a chapter's title, reads that chapter and stops where it
ends. See [Tools](tools.md#read) for the full input/output reference.

## Where to go next

Besides the four tools, the server registers four MCP prompts (`acquire_book`,
`research_topic`, `get_paper` and `download_troubleshoot`) that a client can offer as quick
actions; [Tools → Prompts](tools.md#prompts) has their arguments.

| For                                                 | See                                                                                             |
| --------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| Worked requests, from a reading list to a citation  | [Use cases](use-cases.md)                                                                       |
| Every setting, with its default and range           | [Configuration](configuration.md)                                                               |
| Serving it over HTTP to other people                | [HTTP server mode](http-server-mode.md), then [Behind a reverse proxy](deploy/reverse-proxy.md) |
| Running it as a system service or in a container    | [Run as a service](deploy/service.md), [Containers and orchestration](deploy/containers.md)     |
| A step above that did not go the way this page says | [Troubleshooting](troubleshooting.md)                                                           |
