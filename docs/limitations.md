# Known limitations

**Explanation** — for anyone deciding whether the server fits a job, or wondering why it did
not do something.

**libgen-mcp, a Model Context Protocol server for Library Genesis, has no OCR, hash-checks
only downloads asked for by md5, cannot search Anna's Archive behind its browser challenge,
and on a remote deployment returns links instead of files and offers no `read` by default.**
Each limitation below says what it is, why, and where it is documented in full.

Where the project's design records say whether a limitation is a deliberate decision or open
follow-up work, the entry says so too. Those records are the architecture decision records
in [`decisions/`](decisions/).

## Reading documents

**There is no OCR.** `read` extracts text in pure Go from a PDF's text layer, an EPUB or a
TXT file. A scanned or image-only PDF has no text layer, so `read` returns
`extractable: false` with the reason
`no extractable text layer (likely a scanned or image-only PDF); OCR is not supported`, and
the model can fall back to `download` for the raw file. This is a deliberate decision: the
usable OCR engines need either CGO (Tesseract) or a keyed cloud service, and either would
break the single static binary and the keyless default, as the
[source-and-capability-scope ADR](decisions/2026-07-22-source-and-capability-scope.md)
records. See [Tools → Not extractable](tools.md#not-extractable).

**Three formats are read, and the rest are declined.** PDF, EPUB and TXT are extracted.
DjVu, the comic archives CBR and CBZ, and the MOBI, AZW and AZW3 e-book formats are reported
as unsupported, with the same reason in all three read modes (sequential text, `find` and
`outline`), so a failure in one mode is not an invitation to retry in another. See
[Tools → Not extractable](tools.md#not-extractable).

**TXT and EPUB text is capped at 8 MiB.** A TXT file is read up to its first 8 MiB, and each
chapter document inside an EPUB up to 8 MiB, so extraction stays within bounded memory. Text
beyond the cap cannot be reached by paging with `offset` or `cursor`, and `find` searches
only the same capped text. A sequential read says when this happened: `truncated` is set and
`reason` reads `document exceeds the 8 MiB extraction cap; text beyond it is not available`.
A `find` result does not flag it. Seek-based streaming past the cap is recorded as follow-up
work, not implemented, in the
[known-limitations ADR](decisions/2026-07-22-known-limitations.md).

**Some text layers extract to nonsense.** A PDF whose fonts carry no usable character map
extracts without error, but into characters that are not the ones printed on the page.
`read` cannot recover the real text. It measures the damage heuristically and sets
`text_quality_note`, and because the thresholds are conservative, the note is a strong hint
rather than a verdict. See
[Tools → A text layer that extracts to nonsense](tools.md#a-text-layer-that-extracts-to-nonsense).

**Each read has a 90-second budget.** A damaged or pathologically structured document that
no parser finishes within it is reported as unreadable instead of holding the call open. Go
cannot stop a goroutine, so an abandoned read keeps running, and once four are outstanding
the server declines to start another until they return. See
[Tools → Not extractable](tools.md#not-extractable).

**The first page costs the whole file, and the cache is swept only on insertion.** To return
one chunk of a book or paper named by `md5` or `doi`, `read` downloads the whole file to a
server-side temp file, then keeps it so later pages reuse that one fetch
(`LIBGEN_MCP_READ_CACHE_TTL`, default 10 minutes idle, and `LIBGEN_MCP_READ_CACHE_BYTES`,
default 512 MiB in total). Expired entries are removed only when a new file enters the
cache, not on a timer, so an idle file can stay on disk past its TTL until the next `read`
fetches something new. The
[known-limitations ADR](decisions/2026-07-22-known-limitations.md) accepts this and records
a periodic sweep as follow-up work. See
[Configuration](configuration.md#libgen_mcp_read_cache_bytes-and-libgen_mcp_read_cache_ttl).

## Downloads and verification

**Only a download by md5 is hash-checked.** For a book asked for by `md5`, the streamed
bytes are hashed and compared with the requested digest, and a mismatch deletes the partial
file. A `doi` or `isbn` download carries no digest to compare against, so its result reports
`verified: false`, including for books from OAPEN and the Internet Archive. Nothing then
proves the file is the work requested, which is why its default name keeps what the source
announced instead of the record's title. When `download` returns a link rather than a file,
the server never sees the bytes, and the `verify_md5` flag in the `resolved` object leaves
the check to whoever fetches them. See
[Tools → Behavior and errors](tools.md#behavior-and-errors) and
[How the saved file is named](tools.md#how-the-saved-file-is-named).

**A book asked for by md5 comes from a shadow library.** Only `libgen`, `randombook` and
`annas` are keyed by md5, and all three are shadow libraries, so for such a book the chain
has no legal source to try first and Library Genesis is the first one it meets. The
open-access-first order applies to articles asked for by DOI and to books asked for by ISBN.
See
[Responsible use](responsible-use.md#what-does-libgen-mcp-do-to-prefer-legally-free-sources)
and [Sources → Shadow-library fallbacks](sources.md#shadow-library-fallbacks).

**A book asked for by ISBN must be openly licensed.** A `download` by `isbn` tries OAPEN and
then public-domain Internet Archive scans, and nothing else: there is no shadow-library
fallback on the ISBN path, and a lending-restricted Archive scan is refused. An in-copyright
trade book asked for by ISBN is therefore reported as a miss, and the route to it is a
catalog `search` followed by a download by `md5`. This is by design, as
[Responsible use](responsible-use.md) explains. See
[Troubleshooting → Book not found by ISBN](troubleshooting.md#book-not-found-by-isbn-open-access-only).

**The chain can be narrowed, not reordered.** `LIBGEN_MCP_SOURCES` removes sources from the
download chain but cannot change their order, which is fixed in code and pinned by a test,
so no deployment can promote a shadow-library fallback above the open-access providers. This
is deliberate. See
[Configuration → `LIBGEN_MCP_SOURCES`](configuration.md#libgen_mcp_sources-and-the-source-chain).

**The result does not say which source served the file.** `download` returns the saved path
and size, and records the serving source and mirror only in the server log. A caller who
needs to know pins `source`, which makes that source the entire chain for the call. On the
link path the `resolved` object does name its source, because the URL already identifies the
provider. This is deliberate: the result reveals only what the call revealed, as the
[result-disclosure ADR](decisions/2026-08-08-result-reveals-only-what-the-call-revealed.md)
records. See [Tools → What the result withholds](tools.md#what-the-result-withholds).

## Sources and availability

**Every source is a service this project does not run.** libgen-mcp hosts no content and
operates no mirror. The Library Genesis mirror list is discovered from a public directory
and cached for 24 hours, Sci-Hub hosts rotate (which is what `LIBGEN_MCP_SCIHUB_HOSTS` is
for), and any provider can change its pages or go down. A mirror that fails is set aside and
the next one is tried, but when every mirror fails the call fails with an error that says
so. See [Architecture → Mirror discovery](architecture.md#mirror-discovery) and
[Troubleshooting → All mirrors unreachable](troubleshooting.md#all-mirrors-unreachable).

**Anna's Archive is behind a browser challenge: no search, and downloads only with a key.**
As measured on 2026-09-22, Anna's Archive answers its search page and its per-item `/md5/`
page with a DDoS-Guard browser challenge, which needs a JavaScript runtime to pass. While it
stands:

- the Anna's Archive search provider returns nothing, logs a warning, and asks nothing more
  for fifteen minutes;
- the keyless download path resolves nothing, because it takes the item's IPFS address from
  the `/md5/` page and no gateway request can start without it;
- `get_details` cannot fall back to Anna's own metadata for a record the catalog lacks,
  because it reads the same page;
- the member API is not behind the challenge, so with `LIBGEN_MCP_ANNAS_KEY` set to a
  paid-membership key, downloads through it keep working.

Defeating the challenge is out of scope by decision: it would mean running Anna's anti-bot
code to impersonate a browser. See [Sources → `annas`](sources.md#annas).

**dblp's search API is behind a bot check, so dblp contributes no search results.** As
measured on 2026-10-03, dblp.org and its two mirrors answer the search API with an Anubis
"Making sure you're not a bot!" page (HTTP 200, HTML in place of the requested JSON), and
sometimes with a 429. The block was seen from a consumer ISP address, so it is not reserved
for datacenter ranges. While it stands, the dblp provider returns nothing, logs the reason
once at INFO, and asks nothing more for fifteen minutes. The other providers are unaffected,
and Crossref indexes most of the same computer-science papers by DOI. Passing the
check would mean running its proof of work as a browser does, which is out of scope for the
same reason as Anna's. See [How search works](how-search-works.md).

**Some DOIs are reachable by no source.** The chain reaches only what its sources hold and
will serve to an automated client. SciELO's oldest articles are HTML-only, and no source
reaches the 1998 article the sources page names. Of fourteen SciELO DOIs sampled, three were
served by scielo.br and by no other source in the chain, and as measured on 2026-08-21,
scielo.br's article pages answer automated clients with a browser check. More generally,
Unpaywall can mark an article open access without offering a file, and the large commercial
publishers refuse the link they deposit with Crossref to an anonymous client. See
[Sources → `scielo`](sources.md#scielo) and
[Troubleshooting → Article not found](troubleshooting.md#article-not-found-open-access-vs-sci-hub).

**Two open-access sources need a credential.** `unpaywall` joins the chain only when
`LIBGEN_MCP_UNPAYWALL_EMAIL` is set, because its API requires a contact address, and `core`
only when `LIBGEN_MCP_CORE_KEY` holds a free CORE API key. A client that supports
elicitation can be asked for an Unpaywall address for a single request. `openalex` reads the
same open-access index as Unpaywall with no credential, so a deployment with neither still
has a keyless route into it. See [Sources](sources.md).

**Project Gutenberg, ERIC and arXiv hits are fetched directly, not through `download`.**
Gutenberg and ERIC reach the caller through `search` only and have no download source, by
decision. A Gutenberg text has no DOI and no reliable ISBN, so a download source could only
match on title and author, which risks serving a different book. ERIC keys its records by an
accession number and already puts the full-text URL in the hit. A Gutenberg hit carries a
`full_text_url`, and an arXiv hit or an ERIC hit with a hosted full text carries a
`pdf_url`, for the client to fetch. `download` takes no arXiv identifier. See
[Sources → What is deliberately not a download source](sources.md#what-is-deliberately-not-a-download-source).

**Search results are capped by the mirror.** A Library Genesis mirror reports a match count,
`total_files`, sometimes only as `1000+`, but serves just the first `reachable` results
across pages, and pages beyond that are empty. The result then sets `truncated` and a
`hint`, and the remedy is a narrower query rather than deeper paging. See
[Tools → Pagination and truncation](tools.md#pagination-and-truncation).

## Remote deployments

**`download` returns a link, not a file.** A server started with `--http`, or with
`LIBGEN_MCP_REMOTE_DOWNLOADS=1`, cannot write to the client's disk, and MCP has no way for a
tool to push a file's bytes to the client. Every `download` there returns the resolved URL
as a `resource_link` plus a `resolved` object, and the client's own HTTP tool fetches it.
See [Tools → Where the file goes](tools.md#where-the-file-goes-local-vs-remote).

**`read` is not offered by default.** To return one page of text, `read` pulls the whole
file over the server's connection, and on a hosted deployment that connection is an egress
address shared by every caller: one caller's transfers could get it throttled or blocked for
all of them. So `LIBGEN_MCP_SERVER_FETCH` defaults to off on a remote deployment and `read`
is not registered at all. An operator can set it to `1` to accept the egress cost, and even
then `read`'s `path` argument is refused, because the server cannot see the client's disk.
This is deliberate, as the
[hosted-fetch ADR](decisions/2026-09-08-a-hosted-server-does-not-fetch-file-bodies.md)
records. See
[Tools → read is not on every deployment](tools.md#read-is-not-on-every-deployment).

**The HTTP transport has no authentication.** The server takes no credential from its
callers: any client that can reach the endpoint can call every tool, and the server card
declares no authentication scheme. What the server bounds is per caller, by address: an
inbound rate limit and a ceiling on downloads and reads in flight. Above those, the whole
process holds no more calls and stateful sessions than its descriptor limit allows (75 and
37 under a hard limit of 1024) and answers the next one "busy", which keeps the process from
running out of descriptors but does not tell callers apart. Access control, where a
deployment needs it, belongs to whatever sits in front of the server, such as one of the
proxies in [Behind a reverse proxy](deploy/reverse-proxy.md). See
[Architecture → Stateless mode](architecture.md#stateless-mode) and
[HTTP server mode](http-server-mode.md).

## Throughput

**One outbound request per second, by default.** Every request to a Library Genesis mirror
and every file download waits for a token from a single bucket shared by the whole process,
sized by `LIBGEN_MCP_RATE_RPS` (default 1, at most 20) and `LIBGEN_MCP_RATE_BURST` (default
1). Concurrent calls queue behind one another instead of running side by side, and on an
HTTP deployment every caller draws on that same bucket, so an inbound limit set above it
moves the queue rather than shortening it. A lower rate is gentler on the mirrors, and a
higher one speeds up bursts. See
the `LIBGEN_MCP_RATE_RPS` row in [Configuration](configuration.md) and
[HTTP server mode → What one caller may ask for](http-server-mode.md#what-one-caller-may-ask-for).

**A failing download can take a minute to move on.** Getting a transfer to start is retried
on a staged schedule, `LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS`, by default eight attempts
over about 60 seconds, before the chain tries the next source. A source that answers with a
clean miss or a refusal skips the schedule, but a transient failure spends it, so a download
can take about a minute to start, or to fail over. See
[Architecture → Start-retries and the stall guard](architecture.md#start-retries-and-the-stall-guard).

## Frequently asked questions

### Can libgen-mcp read scanned PDFs?

No. `read` extracts only a PDF's existing text layer, and a scanned or image-only PDF has
none, so it answers `extractable: false` and says OCR is not supported. OCR is out of scope
by decision, because the usable engines need CGO or a keyed cloud service, which would break
the static binary and the keyless default.

`download` still delivers the file itself, so it can be put through OCR elsewhere. See
[Tools → Not extractable](tools.md#not-extractable).

### Why is my DOI download not verified?

Because there is nothing to check it against. libgen-mcp verifies a download by hashing its
bytes and comparing them with the requested md5, and only a book asked for by md5 has one. A
DOI or ISBN download carries no digest, so its result reports `verified: false`.

Nothing then proves the file is the work you asked for, which is why its default name keeps
what the source announced rather than the record's title. Check the file itself before
relying on it. See [Tools → Behavior and errors](tools.md#behavior-and-errors).

### Does libgen-mcp work with Anna's Archive?

Partly. As measured on 2026-09-22, Anna's Archive answers its search and per-item pages with
a DDoS-Guard browser challenge, so search finds nothing there and the keyless IPFS download
cannot start. With a paid membership key in `LIBGEN_MCP_ANNAS_KEY`, its member API still
serves downloads.

Defeating the challenge is out of scope, because it would mean running Anna's anti-bot code
to impersonate a browser. After a challenge the search provider asks nothing for fifteen
minutes rather than asking again on every search. See [Sources → `annas`](sources.md#annas).
