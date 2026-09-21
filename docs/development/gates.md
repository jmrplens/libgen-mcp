# The gates

**Reference** — for a contributor whose pull request just went red.

Every automated check this repository runs, what it is actually asserting, and
where a finding comes from. One row per gate.

This page is for the person whose pull request just went red. The rule each
gate enforces is stated where the rule belongs — in `CLAUDE.md`, beside the
code, or in the command's own doc comment; what is here is the map from a
failure to the place that explains it.

**Two things this page is not.** It is not the list of what CI runs — that is
`.github/workflows/ci.yml`, and the `CI verdict` job is what makes every job
required. And it is not generated: a generated record of the gates would go
stale in exactly the same way the gates do, and silently. When a gate is added,
its row is added here in the same change.

## Reading a row

- **Target** — the `make` target. A `check-` prefix means the CI mode: it exits
  non-zero rather than printing a report. Most have a report-only sibling
  without the prefix, which is the one to run while fixing.
- **Runs in** — `CI` for a job that gates every pull request, `hand` for one
  nothing runs for you.
- **What it asserts** — the property, not the mechanism.

## Go correctness

| Target                           | Runs in                                           | What it asserts                                                                                                                                                                                                                                                                                                                                        |
| -------------------------------- | ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `make test`                      | CI (`Test`, plus `Unit suite` on three platforms) | The unit suite passes offline, on Linux, macOS and Windows.                                                                                                                                                                                                                                                                                            |
| `make test-race`                 | CI (`race.yml`, weekly and at release)            | The suite passes under the race detector. It is deliberately not on the pull-request path: the suite is slow enough under it that every pull request would pay for a class of defect most changes cannot introduce.                                                                                                                                    |
| `make cover-check`               | CI (`Test`)                                       | Coverage over `./internal/...` and `./cmd/...` — everything this module builds — is at or above `COVERAGE_MIN` (90). The scope is mirrored in `sonar-project.properties` and the CI profile, and all three have to agree. `cmd/eval` is the one exclusion, and it is measurement rather than policy: its files are behind a build tag CI does not set. |
| `make coverage-conditions PKG=…` | hand                                              | Every boolean in a package that is never evaluated both ways, through gobco. A line reported is a missing test case: each operand of an and, an or and a not counts separately.                                                                                                                                                                        |
| `make coverage-mutants PKG=…`    | hand                                              | Mutants of a package that no test killed, through gremlins. The gate on a package a change touches is `Lived 0` and `Not covered 0`. Not a CI job and not on the platform matrix: one package takes minutes.                                                                                                                                           |
| `make lint`                      | CI (`golangci-lint`)                              | `golangci-lint` passes under **every** build tag (`e2e,eval,httpe2e,stdioe2e`). A plain `golangci-lint run` skips every tagged file.                                                                                                                                                                                                                   |
| `make vet`                       | CI (`golangci-lint`)                              | `go vet` passes.                                                                                                                                                                                                                                                                                                                                       |
| `make build`                     | CI (`Build`)                                      | The server and `cmd/probe` build, and the live `e2e` suite still **compiles** (`go test -tags e2e -c`) even though nothing in CI runs it. A suite that no longer builds is a suite nobody can run by hand either.                                                                                                                                      |
| —                                | CI (`Type-check (goos/goarch)`)                   | `windows/amd64`, `darwin/arm64` and `linux/arm64` type-check, beside the three-platform matrix rather than instead of it. It costs seconds, and it means narrowing that matrix later can never silently take the compile of `cmd/server/listen_other.go` with it.                                                                                      |
| `make govulncheck`               | CI (`govulncheck`)                                | No known vulnerability reaches this module's call graph.                                                                                                                                                                                                                                                                                               |
| `make godoc-check`               | CI (`godoc`)                                      | Every declaration, test files included, has a doc comment starting with its own name.                                                                                                                                                                                                                                                                  |
| —                                | CI (`SA: SonarCloud`)                             | The SonarCloud quality gate. It is **stricter than the linter**: cognitive complexity 15 per function against `gocognit`'s 25, so a function that passes locally can still fail here.                                                                                                                                                                  |
| —                                | CI (`CodeQL`)                                     | GitHub's semantic analysis, defined in `codeql.yml` as advanced setup. Default setup is deliberately off; the two cannot coexist.                                                                                                                                                                                                                      |

## The transports, end to end

| Target                         | Runs in                                           | What it asserts                                                                                                                                                                                                                     |
| ------------------------------ | ------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make test-e2e-http`           | CI (`Build`)                                      | The real binary, driven over a socket: the handler chain is assembled in `package main` and cannot be imported, so a unit test would be testing its own reassembled copy. Also half the release gate.                               |
| `make test-e2e-stdio`          | CI (`Build`, and `Unit suite` on three platforms) | The real binary over two pipes: stdout carries nothing but JSON-RPC, logs keep their severities on stderr, and a caller-supplied path cannot reach the home directory the client started the server in.                             |
| `make test-e2e`                | hand                                              | The live suite against the real mirrors. Double-gated (`e2e` tag plus `LIBGEN_E2E=1`) and run while developing. Nothing in CI executes it: a suite pointed at third-party mirrors reports their outages as much as our regressions. |
| `make test-e2e-collector`      | hand                                              | A real OpenTelemetry Collector in Docker, reading back what it decoded. A stub answers 200 to anything; this is what tells a valid export from a malformed one.                                                                     |
| `make validate-http-stateless` | hand                                              | The wire-level promises an HTTP deployment makes: no `Mcp-Session-Id`, `GET` on the MCP endpoint is 405, an unknown path is 404 with a JSON body, the security headers, both server-card locations.                                 |

## The served surface

| Target                       | Runs in               | What it asserts                                                                                                                                                                                                                                                                                                  |
| ---------------------------- | --------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make audit-surface-quality` | CI (`Generated docs`) | Every schema field has a description, every enum has values, no description carries an unparsed struct-tag directive, and every tool has its Title, Annotations, description, and an OutputSchema with a root description saying what it returns. Read over a real `tools/list` round-trip, not over the source. |
| `make check-gateway-chars`   | CI (`Generated docs`) | Nothing in `tools/list` or `prompts/list` carries a non-ASCII character or a semicolon. A gateway refused a sibling project over semicolons that were ordinary punctuation; holding the surface to a character class is the only version of clean the next one cannot surprise.                                  |
| `make check-md-escaping`     | CI (`Generated docs`) | No catalog text reaches a Markdown construct without an escaper, and no card row is written by hand instead of through `toolutil.Card`. See `CLAUDE.md` § *Escaping untrusted content*.                                                                                                                          |
| `make check-llms`            | CI (`Generated docs`) | `llms.txt` and `llms-full.txt` are regenerated from the registered tools and structurally valid.                                                                                                                                                                                                                 |
| `make check-lhm-manifest`    | CI (`Generated docs`) | The `tools` and `prompts` arrays in `lhm.plugin.json` match the registered surface. LobeHub reads them straight out of the file; a stale one publishes the previous release's surface.                                                                                                                           |
| `make check-tool-schema`     | CI (`Generated docs`) | `site/src/data/tool-schema.json` matches the registered surface. The docs site renders its reference tables from it.                                                                                                                                                                                             |
| `make audit-tokens`          | hand                  | The context-window footprint of the tool definitions, in tokens. A report, not a gate.                                                                                                                                                                                                                           |

## Tests about tests

These exist because the failures they catch are silent. Each is in `CLAUDE.md`
§ *Testing* with the reasoning.

| Target                       | Runs in     | What it asserts                                                                                                                                                                                                                               |
| ---------------------------- | ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make check-test-file-names` | CI (`Test`) | Every `_test.go` file is named after a module it tests. A theme-named file hides its tests from a reader looking beside the module — and a `coverage_*` name matches a `.gitignore` rule this repository has. Four shapes are exempt.         |
| `make check-test-subtests`   | CI (`Test`) | Every table of cases runs under `t.Run`. `make fix-test-subtests` rewrites the unambiguous ones; read what it did, because wrapping a body moves its `defer` into the subtest.                                                                |
| `make check-test-goroutines` | CI (`Test`) | No `t.Fatal` is called off the test goroutine. `go vet` catches a bare `go func(){t.Fatal()}()` and cannot see that a literal passed to `http.HandlerFunc` crosses the same boundary — which is where almost every assertion here is written. |

## Documentation

| Target                  | Runs in                        | What it asserts                                                                                                                                                                                                                                                                                                                                                                         |
| ----------------------- | ------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make check-md-tables`  | CI (`Build`, `Generated docs`) | Every Markdown pipe table in `README.md` and `docs/` is normalized.                                                                                                                                                                                                                                                                                                                     |
| `make check-eval-pages` | CI (`Build`, `Generated docs`) | The evaluator results pages are generated from `cmd/eval/README.md` rather than hand-edited, in both languages. A scenario added to the catalog without its Spanish entry fails here.                                                                                                                                                                                                   |
| `make check-stats`      | CI (`Generated docs`)          | The README's counts match the source: the tools and prompts from a real registration, the sources and variables from the lists the server reads, the packages and test files from a walk. `make gen-stats` refreshes them, and adding a test file changes a number here — which is the gate working, not a false alarm.                                                                 |
| `make check-doc-names`  | CI (`Generated docs`)          | The documentation names no variable, download source, tool or prompt the server does not have, and every registered tool and prompt is mentioned somewhere. A page may name one on purpose — to say it is *not* read — by declaring it in the page: `<!-- libgen:allow-name NAME: reason -->`. A declaration that excuses nothing is itself a finding. `docs/superpowers/` is not read. |
| `make check-doc-links`  | CI (`Analyze Markdown`)        | Every local Markdown/MDX link and path resolves, anchors included. It reads the index **and** untracked files git does not ignore, so a page written five minutes ago is covered — listing only the index left a new page's links unchecked on exactly the run made to check them.                                                                                                      |
| —                       | CI (`Analyze Markdown`)        | `markdownlint-cli2` over every `*.md`. **No `make` target**, so it passes locally by not being run. MD024 forbids two headings with the same text in one file, which an ADR accumulating amendments trips easily.                                                                                                                                                                       |
| —                       | CI (`Docs Site`)               | The site's own chain: `astro check`, i18n parity, the `PRIVACY.md` digest check, eslint, prettier, html-validate, htmlhint. Run it with `cd site && pnpm run lint`. It is `&&`-joined, so the first failure hides the rest.                                                                                                                                                             |

## Supply chain and release

| Target                                                   | Runs in                     | What it asserts                                                                                                                                                                                                                                         |
| -------------------------------------------------------- | --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make check-supply-chain`                                | CI (`Supply chain`)         | Every `uses:` is pinned to a commit SHA with its version in a trailing comment, no credentialed job runs code resolved at run time, and every Dependabot ecosystem states a cooldown. Its own job takes no secrets, because it audits the jobs that do. |
| `make check-install-buttons`                             | CI (`Generated docs`)       | Every one-click install button decodes to the same configuration for a given command.                                                                                                                                                                   |
| `make check-manifests`                                   | CI (`server.json`)          | Every version-bearing manifest parses and matches the `VERSION` file.                                                                                                                                                                                   |
| `make check-stamper`                                     | CI (`server.json`)          | The release stamper refuses to stamp a tag without the digest of the push that produced it.                                                                                                                                                             |
| `make check-verify-published`                            | CI (`server.json`)          | The published-package verifier's retry rules, offline: a transient failure is retried and a mismatch never is.                                                                                                                                          |
| `make check-server-json-packages`                        | CI (`server.json`, on push) | Every package `server.json` declares is downloadable and is what it claims. Needs network.                                                                                                                                                              |
| `make release-check`                                     | CI (`GoReleaser config`)    | The GoReleaser configuration is valid.                                                                                                                                                                                                                  |
| —                                                        | CI (`hadolint`)             | The `Dockerfile`.                                                                                                                                                                                                                                       |
| —                                                        | CI (`Docker Build`)         | The image builds for `linux/amd64` and `linux/arm64`. Pull-request-only, which is the one legitimate skip on a push.                                                                                                                                    |
| `make validate-npm` / `validate-pypi` / `validate-nuget` | hand                        | Each channel's packages, built and exercised in a container: a real `initialize` handshake over stdio for npm and NuGet, an install under Alpine for PyPI.                                                                                              |

## Assets

| Target                 | Runs in | What it asserts                                                                                                                                                                                                                                                                               |
| ---------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make check-icon-webp` | hand    | The committed WebP icons reproduce byte for byte. **Maintainer-only and not a CI gate**: no workflow installs librsvg or cwebp. It needs librsvg ≥ 2.58 and falls back to a pinned container when the local one is older. Never "fix" a failure by regenerating on a machine below the floor. |

## The check that makes the others required

`CI verdict` is the only status check the branch ruleset requires, and it asserts
nothing of its own: it `needs:` every other job in `ci.yml` and fails unless each
one reported `success`. **Adding a job to the pipeline means adding it to that
`needs` list** — one edit, in the file the job was added to — rather than editing
the ruleset, which is a step nobody remembers and which quietly leaves every new
job ungated. It refuses `skipped` as well as `failure`, with one paired
exception. [Repository settings](repository-settings.md#the-one-required-check)
has the rest.

## When a gate has no row here

Then either the gate is new and this page was not updated with it, or the row
was removed and the gate was not. Both are worth fixing in the change that
found them: a record that disagrees with the gates is worse than no record,
because it is read as if it agreed.
