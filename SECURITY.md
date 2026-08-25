# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it privately through GitHub's
[private vulnerability reporting](https://github.com/jmrplens/libgen-mcp/security/advisories/new),
or by email to <mail@jmrp.io>. If you use email and want an encrypted reply,
the maintainer's key is published at
[keyoxide.org](https://keyoxide.org/0A993B268654DBBA52B7E8D3FCF653391E2C91FC).

Please include enough to reproduce the problem: the version (`libgen-mcp
--version`), the platform, the tool call or configuration involved, and what you
observed versus what you expected. A proof of concept helps, but a clear
description is enough to start.

### What to expect

This is a single-maintainer project, so these are honest targets rather than a
commercial SLA:

| Stage | Target |
| --- | --- |
| Acknowledgement of your report | Within 5 days |
| Initial assessment (valid / not, severity) | Within 14 days |
| Fix released for a confirmed high-severity issue | As soon as practical, coordinated with you |

Reports are handled privately until a fix is released. You will be credited in
the advisory and the release notes unless you ask not to be.

## Supported versions

Only the latest release receives security fixes. There are no long-term support
branches: the project ships a single static binary, upgrading is replacing one
file, and back-porting to superseded tags would create a false impression of
coverage.

| Version | Supported |
| --- | --- |
| Latest release | Yes |
| Anything older | No — upgrade |

## Threat model

libgen-mcp runs locally (or as a service you deploy), takes instructions from an
AI client, and fetches content from third-party sites that it does not control.
That shape determines what counts as a vulnerability here.

### In scope

These are the things this project can get wrong, and reports about them are
wanted:

- **Untrusted remote content escaping its bounds.** Record titles, authors,
  descriptions and extracted document text all come from external sites. They
  are rendered into Markdown tool results through the escaping helpers in
  `internal/tools/markdown.go`, and are explicitly labelled as untrusted data so
  a model treats them as data rather than instructions. A way to break out of a
  table cell or a code fence, or to strip that labelling, is a vulnerability.
- **Prompt injection reaching the client as instructions.** If externally
  controlled text can be made to read as a directive to the calling model rather
  than as quoted content, that is in scope even though the payload originates
  elsewhere.
- **Path traversal or unintended writes.** `download` writes to a configured
  directory and derives filenames from remote metadata. A remote-controlled name
  that escapes the destination directory, overwrites something it should not, or
  survives `sanitizeFilename` is a vulnerability.
- **Server-side request forgery.** Mirror discovery, the download source chain
  and `read` all fetch URLs influenced by remote responses. A path that can be
  steered to an internal address or a non-HTTP scheme is in scope, and matters
  most for HTTP-transport deployments.
- **Credential leakage.** The optional Anna's Archive key, CORE key and Unpaywall
  contact address must go only to the service they belong to, must never appear
  in a resolved file URL, a log line, an error message or a tool result, and
  per-call secrets must never be written to disk.
- **Resource exhaustion that a remote server can trigger** — an unbounded read,
  a decompression bomb, or a stream that defeats the size and stall limits.
- **Vulnerable dependencies** that are actually reachable from this code.

### Out of scope

- **The legality or content of anything the tool retrieves.** That is addressed
  in [Responsible use](https://jmrp.io/docs/libgen-mcp/responsible-use/),
  not here. This project hosts nothing and can take nothing down.
- **Vulnerabilities in the third-party services** libgen-mcp talks to — Library
  Genesis mirrors, Sci-Hub, Anna's Archive, the open-access providers. Report
  those to whoever runs them.
- **The consequences of configuration you chose**, such as exposing the HTTP
  transport to a hostile network without a proxy in front of it, or pointing
  `LIBGEN_MCP_DOWNLOAD_DIR` at a sensitive location.
- **A model misbehaving on correctly labelled untrusted data.** If the content
  was escaped and marked as data and the client's model acted on it anyway, that
  is a client-side issue — though tell us anyway if the labelling could be
  clearer.
- Findings from automated scanners with no demonstrated impact on this code.

## Security practices in this project

- No telemetry, no analytics and no backend of its own; every network request is
  a direct consequence of a tool call. See the
  [privacy policy](https://jmrp.io/docs/libgen-mcp/privacy/).
- No credentials are required for any core capability. The two optional keys are
  opt-in, and a key supplied per call is used for that request and never stored.
- Externally sourced text is escaped through dedicated helpers and marked
  untrusted before it reaches a client.
- Every handler is panic-safe: an unexpected failure becomes a tool error rather
  than a crash of the session.
- CI runs `govulncheck`, `golangci-lint`, `go vet`, the race detector and a
  SonarCloud quality gate on every change; dependencies are updated by
  Dependabot.
- Release binaries are built by GitHub Actions from a tagged commit, and the
  container image runs as a non-root user.

## Disclosure

Once a fix is available, an advisory is published on the repository with the
affected versions, the impact and the upgrade path, and the release notes link
to it. Please give the maintainer a reasonable chance to ship a fix before
disclosing publicly; if a report goes unanswered past the targets above, you are
under no obligation to keep waiting.
