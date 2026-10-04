# Versions and upgrades

**How-to guide** — for anyone keeping an installation current, or holding it still.

`libgen-mcp` is released as a numbered version, and every channel publishes the same number
from the same tag. This page says what a version number promises, where each release is
described, what changed from 2.0.0 onward, and, per install channel, how to upgrade, pin a
version and roll back.

The server never updates itself and never checks for a newer release. An installation stays on
the version you installed until you replace it through the channel that put it there.

## What the version number means

Versions follow [semantic versioning](https://semver.org/), read against what an installation
or a client depends on. The single source of the number is the repository's `VERSION` file; the
release stamps it into every package manifest, so `npx`, `uvx`, `dnx`, Homebrew, the image tag
and the `.mcpb` all carry the same one.

| Change                                                                                                             | Example                                                                 | Bump  |
| ------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------- | ----- |
| A fix, or a small addition that changes no call that already worked                                                | 2.0.1 added the file-name column to search results and the Linux bundle | Patch |
| New behaviour, a new flag or variable with a default that keeps today's behaviour, a new source or provider        | 2.1.0 bounded the calls an HTTP process holds open                      | Minor |
| A change that stops an existing installation, client or integration from working until its owner changes something | 2.0.0 moved the Go module path to `/v2`                                 | Major |

What counts as a contract here, and therefore needs a major version to break:

- **The tool surface.** The four tool names, their argument names and meanings, and the prompt
  names and arguments. A new optional argument is additive; renaming or removing one is not.
- **Flags and environment variables.** Every `--flag` and every `LIBGEN_MCP_*` name in
  [Command-line flags](cli.md) and [Configuration](configuration.md). A removed flag stops
  every deployment that passes it at startup, and a removed variable is worse: it is silently
  no longer read.
- **Published shapes.** The order of each icon's three entries (SVG, light WebP, dark WebP),
  which the server card republishes verbatim, and the OpenTelemetry instrument names a
  dashboard is built on.
- **The Go module path**, which carries the major version from 2.0.0 onward.

Log field names and the Markdown text of a result are not contracts. When a release renames a
log field, its notes say so under **Upgrade note**, as 2.1.0 did for `request_host`.

To find out which version is running, ask the binary or the deployment:

```bash
libgen-mcp --version
curl -s http://127.0.0.1:8080/health   # "version", "commit" and "build" fields
```

## Where release notes live

- **The signed tag message** is the hand-written, user-facing summary of each release: what
  changed and what to do about it. Read it with `git show v2.2.0` in a clone, or verify it
  with `git tag -v v2.2.0`. The sections below are drawn from these messages.
- **The [GitHub release](https://github.com/jmrplens/libgen-mcp/releases)** lists every
  commit in the release, grouped by kind (features, fixes, documentation, maintenance), and
  carries the assets: the binaries, their SBOMs, the signed `checksums.txt` and the `.mcpb`
  bundles. Subscribe to
  [`releases.atom`](https://github.com/jmrplens/libgen-mcp/releases.atom) or watch the
  repository's releases to hear of a new one.
- **What is on `main` and not yet released** is the
  [compare view from the latest tag](https://github.com/jmrplens/libgen-mcp/compare/v2.2.0...main).
  This page keeps no hand-written "unreleased" list: it would need editing in three copies on
  every merge and would be stale between them. The summary of those changes is written once,
  into the next tag message, and lands here with that release.

## Release by release

### 2.2.0

- `search` also asks OpenAlex and Europe PMC, ten providers beyond the catalog in all, and both
  mark open access only where the paper really is free to read. Without a key, OpenAlex allows
  each address 1000 credits a day and a search costs ten. `LIBGEN_MCP_OPENALEX_KEY` raises the
  allowance, and the Claude Desktop extension has a setting for it. See
  [How search works](how-search-works.md).
- `search` takes `year_from` and `year_to`. A provider that can narrow by year does it in its
  own query. The catalog page is filtered after it arrives, and `year_filtered` says how many
  records the range dropped from that page.
- `get_details` takes `citation`: paste a reference and it is resolved to its DOI through
  Crossref, but only when the best match is clear. Otherwise the candidates come back for you to
  choose from.
- `get_details` takes `cite_as` for APA, MLA, Chicago, Harvard, Vancouver, IEEE and CSL-JSON.
  The text comes from doi.org when the record's DOI is confirmed, and is built from the record's
  fields otherwise. A DataCite or mEDRA DOI now gets a record of its own. See
  [Citations](citations.md).
- `get_details` takes `related` to list a record's references, or the works that cite it, from
  OpenAlex.
- `read` takes `section` to read one table-of-contents entry, and every outline entry carries
  the index it is named by.
- A search that only the providers beyond the catalog could answer is no longer reported as
  "No results".
- The `europepmc` download source takes its PDFs from NCBI's PMC Article Datasets, because
  Europe PMC's own PDF links now answer automated clients with a browser challenge. A retracted
  article is declined.
- dblp is searched through its SPARQL service, because its search API now refuses automated
  clients. Each upstream is paced for the whole process, and a provider whose next turn is more
  than a second away sits a search out instead of delaying the answer.
- The contact address in the User-Agent stays with the service it was sent to. A redirect to
  another origin drops it, and every hop of a redirect chain is judged against its first request,
  so no credential follows a hop that left the origin, an `http` one included.
- Claude Desktop gets one bundle per operating system, beside the universal bundle.
- The macOS wheels on PyPI are tagged for macOS 13, the oldest release the binary starts on.
- A flag that overrides an environment variable is named in a warning, `--shutdown` waits for
  the drain of the instance it stops, every refused start is logged at ERROR as its last
  record, and a mirror cache that cannot be written is reported once.
- **Upgrade note:** a variable the client passes blank no longer hides the same variable in a
  settings file. The file's value now applies, so a blank field in Claude Desktop no longer
  clears a key kept in `~/.libgen-mcp.env`.
- **Upgrade note:** `config_digest` on `/health` now covers the sources a server actually
  enabled, so every deployment's digest changes once with this release. A fleet upgraded one
  replica at a time reads as mismatched until the last one is replaced.
- **Upgrade note:** `LIBGEN_MCP_UNPAYWALL_EMAIL` may no longer contain a parenthesis, and a
  server given one refuses to start.

### 2.1.0

- An HTTP deployment bounds the calls and stateful sessions it holds open, sized from the
  process's descriptor limit at startup (83 calls and 41 sessions under a hard limit of 1024).
  Past either bound a request is answered `503` with `Retry-After`, instead of the process
  running out of descriptors and refusing `/health` too. See
  [HTTP server mode](http-server-mode.md#what-the-whole-process-may-hold).
- Behind a corporate `HTTP_PROXY`/`HTTPS_PROXY` on a private address, Crossref, arXiv,
  Unpaywall, OpenLibrary and every download work again. The destination behind the proxy is
  judged per request instead, so a cloud metadata address cannot be reached through it in any
  numeric spelling.
- A refused `Host` header, `net/http`'s error lines and the SDK's composed messages stay on
  stderr and never reach an OTLP collector.
- A pipe in a mirror's label no longer splits the search table, and catalog text can no longer
  open a heading, list or quote in a result.
- Refusals by the rate limit and the per-caller ceiling carry `resultType` on protocol
  `2026-07-28`.
- A stdio server names the HTTP-only flags and variables it was given and ignores.
- The discovery server card is also served after the `/mcp` form of the endpoint, at
  `/mcp/server-card`.
- Citations name the record's real source and read the catalog's author lists.
- The npm launcher stops the server when it is told to stop, once, so an HTTP server started
  with `npx` frees its port and drains.
- Every package carries `LICENSE` and `THIRD_PARTY_NOTICES`, and the NuGet packages are
  attested.
- **Upgrade note:** the log field for a refused `Host` header is now `request_host`.

### 2.0.1

- Anna's Archive stops retrying answers that cannot change: a member-API "no fast copy" behind
  a browser-challenged record page ends the download on the first request, instead of spending
  about a hundred seconds, and tells the model to try another copy of the same work.
- Search shows each result's original file name, which is what tells editions of a standard
  apart when the catalog leaves the year empty.
- The Claude Desktop extension runs on the Linux beta, on x86_64 and arm64. Install it from
  **Extensions > Install Extension…**.
- Every tool's input schema carries an example call in the JSON Schema `examples` keyword.

### 2.0.0

- **The Go module path is `github.com/jmrplens/libgen-mcp/v2`.** Every other channel is
  unchanged. See [Upgrading from 1.x](#upgrading-from-1x).
- No tool's input schema carries `oneOf`, `anyOf` or `allOf` at its root. The Anthropic
  Messages API refuses such a tool with HTTP 400 for the whole request, so one of them disabled
  every tool in the call. The rules they expressed (give exactly one of `md5`, `id` or `doi`)
  are enforced by the handlers and stated in the field descriptions, as before. A call that
  worked on 1.x works unchanged.
- Anna's Archive answers its HTML pages with a browser challenge. Discovery tells that apart
  from an empty result, says so once, and asks nothing for fifteen minutes. A configured member
  key still downloads through the member API.
- Reading a PDF no longer creates a configuration directory in the caller's home.
- Two HTTP flags close what has gone idle: `--session-timeout` for a legacy stateful session
  and `--http-idle-timeout` for a kept-alive connection. See [Command-line flags](cli.md).

### 1.x

The 1.x line ended at 1.7.3. Its notes are on the
[releases page](https://github.com/jmrplens/libgen-mcp/releases), one per tag, and in each
tag's message.

## Upgrading from 1.x

Nothing changes for npm, PyPI, NuGet, Homebrew, Docker, the release binaries or the Claude
Desktop extension: the package names, the command, the arguments and every setting are the
same. Upgrade as you would to any release.

**`go install` is the one channel that moved.** Go requires the major version in the module
path from 2 onward, and the unsuffixed path does not fail loudly — it keeps resolving the
newest 1.x tag forever:

```bash
go install github.com/jmrplens/libgen-mcp/v2/cmd/server@latest   # 2.x
go install github.com/jmrplens/libgen-mcp/cmd/server@latest      # stays on 1.7.3
```

If a script or a Dockerfile builds from source, change the path in it.

## Upgrading, pinning and rolling back

Every release stays downloadable, and every channel can be held at an exact version. Pin one
wherever an unexpected upgrade would hurt — a shared deployment, a CI job, a client config
on a machine you do not watch — and move the pin deliberately.

**Stop the old process after an upgrade.** A client keeps the server it started running, so
replacing the binary under it changes nothing until the client starts it again.
`libgen-mcp --shutdown` asks every other instance on the machine to exit; the client starts the
new one on its next call. See [Installation](install/overview.md).

| Channel                                     | Upgrade                                          | Pin a version                                                                     | Roll back                                                                        |
| ------------------------------------------- | ------------------------------------------------ | --------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| [npm](install/npm.md)                       | `npm install -g @jmrp.io/libgen-mcp@latest`      | `npx -y @jmrp.io/libgen-mcp@2.2.0`, or `npm install -g @jmrp.io/libgen-mcp@2.2.0` | Install the older version the same way                                           |
| [PyPI](install/pypi.md)                     | `pipx upgrade libgen-mcp`                        | `uvx libgen-mcp@2.2.0`, or `pipx install libgen-mcp==2.2.0`                       | `pipx install --force libgen-mcp==<older version>`                               |
| [NuGet](install/nuget.md)                   | `dotnet tool update -g libgen-mcp`               | `dnx libgen-mcp@2.2.0`, or `dotnet tool install -g libgen-mcp --version 2.2.0`    | `dotnet tool uninstall -g libgen-mcp`, then install with `--version`             |
| [Homebrew](install/homebrew.md)             | `brew upgrade libgen-mcp`                        | `brew pin libgen-mcp` holds the installed version                                 | The tap carries only the current formula: use the release binary of that version |
| [Docker](install/docker.md)                 | `docker pull ghcr.io/jmrplens/libgen-mcp:latest` | The version tag, `ghcr.io/jmrplens/libgen-mcp:2.2.0`, or its `@sha256:` digest    | Point at the previous tag or digest                                              |
| [Release binary](install/binary.md)         | Download the new asset over the old one          | Download from the tagged release, `releases/download/v2.2.0/…`                    | Download the older asset the same way                                            |
| [Claude Desktop](install/claude-desktop.md) | Download the new bundle and open it              | Keep the bundle file of the version you want                                      | Remove the extension, then open the older bundle                                 |
| [`go install`](install/binary.md)           | `…/v2/cmd/server@latest`                         | `…/v2/cmd/server@v2.2.0`                                                          | `…/v2/cmd/server@v<older version>`                                               |

Two of these deserve a sentence more.

- **A client config that runs `npx` or `uvx` without a version** takes whatever the package
  manager resolves, subject to its cache, so two machines with the same config can run
  different versions. Put the version in the `args` to make it exact:

  ```json
  {
    "mcpServers": {
      "libgen": { "command": "npx", "args": ["-y", "@jmrp.io/libgen-mcp@2.2.0"] }
    }
  }
  ```

- **A version tag on an image is a name, a digest is the bytes.** The release pushes
  `2.2.0` and `latest` to both registries, and a registry tag is a pointer that can be moved,
  so a digest is what a deployment that must be reproducible should name. Read it with
  `docker buildx imagetools inspect ghcr.io/jmrplens/libgen-mcp:2.2.0`, and verify the image
  you pinned as [Run with Docker](install/docker.md) shows.

Whichever version you land on, check it is the one this project published before running it:
[Installation](install/overview.md#verifying-what-you-install) has the recipe per channel.

## Where to go next

| For                                                       | See                                   |
| --------------------------------------------------------- | ------------------------------------- |
| Every channel, and verifying what it installed            | [Installation](install/overview.md)   |
| The platforms and protocol versions each release supports | [Compatibility](compatibility.md)     |
| A deployment that will not start after an upgrade         | [Troubleshooting](troubleshooting.md) |
