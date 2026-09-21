# libgen-mcp documentation

`libgen-mcp` is an [MCP](https://modelcontextprotocol.io) server, written in Go, for
**federated search, citation and reading** of books, papers, comics, magazines and standards
across the **Library Genesis** catalog (the `libgen.li` mirror family) and open-access
sources. It exposes four tools — `search`, `get_details`, `download`, and `read` — to any
MCP-compatible client such as Claude Code, Claude Desktop, or your own agent.

Mirrors are discovered automatically and cached, with transparent failover, so the server
keeps working as individual mirrors go up and down. Articles can also be fetched from
open-access and Sci-Hub sources by DOI.

## Pages

Each page says what kind of document it is and who it is for, on its first line.
The four kinds are the [Diátaxis](https://diataxis.fr/) ones, and the rows below
are grouped by them: a tutorial is followed start to finish, a how-to guide
answers a goal you already have, a reference is looked things up in, and an
explanation is read to understand why.

| Page                                    | Kind        | What it covers                                                                                                                                       |
| --------------------------------------- | ----------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| [Getting started](getting-started.md)   | Tutorial    | The shortest path from nothing to a working client: install it, wire it in, run your first search.                                                   |
| [Installation](installation.md)         | How-to      | Every channel, one section each: what you get, install, verify what you installed, upgrade, uninstall.                                               |
| [HTTP server mode](http-server-mode.md) | How-to      | Deploying the server over streamable HTTP: the declared host, trusted proxies and what identity a deployment has, the limits, TLS, drain and probes. |
| [Troubleshooting](troubleshooting.md)   | How-to      | Fixes for unreachable mirrors, failed downloads, missing articles, truncated searches, and disk-space errors.                                        |
| [Configuration](configuration.md)       | Reference   | Every environment variable, with its default, valid range, and meaning.                                                                              |
| [Tools](tools.md)                       | Reference   | The `search`, `get_details`, `download`, and `read` tools — inputs, outputs, and error behavior.                                                     |
| [Download sources](sources.md)          | Reference   | Per-source reference for the twenty-one download sources: corpus, resolve mechanics, measured traps, keys, and crawl-rule constraints.               |
| [Telemetry](telemetry.md)               | Reference   | OpenTelemetry: off by default, exported to a collector you run, what each signal records and what none of them ever will.                            |
| [Architecture](architecture.md)         | Explanation | The HTTP client (mirror discovery, failover, retry/cooldown), the download pipeline, the multi-source chain, and the transports.                     |
| [How search works](how-search-works.md) | Explanation | A conceptual walk through what a search queries, when it escalates beyond the catalog, and how each result's origin guides the download.             |

The kinds are a label on the rows rather than four directories on disk. Ten
pages and an index are not a navigation problem, and moving them would break
every inbound link — from `README.md`, from the site, from `CLAUDE.md` and from
doc comments in the source — to buy a directory listing this table already is.

Two trees sit beside them: [`decisions/`](decisions/) holds the architecture
decision records, and [`development/`](development/README.md) is for people
changing this repository rather than using the server.

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible for
respecting the copyright and intellectual-property laws that apply where you live. Use it
only for content you are legally entitled to access.
