# Privacy Policy

Last updated: 2026-07-19

**libgen-mcp** is a local Model Context Protocol (MCP) server. It runs entirely
on your machine and acts as a bridge between your MCP client (Claude Desktop,
Claude Code, Cursor, VS Code, …) and the public Library Genesis mirrors. It
needs **no account, no token, and no credentials**. This policy describes what
data the server handles and where it goes.

## What we collect

**Nothing.** The server has no telemetry, no analytics, no crash reporting, and
no backend of its own. There is no account to create and nothing to log in to.
The maintainer never receives, stores, or has access to any of your data or
usage information.

## Data flows

Every network request is a direct consequence of a tool call you (through your
AI assistant) make. There are no background connections. The destinations are:

- **Library Genesis mirrors.** `search` and `get_details` query the Library
  Genesis mirrors (for example `libgen.li`, `libgen.gl`, `libgen.la`,
  `libgen.bz`, `libgen.vg`), which are discovered automatically and cached, or
  pinned via `LIBGEN_MIRROR`. Book `download` requests (by `md5`) fetch the file
  from the serving mirror and its download CDNs. If the primary mirror path
  fails, the `randombook` source (`randombook.org`) is tried as a fallback.
- **Unpaywall API (only when you request an article by DOI).** Resolving an
  article `download` by `doi` queries the [Unpaywall](https://unpaywall.org) API
  (`api.unpaywall.org`) to locate an open-access copy. Unpaywall's API requires
  a contact email, sent as a query parameter. It defaults to the maintainer's
  address; set `LIBGEN_MCP_UNPAYWALL_EMAIL` to your own so requests are
  attributed to you. No other personal data is sent.
- **Sci-Hub mirrors (only when you request an article by DOI).** If Unpaywall
  finds no open-access copy, the article `download` chain falls through to the
  configured Sci-Hub hosts (`LIBGEN_MCP_SCIHUB_HOSTS`, e.g. `sci-hub.ee`),
  requesting `https://<host>/<doi>` until one serves the paper.

These external services handle your queries under their own policies; the
maintainer of this project has no relationship with them and no visibility into
those requests. You can restrict which download sources participate with
`LIBGEN_MCP_SOURCES`. There are no other network destinations — no update
checks, no phone-home.

## Credentials

None. Library Genesis, its mirrors, and the article sources used here require no
account or token, so the server never asks for, stores, or transmits any
credentials.

## Local storage and downloads

- **Downloads** are written only to the local destination directory
  (`LIBGEN_MCP_DOWNLOAD_DIR`, default `~/Downloads`, or the per-call `path`
  argument). Files stay on your machine; nothing is uploaded anywhere.
- **Logs** go to standard error only (collected, if at all, by your MCP client).
  The server does not create databases or telemetry files. A small in-memory
  cache of discovered mirrors lives only for the life of the process.

## Data retention and sharing

The server retains nothing after it exits and shares data with no third parties
beyond the Library Genesis mirrors and, for article DOIs, the Unpaywall and
Sci-Hub sources you explicitly invoke.

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible
for respecting the copyright and intellectual-property laws that apply where you
live. Use it only for content you are legally entitled to access.

## Changes

Changes to this policy are published in this file and noted in release
changelogs.

## Contact

Questions or concerns: [open an issue](https://github.com/jmrplens/libgen-mcp/issues)
or email <mail@jmrp.io>.
