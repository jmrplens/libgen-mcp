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
| S23 | **In-document search** — asks to search _inside_ a book, so the model must call `read` with a `find` argument instead of downloading the whole file |
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
| S38 | **A never deployment is a lock** — the server default is `never` and the prompt is a known catalog miss; graded on the extras staying out of the results _and_ on the model reporting the miss instead of inventing one |
| S39 | **An always deployment forces the extras** — an ordinary query the catalog answers well; extra-origin hits can only be there because the deployment default forced them |
| S40 | **Read an escalated item** — the strictest of the escalation checks: search, the Anna's download path, the file type and text extraction all have to hold for the model to quote a passage |
| S41 | **Anna's membership opt-in** — the prompt mentions having an account without naming `annas_member`, so the model must discover the argument; the key itself arrives through elicitation and is never stored |
| S42 | **Nothing exists by that name** — a book and an author invented for this test, so every call comes up empty and the only right answer is saying so; graded on the admission *and* on no ISBN or page count appearing anyway |
| S43 | **A restricted deployment holds** — `LIBGEN_MCP_SOURCES` permits the catalog only, so the DOI download must be refused; graded on the refusal and on nothing outside the list having served the file, whichever route the model then finds |
| S44 | **Pagination** — asks for the second page of results, so the model must discover the `page` argument rather than re-running the same search or continuing the list from memory |

**Guided vs. unguided.** S1–S9 spell out the collection / fields / source to exercise a specific path deterministically. S10–S13 are deliberately **under-specified** — the prompts read like a real user and give no such guidance, so they test whether the model can discover the right tool arguments from the tool and field descriptions alone. They are a proxy for how well the server self-describes to an unguided LLM; a live mirror miss is a SKIP, the model's argument choice still graded.

S6 / S6b are the reason this harness exists alongside the older checks: the
`download` tool takes an optional **`source`** argument, and these scenarios
assert the model actually sets it (and that `DownloadResult.Source` matches when
the live fetch succeeds).

**Source availability and degraded runs.** The external sources are not equally
reliable — **libgen** (S5) and **unpaywall** (S7) are dependable, **sci-hub** (S6)
mirrors are volatile and carry only _paywalled_ papers, and **randombook** (S6b)
rediscovers fresh mirrors each run — so a download that the model set up correctly
can still fail to produce bytes.

That does **not** make the scenario unevaluable, and it is not a SKIP. When the
live payload does not arrive, the model still has one move left, and it is the
move worth watching: claiming a result it never received. Those runs are graded on
whether the answer says plainly that nothing came back. A scenario that skips
routinely is not testing anything, so a SKIP is reserved for the two cases where
there is genuinely nothing to grade — a capability the deployment has not
configured (S41 without a membership key), and a model that ran out of turns
before answering.

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

## The run record

`--record <path>` writes a JSONL file with one object per scenario holding
everything the run produced, not only what the assertions looked at. An assertion
can check only what someone thought to check; the record is what makes the
unthought-of visible afterwards, without paying for another live run. `make eval`
writes `eval-record.jsonl` (gitignored) by default.

Each object carries:

| Field | What it holds |
| --- | --- |
| `prompt`, `setup_env` | exactly what was asked, and under which configuration |
| `tools_offered` | the tool surface the model was shown — names, descriptions, input schemas. Recorded per scenario because it genuinely differs: remote mode describes `download` differently, and a description is all a model has to work from when discovering an argument |
| `turns[]` | every model reply: the prose it wrote (including intermediate narration, often where a wrong turn is first visible), the tools it asked for with their arguments, the stop reason, token counts and latency |
| `calls[]` | every executed call: arguments in; and out, both channels the model reads — the Markdown `text` and the `structured` output — plus `duration_ms` |
| `calls[].server_logs` | what the MCP server logged internally while serving that call, at DEBUG: mirror attempts, failover, retries, source-chain decisions. Calls run sequentially, so each call's own lines are attributed to it |
| `elicitations[]` | every prompt the server raised back at the host, its text, and how the host answered |
| `progress[]`, `fetched[]` | the notification stream, and what the harness pulled from resolve-only links |
| `final_answer` | what the model finally told the user |
| `status`, `detail` | how the assertion graded it |

Useful starting points:

```bash
# Every scenario that failed, with the reason
jq -r 'select(.status=="FAIL") | "\(.id): \(.detail)"' eval-record.jsonl

# What the model was told about extra_sources
jq -r '.tools_offered[] | select(.name=="search") | .input_schema.properties.extra_sources.description' eval-record.jsonl | head -1

# Where the time went
jq -r '"\(.id) \(.duration_ms)ms"' eval-record.jsonl | sort -k2 -n -r | head

# What the server did internally on a slow call
jq -r 'select(.id=="S40") | .calls[] | select(.duration_ms>10000) | .server_logs[]' eval-record.jsonl
```

## Re-grading a recorded run

An assertion is a pure function of a transcript, and the record holds the whole
transcript — so `--regrade` re-runs every assertion against a past run instead of
calling anything live:

```bash
go run -tags eval ./cmd/eval --regrade eval-record.jsonl
```

It makes no network calls, spends no API credit and no download quota, needs no
gating, and finishes in a second. Outcomes that changed are marked `(was PASS)` /
`(was FAIL)`, so the effect of an assertion change is visible at a glance. Pass
`--results-doc` alongside it to regenerate the results table from the re-grade.

It is valid for a change to the **assertions only**. Changing the server, the
tools or a prompt changes what a live run would produce, and no amount of
re-grading an old record will show that — it needs a real run.

The record has to be faithful for this to mean anything, which is a property worth
checking rather than assuming: run one scenario live with `--record`, then
`--regrade` that record, and the two outcomes should be identical down to the
message.

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
