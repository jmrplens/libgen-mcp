# Privacy Policy

Last updated: 2026-07-24

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
- **The extra searchers (when a search reaches beyond the catalog).** A `search`
  may send **your query text** to Anna's Archive (`annas-archive.gl` and its
  mirrors), [arXiv](https://arxiv.org), [Crossref](https://www.crossref.org) and
  [OpenLibrary](https://openlibrary.org). When this happens is under your
  control, via the `extra_sources` argument or `LIBGEN_MCP_EXTRA_SOURCES`: by
  default (`auto`) only when the Library Genesis catalog returns nothing or
  fails, with `always` on every search, and with `never` not at all. The Crossref
  request carries the same contact email as Unpaywall. `get_details` also queries
  Anna's Archive, sending **only the md5**, when the catalog has no record for it.
- **Anna's Archive and IPFS gateways (only when you download through them).**
  The `scidb` source resolves an article `download` by `doi` through Anna's
  Archive, and the `annas` source resolves a book `download` by `md5` there,
  then fetches the file from a public IPFS gateway (`dweb.link`, `w3s.link`,
  `ipfs.io`, `gateway.pinata.cloud`). If you set `LIBGEN_MCP_ANNAS_KEY` — or
  supply a key for a single call when asked — that key is sent to Anna's
  Archive to use your membership's faster download tier. It is used for that
  request and never written to disk.

These external services handle your queries under their own policies; the
maintainer of this project has no relationship with them and no visibility into
those requests. You can restrict which download sources participate with
`LIBGEN_MCP_SOURCES`, and which searchers a `search` may reach with
`LIBGEN_MCP_EXTRA_SOURCES=never`. There are no other network destinations — no
update checks, no phone-home.

## Credentials

None are required. Library Genesis, its mirrors, and the keyless article and
search sources used here need no account or token. The one optional credential
is an Anna's Archive membership key (`LIBGEN_MCP_ANNAS_KEY`, or supplied for a
single call through your client's elicitation prompt), which unlocks that site's
faster member download tier. It is sent only to Anna's Archive, only on a
download you asked for, and is never persisted by the server.

## Local storage and downloads

- **Downloads** are written only to the local destination directory
  (`LIBGEN_MCP_DOWNLOAD_DIR`, default `~/Downloads`, or the per-call `path`
  argument). Files stay on your machine; nothing is uploaded anywhere.
- **Logs** go to standard error only (collected, if at all, by your MCP client).
  The server does not create databases or telemetry files. A small in-memory
  cache of discovered mirrors lives only for the life of the process.

## Data retention and sharing

The server retains nothing after it exits and shares data with no third parties
beyond the destinations listed under [Data flows](#data-flows) — the Library
Genesis mirrors, the extra searchers a `search` may reach, and the article and
book download sources you invoke.

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
