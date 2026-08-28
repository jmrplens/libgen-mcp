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

`ls cmd/ internal/` gives the layout; every package carries a doc comment saying
what it is, which `make godoc-check` enforces. What that does not tell you:

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
Two things `make help` does not tell you:

`validate-http-stateless` is a hand-run smoke check, not a CI gate: it builds the binary,
serves it, and asserts the wire-level promises of stateless mode (no `Mcp-Session-Id`,
`GET` → 405 with `Allow: POST`, `--json-response` content type). Run it after touching
`internal/transport` or the HTTP wiring in `cmd/server`.

Coverage is scoped to `./internal/...`, `./cmd/server/...` and
`./cmd/internal/...` (`COVERAGE_PKGS`, mirrored into both
`sonar-project.properties` and the CI profile — **all three have to agree**, or a
package ends up counted and uninstrumented, which reports as 0% and is not).
The rest of `cmd/` is not counted toward the 85% floor — but it must still have tests for its core
logic (see `cmd/audit_tokens` and `cmd/audit_surface_quality` for the pattern).

`cmd/server` was excluded until 2026-08-27, on the premise that it was thin
wiring. That premise expired: it now decides cross-origin access, answers both
preflights and mounts the middleware chain on the request path, which is the
code most worth measuring. **Excluding a package from the metric hides more than
a number** — `cmd/gen_tool_schema` shipped with no test file at all and nothing
reported it, because the rule above is prose and the exclusion was
configuration. When adding a command, check it appears in a coverage report
somewhere, not only that you remember writing tests.

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
4. **If `Supports` gates on a DOI registrant prefix, add a probe to
   `articleProbes` in `internal/libgen/client.go`.** `EnabledSourceNames` decides
   which sources the download tool advertises by offering each one a probe DOI,
   so a prefix-gated source with no probe of its own runs in the chain and is
   **absent from the tool's `source` enum** — reachable by the server, invisible
   to the model. `rfc` and `nist` are the worked examples.
5. **Document it on the sources page, not in the architecture table.**
   Per-source detail — corpus, resolve mechanics, what the source does not
   cover, the traps you measured, whether it is keyed, and any crawl rule it
   observes — lives in `docs/sources.md` and its two Starlight twins
   (`site/src/content/docs/sources.mdx` and `es/sources.mdx`). The table in
   `docs/architecture.md` (and its twins) is a three-column index plus a
   one-line summary and must stay that way; a source's prose belongs in exactly
   one place, or the two copies drift.

`Download` tries each supporting source in chain order and fails over to the
next; keep `Resolve` returning an error (not a partial result) when it cannot
serve an item.

### Adding a discovery provider (the `Provider` seam)

An open-access searcher implements `internal/discovery.Provider`
(`Name`, `Search(ctx, query, limit)`). Register it in `DefaultProviders` (or
`ExtraProviders` for beyond-catalog searchers). `Search` must be best-effort:
return only context errors, and degrade every other failure to an empty slice so
one slow provider never sinks the federated result.

### Adding or editing an icon

Icons live in `internal/toolutil/icons.go`. Each is a three-entry `[]mcp.Icon`:
the hand-authored `currentColor` SVG plus a light/dark 16×16 WebP pair, because
a client can support icons and still reject `image/svg+xml` (VS Code Copilot's
MIME allowlist does exactly that). To add one:

1. Add an `svg<Name>` constant — `currentColor` only, no hardcoded fill, so the
   SVG entry adapts to any client theme and the generator can recolor it.
2. Add `IconName = icon("<name>", svg<Name>)` to the `var` block. The string
   must be the constant's suffix **lowercased with non-alphanumerics stripped**
   (`svgAcquireBook` → `"acquirebook"`); that is the key
   `cmd/gen_icon_webp` writes the asset filenames under, and a mismatch panics
   at startup rather than shipping a broken icon.
3. Run `make gen-icon-webp` and commit the generated `.webp` files.

The entry order — SVG, light WebP, dark WebP — is a **published contract**, documented in
`docs/architecture.md` § Icons and pinned entry by entry in
`internal/toolutil/icons_test.go`. Consumers may read `icons[0]` positionally, and the server
card republishes the arrays verbatim, so reordering `icon()` is a breaking change to a public
surface, not a refactor.

The generator needs `cwebp` (libwebp) and `rsvg-convert` on `PATH`, and the
**librsvg version is part of the requirement, not a detail**: the assets are
compared byte for byte, and librsvg's stroke antialiasing changed between 2.54
and 2.58. Debian 12's 2.54.7 disagrees with the committed assets on three of the
nine icons — the three drawn with rounded caps and joins on diagonals — while
2.58.0 (Ubuntu 24.04) and 2.60.0 (Debian 13) each reproduce all eighteen files
exactly, across two different `cwebp` releases. `minLibrsvg` in
`cmd/gen_icon_webp` holds that floor; the Makefile does not restate it.

So `make gen-icon-webp` and `make check-icon-webp` do not assume the local
librsvg is usable. They ask the tool (`--probe`), run natively when it can
reproduce the bytes, and otherwise fall back to a pinned container
(`ICON_IMAGE`, tagged from `go.mod` so it cannot drift behind the toolchain).
Either way every machine emits identical assets, which is the point — pinning
the renderer beats chasing each host's version.

The guard covers **generating**, not just verifying: run under an old librsvg
the generator would not fail, it would rewrite all eighteen files in its own
dialect and the divergence would be committed. For the same reason, never
"fix" a failing check by regenerating on a machine below the floor — that pins
the assets to the one renderer that does not agree.

It is **maintainer-only and not a CI gate** — the assets are committed, so
ordinary builds never invoke it, and no workflow installs librsvg or cwebp,
which is why `TestRun_CheckModeAcceptsCommittedAssets` skips in CI. It skips on
a below-floor machine too, so `go test ./...` stays green there; the real
verification is `make check-icon-webp`, by hand after touching an icon.

**Look at the rendered result before committing.** A hand-written SVG path that
parses is not necessarily a shape that reads at 16×16, and the tests can only
catch malformed XML and a wrong image size — not a glyph that renders as a
smudge.

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
golangci-lint run --build-tags e2e,eval ./...              # lint, ALL build tags
go vet ./... && go vet -tags e2e ./... && go vet -tags eval ./...
go run ./cmd/godoc_tool/ audit --include-tests --fail-on-findings
go test ./...                                              # unit tests
go test -race ./...                                        # race detector
make cover-check                                           # internal/ >= 85%
make check-md-tables                                       # Markdown tables normalized
make check-llms                                            # llms.txt fresh + valid
make check-lhm-manifest                                    # lhm.plugin.json matches the surface
make check-doc-links                                       # local doc links resolve
make audit-surface-quality                                 # tool surface conventions
cd site && pnpm run lint                                   # the docs site, if you touched it
npx --yes markdownlint-cli2 "**/*.md"                      # CI-only gate, no make target
make check-icon-webp                                       # only if you touched an icon (needs librsvg + libwebp)
```

**`make` does not cover everything CI runs.** Three gates have no `make` target
and so pass locally by not being run: `markdownlint-cli2` over every `*.md` (the
`Analyze Markdown` job — MD024 forbids two headings with the same text in one
file, which an ADR accumulating amendments trips easily), the docs site's own
chain, and the `CodeQL` workflow.

**CodeQL is advanced setup, defined in `.github/workflows/codeql.yml`.** GitHub's
default setup is deliberately left off: it pins `GOTOOLCHAIN=local` to whatever
Go the CodeQL runtime ships, so the Go job breaks every time `go.mod` moves to a
release the runtime has not picked up yet. The workflow installs the toolchain
with `setup-go` and uses `build-mode: manual` for Go instead. Bump its
`GO_VERSION` alongside the ones in `ci.yml` and `release.yml`, and do not enable
default setup in the repository settings — the two cannot coexist.

**`make` does not cover the docs site.** `site/` has its own gate chain —
`astro check`, i18n parity, the PRIVACY.md sync check, eslint, prettier,
html-validate and htmlhint — run by the `Docs Site` job and by `pnpm run lint`
in `site/`. Two of its checks fail on changes that every Go and Markdown gate
above passes happily: **prettier** rewrites a Markdown table's separator widths
when a row grows, and **`privacy:check`** fails whenever `PRIVACY.md` changes
without its digest being re-stamped (run `node scripts/sync-privacy.mjs`, then
review the Spanish page and copy the new digest into its `privacySource`).
Because the chain is `&&`-joined, the first failure hides the rest — so re-run
it to completion after fixing one.

**`js-yaml` is pinned in `site/package.json` for Starlight, not for us.** No file
in this repo imports it. It is there because `@astrojs/starlight` ships
TypeScript source (`utils/translations-fs.ts` does `import yaml from 'js-yaml'`,
`schemas/head.ts` too), which Vite compiles into our bundle and leaves as a bare
external — so it resolves at runtime from `site/node_modules`, *our* tree, not
Starlight's. That makes the root pin the version Starlight actually gets.

**It must stay inside Starlight's declared range (`^4.1.1`)**, and keep the
version exact (no caret) so a range bump cannot drift across the major. Reject
any bot PR that moves it to 5.x while Starlight still declares `^4.1.1` — being
outside the range its own dependency declares is the reason on its own, no build
run needed.

Two details, because the obvious explanation is wrong and sends you down a dead
end. First, it is **not** that "js-yaml 5 dropped the default export": v4's ESM
build has no `export default` either. `import yaml from 'js-yaml'` works today
because the CJS path (`require` → `index.js`) reassigns `module.exports`, which
Node/Vite interop hands over as the default; v5's CJS build emits
`exports.load = …` per-name instead, and that synthesis is what stops. Second,
that part is fixable from the outside (a Vite `resolve.alias` to a shim adding a
default export) — **do not**: v5 is an API rewrite, not a repackaging. It drops
`safeLoad`, `safeLoadAll` and `types` and adds a new AST/visitor surface. The
two calls Starlight actually makes (`load(content, {filename})`, `dump(…)`) do
survive, so a shim would appear to work while leaving us maintaining a patch to
somebody else's transitive dependency, silently breaking the day Starlight
imports anything else from it.

A plain `golangci-lint run` skips every tagged file, which is how the whole
`cmd/eval` harness went unanalyzed until 2026-07-30. `make lint` passes
`GO_ANALYSIS_TAGS` (`e2e,eval`) for this reason — add any new build tag there.

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
- The `tools` and `prompts` arrays in `lhm.plugin.json` are generated the same
  way by `go run ./cmd/gen_lhm_manifest/` (`make check-lhm-manifest` verifies).
  See the LobeHub section under Release Process for why they exist at all.
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

**HTTP end-to-end** (`test/e2e/http/`) starts the real binary and drives it over
a socket, behind the `httpe2e` build tag:

```bash
make test-e2e-http
```

It exists because the handler chain — `newHTTPHandler`, `browserCORS`,
`crossOriginProtected`, `sseNoBuffering` — is assembled in `package main` and
cannot be imported, so a unit test would be testing its own reassembled copy
rather than the binary that ships. Every behavior in it is configuration-
dependent by nature: the same request must be refused with one flag and accepted
with another.

Unlike the live suite above, **this one runs in CI on every PR**, because it
depends on nothing external. It is also the release gate: `release.yml`'s
`http-e2e` job is a `needs:` of both GoReleaser and Docker, so a transport
regression stops everything the tag would have produced. It cannot stop the tag
itself — `release.yml` triggers on the tag push, so by the time the gate runs the
tag exists — but nothing is built, pushed or published behind a failing gate. That
job deliberately declares `contents: read` and no secrets — a gate that fails
must not be able to leak what the jobs behind it hold.

Two things about it are easy to get wrong:

- **`proxy_test.go` runs a real nginx in Docker and skips without it.** It is
  not modeled, because the bug it exists for — the server's CORS headers and the
  proxy's colliding into a response a browser rejects and `curl` reports as
  `200` — only appears when a real proxy adds real headers. It also pins the
  trap that hid it: with nginx answering `OPTIONS`, the preflight is `204` no
  matter what the server behind it would say.
- **The robustness cases assert survival, not correctness.** The bar is that the
  process keeps serving, so every case ends by asking `/health`. That includes
  the misbehaving-mirror suite, which is this repository's own addition: every
  tool call reaches an upstream, so a mirror that is slow, broken or hostile is
  a live input rather than a hypothetical.

**Eval** is a live, LLM-driven harness under `cmd/eval`, gated behind the `eval`
build tag plus `LIBGEN_EVAL=1` and `ANTHROPIC_API_KEY` (real API, mirrors, and
downloads — never runs in ordinary CI):

```bash
LIBGEN_EVAL=1 ANTHROPIC_API_KEY=sk-... go run -tags eval ./cmd/eval
make eval-only ONLY=S61,S62   # re-run named scenarios and publish just those
```

A run **merges** into its results doc rather than replacing it, so re-measuring a
scenario does not cost a full suite. Each row carries the date it was measured,
and merging a run from a different model is refused — one pass rate built from
two models invites a comparison it cannot support. Adding a scenario means a row
in `cmd/eval/README.md` (the catalog the pages are generated from) **and** a
Spanish entry in `scenariosES`, or the generator fails.

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

Cutting a release is a multi-step sequence with two gates that catch different
things and a registry rule that has already broken one publish. It lives in the
`release` skill (`.claude/skills/release/SKILL.md`) — invoke it when bumping the
version, tagging, or publishing to the MCP registry, npm or LobeHub.

### The npm channel

`npm/libgen-mcp/` is the **committed** launcher package (`@jmrp.io/libgen-mcp`):
`package.json`, `cli.js` and a README, and nothing else. The six per-platform
packages that carry the binaries are **generated** from the release assets by
`scripts/build-npm.mjs` at publish time and are gitignored — `npm/packages/`
never enters a commit.

Three things about it are easy to get wrong:

- **`cli.js` lives at the package root, not in `bin/`.** The repo's `.gitignore`
  has a global `bin/` rule, so a launcher under `npm/libgen-mcp/bin/` would be
  silently untracked and every published tarball would be missing its entry
  point.
- **The launcher must never write to stdout.** The server speaks MCP over stdio,
  so a stray byte there is a corrupted JSON-RPC stream, not a cosmetic wart. It
  spawns the binary with `stdio: "inherit"`, prints only to stderr, and mirrors
  the child's exit code and terminating signal. `scripts/validate-npm.mjs` drives
  a real `initialize` handshake and asserts every stdout line parses as JSON-RPC
  2.0, so a regression fails `make validate-npm` rather than a user's client.
- **The version is stamped, not hand-edited.** `build-npm.mjs --sync-only`
  rewrites the version and all six `optionalDependencies` pins together, which is
  why `scripts/update-server-json-sha.sh` calls it rather than editing the JSON:
  a jq edit could move the version and leave the pins a release behind.
  `npm/libgen-mcp/package.json` is in `VERSION_MANIFESTS`, so `make
  check-manifests` fails a bump that skipped `make sync-npm-version`.

The scope is the npm **organization** `jmrp.io`, so packages are
`@jmrp.io/libgen-mcp*`. `npm whoami` returns `jmrpio` (the maintainer's personal
profile) and `npm org ls jmrp.io` lists `jmrpio` as the owner **member** — neither
is the scope, and reading either as one publishes to the wrong place. A granular
token cannot unpublish, so a mis-scoped publish cannot be undone.

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
  `go run ./cmd/<x>/` over building when you just need to run a tool. A new
  command needs its binary name added to `.gitignore`: `gen_eval_pages` was
  missing from it and got committed, and an ignore rule does **not** apply to a
  file already tracked, so the ignore alone would never have caught it.
- **Full surface for audits.** The audit tools force every source on
  (`cfg.Sources = nil`, plus a placeholder for every credential-gated source —
  Unpaywall email and CORE key) so their output is deterministic regardless of
  the ambient environment. A new credential-gated source must add its placeholder
  to **all three** of `cmd/internal/mcpsurface` (`DocsConfig`, shared by
  `gen_llms` and `gen_lhm_manifest`), `cmd/audit_tokens` and
  `cmd/audit_surface_quality`, or the committed llms files, `lhm.plugin.json` and
  the token figure will differ between a machine that holds the credential and
  one that does not, and `make check-llms` / `make check-lhm-manifest` will pass
  or fail by accident.
