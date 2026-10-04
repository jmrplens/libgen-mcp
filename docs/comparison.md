# Comparison with other MCP servers

**Explanation** — for anyone weighing this server against the alternatives.

libgen-mcp is a four-tool Model Context Protocol server, written in Go, that searches the
Library Genesis catalog and open-access sources, tries open access first for a DOI or an
ISBN, exports citations in nine formats, lists a record's references and citing works, and
extracts text. It needs no key. The table below compares it with the servers people most
often weigh it against, as checked on 2026-10-04.

The comparison is sourced rather than scored. Each cell says what the project's own
repository shows, no project is ranked, and [How this was checked](#how-this-was-checked)
explains how the repositories were read.

## At a glance

One row per project, with its language beside its name. "Not stated" means that neither the
project's README nor its code shows the feature. "Not applicable" in the open-access column
means the project has no shadow-library sources, or only shadow-library sources, so there is
no order to compare. Distribution, transports, tool counts and maintenance are in the
[notes per project](#notes-per-project).

The comparison is split in two tables, because seven columns do not fit a reading column.
The first says what each project searches and downloads:

| Project (language)                                                                                     | Search sources                                                      | Downloads full text                                     | Open access first                                    |
| ------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------- | ------------------------------------------------------- | ---------------------------------------------------- |
| [jmrplens/libgen-mcp](https://github.com/jmrplens/libgen-mcp) (Go, this project)                       | Library Genesis catalog, then 10 more providers                     | Yes, through a chain of 21 sources                      | Yes, for a DOI or ISBN (not for an md5)              |
| [openags/paper-search-mcp](https://github.com/openags/paper-search-mcp) (Python)                       | 23 sources (24 with an IEEE key), including arXiv, PubMed, OpenAlex | Yes, per-source PDFs and a fallback chain               | Yes, in its fallback chain (Sci-Hub last and opt-in) |
| [Dianel555/paper-search-mcp-nodejs](https://github.com/Dianel555/paper-search-mcp-nodejs) (TypeScript) | Crossref, arXiv, PubMed, Scopus and more                            | Yes, per-platform PDFs (Sci-Hub opt-in)                 | Not stated (Sci-Hub off by default)                  |
| [blazickjp/arxiv-mcp-server](https://github.com/blazickjp/arxiv-mcp-server) (Python)                   | arXiv (Semantic Scholar for citation graphs)                        | Yes, from arXiv (HTML, PDF with an extra, LaTeX source) | Not applicable (arXiv only)                          |
| [cyanheads/pubmed-mcp-server](https://github.com/cyanheads/pubmed-mcp-server) (TypeScript)             | PubMed, Europe PMC (OpenAlex for related articles)                  | Returns it as content, saves no file                    | Open access only (PMC, Europe PMC, Unpaywall)        |
| [Kaago/openpapers-mcp](https://github.com/Kaago/openpapers-mcp) (Python)                               | OpenAlex (Crossref and Unpaywall per DOI)                           | Yes, a PDF by URL, which Unpaywall finds                | Open access only                                     |
| [yashimosh/biblio-mcp](https://github.com/yashimosh/biblio-mcp) (TypeScript)                           | Anna's Archive, LibGen, Z-Library, Sci-Hub                          | Books by md5, papers as a Sci-Hub URL only              | Not applicable (shadow libraries only)               |
| [rookslog/zlibrary-mcp](https://github.com/rookslog/zlibrary-mcp) (TypeScript, Python)                 | Z-Library, Library Genesis, Anna's Archive                          | Yes, books by Z-Library id or md5                       | Not applicable (shadow libraries only)               |
| [mwaraic/libgen-mcp](https://github.com/mwaraic/libgen-mcp) (TypeScript)                               | Library Genesis (libgen.is)                                         | No, returns download links from a mirror                | Not applicable (no open-access sources)              |
| [iosifache/annas-mcp](https://github.com/iosifache/annas-mcp) (Go)                                     | Anna's Archive                                                      | Yes, by md5 or DOI (Anna's API, SciDB)                  | Not applicable (Anna's Archive only)                 |

The second says what each one does with a record, and what it asks of you:

| Project                                                                                   | Citations                                      | Reads text                               | Key needed                                  |
| ----------------------------------------------------------------------------------------- | ---------------------------------------------- | ---------------------------------------- | ------------------------------------------- |
| [jmrplens/libgen-mcp](https://github.com/jmrplens/libgen-mcp)                             | BibTeX, RIS and 7 styles, references, cited by | Yes (PDF, EPUB, TXT, by section)         | None (four optional credentials)            |
| [openags/paper-search-mcp](https://github.com/openags/paper-search-mcp)                   | References and citing papers only              | Yes (`read_*` tools, PDF sections)       | None (keyed platforms optional)             |
| [Dianel555/paper-search-mcp-nodejs](https://github.com/Dianel555/paper-search-mcp-nodejs) | Counts and references only                     | Only through paid ScrapingAnt            | None (keyed platforms optional)             |
| [blazickjp/arxiv-mcp-server](https://github.com/blazickjp/arxiv-mcp-server)               | BibTeX                                         | Yes (to Markdown, by section)            | None                                        |
| [cyanheads/pubmed-mcp-server](https://github.com/cyanheads/pubmed-mcp-server)             | APA, MLA, BibTeX, RIS, Vancouver               | Yes (PMC XML, PDF or HTML via Unpaywall) | None (Unpaywall tier needs an email)        |
| [Kaago/openpapers-mcp](https://github.com/Kaago/openpapers-mcp)                           | Not stated                                     | Not stated                               | None (contact email optional)               |
| [yashimosh/biblio-mcp](https://github.com/yashimosh/biblio-mcp)                           | Not stated                                     | Not stated                               | None                                        |
| [rookslog/zlibrary-mcp](https://github.com/rookslog/zlibrary-mcp)                         | Not stated                                     | Yes (EPUB, PDF, TXT to RAG files)        | A Z-Library account for its Z-Library tools |
| [mwaraic/libgen-mcp](https://github.com/mwaraic/libgen-mcp)                               | Not stated                                     | Not stated                               | None stated                                 |
| [iosifache/annas-mcp](https://github.com/iosifache/annas-mcp)                             | Not stated                                     | Not stated                               | None to search, an Anna's key to download   |

## How to choose

- **Breadth of academic search.** If you want one server that queries many scholarly
  indexes, [openags/paper-search-mcp](https://github.com/openags/paper-search-mcp) fits
  better: it searches 23 sources by default, or 24 with an IEEE key, from arXiv and PubMed
  to OpenAlex, CORE, OpenReview, ACM and Google Scholar, behind 71 tools by default, and its download fallback chain tries
  open-access routes before an optional Sci-Hub step. It also lists a paper's references and
  citing papers through OpenAlex.
  [Dianel555/paper-search-mcp-nodejs](https://github.com/Dianel555/paper-search-mcp-nodejs)
  also fits, on npm with 23 tools, and adds Web of Science, ScienceDirect and Scopus for
  those who hold their keys.
- **Papers on arXiv.**
  [blazickjp/arxiv-mcp-server](https://github.com/blazickjp/arxiv-mcp-server) fits better
  when your papers are on arXiv: it reads a paper section by section as Markdown, searches
  passages, exports BibTeX from arXiv metadata, fetches the LaTeX source and uses Semantic
  Scholar for a citation graph. In libgen-mcp, arXiv is one of ten search providers, and
  its hits carry a direct PDF link.
- **Biomedical literature.**
  [cyanheads/pubmed-mcp-server](https://github.com/cyanheads/pubmed-mcp-server) fits better
  for PubMed work: it searches PubMed and Europe PMC, returns full text from PMC, then
  Europe PMC, then Unpaywall, formats citations as APA, MLA, BibTeX, RIS and Vancouver, and
  has a public hosted endpoint. libgen-mcp searches PubMed and Europe PMC too. Its Europe PMC
  hits are marked open access when Europe PMC holds a free full text, which its download
  chain then fetches, while its PubMed hits are bibliographic only and never marked open
  access.
- **References that already live in Zotero.**
  [54yyyu/zotero-mcp](https://github.com/54yyyu/zotero-mcp) fits better, although it is
  adjacent rather than a direct alternative: it searches your own Zotero library, not
  external catalogs, and needs Zotero 7 or later with its local API enabled, or a Zotero web
  API key. It attaches an open-access PDF when it adds an item by DOI, exports BibTeX or a
  bibliography in any CSL style, and reads PDF pages as Markdown.
- **Open access and nothing else.**
  [Kaago/openpapers-mcp](https://github.com/Kaago/openpapers-mcp) is built on OpenAlex,
  Crossref and Unpaywall, and its README says "No Sci-Hub, no paywall bypass". No
  shadow-library code was found in pubmed-mcp-server or zotero-mcp either. libgen-mcp can
  drop its shadow-library download sources with `LIBGEN_MCP_SOURCES`, but its search still
  starts at the Library Genesis catalog.
- **Z-Library.** [rookslog/zlibrary-mcp](https://github.com/rookslog/zlibrary-mcp) fits
  when you hold a Z-Library account: it searches and downloads from Z-Library with it, and
  from Library Genesis without one, and turns a downloaded EPUB, PDF or TXT into text files
  for retrieval. [yashimosh/biblio-mcp](https://github.com/yashimosh/biblio-mcp) searches
  Z-Library with no account, beside Anna's Archive and Library Genesis. libgen-mcp has no
  Z-Library source.
- **Books as well as papers, from one keyless binary.** libgen-mcp fits when you want both
  from one server: it searches Library Genesis first, narrows by publication year, tries
  open-access sources first for a DOI or an ISBN, and reads the PDF, EPUB or TXT it saved
  with `read`, one table-of-contents section at a time if asked. Its `get_details` turns a
  reference pasted as free text into a DOI, returns BibTeX and RIS, adds APA, MLA, Chicago,
  Harvard, Vancouver, IEEE or CSL-JSON on request, and lists the works a record cites or the
  works citing it, from OpenAlex. It is one static Go binary, also published on npm, PyPI,
  Homebrew, NuGet, Docker and as Claude Desktop `.mcpb` bundles, and it needs no key.

Two limits of libgen-mcp belong beside that last point. A book asked for by md5 comes from
Library Genesis, because no legal source is keyed by md5;
[Responsible use](responsible-use.md) sets out the whole chain. And its search reaches the
Library Genesis catalog plus ten providers, by default only when the catalog comes up empty
or fails, rather than a broad academic index: for a literature search across many scholarly
databases, a server from the first point covers more.
[How search works](how-search-works.md) describes what it does query, and
[Citations](citations.md) describes each style and where it comes from.

## Notes per project

### jmrplens/libgen-mcp

This project. A Go server with four tools (`search`, `get_details`, `download`, `read`) over
stdio and streamable HTTP, distributed as a static binary and on npm, PyPI, NuGet, Homebrew,
Docker and as a `.mcpb` bundle per operating system. A remote deployment registers three
tools by default, because `read` is left out. Its four credentials, each optional, are an
Anna's Archive key, a CORE key, an OpenAlex key and an Unpaywall contact email. Last commit
2026-10-04, release v2.2.0, 19 stars as of 2026-10-04, MIT license.
[Repository](https://github.com/jmrplens/libgen-mcp).

### openags/paper-search-mcp

A Python server that installs from PyPI, through Smithery, as a Docker image or from source,
and also ships as a Claude Code skill with a command-line tool. It registers 71 tools by
default, or 74 with an IEEE key, over stdio, SSE or streamable HTTP, with optional OAuth on
HTTP. Web of Science and Scopus tools are always listed and need their keys when called. Its
download fallback chain keeps Sci-Hub off by default, while a separate Sci-Hub download tool
is always registered. Last commit 2026-10-02, PyPI version 0.1.4, which predates the
citation, OpenReview, Web of Science and Scopus tools, 2,739 stars as of 2026-10-04, MIT
license. [Repository](https://github.com/openags/paper-search-mcp).

### Dianel555/paper-search-mcp-nodejs

A TypeScript server for Node.js 20.18.1 or later (not 21), published on npm, with 23 tools
over stdio. Crossref and arXiv need no key, while Web of Science, ScienceDirect, Scopus,
Springer and Wiley do. Its Markdown tool relies on ScrapingAnt, a paid service that is
opt-in. Last commit 2026-09-20, npm version 0.3.3, 187 stars as of 2026-10-04, MIT license.
[Repository](https://github.com/Dianel555/paper-search-mcp-nodejs).

### blazickjp/arxiv-mcp-server

A Python server on PyPI, also packaged as a Claude Desktop `.mcpb` for macOS and as Claude
Code, Codex and Kiro plugins. It has 19 tools, two of which need its `[pro]` extra, and 7
prompts, over stdio or streamable HTTP. A download takes the HTML version first and falls
back to the PDF only with its `[pdf]` extra installed. Its README warns that an unrelated
npm package uses the same name. Last commit 2026-10-03, release v0.8.0, 3,188 stars as of
2026-10-04, Apache-2.0 license. [Repository](https://github.com/blazickjp/arxiv-mcp-server).

### cyanheads/pubmed-mcp-server

A TypeScript server distributed on npm, as a GHCR image and as a `.mcpb` bundle, with a
public hosted endpoint at `https://pubmed.caseyjhand.com/mcp`. It has 11 tools, two of which
are its Europe PMC tools and can be switched off, 1 resource and 1 prompt over stdio or
streamable HTTP, with optional JWT or OAuth authentication. Last commit 2026-09-28, release
v2.10.19, 153 stars as of 2026-10-04, Apache-2.0 license.
[Repository](https://github.com/cyanheads/pubmed-mcp-server).

### Kaago/openpapers-mcp

A server for Python 3.12 or 3.13 that runs from a clone of the repository with `uv`. It has
5 tools over stdio. It searches OpenAlex, and looks a single DOI up in Crossref and
Unpaywall. Its PDF download takes a URL, refuses a response declaring a content type other
than PDF and checks the file's `%PDF-` signature. Last commit 2026-07-19 on the default
branch, release v0.1.0, 3 stars as of 2026-10-04, MIT license.
[Repository](https://github.com/Kaago/openpapers-mcp).

### yashimosh/biblio-mcp

A TypeScript server on npm that searches Anna's Archive, Library Genesis and Z-Library
concurrently, deduplicates the results by md5, searches Library Genesis's article index, and
resolves papers through Sci-Hub. It has 6 tools over stdio and needs no key: an optional
Anna's Archive key read on the default branch is not in the README or in npm version 1.1.0.
Last commit 2026-08-28, release v1.1.0, 19 stars as of 2026-10-04, MIT license.
[Repository](https://github.com/yashimosh/biblio-mcp).

### rookslog/zlibrary-mcp

A TypeScript server for Node.js 22 or later that calls a Python 3.10 bridge, set up with
`uv`, published on npm and as a GHCR image. It has 13 tools over stdio, and the image serves
them over SSE through a gateway. The Z-Library tools need a Z-Library account, while its
multi-source search and its Library Genesis downloads need none, and Anna's Archive
downloads need an Anna's key. A Library Genesis download fails over across mirrors. Reading
a PDF or an EPUB needs its `rag` extra, and OCR its `scholar` extra. Last commit 2026-10-03,
a dependency update after its author's last change on 2026-08-23, tag and npm version 1.4.0, 89 stars
as of 2026-10-04, MIT license. [Repository](https://github.com/rookslog/zlibrary-mcp).

### mwaraic/libgen-mcp

An unrelated project that shares this project's name: see
[Which libgen-mcp is this?](https://jmrplens.github.io/libgen-mcp/#which-libgen-mcp-is-this).
It is a TypeScript Cloudflare Worker with 4 tools that search Library Genesis by title or
author and return the download links on the first mirror's page, without saving a file,
served over SSE and streamable HTTP, with no stdio transport. Last commit 2025-06-01, no
releases, 5 stars as of 2026-10-04, MIT license.
[Repository](https://github.com/mwaraic/libgen-mcp).

### iosifache/annas-mcp

A Go server, also usable as a command-line tool, released as single binaries for Linux,
macOS, FreeBSD and Windows. It has 4 tools over stdio. Searching needs no key, and both
download tools require an Anna's Archive key, which comes from a donation, plus a download
directory. Last commit 2026-06-26, release v0.1, 1,061 stars as of 2026-10-04. No license is
stated: the repository has no LICENSE file.
[Repository](https://github.com/iosifache/annas-mcp).

## How this was checked

Every cell was read from the project's own repository on 2026-10-04: its README, its source
on the default branch, and its package manifest. Stars, last-commit dates, releases and
licenses come from the GitHub API, and published versions from PyPI or npm. Tool counts were
counted from the tool registrations in each project's source. Where the default branch has
moved past the published package, the page describes the default branch and says so.
Nothing was taken from reputation, blog posts or third-party directories.

A project is on the page when people weigh it against this one: a search or download server
for papers or books with users and recent commits, or one that covers a niche no other row
does. The search for new ones was a GitHub search for MCP servers over papers, books,
Library Genesis, Anna's Archive, Z-Library and Sci-Hub, sorted by stars.

"Not stated" means that neither the README nor the code shows the feature. It is not written
as "no", because the page records what a project shows rather than a verdict on it. Star
counts and dates are a snapshot and will drift.

If a cell is wrong or out of date,
[open an issue](https://github.com/jmrplens/libgen-mcp/issues) with a link to the part of
the project's repository that shows it. Corrections are welcome.

## Frequently asked questions

### What is the best MCP server for downloading research papers?

It depends on where the papers are. For broad academic search, paper-search-mcp covers 23
sources, or 24 with an IEEE key. For arXiv, arxiv-mcp-server reads papers section by
section. For biomedicine, pubmed-mcp-server returns open-access full text with formatted
citations. For books as well as papers, open access first for a DOI, citations in nine
formats and no key, libgen-mcp fits.

### Is libgen-mcp the same as mwaraic/libgen-mcp?

No. They are unrelated projects that share a name. This one is jmrplens/libgen-mcp, a Go
server registered as `io.github.jmrplens/libgen-mcp` that downloads through a chain of 21
sources. mwaraic/libgen-mcp is a TypeScript Cloudflare Worker that searches Library Genesis
and returns download links from a mirror's page without saving a file.

The home page's
[Which libgen-mcp is this?](https://jmrplens.github.io/libgen-mcp/#which-libgen-mcp-is-this)
section sets out how to tell this one apart.

### Which paper MCP servers produce BibTeX citations?

Among the servers on this page, four do. libgen-mcp returns BibTeX and RIS from
`get_details`, adds APA, MLA, Chicago, Harvard, Vancouver, IEEE or CSL-JSON on request, and
turns a reference pasted as free text into the DOI to cite. arxiv-mcp-server exports BibTeX
from arXiv metadata. pubmed-mcp-server formats APA, MLA, BibTeX, RIS and Vancouver.
zotero-mcp exports BibTeX, or a bibliography in any CSL style, from your Zotero library. The
others do not state a citation export. paper-search-mcp lists a paper's references and
citing papers, as libgen-mcp does, and paper-search-mcp-nodejs returns citation counts and
references.
