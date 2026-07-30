# Source and capability scope

Status: accepted · Date: 2026-07-22

Decision record for how libgen-mcp grows beyond search / get_details / download / read,
based on a fresh evaluation (MCP spec + Go SDK state, the keyless-source landscape, and
the competitive/adoption picture). It supersedes the earlier "second source / async tasks"
evaluation note.

## Context

libgen-mcp is a pure-Go MCP server: one static `CGO_ENABLED=0` binary, no account/key/token,
tight context footprint, stdio + streamable HTTP. It already out-engineers comparable servers
on download robustness (mirror failover, resume, MD5 verification, multi-source). Its real gap
is mindshare, and the highest-traction adjacent category is **keyless open-access discovery**,
not more download hardening.

Guiding constraints (unchanged): pure-Go static binary (no CGO), permissive licenses only
(no AGPL), keyless (no API key / account / login / scraping-behind-auth), MCP-spec compatible,
few generic tools over many narrow ones, lead every output with `next_steps`, treat all fetched
content as untrusted.

## Decisions

### 1. Stay on go-sdk v1.6.1 / spec 2025-11-25; MCP Tasks — NO-GO

`go-sdk v1.6.1` is the current **stable** release and targets the current **stable** spec
(2025-11-25). We do not pin pre-release SDKs. MCP **Tasks** stays rejected, and the case is now
stronger: Tasks is experimental in 2025-11-25 and is being **redesigned into an extension** in
the 2026-07-28 release candidate (early adopters must migrate), and the stable go-sdk has no
Tasks API. libgen operations are sub-second except downloads, which already stream
`notifications/progress`.

Revisit only when BOTH hold: (a) the Tasks extension is final in a shipped spec AND available in
a **stable** go-sdk release; and (b) libgen-mcp grows a genuinely detached long-running operation
(e.g. batch/bulk download, long crawl) where a client should fire-and-poll rather than hold the
call open. Until then, progress notifications are the correct fit.

### 2. Anna's Archive as a source — NO-GO (re-confirmed 2026)

> **Superseded in part on 2026-07-24 — see the correction below. Anna's Archive is now
> integrated as two sources, `scidb` and `annas`. Do not act on the paragraph below
> without reading the correction.**

Every route is off-ethos: the JSON API and fast downloads require a paid membership key, and the
web / SciDB / slow-download paths are Cloudflare/CAPTCHA-gated. There is no dependable keyless,
no-account programmatic path. High corpus overlap with what we already reach. Rejected.

#### Correction — 2026-07-24

The blanket rejection above was too broad. Live testing showed each route resolves
differently, so the decision is corrected route by route rather than reversed wholesale.

**Wrong — SciDB.** SciDB is reachable anonymously with no API key, no account, no CAPTCHA
and no JS challenge, and its article pages embed a direct PDF URL that serves real bytes.
Sampled DOIs from 2011, 2016, 2021 and 2024 all resolved, so it also covers papers
published after Sci-Hub stopped indexing. Implemented as the `scidb` source, placed after
`scihub` so it fills that gap rather than replacing it.

**Wrong — general books, keyless.** A keyless path does exist, but it is IPFS rather than
HTTP: Anna's book pages serve anonymously and publish each item's IPFS CID, and public
gateways return the file with range support. Implemented as the keyless default of the
`annas` source. Caveat learned in testing: public gateway availability varies enough that
this is a genuine fallback, not a fast path — arbitrary items can be very slow or time out.

**Right — general books over HTTP.** The anonymous "slow download" tier sits behind a
DDoS-Guard JS challenge (HTTP 403, "Checking your browser") that no pure-Go HTTP client can
satisfy. The original reasoning holds for this route specifically, and it is deliberately
not implemented.

**Refined — the membership key.** The member fast-download API is usable from a plain Go
client and returns a direct URL plus the account's remaining daily quota, but it does
require an *active paid membership*. It therefore ships strictly as an opt-in enhancement
behind `LIBGEN_MCP_ANNAS_KEY`, never as a requirement: unset, expired or rejected keys fall
through to the keyless IPFS path, so the project's keyless ethos is preserved.

**Still true — corpus overlap.** Anna's non-IPFS external links point largely back at the
libgen family this project already reaches, so the value added is download *reliability* —
an independent rescue route — rather than new corpus.

### 3. Expand `search` to federate keyless open-access discovery — GO

Reposition from "Library Genesis search" to "one keyless static binary that searches Library
Genesis **and** the open-access literature." Fold open-access discovery into the existing
`search` tool (not per-source tools) to keep the surface small and the context footprint tight;
open-access hits are directly `read`-able and `enrich`-able, strengthening the find → read → cite
loop already built.

Sources evaluated:

| Source                          | Keyless (2026)                    | Role                                                                         | Verdict                                                                                      |
| ------------------------------- | --------------------------------- | ---------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| **arXiv** export API            | Yes                               | OA preprint search + direct PDF (`arxiv.org/pdf/{id}`)                       | **GO** — real OA full-text libgen lags on; Atom XML + 3s courtesy delay + attribution string |
| **Crossref** REST               | Yes (already used for enrichment) | DOI/work search, `has-full-text`/license filters                             | **GO** — extend existing integration into discovery                                          |
| **OpenLibrary** `search.json`   | Yes                               | Query resolver: fuzzy title/author → ISBN/OCLC/work id feeding libgen search | **GO (resolver only, not a download stream)**                                                |
| DOAJ                            | Yes                               | OA article/journal search                                                    | MAYBE — overlaps Unpaywall/Crossref; defer                                                   |
| DOAB                            | Yes                               | OA scholarly books (DSpace REST)                                             | MAYBE — endpoint fragility; defer                                                            |
| Internet Archive                | Yes                               | Scans, public-domain, lending                                                | MAYBE — noisy; per-item availability varies; defer                                           |
| Gutendex / Project Gutenberg    | Yes                               | Public-domain classics                                                       | MAYBE — narrow; libgen already carries most; defer                                           |
| **OpenAlex**                    | **No (changed 2026-02-13)**       | Rich metadata + OA links                                                     | **NO-GO** — API key now required, polite email pool discontinued                             |
| Semantic Scholar (keyless tier) | Partial                           | Scholarly graph + OA PDF                                                     | NO-GO — keyless shared pool too throttled to depend on                                       |
| CORE                            | No                                | OA aggregator full-text                                                      | NO-GO — registered key required                                                              |

First wave: arXiv + Crossref search + OpenLibrary resolver. The MAYBE rows are gated behind
demand. Federated results must dedup against libgen and be clearly labeled by origin.

#### Amendment — 2026-07-25 (discovery beyond open access)

**Added — dblp and PubMed.** Neither was evaluated above, because the table asked only which
sources supply *open-access full text*. That framing left a gap: for a citation the caller needs
to know a paper exists and how to cite it, which is a different question from whether anyone will
hand over the PDF. Both new sources answer the first question and are explicitly silent on the
second — their hits are never marked open access and never carry a `pdf_url`.

- **dblp** (`dblp.org/search/publ/api`) — keyless, JSON, no documented rate limit (we still pace
  ourselves to 1 rps; the service throttles bursts in practice). It is authoritative on computer
  science venue, year and authorship, which arXiv and Crossref match poorly for conference
  papers, and it supplies the `venue` field for those records.
- **PubMed** (NCBI E-utilities) — keyless within a hard 3 requests/second allowance, so the
  provider is paced to it and sends the `tool` attribution NCBI asks for (plus the contact email
  only when one is already configured; none is ever invented). It covers the whole biomedical
  literature, where the existing `europepmc` source covers only the downloadable open-access
  slice — so PubMed answers for papers no free source carries at all.

Consequence for the output shape: the `search` tool's `open_access` array is now a
beyond-catalog array whose entries carry their own `open_access` flag. The field name is kept for
compatibility, and the tool's description, its next-step guidance and the rendered table all say
that only a flagged entry is known to be free to read.

#### Amendment — 2026-07-25 (ERIC, and a discovery source with no download source)

**Added — ERIC.** The Institute of Education Sciences' index of education research
(`api.ies.ed.gov/eric/`), keyless, JSON, no documented rate limit (paced to 1 rps like dblp). It
was not in the table because the table was framed around scholarly articles, and ERIC's value
here is the corpus that is not one: technical reports, dissertations, conference papers and
government/agency documents — education **grey literature**, which carries no DOI and therefore
appears in neither Crossref, nor Unpaywall, nor Europe PMC, nor any shadow library.

**Decided — discovery only, no `DownloadSource`.** This is the first source deliberately wired
as half an integration, so the reasoning is recorded rather than inferred:

- ERIC is title/keyword-keyed. Its accession number (`ED427241`) is a third key space alongside
  md5 and DOI, so a download source keyed on it would need a new identifier on `libgen.Item`
  **and** a new field on both the `download` and `read` input schemas. Three schema expansions
  for one source is the opposite of "few, general tools".
- There would be nothing for it to do. ERIC serves every full text it holds at
  `https://files.eric.ed.gov/fulltext/<id>.pdf` — derived from the accession number, with no
  lookup step. `DownloadSource` exists to *resolve* an opaque identifier into a URL; here
  `Resolve` would be a string concatenation wrapped in an interface.
- `DiscoveryResult.PDFURL` already carries exactly this affordance, and already has a precedent
  that the tool chain cannot itself fetch: OpenLibrary's `archive_url`. So a hosted ERIC record
  is surfaced with its `pdf_url` filled in, the tool's next-step guidance states that a
  `pdf_url` is fetched directly rather than passed to `download`, and the integration costs zero
  new plumbing.

**Measured — the availability flag, not the id prefix.** Whether ERIC hosts a document is
carried by the record's `e_fulltextauth` field, not by the `ED`/`EJ` prefix of its accession
number. The prefix records how the document entered the index and predicts hosting badly in both
directions: sampled `EJ` records with the flag set serve a real PDF (`EJ1230460`, 145,619 bytes;
`EJ1440134`, 381,600 bytes), and sampled `ED` records without it answer 404 (`ED416196`,
`ED480030`, `ED644068`). Six of six sampled records matched the flag and none matched the prefix
rule, so the provider reads the flag. `open_access` and `pdf_url` are set together and only when
it is 1: ERIC's own copies are publicly funded documents served without a login, while a record
it merely indexes says nothing about the publisher's terms.

**Measured — the query must be escaped.** ERIC parses `search` as a Lucene query and answers an
unparseable one with **HTTP 200 carrying an error object**, so an ordinary title containing a
colon would silently return nothing. Each term is backslash-escaped and the terms are joined with
`AND`; the default operator is `OR`, which for a two-word query returns an order of magnitude
more hits than the caller meant (711k vs 109k for "professional development"). The cost is that
caller-supplied Lucene syntax is escaped into literal terms — the right default when the query is
a title or a topic.

#### Correction — 2026-07-25 (article *download* sources)

The table above evaluated candidates as **discovery** sources for `search`, and its verdicts
still stand for that purpose. Two of its rows have since been overtaken for the **download**
chain, and two sources it never considered now ship. Recorded here rather than edited into the
table, so the original reasoning stays legible.

**Changed — CORE.** Listed "NO-GO — registered key required". The key requirement is real, but
registration is free and the project already had a precedent for a source that is off by
default and switched on by one setting (`unpaywall`, gated on a contact email). CORE therefore
ships as the `core` article source, gated on `LIBGEN_MCP_CORE_KEY` and left out of the chain
entirely when it is unset. The keyless ethos is preserved the same way `unpaywall` preserves
it: nothing about the default configuration changes.

**Changed — Internet Archive.** Listed "MAYBE — noisy; per-item availability varies; defer".
The noise is a property of *browsing* the archive, not of resolving a known DOI against it.
Via Internet Archive Scholar's fatcat catalog a DOI maps to a specific release with a specific
file list, so the per-item variance is visible up front rather than after a fetch. Shipped as
the keyless `fatcat` source.

*Amended 2026-07-25:* the `api.fatcat.wiki` JSON API this originally used stopped answering
(DNS resolves, TCP never completes) with no deprecation notice, so the source now drives
fatcat's own web frontend at `scholar.archive.org` instead — the DOI lookup there redirects to
a release page whose `citation_pdf_url` meta tags name the preserved full-text copies. The
decision is unchanged; only the transport is.

**Added — Europe PMC and bioRxiv/medRxiv.** Neither was evaluated in the table; both were
found afterwards and both are keyless, single-purpose DOI resolvers with no account and no
quota. `europepmc` serves the open-access subset of PubMed Central; `biorxiv` resolves
`10.1101` preprint DOIs to the versioned `.full.pdf`. They lead the article chain alongside
`unpaywall`, so a freely licensed copy is preferred before any shadow-library fallback.

The article chain that results is `unpaywall → europepmc → biorxiv → fatcat → core → scihub →
scidb`: legal open access first, shadow libraries only as fallback.

**Amended 2026-07-25 — the same principle, applied to books (`oapen`, `archive`, and Gutenberg
as a discovery provider).** The original evaluation treated open access as an article-side
concern; the book side stayed md5-keyed shadow libraries plus OpenLibrary as a query resolver,
so there was no legal path to a free full book at all. Three keyless sources close that gap, and
where each landed is the interesting part:

- **`oapen` — a download source keyed by DOI *or* ISBN.** OAPEN hosts the openly licensed
  monographs scholarly publishers deposit, addressable by either identifier, so it is the one new
  source that serves both the book and the article branch of the chain. Its DSpace REST search is
  **free text**: querying an identifier it does not hold still returns a page of unrelated
  monographs (a nonexistent DOI returned 13 hits when this was measured). The source therefore
  re-checks the candidate record's own `oapen.identifier.doi`/`.isbn` before serving anything —
  without that check it would occasionally hand back a *different book* and report success, which
  is worse than any failure mode the chain already has.
- **`archive` — a download source keyed by ISBN, chained off OpenLibrary.** OpenLibrary already
  runs here and already requests `ebook_access` and `ia`, so it is the natural map from an ISBN to
  the Internet Archive scans of a book. A large share of those scans are controlled-digital-lending
  copies that advertise ordinary `.pdf`/`.epub` files and serve a DRM-wrapped or truncated one, so
  the source gates twice — `ebook_access: public` from OpenLibrary **and** no
  `access-restricted-item` / lending collection on the individual archive.org item — and skips a
  candidate that fails either. Both gates are needed: a work can be publicly readable while a
  particular scan of it is restricted.
- **Project Gutenberg — a discovery provider, NOT a download source.** Gutenberg's catalog is
  keyed by an internal ebook id; its texts carry no DOI and no reliable ISBN, and Gutendex (the
  well-used third-party JSON API over the catalog — Gutenberg publishes none itself) exposes
  neither. A `DownloadSource` resolves an identifier the *caller already holds*, so the only way
  to key one on Gutenberg would be matching title and author — and a title match is not an
  identity match, which is precisely the "serve a different book and call it success" failure the
  OAPEN check exists to prevent. It ships instead as a discovery provider whose hits carry a
  `full_text_url` (the EPUB, or plain text) and are filtered to records Gutenberg states are out
  of copyright, so the query-to-file mapping stays visible to the caller. This is deliberate and
  should not be "fixed" later by adding a title-keyed download source.

**Keying and ordering.** `Item` gains an `ISBN` field and the download tool an `isbn` argument:
DOI-only would have left `archive` unreachable and most OA monographs (which carry an ISBN and
often no DOI) unfetchable, and the ISBN is already in hand — OpenLibrary discovery hits carry it.
`config.KnownSources` becomes `unpaywall → europepmc → biorxiv → fatcat → core → oapen → archive
→ scihub → scidb → libgen → randombook → annas`: the two new sources sit *before* the shadow
libraries, mirroring what §3 decided for articles, so a legally free copy is always preferred when
one exists. Ordering only matters between sources that claim the same item, so the md5 book chain
is untouched.

#### Amendment — 2026-07-30 (technical standards)

Standards were never evaluated: every table above asked about scholarly literature, and a
standard is neither an article nor a monograph. The gap turned out to be narrower than expected
and the fix cheaper, so both the measurements and the rejections are recorded here.

**Measured first — the catalog already covers standards well.** The `standards` collection
returns 76,709 files and carries ISO/IEC and BS in depth (`ISO/IEC 27001:2022`, `ISO 8601:2004`,
`ISO/IEC 14496-10:2022`, `BS EN ISO 140-8:1998`). It is **not** a blind spot in general. What it
holds nothing of is specific bodies: `JIS A 1418` and `JIS Japanese Industrial Standard` both
return zero.

**Measured — Crossref already returns standards today, unasked.** A federated search for
"measurement of sound insulation in buildings standard" came back with `10.3403/30269187` and
`10.3403/00596975` (BSI, `origin: crossref`, `open_access: false`). Crossref's `type:standard`
holds **416,633** records and accepts `query.bibliographic`, so multi-body standards *discovery*
needs no new provider at all — only that the existing Crossref provider pass `filter=type:standard`
when the caller's intent is standards, and carry publisher and year on those hits (today they
arrive as DOI + title alone, so BSI is indistinguishable from IEEE and the edition is invisible).
Recorded as a GO but deliberately **not implemented in this pass**: it adds no files, and the two
sources below do.

##### Two new download sources, both DOI-keyed — GO

Both claim only their own registrant prefix, so they compete with nothing else in the chain and
need **no new identifier on `Item` and no new argument on `download`/`read`**. That is what
separates them from the ERIC decision above, whose accession number was a third key space:

- **`rfc`** — the RFC Editor. RFC DOIs exist and are registered (`10.17487/RFC9110` → "HTTP
  Semantics", publisher RFC Editor), and **all seven DOI sources fail on one today**: unpaywall
  reports not open access, europepmc not indexed, fatcat no preserved full text, core absent,
  oapen no entry, scihub and scidb no PDF — while `rfc-editor.org/rfc/rfc9110.txt` answers 200.
  A document free since 1969 that the chain could not serve.
- **`nist`** — NIST's own repository, reached through the DOI resolver.

##### Measured — the two format traps

Neither shape was the obvious one, and each was found only by fetching:

- **RFCs resolve to `.txt`, not `.pdf`.** PDF coverage is irregular: `rfc9110.pdf`,
  `rfc2616.pdf`, `rfc9000.pdf` and `rfc9457.pdf` serve real bytes, but `rfc1.pdf`, `rfc791.pdf`
  and `rfc8446.pdf` all 404. `.txt` answered 200 for every RFC sampled from 1 (1969) through 9457,
  `extract` reads it, and it is the canonical RFC format — so the source resolves to `.txt` and
  the file is small. Preferring PDF with a fallback would cost an extra request per download and
  make the returned format unpredictable across RFCs.
- **A NIST DOI redirects straight to the PDF.** `https://doi.org/10.6028/NIST.SP.800-53r5` ends at
  `nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-53r5.pdf` with
  `content-type: application/pdf`, and the same holds for FIPS, IR, TN, CSWP and even *jres*
  journal articles. So `Resolve` hands over the `doi.org` URL and the pipeline's redirect
  following does the rest — **no series→path map and no year lookup**. That matters because the
  path is otherwise not derivable: the IR series is partitioned by year
  (`/nistpubs/ir/2020/NIST.IR.8259.pdf` is 200, the same file under `2019/` is 404), and the DOI
  does not carry the year. The whole `10.6028` prefix is claimed rather than just the `NIST.`
  suffixes, because *jres* articles resolve to a PDF the same way.
- **HEAD is not used to pre-check either one.** nvlpubs answers **404 to HEAD** on a URL whose GET
  serves the file, so a HEAD pre-check would report every NIST publication as missing. The
  download's own GET is the check.

##### Rejected, with the measurement (so these are not re-litigated)

- **kikakurui.com (JIS) — NO-GO.** The originally proposed source. Its `robots.txt` is empty and
  its HTML carries the real normative text, but it has **no search interface at all** (the `/a1/`
  index has 257 links and zero `<form>`/`<input>`), so "search" would mean crawling and caching
  per-prefix indexes — a stateful crawler inside a stateless binary. Titles are Japanese-only, so
  matching only works for a caller who already knows the JIS number and can build the URL
  unaided. The pages are HTML, which `extract` does not read, and carry figures as images (13
  `<img>`, 0 `<table>` in the sampled page) — precisely the quantitative content of a measurement
  standard. It is also a single unauthorized republication of a catalog JSA actively sells, with
  no redundancy behind it.
- **IEEE 802 (GET program) — NO-GO.** `stampPDF` answers HTTP 202 with **0 bytes**: a bot gate no
  pure-Go client passes.
- **ETSI — NO-GO for now.** Free PDFs are real (2,334,396 bytes for TS 136 211) and the initial
  403 was only the bare `curl` User-Agent — an honest `libgen-mcp/<version> (+URL)` gets 200, so
  no browser spoofing is involved. It stays out because its `/deliver/` path needs a version *and* a
  directory range that only its search can supply, and its `robots.txt` disallows `/search/`.
- **ITU-T — MAYBE, deferred.** Free PDFs confirmed (H.264 16,426,192 bytes; X.509 3,581,573;
  G.711 196,237), but the download id embeds the publication date
  (`T-REC-X.509-201910-I!!PDF-E`), which no caller holds, so it needs a per-recommendation
  landing-page lookup and a new key space — the ERIC objection, for a corpus of a few thousand.
- **W3C — discovery only at best.** `api.w3.org/specifications` is keyless and lists 1,706 specs,
  but they are HTML, which `extract` does not read.
- **ECMA — NO-GO.** The PDF is free (4,799,672 bytes for ECMA-262) but the filename encodes the
  edition and month (`ECMA-262_15th_edition_june_2024.pdf`), so nothing is derivable from the
  standard's number.
- **3GPP — NO-GO.** Specs ship as `.zip` of `.doc`; `extract` reads neither.
- **NASA, DoD ASSIST, OGC — deferred.** HTML portals with no JSON query endpoint found; each
  would need its own scraper.

**Chain placement.** `config.KnownSources` becomes `unpaywall → europepmc → biorxiv → rfc → nist →
fatcat → core → oapen → archive → scihub → scidb → libgen → randombook → annas`. Both new sources
join the publisher-direct group ahead of the shadow libraries, per §3, and being prefix-gated they
change nothing for any other identifier.

#### Amendment — 2026-07-30 (three more publisher-direct DOI sources)

The standards amendment above established the shape: a source that claims one DOI registrant
prefix, competes with nothing, and needs no new identifier on `Item` and no new argument on
`download`/`read`. Three more publishers fit it. Each was re-measured from scratch before
being accepted — including, in every case, whether the chain already reached it.

##### Measured — none of the three is reachable by the default, keyless chain

The gap is the reason each ships, so it was checked against every source in the chain rather
than assumed:

| Candidate                | Unpaywall                             | Crossref | Europe PMC | fatcat                             | Sci-Hub           |
| ------------------------ | ------------------------------------- | -------- | ---------- | ---------------------------------- | ----------------- |
| Dagstuhl `10.4230`       | 404                                   | **404**  | 0 hits     | release page, **0** full-text tags | 2 KB stub, no PDF |
| ACL Anthology `10.18653` | is_oa in 12 of 13 sampled — see below | 200      | 0 hits     | 404                                | 2 KB stub, no PDF |
| Zenodo `10.5281/zenodo`  | n/a (DataCite)                        | n/a      | 0 hits     | 404                                | 2 KB stub, no PDF |

**Dagstuhl is the cleanest case.** It registers with DataCite rather than Crossref, so
`api.crossref.org` answers **404** for a Dagstuhl DOI and Unpaywall — which is built on
Crossref — answers 404 in turn. Confirmed on `10.4230/LIPIcs.ICALP.2023.1`,
`10.4230/LIPIcs.STACS.2015.1` and `10.4230/DagRep.9.1.1`. fatcat is the near miss: it *does*
hold a release page for the ICALP paper, but the page advertises zero `citation_pdf_url` tags,
so the `fatcat` source correctly reports no preserved full text. Six documents spanning
2015–2023 and three series (LIPIcs, OASIcs, DagRep) all served real PDF bytes, 339 KB to 8.4 MB.

**ACL is the honest one, and the caveat is recorded rather than glossed.** Of thirteen *real*
Anthology DOIs sampled, **twelve** carry a usable `url_for_pdf` in their Unpaywall record. So
this is not new corpus for a deployment that has configured Unpaywall. It is new corpus for
every deployment that has not — which is the default, since `unpaywall` is gated on
`LIBGEN_MCP_UNPAYWALL_EMAIL` and is absent from the chain without it. Given the keyless ethos
(§Architecture Decisions), "reachable only with a credential" is the same as "not reachable",
and that is what decided it. The thirteenth DOI is the sharper argument:
`10.18653/v1/N19-1423` — BERT, the most-cited paper in the field — is reported `is_oa: true`
by Unpaywall with **zero** `url_for_pdf` across every `oa_location`, so even a configured
Unpaywall cannot serve it, while `aclanthology.org/N19-1423.pdf` returns 786,279 bytes.

*A methodological note, because it nearly produced a wrong number.* An initial sample put
Unpaywall's coverage at 8 of 13. Five of those "misses" were DOIs that do not exist —
`10.18653/v1/D14-1162`, `10.3115/v1/W04-1013` and three others all answer 404 at `doi.org`
itself. Coverage over a set that includes fabricated identifiers measures nothing. Every DOI
was resolved at `doi.org` first, and the corrected figure is 12 of 13.

##### Measured — the three format traps

Each was found by fetching, and each is the reason its source is not a one-liner:

- **Dagstuhl needs the landing page; the PDF URL is not derivable.** DROPS storage paths embed
  the volume the paper appeared in, which the DOI does not carry:
  `10.4230/LIPIcs.ICALP.2023.1` is served from `/storage/00lipics/lipics-vol261-icalp2023/…`,
  `10.4230/LIPIcs.STACS.2015.1` from `lipics-vol030-stacs2015/…`, and `10.4230/DagRep.9.1.1`
  from a different scheme again (`/storage/04dagstuhl-reports/volume09/issue01/19021/…`).
  Nothing in the identifier predicts `vol261` or `19021`. The document page carries exactly one
  `citation_pdf_url` tag and that is the only route to the file. The **doi.org hop is skipped**:
  the resolver redirects to `drops.dagstuhl.de/entities/document/<doi>`, so the source builds
  that URL itself — one request instead of two. An unheld DOI answers 404 there.
- **ACL's identifier case is load-bearing, and conditionally so.** The DOI suffix after `/v1/`
  *is* the Anthology identifier and the identifier *is* the filename, so no request is needed at
  all — but the filename is case-sensitive in opposite directions for the two id generations.
  `N19-1423.pdf` is 200 (786,279 bytes) and `n19-1423.pdf` is 404; `2024.emnlp-main.856.pdf` is
  200 (1,068,216 bytes) and `2024.EMNLP-MAIN.856.pdf` is 404. Uppercasing unconditionally would
  break every paper since 2020 and lowercasing unconditionally every paper before it, so the
  case is normalized on the one bit that separates them: an identifier opening with a letter is
  uppercased, one opening with a digit is left alone. Separately, only the `/v1/` shape maps —
  the pre-2014 numeric `10.3115/1072228.1072256` resolves to `portal.acm.org` and embeds no
  Anthology identifier, so it is declined rather than guessed at.
- **Zenodo must trust the file extension, never the mimetype.** Two independent measurements
  say so. In the listing, Zenodo reports formats it does not recognize as
  `application/octet-stream` — observed for `.md` and `.xlsx`. On the wire it is worse: the file
  endpoint serves **everything** as `application/octet-stream`, including three PDFs whose first
  bytes were `%PDF` and a `.zip`. The file's name is the only field that says what it is.

##### Measured — the Zenodo concept DOI, which nearly shipped broken

Zenodo mints **two** DOIs per deposit: one naming the specific version and one naming the
deposit across all its versions. The concept DOI is what its own "cite all versions" affordance
hands out, and it has **no file listing** — `/api/records/<concept>/files` answers 404. Sampling
DataCite for Zenodo DOIs returned concept and version DOIs in roughly equal numbers, so
treating that 404 as "no such record" would have silently lost about half of all Zenodo DOIs.

The fix is one extra request on the miss path: `HEAD /records/<id>` answers **302** to the
version for a concept id, **200** for a version id and **404** for neither — measured on all
three. HEAD is used because the page body is ~100 KB of markup, and unlike NIST's repository
(which 404s to HEAD on a URL whose GET serves the file) Zenodo answers HEAD faithfully. Nor is
the version id derivable: it is usually the concept id plus one (21698240 → 21698241) but not
always (19978417 → **21676215**).

##### Measured — robots.txt permits every path used

- **Dagstuhl** disallows `/api/*`, `/metadata/*/*` and `/entities/*/metadata/*`. The source
  touches `/entities/document/…` and `/storage/…`, neither of which is disallowed.
- **Zenodo** disallows `/api` wholesale and then allows one path back out:
  `Allow: /api/records/*/files`, with `Disallow: /api/records/*/files-archive` re-excluded. So
  the file listing is explicitly permitted while the record-*metadata* endpoint
  (`/api/records/<id>`) is not — which is why the version hop goes through the human-facing
  `/records/<id>` page (carrying no Disallow; only `/records/*/preview` does) rather than
  through `/api/records/<id>/versions/latest`. `Crawl-delay: 10` is also set; a resolve makes
  at most two requests for one caller-initiated download rather than crawling.
- **ACL Anthology** serves no `robots.txt` at all (404), so nothing is disallowed.

Every measurement above was taken with an honest `libgen-mcp/<version> (+URL)` User-Agent. No
browser User-Agent was spoofed for any of the three, and none was needed.

##### File selection, the one judgement call

A Zenodo record is a deposit rather than a document: it can hold one file or fourteen, and the
caller named the record, not a file. The source prefers a format `extract` can read — PDF, then
EPUB, then plain text — and failing that takes the record's largest file, on the reasoning that
a deposit's payload is its biggest file and the rest are READMEs, checksums and manifests. A
sampled 14-file record bears this out: the deposit is a 270 MB `.zip` and the other thirteen
entries are a `README.md`, a `CHANGELOG.md`, a manifest script and CSV templates. Dagstuhl and
ACL need no such rule — both serve exactly one PDF per DOI.

##### Chain placement

`config.KnownSources` becomes `unpaywall → europepmc → biorxiv → rfc → nist → dagstuhl → acl →
zenodo → fatcat → core → oapen → archive → scihub → scidb → libgen → randombook → annas`. All
three join the publisher-direct group ahead of the shadow libraries, per §3, and being
prefix-gated they change nothing for any other identifier. Each also adds a probe to
`articleProbes`, without which a prefix-gated source runs in the chain while being absent from
the download tool's `source` enum — and the probe has to satisfy the source's own
well-formedness check, not merely carry the right prefix, since `acl` declines a DOI without
the `/v1/` segment and `zenodo` one whose suffix is not a record number.

##### Not done, and why

- **Crossref `filter=type:standard` for standards discovery** — still a recorded GO from the
  2026-07-30 standards amendment, still not implemented. Unchanged by this pass.
- **A Dagstuhl, ACL or Zenodo *discovery* provider** — none added. All three are DOI-keyed
  download sources only. Dagstuhl and Zenodo have search interfaces, but `search` already
  federates Crossref, which indexes ACL and would index the others were they Crossref
  registrants; adding three narrow providers for corpora the existing federation partly covers
  is the "many narrow tools" this project rejects.
- **Zenodo's `/api/records/<id>` metadata endpoint** — deliberately never called, because its
  robots.txt disallows it. Record titles and authors are therefore not available to the source,
  which is why a Zenodo download is named from the file key rather than from the deposit's
  bibliographic metadata.

#### Amendment — 2026-07-30 (two more publisher-direct DOI sources, three rejections)

Five candidates that survived an earlier diagnostic pass were re-measured from scratch against
the whole chain. **Two ship, three do not.** The bar applied throughout is the one the previous
amendment established: a candidate ships only if it serves documents the current chain cannot,
and "the current chain" means the *keyless default* chain as well as a fully configured one.
Those are different claims and are kept apart below.

##### Shipped — `scielo` (`10.1590`)

SciELO is the open-access publishing platform of Latin American science; the Brazilian
collection alone is roughly half a million articles, mostly in Portuguese and Spanish, all
Creative Commons licensed and served without an account.

A previous pass reported "Unpaywall covers this corpus", and that is **half right in a way that
matters**. Over fourteen real DOIs sampled from 1998 to 2025, Unpaywall reported `is_oa: true`
for **all fourteen** — the metadata claim is correct — but supplied a downloadable
`url_for_pdf` for only **eight**, falling back to a `best_oa_location` that is the HTML article
page. "Open access according to Unpaywall" and "retrievable through the `unpaywall` source" are
not the same statement, and the earlier pass conflated them.

| Sample (14 DOIs, 1998–2025) | Unpaywall `is_oa` | Unpaywall `url_for_pdf` | fatcat full text | scielo.br |
| --------------------------- | ----------------- | ----------------------- | ---------------- | --------- |
| all years                   | 14/14             | 8/14                    | 10/14            | 13/14     |
| the four sampled from 2025  | 4/4               | **0/4**                 | 2/4              | 4/4       |

Three of the fourteen were reachable from **no** source in the chain, keys or no keys —
`10.1590/s0103-4014.202438112.008-en`, `10.1590/1413-7054202549009425` and
`10.1590/1982-2553202568745` — and scielo.br served them at 1,542,793, 737,104 and 610,410
bytes. Europe PMC returned zero hits for thirteen of the fourteen. The gap is **systematic
rather than random**: it concentrates on recent publications, where Unpaywall has not recorded
a PDF location and Internet Archive Scholar has not finished ingesting. That is the material a
research tool is most often asked for, which is what decided this one.

The fourteenth case is recorded because it bounds the claim: `10.1590/s0104-66321998000100007`
(1998) is HTML-only at SciELO itself — the article page carries no `citation_pdf_url` and the
PDF endpoint answers "PDF do Artigo não encontrado" — so it is unreachable everywhere, this
source included, and the source declines rather than handing the pipeline a 404.

**Measured — the resolver hop cannot be skipped.** Unlike `dagstuhl`, whose landing page URL is
derivable, a SciELO DOI predicts nothing. The registered URL is the legacy
`scielo.php?script=sci_arttext&pid=<PID>` form, whose PID is embedded only in the older DOIs
(`10.1590/s0104-6632…`) and not at all in the modern ones (`10.1590/1982-2553202568745`); it
then redirects to a `/j/<journal>/a/<key>/` address whose key appears in no identifier the
caller holds. SciELO's own ArticleMeta API *does* accept a DOI as `code` and return the PID, but
its 88 KB legacy record carries no `/j/…/a/…` key either — checked field by field — so it would
cost a request and still leave the article page to fetch. The source therefore follows
`doi.org`, checks it landed on `scielo.br`, and reads the page's `citation_pdf_url`.

##### Shipped — `fao` (`10.4060`)

The Food and Agriculture Organization of the United Nations registers about 9,500 DOIs under
`10.4060` and publishes all of it openly — flagship reports, technical guidance, country
humanitarian response plans, standards work and statistical yearbooks, in six languages.

This one is an **absolute gap, 8 of 8**. Across DOIs sampled from 2019 to 2024, Unpaywall
reported `is_oa: false` for **every one**: FAO deposits no open-access location with Crossref,
so the whole corpus is invisible to a metadata-driven resolver however open its licenses
actually are. Europe PMC returned zero hits for all eight. fatcat either held a release page
advertising no preserved full text or answered 404. The LibGen catalog carries a handful of the
flagship titles (`The State of Food Security and Nutrition in the World`, `Compendium of food
additive specifications`) and nothing else — a catalog search for "FAO humanitarian response
plan" returns zero results. All eight resolved through the repository.

**Measured — the doi.org hop is skipped, and not only to save a request.** The DOI's suffix is
also the item's handle, so `openknowledge.fao.org/handle/20.500.14283/<suffix>` follows from the
identifier with no lookup; it answered 200 with a `citation_pdf_url` for eight of eight,
including several whose DOI resolver sends callers to `www.fao.org/documents/card/…` instead.
That alternative is worse than indirect: **`www.fao.org` answered HTTP 504 for five of the eight
sampled DOIs** while the repository served all eight.

**Measured — the frontend/backend trap, which would have shipped a source that downloads
nothing.** The `citation_pdf_url` tag names `/bitstreams/<uuid>/download`, an Angular *frontend*
route. To a browser it hands over the file; to a plain HTTP client it answers **HTTP 200,
`text/html`, 372,862 bytes** of application shell. The backend endpoint
`/server/api/core/bitstreams/<uuid>/content` serves the bytes — `application/pdf` at 2,929,735,
12,549,068 and 8,677,202 for three sampled items whose frontend URLs all returned the shell — so
the source lifts the UUID out and rebuilds the URL. Taking the advertised tag at face value, as
`dagstuhl` and `fatcat` do, would have been wrong here.

**Measured — robots.txt permits exactly the two paths used, and forbids the one the diagnostic
pass proposed.** The repository's file reads:

```text
# Only allow access to the core bitstream and mapping endpoints on the rest api, nothing else
Allow: /server/api/core/mapping
Allow: /server/api/core/bitstreams
Disallow: /server/api/*
Disallow: /server/api
```

So the bitstream content endpoint is explicitly permitted, and the DSpace discovery search
`/server/api/discover/search/objects` — the route an earlier pass suggested driving, and which
does answer `application/hal+json` to a truthful `Referer` — is **disallowed**, and is never
called. The item page path `/handle/<prefix>/<id>` carries no `Disallow`; `/search`, `/browse`,
`/entities/*`, `/items/*/full` and the login and submission routes do. `Crawl-delay: 10` is set.
No `Referer` turned out to be needed for either path used, truthful or otherwise.

**Measured — DSpace reports an unheld handle as HTTP 200.** An unknown handle returns the
ordinary Angular shell carrying "No item found for the identifier", not a 404, so the status
line cannot separate a held item from an unheld one and the absence of the full-text tag reports
both. A response missing the repository's own `<ds-root` element is treated differently — that
is a challenge or a layout change, and is reported as the source being unavailable.

##### Rejected — Project Euclid, with the measurement

Project Euclid (IMS, Duke, Bernoulli and the mathematics/statistics journals) was previously
rejected on a misread, and the correction was right as far as it went: the block is a filter on
the literal `curl/8.7.1` User-Agent string, and an honest descriptive UA gets the article page
and, at first, the PDF. It is rejected anyway, on **both** counts of the bar.

**Overlap.** Of eight `10.1214` DOIs sampled across 1935, 1955, 1975, 1990, 2005 (×2), 2015 and
2024, Unpaywall supplied a `url_for_pdf` for **six**, and that link points at
`projecteuclid.org` itself. For a configured deployment this is a second route to the same file
served by the same host. The two misses were the 1935 *Annals of Mathematical Statistics* paper
and a 2024 *Annals of Statistics* paper, both `is_oa: false`.

**Dependability, which is the decisive part.** The download endpoint is behind Imperva bot
scoring. Requests spaced ten seconds apart returned PDFs for the 1st, 3rd and 4th DOIs and a
**6,183-byte "Pardon Our Interruption" interstitial, served with HTTP 200**, for the rest. A
retry pass 45 seconds apart returned the interstitial for all three DOIs tried, and about thirty
minutes later the plain article page — not merely the download endpoint — was also serving it.
Fourteen requests over one session were enough to close the host. A source that must
content-sniff every 200 and degrades to permanent failure under light, polite use is not a
source; it is a liability in a chain whose other members are expected to answer.

`robots.txt` (`Crawl-delay: 10`, `Disallow: /search` and the institutional-signin paths) would
have permitted resolve-by-DOI, so policy was never the obstacle. Behaviour was.

##### Rejected — DLA ASSIST, with the measurement

`quicksearch.dla.mil` is the US Department of Defense's official channel for MIL-STD/MIL-SPEC,
and the corpus is a genuine hole: the catalog's `standards` collection returns **zero** results
for "MIL-STD-810", "MIL-STD-461" and "military standard environmental engineering
considerations". MIL-STDs carry no DOI, so — per the ERIC decision above — the only option is a
discovery provider whose hits carry `pdf_url`. A schema expansion was never on the table.

**It works.** This is recorded because the previous entry deferred it as "an HTML portal with no
JSON query endpoint", which understates it. The search is an ASP.NET WebForms postback and it
does answer: GET `qsSearch.aspx`, carry `__VIEWSTATE`, `__VIEWSTATEGENERATOR`,
`__VIEWSTATEENCRYPTED` and `__EVENTVALIDATION` into a POST with the session cookie, and
`DocumentIDTextBox=MIL-STD-810` comes back with `qsDocDetails.aspx?ident_number=35978`. That
detail page lists 21 revision images as `ImageRedirector.aspx?token=<A>.<ident>`, and the
`<A>` half alone builds `WMX/Default.aspx?token=<A>`, which 302s to `/Transient/<GUID>.pdf` and
served **24,788,992 bytes** of MIL-STD-810H Change 1 to the honest UA. There is no `robots.txt`
(404).

**It is rejected on cost, and the cost is in the wrong place.** A `discovery.Provider` must be
best-effort and fast — the contract says degrade to an empty slice rather than sink the
federated result. Filling `pdf_url` here means two requests to open a search session, and then
**one detail-page fetch per hit**: a default-sized result set is 20-plus serialized requests to
a single F5-fronted `.mil` host inside one federated search, an order of magnitude more traffic
than every other provider combined. Drop the per-hit fetches and the hits carry a document
number and a title for a corpus whose users type the document number to begin with — the ERIC
affordance minus the part that made ERIC worth wiring. And the state is opaque and unversioned:
`__VIEWSTATEENCRYPTED` is set, so a 1,708-character VIEWSTATE and a 1,048-character
EVENTVALIDATION must be round-tripped verbatim with a cookie, and any ASP.NET redeploy or
machineKey rotation changes the contract silently.

##### Rejected — EverySpec, with the measurement

EverySpec mirrors the same military-standards corpus and is, on mechanics, the better of the
two: `robots.txt` is `User-agent: *` / `Disallow:` (nothing disallowed), the first-party search
is a **stateless** `POST /specifications-standards-search.php` with `query_id` and `query` that
returns server-rendered document links, and the download URL is derivable from a result's own
href with **no extra request** — `<dir>/download.php?spec=<NAME>.<id zero-padded to 6>.pdf`,
confirmed on MIL-STD-810H (65,345,109 bytes), MIL-STD-810G CHG-1, MIL-STD-1472H and
MIL-STD-1553C. Its Google Custom Search box is a red herring; the first-party form is what
works.

It is rejected because **it is the same corpus as ASSIST and a stale copy of it**:

| Document       | ASSIST's newest                     | EverySpec's newest        |
| -------------- | ----------------------------------- | ------------------------- |
| MIL-STD-882    | Revision E Change 1 (27-SEP-2023)   | 882E (2012)               |
| MIL-STD-464    | Revision D (2020) + Notice 1 (2026) | 464C (2010)               |
| MIL-STD-2073-1 | Revision E Change 4 + Notice (2024) | 2073-1C                   |
| MIL-STD-810    | 21 images, incl. H Change 1 (2022)  | 21 entries, no H Change 1 |

Whichever of the two ships, the other adds routes rather than documents — and the one that is
cheap to implement is the one that is years behind. Since ASSIST is rejected above, EverySpec
would not be a *second* route; it would simply be a dependency on one small ad-funded mirror for
a corpus whose official channel we declined as too expensive, delivering superseded revisions of
safety and environmental-test standards. That is a worse failure than not covering the corpus,
because a superseded MIL-STD looks exactly like a current one.

##### Two corrections to earlier records

- **Semantic Scholar** is listed in §3's table as "NO-GO — keyless shared pool too throttled to
  depend on". That is true only of `graph/v1/paper/search`, which answered 429 on six of six
  attempts spaced three seconds apart. **Per-paper lookup and the `batch` endpoint answered 200
  on nine consecutive calls with zero 429s**, and the empty `openAccessPdf` that partly motivated
  the rejection was correct data rather than throttling — that record is `isOpenAccess: false`.
  The corrected record: Semantic Scholar is not viable for *search* on the keyless tier, and is
  viable for *resolution and enrichment*. It is still not implemented; the point of the
  correction is that the reason recorded for the rejection was wrong.
- **HTTP 202 with an empty body is an AWS WAF challenge**, not a bot gate that returns nothing.
  It was recorded that way for IEEE 802's `stampPDF` in the standards amendment above, and the
  same signature has since been seen on IEEE Xplore, ADS/IVOA and the UN Digital Library. The
  host is withholding its own challenge page from a non-browser header set: with a full browser
  header set those hosts emit a ~2 KB page carrying `window.awsWafCookieDomainList` and
  `window.gokuProps`. The conclusion for IEEE 802 is unchanged — no pure-Go HTTP client passes a
  WAF challenge — but the signature is recorded here so the next person recognizes a 202/0-byte
  response instead of re-measuring it.

##### Chain placement — scielo before fatcat

`config.KnownSources` becomes `unpaywall → europepmc → biorxiv → rfc → nist → dagstuhl → acl →
zenodo → scielo → fao → fatcat → core → oapen → archive → scihub → scidb → libgen → randombook →
annas`. Both join the publisher-direct group ahead of the shadow libraries, per §3, and both add
a probe to `articleProbes` — `fao`'s probe has to be a plausible handle suffix, not a bare
prefix, since the source declines a suffix that could not be one.

`scielo` is the first publisher-direct source whose prefix another keyless source also reaches:
`fatcat` preserves part of the SciELO corpus. It is placed **before** `fatcat` deliberately, so
the publisher's own current copy is preferred over an archive capture that lags publication —
which is precisely the lag the measurement above found.

### 4. Deepen the read loop — GO (pure-Go)

- **`search_in_document`** — search the already-extracted text and return snippet + page/offset
  (jumpable via `read`'s cursor). Trivial, no new dependency, high value on large books. GO.
- **TOC / outline navigation** — EPUB nav/NCX (trivial; the container is already unzipped) and
  PDF bookmarks via **pdfcpu** (pure-Go, Apache-2.0). Best-effort: many scanned/old PDFs carry no
  outline, so degrade cleanly when absent. GO (EPUB first, then PDF).
- New pure-Go, permissively licensed dependencies are acceptable where they earn their place
  (pdfcpu is the first). **OCR remains out of scope**: the viable engines are CGO (Tesseract) or
  keyed cloud services, either of which breaks the static-binary / keyless identity. Revisit only
  as an explicit, separately-decided opt-in that does not regress the default static build.
- Server-side summarization / RAG embeddings — NO-GO (redundant with the calling model, or needs
  a model/key).

### 5. Elicitation — GO (opt-in, with a deterministic fallback)

Adopt `ServerSession.Elicit` (stable in the spec and in go-sdk v1.6.1) at the natural ambiguity
points: choosing among multiple editions matching a title, confirming a large or overwriting
download, and requesting the Unpaywall contact email when unset. **Hard rule:** elicitation fires
only when the client advertised the capability; otherwise fall back to today's behavior (return
ranked candidates / require the env var). It must never become a hard dependency — headless/CI
clients and the no-friction promise must keep working unchanged.

## Rejected (recorded so they are not re-litigated)

- MCP Tasks now (unstable primitive, no stable API, no detached workload) — revisit per §1.
- ~~Anna's Archive as a source (no keyless path).~~ **Corrected 2026-07-24 — see §2:** keyless
  paths do exist (SciDB for articles, IPFS for books) and both are implemented. Only the
  DDoS-Guard-gated slow-download HTTP route stays rejected.
- randombook.org's own search (`/api/search/by-params`) as a discovery or download source —
  **measured and rejected 2026-07-24.** randombook.org is a Library Genesis frontend (it
  brands itself `libgen.pw`), so it adds nothing we do not already reach:
  its download links resolve to the same hosts we already use (`libgen.net/.me/.xyz`,
  `annas-archive.gl`, plus `sci-hub.ru` for articles — all already covered by the
  `randombook`, `annas` and `scihub` sources); its `collection=fiction` returns results
  identical to `collection=libgen`, so that parameter does not filter; and its three
  collections are a strict subset of the seven this project's own `search` already
  indexes (`nonfiction`, `fiction`, `articles`, `magazines`, `comics`, `standards`,
  `fiction_rus`). No new servers, no new corpus, fewer collections.
- OpenAlex (now key-required), Semantic Scholar keyless dependency.
- ~~CORE (key required).~~ **Corrected 2026-07-25 — see §3:** registration is free and the
  source ships as an opt-in `core` download source behind `LIBGEN_MCP_CORE_KEY`, off by
  default. ~~Internet Archive (noisy, deferred).~~ Also corrected — it ships as the keyless
  `fatcat` download source, resolving a DOI through Internet Archive Scholar.
- Standards sources that cannot work like the rest — kikakurui.com (JIS), IEEE 802's GET program,
  ETSI, ECMA, 3GPP, W3C as a download source. **Measured and rejected 2026-07-30 — see the
  standards amendment under §3** for each one's blocking measurement; ITU-T is deferred rather
  than rejected.
- Project Euclid, DLA ASSIST and EverySpec. **Measured and rejected 2026-07-30 — see the
  two-more-publisher-direct-sources amendment under §3.** Project Euclid duplicates Unpaywall on
  six of eight sampled DOIs and its download endpoint is Imperva-gated (HTTP 200 carrying a
  6,183-byte interstitial after light use); DLA ASSIST works but needs an encrypted-VIEWSTATE
  WebForms session plus one detail fetch per search hit to produce a `pdf_url`; EverySpec is the
  same corpus as ASSIST, years out of date.
- ~~Semantic Scholar keyless dependency (throttled).~~ **Partly corrected 2026-07-30:** only
  `graph/v1/paper/search` is throttled; per-paper and `batch` lookups answered 200 on nine
  consecutive calls. Still unimplemented, but the recorded reason was wrong.
- Discovery providers for Dagstuhl, the ACL Anthology and Zenodo, and any use of Zenodo's
  robots-disallowed `/api/records/<id>` metadata endpoint. **Decided 2026-07-30 — see the
  publisher-direct amendment under §3.** All three ship as DOI-keyed download sources only.
- OCR (CGO/keyed — breaks the static-binary, keyless identity).
- Server-side summarization / RAG / embeddings (redundant or needs a model/key).
- Resource subscriptions, sampling, MCP logging (no fit; logging also deprecated in the RC).
- Zotero/Calibre write-back (separate stateful product; BibTeX/RIS export already covers the
  lightweight path).
- Chasing more download mirrors / robustness (already ahead of peers; near-zero marginal
  mindshare).

## Consequences

- Positioning, README, and registry listings shift toward "keyless research retrieval across
  Library Genesis and open access."
- The context footprint grows with federated discovery and the new read affordances; keep result
  fields lean and re-measure `make audit-tokens` as each lands.
- pdfcpu enters go.mod; the pure-Go static build and license posture are preserved.
- Adoption also needs a non-feature push (clear one-line hook, awesome-list / registry presence);
  tracked separately from this record.
