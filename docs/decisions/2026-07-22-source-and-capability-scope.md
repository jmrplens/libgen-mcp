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
