# libgen-mcp — Development Context

Guidance for working on this repository. Read it before adding a tool, a
download source, or a docs page; it records the conventions the quality gates
enforce and the architecture decisions behind them.

## Project Overview

libgen-mcp is a Model Context Protocol server, written in Go, that exposes
Library Genesis (and a set of open-access fallbacks) to an LLM client. It is
**keyless by default**: every core capability works with no account, no API key,
and no configuration. Keys are strictly opt-in and, when supplied per call, are
used once and never persisted.

The server presents a deliberately small surface — four tools plus a handful of
prompts:

- `search` — search the LibGen catalog, escalating to Anna's Archive and the
  open-access providers (arXiv, Crossref, OpenLibrary) when configured or when
  the catalog comes up empty.
- `get_details` — full metadata for a record by md5, edition/file id, or DOI,
  with optional keyless enrichment (Crossref/OpenLibrary).
- `download` — resolve and download a book (by md5) or article (by DOI) through
  an ordered source chain with transparent failover.
- `read` — extract text, search within, and outline a downloaded PDF/EPUB/TXT.

The module path is `github.com/jmrplens/libgen-mcp` (no major-version suffix).
The single source of truth for the version is the `VERSION` file.

## Project Structure

```text
cmd/
  server/               # The MCP server entrypoint (stdio + HTTP transports)
  probe/                # Standalone mirror/diagnostics CLI
  audit_tokens/         # Reports the token footprint of the tool definitions
  audit_surface_quality/# Fails if the tool surface breaks a quality convention
  godoc_tool/           # Go doc-comment auditor + fixer (audit | fix)
  format_md_tables/     # Normalizes Markdown pipe tables (--check in CI)
  gen_llms/             # Generates llms.txt / llms-full.txt (--check in CI)
  gen_eval_pages/       # Regenerates the evaluator results docs (--check in CI)
  eval/                 # Live LLM-driven eval harness (build tag: eval, gated)
internal/
  config/               # Env-var configuration (LIBGEN_MCP_*), KnownSources
  discovery/            # Open-access + Anna's search providers; Federate()
  extract/              # PDF/EPUB/TXT text extraction, in-doc search, outline
  libgen/               # LibGen client: search, details, enrich, download chain
  logging/              # slog setup
  mirrors/              # Mirror discovery, health, rotation
  prompts/              # MCP prompt definitions (acquire_book, research_topic, …)
  tools/                # MCP tool registration, handlers, schemas, Markdown render
test/e2e/               # Opt-in live end-to-end suite (build tag: e2e)
docs/                   # English developer docs (source of truth for prose)
site/                   # Starlight docs site (EN + ES parity)
```

### Package roles worth knowing

- `internal/tools` is where the four tools are wired onto the server
  (`tools.Register`). Input/output types, their `jsonschema` field descriptions,
  the handlers, and the Markdown renderers all live here.
- `internal/libgen` owns the download pipeline. Sources are pluggable via the
  `DownloadSource` interface; the ordered chain is assembled in
  `Client.buildSourceChain`.
- `internal/discovery` owns keyless search beyond the catalog via the `Provider`
  interface, fanned out concurrently by `Federate`.
- `internal/config` defines every `LIBGEN_MCP_*` environment variable and the
  canonical `KnownSources` list.

## Build & Test Commands

Everything is driven through the `Makefile`; run `make help` for the full list.

```bash
make build            # Build dist/libgen-mcp
make run              # Run the server on stdio
make test             # go test with a coverage profile (./cmd/... ./internal/...)
make test-race        # Race detector
make cover-check      # Fail if internal/ coverage < COVERAGE_MIN (85%)
make lint             # golangci-lint + govulncheck
make analyze          # Full static-analysis sweep
```

Coverage is scoped to `./internal/...` (`COVERAGE_PKGS`), so command packages
under `cmd/` are not counted toward the 85% floor — but they must still have
tests for their core logic (see `cmd/audit_tokens` and `cmd/audit_surface_quality`
for the pattern).

## Key Development Patterns

### Adding a new MCP tool

Tools are registered in `internal/tools/tools.go` inside `Register` via
`mcp.AddTool`. Each tool needs:

1. An input struct and an output struct. **Every** JSON field carries a
   `jsonschema:"…"` description, and optional fields use `,omitempty` in the
   **json** tag — that is also what makes a field optional, since jsonschema-go
   derives `required` from the absence of `omitempty`, not from anything in the
   jsonschema tag.
2. A `Name` (see naming below), a `Title`, a `Description`, and `Annotations`
   (`ReadOnlyHint` for read-only tools, `DestructiveHint`/`IdempotentHint` for
   writes, `OpenWorldHint` when it reaches the network).
3. A handler wrapped with `withRecovery("<name>", handler)` so panics become
   `IsError` tool results and every call is metered.

**The jsonschema tag is a description, not a DSL.** `jsonschema-go` assigns the
entire tag string to the property's `description` and parses no directives out
of it — a trailing `,enum=a,enum=b` or `,required` constrains nothing and ships
as literal text to every client and model. A constrained field needs an explicit
`InputSchema`: infer the struct's schema, then pin the enum on it, as
`searchInputSchema` and `downloadInputSchema` do. Source the values from
wherever they are validated (`internal/libgen`, `internal/config`) rather than
restating them, so the schema and the validator cannot disagree.

`make audit-surface-quality` enforces points 1–2 over a real `tools/list`
round-trip: it fails if any field lacks a description, any enum is empty, any
description carries an unparsed struct-tag directive, or a tool is missing its
Title/Annotations/description.

### Tool naming convention

Tool names are plain snake_case verbs or verb-nouns with **no vendor prefix**:
`search`, `get_details`, `download`, `read`. The surface is small enough that a
prefix would only add noise. Prefer few, general tools over many narrow ones
(see Architecture Decisions below).

### Adding a download source (the `DownloadSource` seam)

A source implements `internal/libgen.DownloadSource`
(`Name`, `Supports(Item)`, `Resolve(ctx, Item) (Resolved, error)`). To wire one
in:

1. Add its name to `config.KnownSources` (this also gates
   `LIBGEN_MCP_SOURCES`).
2. Add a factory entry in `Client.buildSourceChain` keyed by that name.
3. If the source needs a credential, follow the opt-in-key pattern: keyless by
   default, with an optional per-call secret (see `withPerCallAnnas` /
   `withPerCallUnpaywall`) that is used once and never stored.

`Download` tries each supporting source in chain order and fails over to the
next; keep `Resolve` returning an error (not a partial result) when it cannot
serve an item.

### Adding a discovery provider (the `Provider` seam)

An open-access searcher implements `internal/discovery.Provider`
(`Name`, `Search(ctx, query, limit)`). Register it in `DefaultProviders` (or
`ExtraProviders` for beyond-catalog searchers). `Search` must be best-effort:
return only context errors, and degrade every other failure to an empty slice so
one slow provider never sinks the federated result.

### Error handling in handlers

Handlers return `(*mcp.CallToolResult, Out, error)`. Return a real `error` for
unexpected failures; the `withRecovery` wrapper also converts panics into
`IsError` results. Human-readable Markdown output is built with `markdownResult`.

### Escaping untrusted content

Record titles, authors, and any other externally-sourced text are **untrusted**.
When rendering them into Markdown, always pass them through `mdCell` (table
cells) or `fencedBlock` (code blocks) from `internal/tools/markdown.go`. Never
interpolate external strings into Markdown directly.

### Doc comments

Every exported (and, per the audit config, every) declaration needs a godoc
comment that starts with the identifier's name. `main` packages get exactly one
package comment starting with the word `Command` (and a space), placed in
`main.go`; secondary files
in the same package start with a plain comment separated from `package main` by
a blank line so it is not treated as a second package doc.

`go run ./cmd/godoc_tool/ audit --include-tests --fail-on-findings` (also
`make godoc-check`) enforces this, including test files.

## Verification Checklist

All of these must exit 0 before a change is done. Run the Go gates on the
packages you touched, then the doc/surface gates:

```bash
golangci-lint fmt --diff                                   # formatting (no diff)
golangci-lint run                                          # lint
go vet ./...                                               # vet
go run ./cmd/godoc_tool/ audit --include-tests --fail-on-findings
go test ./...                                              # unit tests
go test -race ./...                                        # race detector
make cover-check                                           # internal/ >= 85%
make check-md-tables                                       # Markdown tables normalized
make check-llms                                            # llms.txt fresh + valid
make check-doc-links                                       # local doc links resolve
make audit-surface-quality                                 # tool surface conventions
```

Complexity budgets are enforced by golangci-lint: `gocyclo` min-complexity 20,
`gocognit` 25, `nestif` 5. Keep functions under these; factor helpers out rather
than raising the thresholds.

**SonarCloud is stricter than the linter.** The project's quality gate
(`jmrplens_libgen-mcp2`) enforces **cognitive complexity ≤ 15** per function
independently of `.golangci.yml`, so a function that `gocognit 25` happily passes
locally can still fail the gate on the PR. Treat 15 as the real budget for
cognitive complexity; the linter only catches the worst offenders.

## Documentation Rules

Docs are **bilingual and kept in parity**:

- `docs/` is English only and is the source of truth for developer prose.
- `site/src/content/docs/` carries the published Starlight docs in English, and
  `site/src/content/docs/es/` mirrors each page in Spanish. A page added or
  materially changed in one language must be updated in the other.
- `README.md` tables and `docs/` tables are normalized by
  `go run ./cmd/format_md_tables/` (`make check-md-tables` verifies).
- `llms.txt` / `llms-full.txt` are generated from the registered tools by
  `go run ./cmd/gen_llms/`; regenerate them whenever the tool surface changes
  (`make check-llms` verifies freshness).
- Architecture Decision Records live in `docs/decisions/`.

## Testing

**Unit tests** run offline with `go test ./...`. HTML fixtures live in each
package's `testdata/`.

**End-to-end** tests hit the real site and are double-gated: they need the `e2e`
build tag **and** `LIBGEN_E2E=1`:

```bash
LIBGEN_E2E=1 go test -tags e2e ./test/e2e/    # or: make test-e2e
```

They are **run by hand while developing**, not on a schedule. Nothing in CI
executes them: a suite pointed at live third-party mirrors reports their outages
as much as our regressions, and triaging that daily costs more than it returns.
`cmd/probe` is the quicker check that every route still works against the real
mirrors; a genuine breakage otherwise surfaces from use.

The suite loads the repo-root `.env` itself, so either invocation above — and an
IDE running a single test — picks up `LIBGEN_MCP_UNPAYWALL_EMAIL`,
`LIBGEN_MCP_CORE_KEY` and `LIBGEN_MCP_ANNAS_KEY`. Anything already exported wins
over the file. It prints which of them it found before the first test, because a
missing credential turns real coverage into a skip and a partial run otherwise
looks exactly like a full one: without the CORE key that source is out of the
chain entirely, and without the Anna's key its case exercises keyless IPFS
instead of the member fast-download.

**Eval** is a live, LLM-driven harness under `cmd/eval`, gated behind the `eval`
build tag plus `LIBGEN_EVAL=1` and `ANTHROPIC_API_KEY` (real API, mirrors, and
downloads — never runs in ordinary CI):

```bash
LIBGEN_EVAL=1 ANTHROPIC_API_KEY=sk-... go run -tags eval ./cmd/eval
```

## Architecture Decisions

- **Keyless ethos.** Search, details, and downloads all work with zero
  configuration. This is a hard product constraint, not a default — features are
  designed to have a keyless path first.
- **Opt-in keys, used once.** Credentials (Anna's Archive membership, Unpaywall
  email) are optional. A server can configure them, or a client can supply one
  per call; a per-call secret is used for that request only and never persisted.
- **Few, general tools.** The surface is four tools by design. New capability is
  added by deepening existing tools (new sources, new providers, new arguments)
  before adding a new tool.
- **Catalog-first, then federate.** Search consults the LibGen catalog first and
  only reaches Anna's Archive + open-access providers per the `extra_sources`
  policy (`auto` / `always` / `never`), so the common path stays fast.
- **Source-agnostic download pipeline.** The download code knows nothing about
  individual providers; each is a `DownloadSource` in an ordered failover chain.
- See `docs/decisions/` for the recorded ADRs (source & capability scope, known
  limitations).

## Release Process

The version lives in `VERSION` and is mirrored into four JSON manifests plus the
`fly.toml` build arg. To cut a release:

1. Bump `VERSION`.
2. Update the version in `server.json` (both `.version` and the six release-asset
   URLs), `mcpb/manifest.json`, `lhm.plugin.json`, `.plugin/plugin.json`, and the
   `[build.args] VERSION` in `fly.toml`.
3. Run `make check-manifests`. It gates all five against `VERSION`, and CI runs
   it in the `server.json` job. Add any new version-bearing manifest to
   `VERSION_MANIFESTS` in the `Makefile` — a file that is not listed there is not
   gated, and will silently ship the previous release's number.
4. Open a PR; once merged, tag `vX.Y.Z` on main to trigger the release.

The `server.json` CI job is named for a required status check in the branch
ruleset, not for its scope — do not rename it without updating the ruleset too.

## Commit & PR Conventions

- Conventional Commits in a plain **developer voice** describing the change
  itself: `chore(tooling): …`, `feat: …`, `fix: …`, `perf: …`, `test: …`.
- Commit messages and PR text describe *what changed and why* — never process,
  tooling-assistants, plans, or audits.
- Follow `.github/pull_request_template.md` for PR descriptions.

## Gotchas

- **Root binaries.** `go build ./cmd/<x>` drops the binary in the repo root
  (e.g. `./gen_eval_pages`). These are gitignored, but **never** `git add -A` —
  stage files explicitly so a stray binary or `.env` is never committed. Prefer
  `go run ./cmd/<x>/` over building when you just need to run a tool.
- **Full surface for audits.** The audit tools force every source on
  (`cfg.Sources = nil`, plus a placeholder for every credential-gated source —
  Unpaywall email and CORE key) so their output is deterministic regardless of
  the ambient environment. A new credential-gated source must add its placeholder
  to **both** `cmd/gen_llms` and `cmd/audit_tokens`, or the committed llms files
  and the token figure will differ between a machine that holds the credential
  and one that does not, and `make check-llms` will pass or fail by accident.
