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

| Page                                                     | Kind        | What it covers                                                                                                                                       |
| -------------------------------------------------------- | ----------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| [Getting started](getting-started.md)                    | Tutorial    | The shortest path from nothing to a working client: install it, wire it in, run your first search.                                                   |
| [Connect a client](clients.md)                           | How-to      | One complete configuration per MCP client, over stdio or over HTTP.                                                                                  |
| [Hosted endpoint](hosted.md)                             | How-to      | The public endpoint: connecting a client to it, what it serves and what it leaves out.                                                               |
| [Installation](install/overview.md)                      | How-to      | Picking a channel, what every channel shares, and verifying what you install.                                                                        |
| [Install with npm](install/npm.md)                       | How-to      | The `@jmrp.io/libgen-mcp` launcher: `npx` or a global install, verify, upgrade, remove.                                                              |
| [Install with PyPI](install/pypi.md)                     | How-to      | The `libgen-mcp` wheels: `uvx` or `pipx`, verify, upgrade, remove.                                                                                   |
| [Install with NuGet](install/nuget.md)                   | How-to      | The `libgen-mcp` .NET tool: `dnx` or `dotnet tool install`, verify, upgrade, remove.                                                                 |
| [Install with Homebrew](install/homebrew.md)             | How-to      | The `jmrplens/tap` formula: install, verify, upgrade, remove.                                                                                        |
| [Run with Docker](install/docker.md)                     | How-to      | The container image: stdio or HTTP, signature and provenance, upgrade, remove.                                                                       |
| [Release binaries and go install](install/binary.md)     | How-to      | The static release binaries, verified against the signed checksums, and building with `go install`.                                                  |
| [Claude Desktop extension](install/claude-desktop.md)    | How-to      | The one-click `.mcpb` bundle: install, set up, upgrade, remove.                                                                                      |
| [Agent plugin](install/agent-plugin.md)                  | How-to      | The Open Plugins and Agent Plugins manifests and the tools that read them.                                                                           |
| [Install with winget](install/winget.md)                 | How-to      | The Windows Package Manager manifest, pending review.                                                                                                |
| [Download a paper by DOI](download-a-paper.md)           | How-to      | Asking for a paper by DOI: what to set up, the call the model makes, and what to do on a miss.                                                       |
| [Citations](citations.md)                                | How-to      | A ready-to-paste BibTeX entry or RIS record from `get_details`, and why its DOI appears only once Crossref confirms it.                              |
| [Use cases](use-cases.md)                                | How-to      | Worked requests: what to ask, which tools are called in what order, and what comes back.                                                             |
| [HTTP server mode](http-server-mode.md)                  | How-to      | Deploying the server over streamable HTTP: the declared host, trusted proxies and what identity a deployment has, the limits, TLS, drain and probes. |
| [Behind a reverse proxy](deploy/reverse-proxy.md)        | How-to      | Complete reverse-proxy configurations: trusted proxies, the public URL, unbuffered streaming and TLS.                                                |
| [Run as a service](deploy/service.md)                    | How-to      | A unit file, a TCP port or a unix socket, graceful drain and the health check.                                                                       |
| [Containers and orchestration](deploy/containers.md)     | How-to      | Compose and orchestrator deployments: ports, the download volume, the non-root user, probes and signals.                                             |
| [Troubleshooting](troubleshooting.md)                    | How-to      | Fixes for unreachable mirrors, failed downloads, missing articles, truncated searches, and disk-space errors.                                        |
| [Versions and upgrades](changelog.md)                    | How-to      | How releases are versioned, and how to upgrade, pin and roll back on each channel.                                                                   |
| [Configuration](configuration.md)                        | Reference   | Every environment variable, with its default, valid range, and meaning.                                                                              |
| [Command-line flags](cli.md)                             | Reference   | Every flag the binary accepts, with its default, its environment variable and the transport it applies to.                                           |
| [Tools](tools.md)                                        | Reference   | The `search`, `get_details`, `download`, and `read` tools — inputs, outputs, and error behavior.                                                     |
| [Download sources](sources.md)                           | Reference   | Per-source reference for the twenty-one download sources: corpus, resolve mechanics, measured traps, keys, and crawl-rule constraints.               |
| [Telemetry](telemetry.md)                                | Reference   | OpenTelemetry: off by default, exported to a collector you run, what each signal records and what none of them ever will.                            |
| [Compatibility](compatibility.md)                        | Reference   | Platforms, architectures and C libraries, MCP protocol versions, and what each install channel needs.                                                |
| [What it costs to run](benchmarks/resource-benchmark.md) | Reference   | Measured memory, startup and latency on both transports, and what the outbound budget does to a tool call.                                           |
| [Glossary](glossary.md)                                  | Reference   | The terms these pages use, each defined once.                                                                                                        |
| [Architecture](architecture.md)                          | Explanation | The HTTP client (mirror discovery, failover, retry/cooldown), the download pipeline, the multi-source chain, and the transports.                     |
| [How search works](how-search-works.md)                  | Explanation | A conceptual walk through what a search queries, when it escalates beyond the catalog, and how each result's origin guides the download.             |
| [Scaling and capacity](deploy/scaling.md)                | Explanation | What bounds one process, how the per-caller limits and the outbound budget interact, and when more replicas help.                                    |
| [Known limitations](limitations.md)                      | Explanation | What the server does not do or does only in part, from OCR and hash checks to Anna's Archive and remote reads, each with its reason.                 |
| [Comparison with other MCP servers](comparison.md)       | Explanation | How it compares with other servers that search and download papers and books, as read from each project's repository.                                |
| [Responsible use](responsible-use.md)                    | Explanation | What it does to prefer legally free sources, what it refuses to serve, and how to raise a concern.                                                   |
| [Security model](security.md)                            | Explanation | What the server trusts and what it refuses: local paths, outbound destinations, untrusted text, HTTP hardening and per-call keys.                    |

The kinds are a label on the rows rather than four directories on disk. The two
subdirectories that do exist follow the site's URLs instead: `install/` holds one
page per install channel and `deploy/` the operator's recipes, so a page here and
its twin on the site share a path. A page that moves leaves a redirect on the
site, and every inbound link — from `README.md`, from the site, from `CLAUDE.md`
and from doc comments in the source — moves with it.

Two trees sit beside them: [`decisions/`](decisions/) holds the architecture
decision records, and [`development/`](development/README.md) is for people
changing this repository rather than using the server.

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible for
respecting the copyright and intellectual-property laws that apply where you live. Use it
only for content you are legally entitled to access.
