# cmd/eval — live LLM-driven eval harness

A small end-to-end harness that drives a real Anthropic model over the real
libgen-mcp tools and grades whether the model picks the right tool with
well-formed arguments and gets a usable response back.

It is deliberately **not** a unit test. It is:

- **Compiled only under the `eval` build tag** (every file starts with
  `//go:build eval`), so a normal `go build ./...`, `go test ./...`, or CI run
  never touches it.
- **Gated at runtime**: even under the tag it exits 0 with a skip notice unless
  both `LIBGEN_EVAL=1` and a non-empty `ANTHROPIC_API_KEY` are set.

## What it exercises

The model talks to libgen-mcp's four tools (`search`, `get_details`,
`download`, `read`) registered on an **in-process** MCP server (`mcp.NewServer` +
`tools.Register` + `mcp.NewInMemoryTransports`). Every tool call the model makes
is executed for real against Library Genesis via `session.CallTool` — real
search pages, real details lookups, real downloads.

The Anthropic side is a raw `net/http` Messages API client (no SDK): model
`claude-haiku-4-5-20251001`, temperature 0, `tool_choice: auto`. The tool-use
loop runs up to 4 turns per scenario: send the prompt + tool defs, execute each
`tool_use` block, feed `tool_result` blocks back, and stop when the model
answers (or asks to clarify) without a tool call.

Assertions check the **tool name, the argument JSON shape, and that the real MCP
response is non-empty / well-formed** — never exact catalog content, which drifts.

## Scenarios

| ID  | What it checks |
| --- | --- |
| S1  | Book search: nonfiction topic, title/author columns, first result has a 32-hex md5 |
| S2  | Article search: articles topic, at least one result with a valid DOI |
| S3  | Standards search (SKIPs if the mirror returns 0) |
| S4  | `get_details` on an md5 taken from a prior search result |
| S5  | Book download by md5: saved path + non-zero size |
| S6  | Download **choosing a source**: model sets `source:"scihub"` for a paywalled article DOI |
| S6b | Download choosing a book source: model sets `source:"randombook"` for an md5 |
| S7  | Open-access article by DOI via unpaywall (needs a contact email) |
| S8  | Ambiguous "find me a good book" — passes if the model clarifies or the tool rejects it |
| S9  | **Start-retries**: sci-hub pinned to a dead host, so the staged retry schedule exhausts and the tool must surface the actionable "could not start" error — and the model must not fabricate success |
| S10 | **Unguided book search** ("I want to read _Dune_…") — model must form a search from a bare request, no collection/field hints |
| S11 | **Unguided search, comics** ("find the graphic novel _Watchmen_") — tests whether the model discovers the right collection unaided |
| S12 | **Unguided book download** ("download _Clean Code_…") — model must search, then download by an md5 it discovered, choosing the source itself |
| S13 | **Unguided article download** ("get me a PDF of _Hallmarks of Cancer_") — model must discover that articles are keyed by DOI, not md5 |
| S14 | **Download progress** — attaches a progress token to the download and asserts progress notifications actually reach the client end to end |
| S15 | **Ordered table with links** — a large, sorted results request; asserts the model sets a big page size + ordering and includes the results' download links in its answer (the tool's next_steps instructs it to) |
| S16 | **Resolve-only link** ("give me the direct download URL, don't download it") — asserts the model sets `resolve_only=true` and the tool returns a URL (as a `resource_link`) instead of a saved file — the remote/hosted delivery path |
| S17 | **Remote download (book)** — same book-download request as elsewhere, but run against a server started in **remote mode** (`--http`): `download` returns a link instead of saving a file, and the harness — acting as the agent's own fetch tool — fetches it to local disk |
| S18 | **Remote download (article)** — same for a paywalled DOI: the model calls `download`, the remote server returns a link, and the harness fetches it locally |
| S19 | **Search → read → summarize**: model searches for a paper by title, calls `read` (not `download`) with the DOI found in the search results, and writes its own summary of the extracted first page rather than dumping the UNTRUSTED text verbatim |
| S20 | **Open-access discovery** — under-specified like S10–S13: the prompt asks the model to "also check the open-access literature" without naming `extra_sources`; the model must set it to `always` itself and reference one of the federated arXiv/Crossref hits in its answer (SKIPs if the keyless providers return nothing live) |
| S21 | **Citations** — asks for a BibTeX citation; the model must reach `get_details` (which builds it) rather than fabricate one |
| S22 | **Enrichment** — asks for a paywalled DOI's journal and citation count, so the model must set `enrich=true` on `get_details` to pull the Crossref metadata |
| S23 | **In-document search** — asks to search *inside* a book, so the model must call `read` with a `find` argument instead of downloading the whole file |
| S24 | **Outline** — asks for a book's table of contents, so the model must call `read` with `outline=true` |
| S25 | **Elicited Unpaywall email** — the deployment email is forced empty, so the download can only succeed via the per-call email the host's elicitation handler supplies |
| S26 | **Elicited save confirmation** — a disk-writing download must raise the save-confirmation prompt; the host counts the confirmations it answers, so the assertion is hard, not inferred |
| S27 | **Remote in-document search** — S23 against a server in remote mode |
| S28 | **Remote outline** — S24 against a server in remote mode |
| S29 | **Remote open-access discovery** — S20 against a server in remote mode, phrased as an open-ended research request |
| S30 | **Remote enrichment** — S22 against a server in remote mode |
| S31 | **Remote citations** — S21 against a server in remote mode |
| S32 | **Search escalation** — the title is one the Library Genesis catalog does not carry, so a hit can only come from the automatic escalation to Anna's Archive; the model must report the file's format and size without being told to ask for extra sources |
| S33 | **Remote search escalation** — S32 against a server in remote mode |
| S34 | **Escalated search → download** — the same catalog-miss title, but the model must go on to download it, proving an escalated result carries an md5 the `download` tool accepts |
| S35 | **Remote escalated search → download** — S34 against a server in remote mode: `download` returns a link and the harness fetches it locally |
| S36 | **Escalated record lookup** — the model must follow the escalated search with `get_details` on an md5 the catalog has no record for, which only answers via the Anna's fallback; graded on the record's `origin` |
| S37 | **Remote escalated record lookup** — S36 against a server in remote mode |

**Guided vs. unguided.** S1–S9 spell out the collection / fields / source to exercise a specific path deterministically. S10–S13 are deliberately **under-specified** — the prompts read like a real user and give no such guidance, so they test whether the model can discover the right tool arguments from the tool and field descriptions alone. They are a proxy for how well the server self-describes to an unguided LLM; a live mirror miss is a SKIP, the model's argument choice still graded.

S6 / S6b are the reason this harness exists alongside the older checks: the
`download` tool takes an optional **`source`** argument, and these scenarios
assert the model actually sets it (and that `DownloadResult.Source` matches when
the live fetch succeeds).

**Source availability and SKIPs.** A download scenario grades the model's tool
call; if the model picks the right source but the live fetch fails, it SKIPs
rather than fails, because the external sources are not equally reliable:

- **libgen** (S5) and **unpaywall** (S7) are the dependable download paths.
- **sci-hub** (S6) mirrors are volatile and only host _paywalled_ papers — S6 uses
  a heavily-cited paywalled DOI (not an arXiv one, which Sci-Hub does not carry),
  so it can actually complete when a mirror is up, and SKIPs when none are.
- **randombook** (S6b) rediscovers fresh mirrors each run, so whether a given md5
  resolves varies; a live miss is a SKIP, the source-selection behavior still
  graded.

**Unpaywall needs a contact email.** The unpaywall source is disabled unless
`LIBGEN_MCP_UNPAYWALL_EMAIL` is set (its API rejects requests without one), so it
is also hidden from the download tool's `source` schema when unset. S7 sets the
email via its per-scenario environment to exercise the open-access path.

S9 exercises the download **start-retry** path deterministically without needing
a flaky live failure: it enables only `scihub`, points `LIBGEN_MCP_SCIHUB_HOSTS`
at `127.0.0.1` (connection refused instantly), and shrinks
`LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS` to `1ms,1ms` so the whole staged schedule
runs sub-second. It asserts the tool returns the actionable could-not-start error
(naming retry-now / retry-later / ask-the-user recovery) and that the model
reacts — relaying the failure or retrying — instead of claiming a saved file.

**S17–S18 are a remote block.** Every other scenario runs against a **local**
server, where `download` saves the file to disk. S17–S18 run the same download
requests (book, then paywalled DOI) against a server started in **remote mode**
(as if launched with `--http`), where `download` returns a link (a
`resource_link` + a `resolved` object) instead of saving a file. The harness
then acts as the agent's own fetch tool: it fetches the resolved URL to the
sandbox download dir, so the file lands locally either way — verifying the
model behaves the same while the server's delivery mechanism differs.

## Running

```sh
# all scenarios
LIBGEN_EVAL=1 ANTHROPIC_API_KEY=sk-... go run -tags eval ./cmd/eval

# or via the Makefile target (still needs ANTHROPIC_API_KEY in the env)
ANTHROPIC_API_KEY=sk-... make eval

# a subset, keep the downloads, and write a markdown report
go run -tags eval ./cmd/eval --only S1,S6 --keep-downloads --results-doc dist/eval.md
```

Flags: `--only S1,S6` (comma-separated IDs), `--keep-downloads` (don't delete
the temp dir), `--results-doc <path>` (write a markdown results table).

## Cost, rate, and network caveats

- **It costs money**: every scenario spends Anthropic API tokens (small model,
  but real spend).
- **It hits third parties**: real Library Genesis mirrors, Anna's Archive, Unpaywall, and
  Sci-Hub. These are flaky and rate-limited; results vary run to run. Download
  scenarios that correctly select the tool/source but fail on a dead mirror are
  reported as **SKIP**, not FAIL.
- **S32–S35 depend on a pinned fixture** (`test/e2e/testdata/escalation_item.json`):
  an item Anna's carries and the Library Genesis catalog does not. If the catalog
  later absorbs it, re-pin with the commands in `plan/2026-07-24-extra-search-sources.md`.
- **It downloads files**: into an `os.MkdirTemp` directory (removed on exit
  unless `--keep-downloads`). Downloads are capped at 25 MiB
  (`LIBGEN_MCP_MAX_DOWNLOAD_BYTES`) and confined to that temp dir
  (`LIBGEN_MCP_DOWNLOAD_DIR`), both set before the server config loads.
- The process exits non-zero if any scenario **fails** or **errors** (skips and
  passes do not).
