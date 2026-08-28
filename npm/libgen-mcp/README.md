# @jmrp.io/libgen-mcp

A [Model Context Protocol](https://modelcontextprotocol.io) server that lets an
AI assistant search, cite, download and read books, papers, comics, magazines
and standards across the [Library Genesis](https://en.wikipedia.org/wiki/Library_Genesis)
catalog and a long list of open-access sources. It runs as a local binary over
stdio (default) or HTTP, and needs **no account, API key or token**.

This package is a thin launcher and needs Node 18 or newer. The actual server is
a prebuilt Go binary that ships inside a per-platform package, listed here as an
optional dependency; npm resolves only the one whose `os` and `cpu` match your
machine and downloads it with the launcher. Nothing is compiled, and no install
script runs and fetches anything else — the binary is already in the package.

## Run without installing

```bash
npx @jmrp.io/libgen-mcp
```

Most MCP clients are configured to launch the server this way. For example:

```json
{
  "mcpServers": {
    "libgen": {
      "command": "npx",
      "args": ["-y", "@jmrp.io/libgen-mcp"]
    }
  }
}
```

## Install

```bash
npm install -g @jmrp.io/libgen-mcp   # or: pnpm add -g @jmrp.io/libgen-mcp
libgen-mcp --help
```

This puts `libgen-mcp` in your package manager's global binary directory. If the
command is not found afterwards, that directory is not on your `PATH` —
`npm config get prefix` shows npm's, and pnpm's is set up by `pnpm setup`.

## Tools

- `search` — search the Library Genesis catalog, escalating to Anna's Archive
  and the open-access providers (arXiv, Crossref, OpenLibrary, Project
  Gutenberg, dblp, PubMed, ERIC) when the catalog comes up empty.
- `get_details` — full metadata for a record by md5, edition/file id or DOI,
  with an optional BibTeX/RIS citation export.
- `download` — resolve and download a book (by md5) or article (by DOI) through
  an ordered source chain with transparent failover.
- `read` — extract text, search within, and outline a downloaded PDF/EPUB/TXT.

Four prompts (`acquire_book`, `research_topic`, `get_paper`,
`download_troubleshoot`) turn common requests into ready-to-run tool plans.

## Configuration

Everything works with zero configuration. Optional `LIBGEN_MCP_*` environment
variables and command-line flags — download directory, HTTP mode, extra search
sources, opt-in keys — are documented in the
[configuration reference](https://jmrp.io/docs/libgen-mcp/configuration/).

## Supported platforms

Linux, macOS and Windows, on x64 and arm64. On any other platform the launcher
exits with a message pointing to the
[release binaries](https://github.com/jmrplens/libgen-mcp/releases) and the
option to build from source.

## Links

- Documentation: <https://jmrp.io/docs/libgen-mcp>
- Source and issues: <https://github.com/jmrplens/libgen-mcp>
- License: MIT
