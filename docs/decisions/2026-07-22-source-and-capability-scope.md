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
- OpenAlex (now key-required), Semantic Scholar keyless dependency, CORE (key required).
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
