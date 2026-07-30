# Privacy Policy

Last updated: 2026-07-25

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
- **Unpaywall API (only when you request an article by DOI, and only if you
  enable it).** `LIBGEN_MCP_UNPAYWALL_EMAIL` is **empty by default**, which
  disables the `unpaywall` source: no request is made to Unpaywall, and no
  address of the maintainer's or anyone else's is ever substituted for yours.
  There are exactly two ways an address is sent, both of which you initiate.
  Set the variable to your own contact address, and resolving an article
  `download` by `doi` queries the [Unpaywall](https://unpaywall.org) API
  (`api.unpaywall.org`) with that address as a query parameter, which is what
  its API requires. Or leave it unset: a client that supports MCP elicitation
  may then offer to ask you for a one-off address for that single call, which
  is used for that request only, is never written to disk, and is never
  reused — and the prompt is skipped entirely when `source` was set
  explicitly. Decline it and the request proceeds without Unpaywall. No other
  personal data is sent.
- **Keyless open-access providers (only when you request an article by DOI).**
  Before any shadow-library fallback, the article `download` chain asks the open
  repositories for a freely licensed copy: [Europe PMC](https://europepmc.org)
  (`ebi.ac.uk`, `europepmc.org`), [bioRxiv/medRxiv](https://www.biorxiv.org)
  (`api.biorxiv.org`, plus the `biorxiv.org`/`medrxiv.org` content hosts), the
  [RFC Editor](https://www.rfc-editor.org) (`www.rfc-editor.org`) for an RFC DOI,
  [NIST](https://nvlpubs.nist.gov) for a `10.6028` DOI (the request goes to
  `doi.org`, whose redirect leads to `nvlpubs.nist.gov`),
  [Schloss Dagstuhl](https://drops.dagstuhl.de) (`drops.dagstuhl.de`) for a
  `10.4230` DOI, the [ACL Anthology](https://aclanthology.org)
  (`aclanthology.org`) for a `10.18653`/`10.3115` DOI,
  [Zenodo](https://zenodo.org) (`zenodo.org`) for a `10.5281/zenodo` DOI,
  and Internet Archive Scholar / fatcat (`scholar.archive.org`, then
  `web.archive.org` for the file). A monograph DOI is also offered to
  [OAPEN](https://library.oapen.org) (`library.oapen.org`). Each request carries
  only the DOI.
- **Open-access book sources (only when you request a book by ISBN).** A
  `download` by `isbn` sends **only that ISBN** to [OAPEN](https://library.oapen.org)
  (`library.oapen.org`) and to [OpenLibrary](https://openlibrary.org)
  (`openlibrary.org`), which is asked which [Internet Archive](https://archive.org)
  scans hold the book; the candidate scans are then confirmed and fetched from
  `archive.org` (whose download URL redirects to one of its own CDN nodes). No
  account, key or contact address is involved in any of these requests.
- **CORE (only when you request an article by DOI and configure a key).**
  `LIBGEN_MCP_CORE_KEY` is empty by default, which leaves the `core` source out
  of the chain. When you set it, the DOI is sent to `api.core.ac.uk` with the key
  as a bearer token; the key is never attached to the file URL CORE returns.
- **Sci-Hub mirrors (only when you request an article by DOI).** If none of the
  open-access providers above yields a copy, the article `download` chain falls
  through to the configured Sci-Hub hosts (`LIBGEN_MCP_SCIHUB_HOSTS`, e.g.
  `sci-hub.ee`), requesting `https://<host>/<doi>` until one serves the paper.
- **The extra searchers (when a search reaches beyond the catalog).** A `search`
  may send **your query text** to Anna's Archive (`annas-archive.gl` and its
  mirrors), [arXiv](https://arxiv.org), [Crossref](https://www.crossref.org),
  [OpenLibrary](https://openlibrary.org), Project Gutenberg via the third-party
  [Gutendex](https://gutendex.com) API (`gutendex.com`; the ebook files it links
  to live on `gutenberg.org`, which is contacted only if you fetch one),
  [dblp](https://dblp.org) (`dblp.org`),
  [PubMed](https://pubmed.ncbi.nlm.nih.gov)
  (`eutils.ncbi.nlm.nih.gov`) and [ERIC](https://eric.ed.gov)
  (`api.ies.ed.gov`). When this happens is under your
  control, via the `extra_sources` argument or `LIBGEN_MCP_EXTRA_SOURCES`: by
  default (`auto`) only when the Library Genesis catalog returns nothing or
  fails, with `always` on every search, and with `never` not at all. When — and
  only when — you have configured `LIBGEN_MCP_UNPAYWALL_EMAIL`, the Crossref
  request carries that same address as its polite-pool contact, and the PubMed
  requests carry it as the contact address NCBI's usage etiquette asks for; with
  no address configured, none is sent and none is invented. An ERIC hit for a
  document ERIC hosts carries a `files.eric.ed.gov` full-text URL; that host is
  named in the result but is **never contacted by this server** — nothing is
  fetched from it unless you follow the link yourself. `get_details`
  also queries
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
search sources used here need no account or token. Two credentials are optional:

- An **Anna's Archive membership key** (`LIBGEN_MCP_ANNAS_KEY`, or supplied for a
  single call through your client's elicitation prompt), which unlocks that
  site's faster member download tier. It is sent only to Anna's Archive, only on
  a download you asked for, and is never persisted by the server.
- A **CORE API key** (`LIBGEN_MCP_CORE_KEY`, free registration at core.ac.uk),
  which enables the `core` open-access article source. It is sent only to
  `api.core.ac.uk`, and never with the file URL that CORE resolves to.

The Unpaywall contact email (`LIBGEN_MCP_UNPAYWALL_EMAIL`) is not a credential —
it is an attribution address the Unpaywall API requires — but it is likewise
optional, and unset by default.

## Local storage and downloads

- **Downloads** are written only to the local destination directory
  (`LIBGEN_MCP_DOWNLOAD_DIR`, default `~/Downloads`, or the per-call `path`
  argument). Files stay on your machine; nothing is uploaded anywhere.
- **Logs** go to standard error only (collected, if at all, by your MCP client).
  The server creates no database and no telemetry file.
- **Mirror cache.** The lists of discovered Library Genesis and Anna's Archive
  mirrors are cached on disk for 24 hours, as `mirrors.json` and
  `annas-mirrors.json` under the OS cache directory
  (`~/.cache/libgen-mcp/` on Linux, `~/Library/Caches/libgen-mcp/` on macOS).
  They hold public mirror URLs only — no queries, no identifiers, and nothing
  about you. Deleting them just forces a fresh discovery on the next call.
- **Temporary files.** `read` fetches the file it extracts text from into a
  temporary directory on the machine running the server, so successive pages of
  one document reuse a single fetch; those files are evicted on a size cap and a
  TTL (`LIBGEN_MCP_READ_CACHE_BYTES` / `LIBGEN_MCP_READ_CACHE_TTL`). An
  interrupted `download` likewise leaves a `.part` file in the destination
  directory so a later call can resume it.

## Data retention and sharing

The only things the server leaves behind after it exits are the files described
under [Local storage and downloads](#local-storage-and-downloads): what you asked
it to download, the 24-hour mirror cache, and any temporary `read` files not yet
evicted. None of them records a query or an identifier of yours except the names
of the files you chose to fetch. It shares data with no third parties beyond the
destinations listed under [Data flows](#data-flows) — the Library Genesis
mirrors, the extra searchers a `search` may reach, and the article and book
download sources you invoke.

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible
for respecting the copyright and intellectual-property laws that apply where you
live. Use it only for content you are legally entitled to access.

## Frequently asked questions

### Does libgen-mcp collect any telemetry or analytics?

No. The server has no telemetry, no analytics, no crash reporting and no backend
of its own. It creates no database and no telemetry file, and logs only to
standard error, where your MCP client collects them if it collects them at all.
The maintainer never receives your queries, your downloads or any usage
information.

### What data leaves my machine, and who receives it?

Only the identifiers you ask for, and only to the service being asked. A search
sends your query text to a Library Genesis mirror; a download by DOI sends that
DOI to the article sources in the chain; a download by ISBN sends that ISBN to
OAPEN and the Internet Archive. Every destination is listed under
[Data flows](#data-flows). Nothing is sent to the maintainer, and there are no
background connections — every request is a direct consequence of a tool call.

### Does libgen-mcp store my credentials?

No credentials are required, and none are persisted. The two optional ones — an
Anna's Archive membership key and a free CORE API key — are read from the
environment and sent only to the single service each belongs to. A credential
supplied per call through your client's elicitation prompt is used for that one
request and never written to disk.

### Do the downloaded files stay on my machine?

Yes. Downloads are written only to the local destination directory
(`LIBGEN_MCP_DOWNLOAD_DIR`, default `~/Downloads`, or the per-call `path`
argument) and nothing is uploaded anywhere. The `read` tool extracts text
locally from a file you already have.

## Changes

Changes to this policy are published in this file and noted in release
changelogs.

## Contact

Questions or concerns: [open an issue](https://github.com/jmrplens/libgen-mcp/issues)
or email <mail@jmrp.io>.
