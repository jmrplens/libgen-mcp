# libgen-mcp documentation

`libgen-mcp` is an [MCP](https://modelcontextprotocol.io) server, written in Go, for
searching and downloading from **Library Genesis** (the `libgen.li` mirror family). It
exposes four tools — `search`, `get_details`, `download`, and `read` — to any MCP-compatible
client such as Claude Code, Claude Desktop, or your own agent.

Mirrors are discovered automatically and cached, with transparent failover, so the server
keeps working as individual mirrors go up and down. Articles can also be fetched from
open-access and Sci-Hub sources by DOI.

## Pages

| Page                                    | What it covers                                                                                                                           |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| [Getting started](getting-started.md)   | Install (release binary, Docker, or `go install`), wire the server into an MCP client, and run your first search.                        |
| [Configuration](configuration.md)       | Every environment variable, with its default, valid range, and meaning.                                                                  |
| [Tools](tools.md)                       | The `search`, `get_details`, `download`, and `read` tools — inputs, outputs, and error behavior.                                         |
| [Architecture](architecture.md)         | The HTTP client (mirror discovery, failover, retry/cooldown), the download pipeline, and the multi-source chain.                         |
| [How search works](how-search-works.md) | A conceptual walk through what a search queries, when it escalates beyond the catalog, and how each result's origin guides the download. |
| [Troubleshooting](troubleshooting.md)   | Fixes for unreachable mirrors, failed downloads, missing articles, truncated searches, and disk-space errors.                            |

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible for
respecting the copyright and intellectual-property laws that apply where you live. Use it
only for content you are legally entitled to access.
