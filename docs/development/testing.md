# The test surfaces

This repository has five of them, and they answer different questions. Knowing
which one a change needs is most of knowing how to test it.

The conventions each suite is held to — how a test file is named, why a table
runs under `t.Run`, why `t.Fatal` may not be called from an `http.HandlerFunc`
— are in `CLAUDE.md` § *Testing*, with the reasoning. What is here is the map
of the surfaces themselves.

## The unit suite

```sh
go test ./...        # or: make test
```

Offline, on every platform, on every pull request. HTML fixtures live in each
package's `testdata/`. This is where most tests belong, and a test that can
live here should.

It is also where the coverage floor is measured — `./internal/...`,
`./cmd/server/...` and `./cmd/internal/...`, at or above 85%.

## Live end-to-end

```sh
LIBGEN_E2E=1 go test -tags e2e ./test/e2e/     # or: make test-e2e
```

Double-gated: the `e2e` build tag **and** `LIBGEN_E2E=1`. It reaches the real
mirrors, and it is **run by hand while developing, never on a schedule**. A
suite pointed at live third-party mirrors reports their outages as much as our
regressions, and triaging that daily costs more than it returns.

The suite loads the repository-root `.env` itself and prints which credentials
it found before the first test. That line matters: without the CORE key that
source is out of the chain entirely, and without the Anna's key its case
exercises keyless IPFS instead of the member fast-download — so a partial run
otherwise looks exactly like a full one.

`cmd/probe` is the quicker check that every download route still works against
the real mirrors. It is **not** the same thing as the server's `--healthcheck`
flag, which asks the local process for its `/health` and reaches no mirror.

## HTTP end to end

```sh
make test-e2e-http        # build tag: httpe2e
```

Starts the real binary and drives it over a socket. It exists because the
handler chain — `newHTTPHandler`, `browserCORS`, `crossOriginProtected`,
`sseNoBuffering` — is assembled in `package main` and cannot be imported, so a
unit test would be testing its own reassembled copy rather than the binary that
ships. Every behavior in it is configuration-dependent by nature: the same
request must be refused with one flag and accepted with another.

It runs **on every pull request**, because it depends on nothing external, and
it is half the release gate.

Two cases are unusual on purpose. `proxy_test.go` runs a real nginx in Docker
and skips without it, because the bug it exists for — the server's CORS headers
and the proxy's colliding into a response a browser rejects and `curl` reports
as `200` — only appears when a real proxy adds real headers. And the robustness
cases **assert survival, not correctness**: every one ends by asking `/health`,
because a mirror that is slow, broken or hostile is a live input here rather
than a hypothetical.

## stdio end to end

```sh
make test-e2e-stdio       # build tag: stdioe2e
```

The same idea for the other transport, and for the primary one. It builds the
binary and drives it over two pipes the way a client does, on every pull
request and on all three platforms.

What it covers is what only exists once there is a process: stdout carries
nothing but JSON-RPC (one stray `Println` breaks every client), logs go to
stderr with their severities intact, an idle session is not closed by the
server, both shutdowns exit 0 — and a caller-supplied local `path` cannot reach
the home directory the client started the server in.

That last one can only live here. `internal/pathguard` computes its roots from
the process, so a unit test can only ask it about the directory the test binary
happens to run in. Claude Desktop starts its servers in `/` and other clients
start them in the user's home; the only way to produce that is to start a
process there.

## Collector acceptance

```sh
make test-e2e-collector   # build tag: collectore2e; needs Docker, skips without it
```

Starts a real OpenTelemetry Collector and reads back what it decoded. It exists
because **a stub answers 200 to anything**: the OTLP stub in `test/e2e/http` is
the right shape for asking what a payload does *not* contain, and it cannot
tell a valid export from a malformed protobuf, a resource missing an attribute
a pipeline requires, or a metric whose unit contradicts its name. Here the
pipeline ends in a file exporter, so a document appearing in that file means a
real implementation parsed, routed and re-encoded what this server sent.

Hand-run, no CI job: nothing in CI installs a daemon.

## The evaluator

```sh
LIBGEN_EVAL=1 ANTHROPIC_API_KEY=sk-... go run -tags eval ./cmd/eval
make eval-only ONLY=S61,S62
```

A live, LLM-driven harness. Real API, real mirrors, real downloads; never in
ordinary CI. A run **merges** into its results document rather than replacing
it, so re-measuring one scenario does not cost a full suite — and merging a run
from a different model is refused, because one pass rate built from two models
invites a comparison it cannot support.

## Which one to reach for

| The change is in                                                         | Write the test in                                                                                                          |
| ------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| a package under `internal/`                                              | the unit suite, beside the module                                                                                          |
| the handler chain or a flag `cmd/server` reads                           | `test/e2e/http` or `test/e2e/stdio` — it is assembled in `package main`                                                    |
| what the process does to its own stdio, environment or working directory | `test/e2e/stdio`                                                                                                           |
| a parser against a real mirror's HTML                                    | the unit suite with a fixture in `testdata/`, and `test/e2e` by hand to confirm the fixture is still what the mirror sends |
| a span, a metric or a log field                                          | the unit suite for the shape, `test/e2e/collector` for whether a collector can read it                                     |
| how a model uses the surface                                             | a scenario in `cmd/eval/README.md`                                                                                         |
