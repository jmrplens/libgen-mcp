# Contributing to libgen-mcp

Thank you for your interest in contributing to libgen-mcp! This guide covers the
process for building the project, submitting changes, reporting issues, and
following project conventions.

By participating, you agree to abide by the [Code of Conduct](CODE_OF_CONDUCT.md).
For security issues, please report privately via
[GitHub Security Advisories](https://github.com/jmrplens/libgen-mcp/security/advisories/new)
instead of opening a public issue.

## Table of Contents

- [Getting Started](#getting-started)
- [Project Layout](#project-layout)
- [Building and Testing](#building-and-testing)
- [Coding Standards](#coding-standards)
- [Documentation](#documentation)
- [Branch Naming](#branch-naming)
- [Commit Messages](#commit-messages)
- [Pull Requests](#pull-requests)
- [Issue Reporting](#issue-reporting)

## Getting Started

libgen-mcp is a Go MCP server that needs **no account, token, or credentials** —
Library Genesis and the article sources it uses are all public. Getting a
development build running is just:

1. Clone the repository: `git clone https://github.com/jmrplens/libgen-mcp`
2. Build the server: `make build` (produces `dist/libgen-mcp`)
3. Run the tests: `make test`

### Prerequisites

- **Go 1.26+** — [Download](https://go.dev/dl/). The module is
  `github.com/jmrplens/libgen-mcp`.
- **Static-analysis tools** — `golangci-lint` and `govulncheck`. Install both
  with `make install-tools`.
- **Node.js with Corepack** (only for the docs site) — the site under `site/`
  pins `pnpm@11.8.0` via its `packageManager` field.
- **Network access** (only for the gated end-to-end suite and the live probe).

## Project Layout

```text
cmd/
├── server/            # The MCP server entry point (the libgen-mcp binary)
├── probe/             # Live diagnostic CLI that hits a real mirror
├── godoc_tool/        # Go doc-comment auditor (backs make godoc-check)
├── format_md_tables/  # Markdown pipe-table formatter (backs make check-md-tables)
└── gen_llms/          # Generates llms.txt / llms-full.txt (backs make check-llms)
internal/
├── config/            # Environment-variable parsing and validation
├── libgen/            # Search, details, and the multi-source download chain
├── mirrors/           # Mirror discovery, caching, and failover
├── logging/           # Structured logging to stderr
└── tools/             # MCP tool registration (search, get_details, download, read)
test/e2e/              # Opt-in live end-to-end suite (build tag: e2e)
docs/                  # Markdown guides (getting started, configuration, …)
site/                  # Astro Starlight documentation site (bilingual EN/ES)
mcpb/                  # Claude Desktop .mcpb bundle manifest
```

The server exposes exactly four MCP tools — `search`, `get_details`,
`download`, and `read`. New capabilities are usually additions to
`internal/libgen` (a new download source, a new parser) rather than new tools.

## Building and Testing

Common tasks are wrapped by the `Makefile` (`make help` lists them all):

```bash
# Build the server binary into dist/
make build

# Run all tests with a coverage profile
make test

# Run tests without the coverage profile (faster)
make test-short

# Run all tests under the race detector
make test-race

# Fail if coverage over internal/ drops below the minimum (85%)
make cover-check

# Generate an HTML coverage report (coverage.html)
make coverage
```

### End-to-end tests (gated)

The live suite queries the real Library Genesis site, so it is gated twice: it
requires the `e2e` build tag **and** the `LIBGEN_E2E=1` environment variable, and
it never runs under a plain `go test ./...`. The Makefile target sets both:

```bash
make test-e2e     # LIBGEN_E2E=1 go test -tags e2e -timeout 600s ./test/e2e/
```

If searches or downloads start failing against live mirrors, the probe CLI
reports which routes and parsers still work:

```bash
go run ./cmd/probe
```

### Static analysis

```bash
make lint     # golangci-lint (govet, staticcheck, gosec, …) + govulncheck
make fmt      # apply formatters: goimports, gofumpt, gci
make vet      # go vet
```

`make lint` is the same gate CI runs; keep it clean before opening a PR.

## Coding Standards

- **Idiomatic Go**, formatted by `make fmt` (goimports, gofumpt, gci). CI runs
  `golangci-lint fmt --diff`, so unformatted code fails the build.
- **Doc comments** on all exported types and functions. This is enforced —
  `make godoc-check` fails on missing or malformed doc comments (use
  `make godoc-audit` for a readable report).
- **Error wrapping** with `fmt.Errorf("context: %w", err)`.
- **`context.Context`** threaded through for cancellation and timeouts.
- **Table-driven tests** with `t.Run()` subtests; unit tests must not touch the
  network — use `httptest` to mock mirror responses. The live suite under
  `test/e2e/` is the only code allowed to hit the real site.
- **Clean Markdown tables** — run `make format-md-tables` (CI runs
  `make check-md-tables`) so pipe tables in `README.md` and `docs/` stay aligned.
- **English** for all source, comments, commit messages, and documentation.

## Documentation

- User-facing guides live in [`docs/`](docs/) as plain Markdown. Update them when
  you change configuration, tools, or behavior — in particular
  `docs/configuration.md` when you add or change an environment variable, and the
  environment-variable table in `README.md`.
- The published site lives in [`site/`](site/) (Astro Starlight, bilingual
  EN/ES). To work on it, run `corepack pnpm install` and `corepack pnpm dev`
  inside `site/`.
- `llms.txt` and `llms-full.txt` (LLM-discovery files at the repo root) are
  generated from the registered tools — never edit them by hand. Run
  `make gen-llms` after changing a tool's name, description, or schema; CI runs
  `make check-llms` and fails if they are stale. The site build republishes them
  to `/llms.txt` and `/llms-full.txt` via its `prebuild` step.

## Branch Naming

| Prefix      | Purpose                 | Example                         |
| ----------- | ----------------------- | ------------------------------- |
| `feature/`  | New functionality       | `feature/new-download-source`   |
| `fix/`      | Bug fixes               | `fix/mirror-failover-retry`     |
| `docs/`     | Documentation only      | `docs/clarify-scihub-hosts`     |
| `test/`     | Test additions          | `test/increase-libgen-coverage` |
| `refactor/` | Code restructuring      | `refactor/extract-source-chain` |
| `chore/`    | Build, CI, dependencies | `chore/upgrade-go-toolchain`    |

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```text
<type>(<scope>): <description>

[optional body]

[optional footer(s)]
```

| Type       | Description                                             |
| ---------- | ------------------------------------------------------- |
| `feat`     | New feature or download source                          |
| `fix`      | Bug fix                                                 |
| `docs`     | Documentation changes                                   |
| `test`     | Adding or updating tests                                |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `chore`    | Build process, CI, dependency updates                   |
| `perf`     | Performance improvement                                 |
| `style`    | Code formatting (no logic change)                       |

Use the package name as scope when it helps, e.g. `feat(libgen): add randombook
fallback`, `fix(config): reject out-of-range rate limits`, `docs(readme): update
env var table`.

## Pull Requests

### Before submitting

- [ ] Code builds: `make build`
- [ ] All tests pass: `make test`
- [ ] Coverage holds: `make cover-check`
- [ ] Static analysis is clean: `make lint`
- [ ] Formatting is applied: `make fmt` and `make format-md-tables`
- [ ] LLM-discovery files are current: `make check-llms`
- [ ] Doc comments pass: `make godoc-check`
- [ ] Docs updated if behavior or configuration changed
- [ ] Commit messages follow Conventional Commits

### Process

1. Create a branch from `main`.
2. Make your changes in small, focused commits.
3. Push the branch and open a pull request; fill in the template
   (auto-populated from `.github/pull_request_template.md`).
4. Address review feedback.

Keep PRs small where possible — smaller changes review faster and conflict less.

## Issue Reporting

Open an issue at <https://github.com/jmrplens/libgen-mcp/issues/new>:

- **Bug reports** — reproducible defects. Include the query or `md5`/`doi`, the
  configured mirror or sources, and relevant `LIBGEN_MCP_LOG_LEVEL=debug` output.
- **Feature requests** — new functionality, e.g. a new download source.
- **Documentation** — missing, outdated, or incorrect docs.

For **security issues**, do not open a public issue — report privately via
[GitHub Security Advisories](https://github.com/jmrplens/libgen-mcp/security/advisories/new).
