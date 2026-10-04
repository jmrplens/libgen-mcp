# Glossary

**Reference** — for anyone meeting a term these pages use without defining it.

Each term is defined once, in a sentence or two, and links the page where it matters most.
Terms in code font are the literal names a tool argument, a flag or a variable uses.

## Identifiers

### md5

The 32-character hex digest of a file's bytes, and the key the Library Genesis catalog and Anna's
Archive file books under. A search result's `md5` is what `download`, `read` and `get_details`
take for a book, and a download by md5 reports `verified: true` when the saved bytes hash to it.
See
[Tools](tools.md#download).

### DOI

A Digital Object Identifier, such as `10.1038/nature12373`: the persistent name a registration
agency (Crossref for most journals) gives a paper or a book chapter. `download`, `read` and
`get_details` take it as `doi`, and the [source chain](#source-chain) tries the open-access
sources for it first. See [Download a paper by DOI](https://jmrp.io/docs/libgen-mcp/download-a-paper/).

### ISBN

The 10- or 13-character number a book edition is published under, hyphens optional. A search
can match it with `search_in: ["isbn"]`, and `download` takes it as `isbn` to fetch an openly
licensed copy (OAPEN, or a public Internet Archive scan). See [Tools](tools.md#download-input).

### Edition id and file id

The catalog's own numbers for a work's edition and for one file of it, returned in a search
result as `edition_id` and `file_id`. `get_details` takes either as `id`, with `object` set to
`edition` (the default) or `file`. See [Tools](tools.md#get_details-input).

### Record

What `get_details` returns for one identifier: the catalog's file and edition metadata, a
`citations` block when the record has a title, and optional enrichment. When the catalog has no
record, Anna's Archive, Crossref or the DOI's own registry through doi.org may stand in,
labelled by `origin`. See [Tools](tools.md#records-the-catalog-does-not-carry).

### Citation lookup (`citation`)

A reference pasted into `get_details` as free text, in any style, instead of an identifier. It
is resolved to a DOI through Crossref only when the best candidate clearly wins, by score and
by how much of its title the reference contains. Otherwise `citation_match` lists the
candidates and nothing is chosen for you. See [Tools](tools.md#get_details-input).

## Citing

### Confirmed DOI

A DOI that names the work it is attached to: one Crossref confirmed against the record's title,
or one whose record came from Crossref or the DOI's registry in the first place. Only a
confirmed DOI goes into BibTeX and RIS, is sent to doi.org for `cite_as` and is used for
`related`, because a catalog DOI that fails the check belongs to another work. See
[Citations](citations.md#why-a-doi-can-be-missing-from-the-citation).

### Citation styles (`cite_as`)

The extra formats `get_details` can add beside BibTeX and RIS: `apa`, `mla`, `chicago`,
`harvard`, `vancouver`, `ieee` and `csl-json`. Each says which path produced it: `doi.org`
(the DOI's registration agency formatted it), `local` (built from the record's own fields) or
`unavailable`. See [Citations](citations.md#other-citation-styles).

### CSL and CSL-JSON

The Citation Style Language, the open standard reference managers and doi.org use to describe
a citation style. CSL-JSON is its record format, which `cite_as: ["csl-json"]` returns and
tools such as Zotero and pandoc read. See [Citations](citations.md#other-citation-styles).

### Related works (`related`)

The references a record cites, or the works citing it, listed by `get_details` from OpenAlex
with `related: "references"` or `"cited_by"`, most cited first. It needs a confirmed DOI. See
[Tools](tools.md#related-works).

## Where results come from

### Catalog

The Library Genesis catalog, which every search asks first and, by default, only. It holds
books, papers, comics, magazines and standards, and answers in one round trip. See
[How search works](how-search-works.md).

### Mirror

One host of the Library Genesis mirror family (`libgen.li` by default). The server discovers the
list, caches it for 24 hours, and fails over between mirrors on its own; `LIBGEN_MIRROR` puts one
first. See [Architecture](architecture.md#mirror-discovery).

### Collection

One of the catalog's divisions, chosen with `search`'s `topics` argument: `nonfiction`,
`fiction`, `articles`, `magazines`, `comics`, `standards` and `fiction_rus`. Omitting it searches
all of them. See [Tools](tools.md#search-input).

### Shadow library

A collection that distributes copyrighted books and papers without the rightsholders'
permission. Library Genesis, Anna's Archive and Sci-Hub are shadow libraries; arXiv, Crossref
and OpenLibrary are not. See [Responsible use](https://jmrp.io/docs/libgen-mcp/responsible-use/).

### Open access

Work its rightsholder has made free to read, usually under a Creative Commons licence. Only an
open-access result is marked `open_access: true`, and even then a publisher may refuse an
automated fetch. See [How search works](how-search-works.md#what-comes-back-origins-and-downloads).

### Provider

A searcher beyond the catalog that a search can consult: Anna's Archive, arXiv, OpenAlex,
Europe PMC, Crossref, OpenLibrary, Project Gutenberg, dblp, PubMed and ERIC. Each is best-effort, so a slow or failing
provider leaves the others' results intact. See
[How search works](how-search-works.md#reaching-beyond-the-catalog).

### Extra sources (`extra_sources`)

The setting that decides when a search consults the providers: `auto` (the default) only when
the catalog finds nothing or fails, `always` on every search and at the same time as the
catalog, `never` not at all. `LIBGEN_MCP_EXTRA_SOURCES=never` is a lock a call cannot lift. See
[Configuration](configuration.md#libgen_mcp_extra_sources).

### Origin

The label on every result naming the searcher that produced it (`libgen`, `annas`, `crossref`,
`openalex`, `europepmc`, `arxiv`, `openlibrary`, `gutenberg`, `dblp`, `pubmed`, `eric`). It tells you which identifier
the result carries, and so which argument the next call takes. See
[How search works](how-search-works.md#what-comes-back-origins-and-downloads).

### Year range (`year_from`, `year_to`)

The inclusive bounds a search keeps to, either side open. Most providers take the range in
their own query. The catalog cannot, so its page is filtered after it arrives and
`year_filtered` counts what that page lost, undated records included. See
[How search works](how-search-works.md#narrowing-by-year).

### OpenAlex credits

The allowance OpenAlex meters its API in: 1000 a day per address without a key, reset at
midnight UTC, where a search costs 10, a filtered list 1 and a single-record lookup nothing.
Every OpenAlex request this server makes draws on the same allowance, and keyless the search
provider stops short of a reserve kept for `related`. `LIBGEN_MCP_OPENALEX_KEY` moves it onto
the key's larger allowance. See [Configuration](configuration.md#libgen_mcp_openalex_key).

### Pacing

The rate each provider is asked at, kept for the whole process rather than per search: arXiv
once every 3 seconds, dblp once every 10, most others once a second. A provider whose next
request is more than a second away sits the search out instead of delaying it. See
[How search works](how-search-works.md#when-a-source-sits-a-search-out).

### Refusal window

The fifteen minutes a provider is left alone after it refuses to answer: dblp's bot check or
rate limit, an Anna's Archive mirror's browser challenge. The server logs the refusal once and
the provider returns nothing until the window ends. See
[How search works](how-search-works.md#when-a-source-sits-a-search-out).

## Downloading

### Source

One place `download` can fetch a file from, such as `unpaywall`, `scihub` or `libgen`. Each
source says which identifiers it supports; `download`'s `source` argument restricts a call to
one of them. See [Download sources](sources.md).

### Source chain

The fixed order in which `download` offers an item to the sources that support it: open-access
sources first for a DOI, then the article fallbacks, and `libgen`, `randombook` and `annas` for
an md5. `LIBGEN_MCP_SOURCES` removes sources without reordering them. See
[Architecture](architecture.md#multi-source-chain).

### Failover

Moving on to the next mirror or the next source when one fails, within the same call, without
the caller doing anything. A failed download reports every source it tried and why. See
[Architecture](architecture.md#failover-retry-and-cooldown).

### Cooldown

A pause on something that just proved unreachable: 45 seconds for a mirror, 5 minutes for a
source. A source that answered "I do not hold this item" is never set aside, because that says
nothing about its health. See [Architecture](architecture.md#per-source-cooldown).

### PMC Article Datasets

NCBI's copy of the PubMed Central open-access articles, published on AWS Open Data for
automated reuse. The `europepmc` source checks with Europe PMC that an article is open access
and not retracted, then takes the PDF from this dataset, because Europe PMC's own PDF links
answer automated clients with a challenge page. See [Download sources](sources.md#europepmc).

### Resolve only (`resolve_only`)

The `download` mode that returns the direct link, with any headers it needs, instead of saving
the file. A remote server always answers this way, because the file should travel over your
connection, not the server's. See [Tools](tools.md#where-the-file-goes-local-vs-remote).

## Reading

### Text layer

The text a PDF carries alongside its page images. `read` extracts it with no OCR, so a scanned
book with no text layer comes back `extractable: false` with a reason. See
[Tools](tools.md#not-extractable).

### Cursor

The opaque token a `read` response returns when more remains. Passing it back fetches the next
chunk of text, or the next page of matches for a `find`. See
[Tools](tools.md#pagination-and-the-cache).

### Outline

A document's table of contents, from PDF bookmarks or EPUB navigation, returned by `read` with
`outline: true`. Each entry carries a number, `index`, and `max_depth` trims it to the levels
you need without renumbering. See [Tools](tools.md#table-of-contents).

### Section (`section`)

One outline entry read on its own, named by its `index` or by its title: from where it starts
to where the next entry at the same or a higher level starts. A PDF section ends on a whole
page, so it may include the start of the next one. See [Tools](tools.md#read-one-section).

### Untrusted text

Everything a result quotes from a third party: titles, descriptions, extracted text, matches.
It is data, never instructions, and the server escapes it so it cannot reshape the Markdown it
lands in. See [Security model](security.md).

## Keys and interaction

### Keyless

Working with no account, API key or token. Search, details and downloads all have a keyless
path. A credential only adds something: the `unpaywall` source (a contact email), the `core`
source (an API key), Anna's Archive's member downloads (a member key) and a larger OpenAlex
allowance (an API key). See
[Configuration](configuration.md).

### Per-call key

A credential the client supplies for one request, used for that request only and never stored:
an Unpaywall contact email or an Anna's Archive member key, asked for during a `download` when
the server has none configured. See [Tools](tools.md#interactive-prompts-elicitation).

### Elicitation

The MCP mechanism a server uses to ask the person at the client for input mid-call. `download`
uses it to confirm a file it is about to save or to ask for a per-call key, and only with a
client that supports it. See [Tools](tools.md#interactive-prompts-elicitation).

### Prompt

A ready-made request template the server publishes beside its tools: `acquire_book`,
`research_topic`, `get_paper` and `download_troubleshoot`. Many clients list them as commands
the person can pick. See [Tools](tools.md#prompts).

## Transports and deployment

### stdio

The transport where a client starts the server as a child process and talks to it over
standard input and output. It is the default, the full server, and a file `download` saves goes
to your disk. See [Connect a client](clients.md).

### Streamable HTTP

The MCP transport over HTTP: a client POSTs JSON-RPC to one endpoint and may get a stream back.
`--http` starts it, on a TCP address or a unix socket. See [HTTP server mode](http-server-mode.md).

### Stateless and stateful

Stateless, the default over HTTP, serves every POST on its own, with no session to keep and no
`Mcp-Session-Id`, and it is the only shape that can serve protocol `2026-07-28`. Stateful
(`--stateless=false`) keeps a session per client, closed after `--session-timeout`. See
[Architecture](architecture.md#stateless-mode).

### Local and remote server

A local server runs on your machine over stdio and saves files there. A remote one (`--http`,
or `LIBGEN_MCP_REMOTE_DOWNLOADS=1` on a stdio server whose disk you cannot reach) returns links
instead, and leaves `read` unregistered unless the operator sets `LIBGEN_MCP_SERVER_FETCH`. See [Tools](tools.md#read-is-not-on-every-deployment).

### Hosted endpoint

The public remote server at `https://mcp.jmrp.io/libgen`, which needs nothing installed. See
[Hosted endpoint](hosted.md).

### Server card

A JSON document describing the server, served without connecting. An HTTP deployment publishes
two: the discovery card at `/server-card` and the enumerating card at
`/.well-known/mcp/server-card.json`. See
[HTTP server mode](http-server-mode.md#two-cards-and-which-one-a-scanner-wants).

### Charged address

The address a request counts against for the per-caller limits: the connection's peer, or the
one a trusted proxy's header names when `--trusted-proxies` says to believe it. See
[HTTP server mode](http-server-mode.md#what-identity-this-deployment-has).

### Held call

A call the process is holding open, mostly waiting for the outbound budget. An HTTP process holds
at most a number sized from its descriptor limit, and refuses past it with `503`. See
[HTTP server mode](http-server-mode.md#what-the-whole-process-may-hold).

### Outbound budget

The one token bucket every catalog request to a mirror and every download waits on,
`LIBGEN_MCP_RATE_RPS` (one a second by default). The providers are paced on buckets of their
own (see [Pacing](#pacing)). It is shared by every caller of a process, which is why an inbound limit
above it only moves the queue. See [Scaling and capacity](deploy/scaling.md).

### Drain

The handover on shutdown: `/health` turns `503`, the listener stays open for `--drain-delay`,
and calls in flight get a bounded time to finish. See
[HTTP server mode](http-server-mode.md#draining-before-the-listener-closes).

## Containment

### Path roots

The directories a caller-supplied local path may resolve into: the working directory, the OS temp
directory and the download directory, plus `LIBGEN_MCP_ALLOWED_READ_DIRS` for `read` and
`LIBGEN_MCP_ALLOWED_DOWNLOAD_DIRS` for `download`. Symlinks are resolved before the check. See
[Configuration](configuration.md#reference).

### Address tiers

The two rules the outbound guard applies to a destination: the private-address tier, which
refuses loopback, private and link-local addresses, and the metadata tier, which refuses cloud
metadata endpoints whatever else is allowed. A host the operator named is exempt from the first,
never the second. See [Architecture](architecture.md#outbound-address-policy).
