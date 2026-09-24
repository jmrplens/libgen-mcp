# Privacy Policy

Last updated: 2026-09-24

**libgen-mcp** is a Model Context Protocol (MCP) server you run yourself. In its
normal use it runs entirely on your machine and acts as a bridge between your MCP
client (Claude Desktop, Claude Code, Cursor, VS Code, …) and the public Library
Genesis mirrors. It needs **no account, no token, and no credentials**. This
policy describes what data the software handles and where it goes.

One path is different and is described separately below: the public hosted
endpoint at `mcp.jmrp.io/libgen`, where the software runs on someone else's
machine rather than yours. See [Hosted endpoint](#hosted-endpoint).

## What we collect

**Nothing.** The server sends no telemetry unless you turn it on, and even then
only to a collector you run (see below). It has no analytics, no crash reporting,
and no backend of its own. There is no account to create and nothing to log in to.
When you run it yourself — which is how this documentation recommends using it —
the maintainer never receives, stores, or has access to any of your data or usage
information, because nothing is ever sent anywhere that the maintainer controls.

That last sentence is a statement about the software, and it holds wherever you
run it. It is not a statement about the [hosted endpoint](#hosted-endpoint),
where the same software runs on a machine the maintainer operates.

### OpenTelemetry, if you turn it on

The server can export traces, metrics and logs, and this section exists so the
paragraph above stays exactly true rather than becoming a technicality.

It is **off by default** (`LIBGEN_MCP_TELEMETRY`). When you enable it, the
telemetry goes to a collector **you** configure and run. There is no path by
which it could reach the maintainer: the only default the exporters have is
`https://localhost:4318` — your own machine, where the export fails and says so in
your own server log unless you are running a collector there. Nothing in any code
path carries anyone else's address. Turning it on is a decision you make about
your own deployment and the people using it.

**What it records describes operations, never their contents:** the method
called, the tool named, whether it succeeded, how long it took, which download
source served a file and which mirror host it came from. **What somebody searched
for is excluded by design and by no setting** — there is no value of any variable
that puts a search query or a record title into an exported signal, because there
is no operator for whom a collector holding "what this person looked for" is the
right outcome. Tool arguments, tool results and any credential supplied for a
single call are excluded on the same terms.

The identifier of the item a call was about (an md5, a DOI, an ISBN) is exported
as a keyed digest rather than literally, so that "this one book fails on every
source" stays distinguishable from "every download is failing" without naming the
book. The literal identifier is exported only under the full identity policy
below.

**Who made a call is recorded only if you ask**, through
`LIBGEN_MCP_TELEMETRY_IDENTITY`. The default, `none`, records nothing about the
caller. `pseudonymous` records a keyed digest of the address the request was
charged to, which tells one caller's traffic from another's while naming nobody.
`full` records that address and the client's own name and version, which is for
an operator running this for a known group of people on their own collector — on
a public endpoint an address is personal data, which is why it is neither the
default nor a step anything takes for you.

The full detail, including what each signal carries and the four traps in the
standard `OTEL_*` variables, is in the
[telemetry guide](https://jmrp.io/docs/libgen-mcp/telemetry/).

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
  [SciELO](https://www.scielo.br) for a `10.1590` DOI (the request goes to
  `doi.org`, whose redirect leads to `www.scielo.br`), the
  [FAO Knowledge Repository](https://openknowledge.fao.org)
  (`openknowledge.fao.org`) for a `10.4060` DOI,
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
  The server creates no database and no telemetry file. With
  [OpenTelemetry](#opentelemetry-if-you-turn-it-on) enabled, records above an
  INFO floor are also sent to the collector you configured — still nothing on
  disk, and still nowhere the maintainer can reach.
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

## Hosted endpoint

A public instance of this server is hosted at `https://mcp.jmrp.io/libgen`. Using
it is optional and is never the default: nothing installs it, and no
configuration in this repository points at it.

What changes when you use it is simple and worth stating plainly. Your tool calls
— the titles, authors, DOIs and identifiers you search for — are sent over the
network to a machine operated by this project's maintainer, instead of staying on
your own. The requests to Library Genesis and to the open-access sources are then
made by that machine rather than by yours, so those third parties see its address
instead of yours.

That machine may also be running the server's own
[OpenTelemetry export](#opentelemetry-if-you-turn-it-on) to a collector its
operator controls, which is the one thing about it this policy can usefully say:
the software's default is off, and whether a deployment you did not configure has
turned it on, and under which identity policy, is a question for the person
operating it. The instance's own card at `GET /.well-known/mcp/server-card.json`
publishes the answer — whether telemetry is on, what each enabled signal records
and what it records about callers — so it can be read rather than asked for.

That instance is operated as part of [mcp.jmrp.io](https://mcp.jmrp.io/) and its
handling of requests is governed by [that service's own policies](https://mcp.jmrp.io/policies/),
not by this one, which describes the software. This document can only tell you what the software does; it cannot make
promises on behalf of a server you are not running.

If what you search for is sensitive to you, run the server locally. That is the
whole of the advice, and it is why every install path in the documentation leads
there first.

## Data retention and sharing

The only things the server leaves behind after it exits are the files described
under [Local storage and downloads](#local-storage-and-downloads): what you asked
it to download, the 24-hour mirror cache, and any temporary `read` files not yet
evicted. None of them records a query or an identifier of yours except the names
of the files you chose to fetch. It shares data with no third parties beyond the
destinations listed under [Data flows](#data-flows) — the Library Genesis
mirrors, the extra searchers a `search` may reach, and the article and book
download sources you invoke. With
[OpenTelemetry](#opentelemetry-if-you-turn-it-on) enabled, a fourth destination
exists and it is one you named: the collector you configured, whose retention is
yours to set.

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible
for respecting the copyright and intellectual-property laws that apply where you
live. Use it only for content you are legally entitled to access.

## Frequently asked questions

### Does libgen-mcp collect any telemetry or analytics?

Not unless you turn it on, and never to the maintainer. There is no analytics, no
crash reporting and no backend of its own; the server creates no database and no
telemetry file, and logs to standard error, where your MCP client collects them if
it collects them at all. The maintainer never receives your queries, your
downloads or any usage information, whatever you configure.

`LIBGEN_MCP_TELEMETRY` lets **you** export OpenTelemetry traces, metrics and logs
to a collector **you** run; it is off by default, the exporters' only default
destination is your own `localhost`, and what they carry describes operations
rather than what was searched for. See
[OpenTelemetry, if you turn it on](#opentelemetry-if-you-turn-it-on).

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
