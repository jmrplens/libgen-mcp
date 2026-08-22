# libgen-mcp

**Connect your AI assistant to Library Genesis and the open-access web so it can find, cite, download and read books, papers, comics, magazines and standards — in plain language.** One static binary (or this container), four focused tools (`search`, `get_details`, `download`, `read`) plus guided prompts, working with Claude, Cursor, VS Code, and any MCP client.

You talk to your AI assistant; it does the searching and fetching. No mirrors, MD5 hashes, or download URLs to track — mirrors are discovered automatically and cached, with transparent failover.

> "Find me the latest edition of _Clean Code_." · "Download that paper by its DOI." · "Search comics for _Watchmen_ and grab the CBR." · "Read the first chapter and summarize it."

## Run with Docker

The image runs on **stdio by default** — the correct mode for MCP clients. Stdio transport, for desktop clients such as Claude Desktop, Cursor or VS Code:

```json
{
  "mcpServers": {
    "libgen": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "jmrplens/libgen-mcp:latest"]
    }
  }
}
```

Or directly, with the options combining freely:

```bash
# Plain (stdio, zero config)
docker run -i --rm jmrplens/libgen-mcp:latest

# Enable open-access articles via Unpaywall
docker run -i --rm -e LIBGEN_MCP_UNPAYWALL_EMAIL=you@example.com jmrplens/libgen-mcp:latest

# Consult the extra searchers on every search, not only when the catalog misses
docker run -i --rm -e LIBGEN_MCP_EXTRA_SOURCES=always jmrplens/libgen-mcp:latest

# Save downloads to a host folder
docker run -i --rm -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads -v "$HOME/Downloads:/downloads" jmrplens/libgen-mcp:latest

# Serve streamable HTTP instead of stdio
docker run --rm -p 8080:8080 jmrplens/libgen-mcp:latest --http :8080
```

Images are multi-arch (`linux/amd64`, `linux/arm64`), published for every release with provenance and SBOM attestations, and signed with Cosign. The same image is also available as `ghcr.io/jmrplens/libgen-mcp`.

## Why this server

- 🔑 **Keyless by default.** No account, no API key, no token. Search, details and downloads all work with zero configuration; credentials are strictly opt-in and, when supplied per call, used once and never stored.
- 🌐 **Catalog first, then the open-access web.** Queries the Library Genesis catalog, then reaches beyond it — Anna's Archive, arXiv, Crossref, OpenLibrary, Project Gutenberg, dblp, PubMed, ERIC — merged, deduped and labeled by origin.
- 📄 **Papers that actually download.** An ordered source chain with transparent failover across Unpaywall, OpenAlex, Europe PMC, bioRxiv/medRxiv, the RFC Editor, NIST, Schloss Dagstuhl, the ACL Anthology, Zenodo, SciELO, the FAO Knowledge Repository, Internet Archive Scholar, CORE, OAPEN and the Internet Archive.
- 📚 **Reads what it fetches.** `read` extracts and paginates a PDF/EPUB/TXT so your assistant can summarize a book or paper, search inside it, or outline it.
- 🔖 **Citations built in.** `get_details` returns a ready-to-paste BibTeX/RIS export, with opt-in Crossref/OpenLibrary enrichment.
- 🪶 **Light context footprint.** The four tools add ~6,700 tokens to a request, and are verified against a real LLM by an automated evaluator.

## Try it without installing anything

A public instance runs at **`https://mcp.jmrp.io/libgen`** — no account, no key, nothing to install. It is stateless streamable HTTP: `POST` is the transport, a `GET` answers `405` by design, and `/health` answers `{"status":"ok","version":"…","commit":"…","started_at":"…","uptime_seconds":…}`.

## Responsible use

This tool accesses third-party mirrors of Library Genesis. You are responsible for respecting the copyright and intellectual-property laws that apply where you live. Use it only for content you are legally entitled to access.

## Documentation

- [Repository and full README](https://github.com/jmrplens/libgen-mcp)
- [Documentation, install guides & configuration reference](https://jmrp.io/docs/libgen-mcp) (also in [Español](https://jmrplens.github.io/libgen-mcp/es/))
- [Releases and signed binaries](https://github.com/jmrplens/libgen-mcp/releases)

---

Maintained by [José M. Requena Plens](https://jmrp.io/) ·
[Project page](https://jmrp.io/projects/) ·
Hosted instance: [mcp.jmrp.io/libgen](https://mcp.jmrp.io/libgen) (POST-only; a GET returns 405 by design)
