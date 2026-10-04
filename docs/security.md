# Security model

**Explanation** — for an operator or reviewer deciding what the server can be trusted with.

`libgen-mcp` takes instructions from a language model and fetches content from sites it does not
control, so almost everything it handles was written by somebody other than the person running
it. This page says which inputs it trusts, which it does not, and what it does about the second
kind: the keys it holds, the destinations it refuses to reach, the local paths it refuses to
open, the third-party text it keeps from reading as instructions, and what an HTTP deployment
checks before it answers. It ends with a checklist for an HTTP deployment.

The mechanisms themselves are documented where they live, and this page links to them rather
than repeating them. How to report a vulnerability, and what counts as one, is in the
[security policy](../SECURITY.md).

## What the server trusts, and what it does not

| Input                                                                          | Who controls it                                   | Treated as                                                                          |
| ------------------------------------------------------------------------------ | ------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Flags, the process environment, `~/.libgen-mcp.env`, a file `--env-file` names | The operator                                      | Configuration. The only input that decides what the server may do.                  |
| A `.env` in the working directory                                              | Whoever wrote the workspace the client opened     | Nothing. It is never loaded.                                                        |
| Tool arguments: an md5, a DOI, a query, a `path`, a `source`                   | The model, which reads untrusted text             | Untrusted. Validated, and every path confined.                                      |
| Catalog and mirror content: titles, authors, descriptions, extracted text      | Whoever uploaded or cataloged the record          | Untrusted data. Escaped for where it lands and labeled `UNTRUSTED` in the result.   |
| URLs a third party supplied: download links, index results, redirects, mirrors | Publishers, open-access indexes, mirror operators | Untrusted destinations. Dialed only through the outbound address guard.             |
| Requests to an HTTP endpoint                                                   | Anyone who can reach it                           | Anonymous. The server authenticates nobody, and bounds what each address may spend. |

Two consequences are worth stating plainly.

**The model's arguments are not the operator's.** A model that has just read a document can be
steered by an instruction embedded in it, so a `path` or a URL arriving in a tool call is treated
as if a stranger chose it. That is why `read` refuses `~/.ssh/id_rsa` by default, even on a
server the user started themselves, and why a DOI whose resolved link names the cloud metadata address is
refused rather than fetched.

**The working directory is not configuration.** A stdio server inherits its working directory
from the client, which sets it to whatever workspace is open, and that directory can arrive with
a cloned repository or an extracted archive. A `.env` there could set
`LIBGEN_MCP_ALLOWED_READ_DIRS` and hand `read` the rest of the disk, or set `LIBGEN_MIRROR` and
decide where every search goes. So it is never read. A dotenv file configures the server only
when somebody put it where the server looks (`~/.libgen-mcp.env`) or named it deliberately
(`LIBGEN_MCP_ENV_FILE`, or `--env-file`, preferably by absolute path).

## Keys

### Keyless by default

Search, details, downloads and reading all work with no account, no key and no configuration.
The credentials below are opt-in, and each one adds a source, a faster path or a larger
allowance rather than
unlocking a capability the server otherwise lacks.

### Keys the operator configures

| Variable                     | What it is for                                 |
| ---------------------------- | ---------------------------------------------- |
| `LIBGEN_MCP_ANNAS_KEY`       | Anna's Archive member (fast) downloads         |
| `LIBGEN_MCP_CORE_KEY`        | The CORE open-access source                    |
| `LIBGEN_MCP_OPENALEX_KEY`    | A daily OpenAlex allowance ten times larger    |
| `LIBGEN_MCP_UNPAYWALL_EMAIL` | The Unpaywall source, which requires a contact |

They are set in the environment or a dotenv file and **have no command-line flags**, on purpose:
a secret on a command line is visible to every local account through `ps` and lands in shell
history. On an HTTP deployment a configured key is the deployment's, so every caller spends it.
A `download` result does not report an Anna's Archive account or how much of its allowance is
left unless the call asked for the member tier itself. A key that enables a source is visible in
a different way: the source appears in `download`'s `source` enum. See
[the result-disclosure ADR](decisions/2026-08-08-result-reveals-only-what-the-call-revealed.md).

### Keys a caller supplies for one call

When no key is configured, `download` can ask the client for one through elicitation: an Anna's
Archive key when the call sets `annas_member: true`, an Unpaywall contact address for a DOI
download. The answer is used for that request only. It is **never stored, keyed on, counted or
logged**, and it is never a tool argument, so it stays in the client's form rather than in the
model's context. A client that does not support elicitation is never asked, and the download
proceeds on the keyless path. The prompts are described in
[Tools → Interactive prompts](tools.md#interactive-prompts-elicitation).

### Where a key travels

A key rides in a request header wherever the service accepts one. Two services accept it only in
the query string, and there the rule moves to what happens to the URL afterwards:

| Credential                | Sent as                        | Why                                    |
| ------------------------- | ------------------------------ | -------------------------------------- |
| CORE key                  | `Authorization: Bearer` header | The API takes it there                 |
| OpenAlex key              | `Authorization: Bearer` header | The API takes it there                 |
| Anna's Archive key        | `key` query parameter          | The only place the member API reads it |
| Unpaywall contact address | `email` query parameter        | The only place the API reads it        |

A configured contact address is also offered to the open-access APIs that keep a polite pool for
identified callers, Crossref among them, in a `mailto` parameter or the `User-Agent`. A per-call
address goes to Unpaywall alone. OpenAlex is not sent the contact address, because it retired
its polite pool. A configured OpenAlex key that OpenAlex rejects is logged once at `WARN`, naming
the variable and never the key.

A URL with a secret in it leaks in two predictable ways, and both are closed:

- **On a redirect.** `net/http` sends the previous request's full URL as `Referer` on every hop
  it follows. The outbound client strips `Referer`, `Authorization`, `Cookie`, `Cookie2`,
  `Proxy-Authorization` and `WWW-Authenticate` whenever a redirect changes scheme, host or port,
  and stops a chain after five hops.
- **In an error.** A transport failure is reported as an error whose text is the whole request
  URL, query string included, and this server writes such errors both to its log and into the
  model's transcript. The outbound request paths redact such an error (userinfo, query and
  fragment dropped; scheme, host and path kept) before anything wraps it.

The same holds for the presigned file URL the member API returns, which is a working credential
in its own right. Where each source's key goes, and how that was measured, is on
[Sources](sources.md).

## Outbound destinations

Almost every URL this server fetches was chosen by someone else: a publisher's link deposited
with Crossref, a repository URL republished by an open-access index, a mirror scraped from a
catalog page. Without a guard, anyone able to place a URL in such an index could aim the server
at the operator's LAN or at a cloud metadata endpoint. The guard sits in the dialer, so it judges
the address a name actually resolved to, which a check on the URL cannot do against a public name
with a private `A` record or against DNS rebinding.

| Tier | Refused                                                                                                                                                        | Lifted by                                                                                                                                                                              |
| ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A    | The cloud metadata addresses `169.254.169.254`, `169.254.170.2`, `fd00:ec2::254` and `100.100.100.200`                                                         | Nothing. Not the flag, not naming the host.                                                                                                                                            |
| B    | Loopback, `0.0.0.0/8`, broadcast, link-local, every multicast scope, RFC 1918, RFC 4193 and RFC 6598 carrier-grade NAT, each also in its IPv4-mapped IPv6 form | A host the operator named, every request of the catalog and download client once a named host is itself private, or `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES` for every destination at once |

**A host the operator named is exempt from tier B, whatever it resolves to.** The hosts of
`LIBGEN_MIRROR` and `LIBGEN_MCP_SCIHUB_HOSTS`, and each mirror family's own catalog, preferred
mirror and fallbacks, are destinations this deployment chose. Anything else — a resolved
download URL, a mirror hostname scraped from the catalog, a redirect that leaves a named host —
gets the strict tier, **as long as every named host is public**.

**Naming a private host widens the exemption.** When any host the operator named resolves to
private space, as a mirror on your own network does, tier B is lifted for every request the
catalog and download client makes, not only for that mirror: resolved download URLs and their
redirects included. The case it exists for is a private mirror redirecting to a sibling host on
the same network; the price is that a third-party download URL naming a private address is no
longer refused on that deployment. Tier A still holds, and the discovery providers (arXiv,
Crossref and the rest) keep the strict tier, because they name no host at all. The reasoning is
in
[the operator-named-host ADR](decisions/2026-09-17-an-operator-named-host-is-exempt-from-the-destination-guard.md).

**`LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES`** (or `--allow-private-addresses`) lifts tier B for every
destination, including the ones a third party chose. It is off by default, and the server
**refuses to start** with it on a listener other machines can reach, because any caller who can
POST could then aim the server at your network. It is checked twice: once on the configured
address and again on the address the kernel actually bound. Bind a loopback address or a unix
socket, or leave it unset: naming a mirror on your own network in `LIBGEN_MIRROR` reaches it
without the flag, at the narrower cost described above.

**Behind `HTTP_PROXY` or `HTTPS_PROXY`** the dialer only ever sees the proxy, so the guard
splits its questions. The proxy is the operator's configuration and answers to tier A alone. The
destination is judged per request, before anything is sent, under both tiers, as far as its URL
spells an address — and an address means every form a C resolver accepts: `2852039166`,
`0xa9fea9fe`, `0251.0376.0251.0376` and `169.254.43518` are all `169.254.169.254` to the proxy,
and each is judged as that address. A host whose last label only looks numeric is refused
outright. A hostname behind a proxy is resolved by the proxy, so tier B is the proxy's decision
there. The name is also resolved locally and refused if any answer is a metadata address, when
this machine can resolve it at all; when it cannot, only the proxy sees the answer and the
request goes through.

The full mechanics are in
[Architecture → Outbound address policy](architecture.md#outbound-address-policy), and the two
refusals an operator meets are in [Troubleshooting](troubleshooting.md#the-server-will-not-start-the-private-address-hatch-on-an-open-listener)
and [behind an outbound proxy](troubleshooting.md#behind-an-outbound-proxy-refusing-to-connect-to-a-private-or-local-address).

## Local files

Two tool arguments are local paths: `read`'s `path`, which the server opens and returns the text
of, and `download`'s `path`, which it writes into. Both come from the model, so both are
confined to a set of roots:

- the directory the server was started in — **unless that is the home directory**, which is never
  an implicit root, because Claude Desktop starts servers in `/` and other clients start them in
  the user's home;
- the OS temporary directory;
- the download directory (`LIBGEN_MCP_DOWNLOAD_DIR`);
- whatever the operator adds: `LIBGEN_MCP_ALLOWED_READ_DIRS` for `read`,
  `LIBGEN_MCP_ALLOWED_DOWNLOAD_DIRS` for `download`. They are separate, because what a deployment
  will have read and what it will have written are different decisions.

A refusal names the variable that would widen it, because a containment nobody can widen on
purpose gets switched off by whoever hits it.

Paths are resolved through symlinks before the check, so a link counts by where it points: one
inside an allowed root that points outside it is refused, and one that points inside is
followed. What happens after the check differs by tool:

- **`download`** opens its partial file without following a symlink at the leaf, so a link
  placed there after the check is refused. Windows has no such open flag, so there the check is
  all there is.
- **`read`** opens the file relative to the allowed root it was found under, through Go's
  `os.Root`, and reads only from that open descriptor; nothing reopens it by name. Every path
  component below the root is walked from the root's own descriptor, so a symlink, a junction or
  a `..` that would leave the root fails the open, on every platform this server ships for. When
  roots are nested, the outermost one holding the path is the one opened. The root directory
  itself is opened by name, so the opened file must also be the very file the check examined
  just before the open (same device and inode, or volume and file ID on Windows), or the read is
  refused. A local user who can write in an allowed root — and the OS temporary directory is
  always one — therefore cannot redirect a read outside the roots by swapping the file, a
  directory between it and the root, or the root itself, after that check. The regular-file
  check is repeated on the open descriptor, and on unix the open does not block, so a fifo
  swapped in is refused rather than hanging the call. Three things are not defended: a swap of
  the root or a directory above it (which needs write access outside every root) made after the
  path was resolved but before that pre-open check, and still in place at the open; a swap to a
  different file that is itself inside the roots, which the caller could have named anyway; and
  a hard link to an outside file made inside a root, which is not a path escape at all (on
  Linux, `fs.protected_hardlinks` is what stops an account linking a file it cannot read).

A download's filename is sanitized to a single path component, so a title from the
catalog cannot name a directory.

**A remote deployment refuses local paths altogether.** When the caller's disk and the server's
are different machines — any HTTP listener, or a stdio server with
`LIBGEN_MCP_REMOTE_DOWNLOADS` set — no caller-supplied path is accepted, and `read` takes an md5
or a DOI instead. On top of that, `LIBGEN_MCP_SERVER_FETCH` defaults to off for a remote
deployment: `read` is not registered, and `download` — link-only on any remote deployment,
whatever this is set to — saves nothing, so no file body crosses the server's own connection. The default is about a shared egress address, not
secrecy, and an explicit value wins either way; see
[Configuration](configuration.md#libgen_mcp_server_fetch) and
[the hosted-fetch ADR](decisions/2026-09-08-a-hosted-server-does-not-fetch-file-bodies.md).

Size bounds apply as well: `LIBGEN_MCP_MAX_DOWNLOAD_BYTES` caps a download when set (it is
unlimited by default), enforced against
`Content-Length` and again while streaming, and a plain-text read stops at 8 MiB.

## Third-party text in a result

A record's title, a book's description and the text extracted from a PDF are written by whoever
uploaded them, and a tool result is read by a model that acts on what it reads. Three rules keep
the one from becoming instructions to the other.

**It is labeled.** Each tool's description tells the model that what it returns is `UNTRUSTED`
third-party text, to be read as data and never followed as instructions. The result says it
again where the text begins — before a list of search hits, an extracted passage, the matches of
a search inside a document, a table of contents — and the prompts carry the same caveat. See
[Tools → read](tools.md#untrusted-text).

**It is escaped for where it lands.** A value going into a table cell, a list item, a heading, a
code block or a link passes through the helper for that construct, which takes away what would
change the document's shape there: a pipe ending a cell, a newline starting a heading, a fence
closed from inside. Control bytes are stripped before any check, and a link is written only for
an absolute `http` or `https` address, because a `javascript:` destination is a whole link some
clients render live. `make check-md-escaping` walks the renderer packages and reports a runtime
value that reaches a Markdown construct with no escaper in between.

**A registry's text is third-party text too.** A reference `get_details` formats through
`doi.org` for `cite_as` is written by the DOI's registration agency, and the candidates a
`citation` lookup returns are Crossref's records. The formatted reference is reduced to one plain
line before it is shown (the named formatting tags `i`, `b`, `em`, `strong`, `sub`, `sup`, `sc`,
`span` and `u` and the entities removed, control characters and bidirectional controls
dropped) and lands in a fenced block, where any other angle bracket stays as text, and the candidates go through the table-cell
helpers like any catalog record.

**The guidance heading is reserved.** The `Next steps` section at the end of a result is the one
part written in the server's own voice. A record carrying that exact heading, `💡 **Next
steps:**`, has it rewritten to an HTML entity that renders identically but is no longer the
marker. The rewrite matches the exact string only, so a lookalike heading passes through as
text; the `UNTRUSTED` label around it is what still applies.

A result also reveals only what the call revealed: when `download` saves a file, the result does
not name the source or the mirror that served it unless the caller pinned one.

## The HTTP endpoint

Over stdio the server serves no network endpoint for MCP, and its only caller is the process
that started it. Over
HTTP it is a network service, and the following holds for every deployment. The operator's guide
is [HTTP server mode](http-server-mode.md), with proxy configurations on
[Behind a reverse proxy](deploy/reverse-proxy.md).

**There is no authentication.** Anyone who can reach the endpoint can call every tool it
registers. If that is not what you want, put authentication in the proxy in front of it.

| Check             | What it does                                                                                                                                                                                                                                                                                                                          |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Host guard        | A connection accepted on a loopback address may carry only a loopback `Host`, against DNS rebinding; a proxied public name is refused with `403` until `--public-url` or `--trusted-proxies` declares it. A unix socket is exempt.                                                                                                    |
| Cross-origin POST | A browser POST from another origin is refused with `403`. Clients that send neither `Origin` nor `Sec-Fetch-Site` — every non-browser client — are unaffected.                                                                                                                                                                        |
| CORS              | Off unless `--trusted-origins` names an origin; then the MCP endpoint answers the preflight and echoes that origin rather than `*`. `*` is accepted with a startup warning. The server cards are public documents and always answer `Access-Control-Allow-Origin: *`. Let one layer answer CORS, never the proxy and the server both. |
| Security headers  | `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store` and `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` on every response, including the ones an inner layer writes. The server cards override the cache header with `public, max-age=3600`.  |
| HSTS              | `Strict-Transport-Security: max-age=31536000; includeSubDomains`, only when this process terminates TLS (`--tls-cert`). No `preload`.                                                                                                                                                                                                 |
| TLS               | `--tls-cert` and `--tls-key`, both or neither, TLS 1.2 minimum, HTTP/2 negotiated, re-read on rotation without a restart.                                                                                                                                                                                                             |
| Unix socket       | `--http` with a path binds a socket created `0660` (`--http-socket-mode`) on a platform with file modes, with the umask narrowed around the bind so it is never briefly world-connectable. An existing file is never replaced unless it is a dead socket.                                                                             |
| Routes            | The MCP endpoint (and its `/mcp` alias), `GET /health` and the server cards. Every other path is `404`.                                                                                                                                                                                                                               |
| Request body      | `--max-request-body-bytes`, 4 MiB by default; a larger body gets `413`. A negative value fails startup.                                                                                                                                                                                                                               |
| Per-caller limits | `--rate-limit-rps` (`10`) and `--rate-limit-burst` (`40`) per charged address, and `--max-inflight-per-client` for held `download`/`read` calls. The rate limit is off on a loopback listener or socket unless `--trusted-proxies` says who the callers are.                                                                          |
| Process ceilings  | Held calls and stateful sessions bounded from `RLIMIT_NOFILE` at startup; past them a call gets `503` with `Retry-After: 30`.                                                                                                                                                                                                         |
| Profiling         | `--pprof-addr` / `LIBGEN_MCP_PPROF_ADDR` is refused unless it names a loopback address, because a heap profile is a copy of the process's memory, per-call secrets included.                                                                                                                                                          |

A trusted proxy's forwarded-address header is believed only from a peer `--trusted-proxies`
names; from anybody else it is text the caller wrote. Each of these settings that could be
mistyped into silence fails startup instead — the list is
[What the server refuses to start with](http-server-mode.md#what-the-server-refuses-to-start-with).

## Logs and telemetry

**Logs go to stderr** and stay on the machine. They are the operator's own record, so they keep
what a person debugging needs: the query a search ran, the source and mirror that served a
download. A per-call credential is never written there, and an outbound error is redacted before
it is logged.

**Telemetry is off by default**, and when it is on it goes only to the OpenTelemetry collector
the operator configures — this project runs no backend. The exported copy of each log record
drops a fixed list of fields (the query, a record title, a per-call credential, the charged
address, a recovered panic and its stack, among others) and replaces every error's text with its
type. Some things are never exported under any setting: the search query, a returned title, tool
arguments and results, a per-call credential, and the contents of anything downloaded or read.

**Callers are not identified unless the operator decides to.** `LIBGEN_MCP_TELEMETRY_IDENTITY`
defaults to `none`; `pseudonymous` exports a keyed digest of the caller's address, and `full`
the address itself. On a public endpoint that address is a stranger's, which is why `full` is a
decision with a legal shape. A caller can see what a deployment records about them on its
server card, without asking.

The detail is on [Telemetry](telemetry.md#what-is-never-recorded), and what leaves the machine
in the default configuration is on [Privacy](../PRIVACY.md).

## Supply chain

| Measure             | What it covers                                                                                                                                                                                                                                                |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Signed checksums    | `checksums.txt` for the binaries and the files GoReleaser publishes beside them, with a keyless cosign (Sigstore) bundle whose identity is the release workflow. The `.mcpb` bundles are built afterwards and are not in it; they carry build provenance only |
| Build provenance    | SLSA provenance in GitHub's attestation store for every release asset, every `.mcpb`, the container image (index and each platform) and, from 2.1.0, every NuGet package                                                                                      |
| Image signatures    | A keyless cosign 3.x signature on the image index and both platform manifests, on GHCR and Docker Hub                                                                                                                                                         |
| SBOMs               | An SPDX SBOM per release binary, published beside it, and per image platform, attested to the image                                                                                                                                                           |
| Registry provenance | npm provenance and PyPI PEP 740 attestations from trusted publishing, with no stored token. The npm, PyPI and NuGet packages carry binaries checked against the signed `checksums.txt` before packing                                                         |
| Vulnerability gates | `govulncheck` on every pull request for what reaches this module's call graph, and a gate on the built binaries that fails on any advisory against a module their build information names unless a reviewed reason is declared                                |
| Pinned CI           | Every third-party action pinned to a commit SHA, audited by `make check-supply-chain`; downloaded tools (GoReleaser, cosign) pinned to exact versions                                                                                                         |
| Standalone binaries | `CGO_ENABLED=0` with no ELF interpreter, so a binary runs on glibc and musl alike and the image runs it as UID `10001`, not root                                                                                                                              |

How to verify each channel, with the commands, is on
[Installation → Verifying what you install](install/overview.md#verifying-what-you-install).

### What a scanner reports, and the VEX statement

A scanner that reads the server binary (Trivy, Docker Scout, osv-scanner, a
registry's own scan) reports **GO-2026-5932** against `golang.org/x/crypto`.
That advisory covers the module's `openpgp` packages, has no fixed version and
no CVSS score, so some tools show it with no severity. The binary does not
contain them: the only package of the module it links is
`golang.org/x/crypto/ocsp`, which the PDF library (pdfcpu) imports for its
signature code. A scanner matches the module named in the build information,
not the packages linked, which is why the finding appears at all. You can check
the package list yourself from a source checkout:

```bash
go list -deps ./cmd/server | grep '^golang.org/x/crypto'
```

The release gate (`make check-binary-vulns`) fails on any such finding unless a
reviewed declaration accepts it, and this one is declared `not-linked`. That
declaration is published as an [OpenVEX](https://openvex.dev) statement,
`not_affected` with the justification `vulnerable_code_not_present`, in
[`.vex/libgen-mcp.openvex.json`](../.vex/libgen-mcp.openvex.json). It is
generated from the declarations and a CI check fails when the two disagree, so a
statement cannot outlive the evidence behind it. From the release after 2.2.0
each release also attaches a copy pinned to its own version and image digest to
the image on GHCR and Docker Hub, as a signed attestation, and publishes it as
the `libgen-mcp.openvex.json` release asset.

| Scanner          | How it uses the statement                                                                                                                                                                                                                                                                                              |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Trivy            | `--vex oci` reads the attestation from the registry. `--vex <file>` reads the document from the repository or the release. The finding is then listed as suppressed, `not_affected`, instead of as a vulnerability                                                                                                     |
| Docker Scout     | `docker scout cves --vex-location <dir>` reads a local copy. The document also names the image as `pkg:docker/jmrplens/libgen-mcp`, the form Scout documents. Scout needs a Docker login, so this has not been measured here, and whether Scout and the Docker Hub page read the registry attestation is not yet known |
| Grype            | Does not report this advisory against the binary, so there is nothing to suppress                                                                                                                                                                                                                                      |
| Other registries | A scanner that reads no VEX keeps showing the finding. The explanation above is the answer to give                                                                                                                                                                                                                     |

To check it yourself:

```bash
# The statement applied from the registry (attached from the release after 2.2.0)
trivy image --vex oci --show-suppressed ghcr.io/jmrplens/libgen-mcp:latest

# The committed statement applied to any image or binary, 2.2.0 included
curl -fsSLO https://raw.githubusercontent.com/jmrplens/libgen-mcp/main/.vex/libgen-mcp.openvex.json
trivy image --vex libgen-mcp.openvex.json --show-suppressed ghcr.io/jmrplens/libgen-mcp:latest
mkdir -p vex && mv libgen-mcp.openvex.json vex/
docker scout cves --vex-location ./vex docker.io/jmrplens/libgen-mcp:latest

# The attestation itself: who signed it and what it says
gh attestation verify oci://ghcr.io/jmrplens/libgen-mcp:latest --bundle-from-oci \
  --predicate-type https://openvex.dev/ns/v0.2.0 -R jmrplens/libgen-mcp
```

## Reporting a vulnerability

Do not open a public issue. Report privately through GitHub's
[private vulnerability reporting](https://github.com/jmrplens/libgen-mcp/security/advisories/new)
or by email, as the [security policy](../SECURITY.md) describes; it also lists what is in scope,
the response targets, and which versions receive fixes (the latest release only).

## Hardening checklist for an HTTP deployment

Go through it in order; the later items assume the earlier ones.

1. **Run the latest release, verified.** Only the latest receives fixes, and the signature is
   the half of verification usually skipped.
2. **Pick the listener.** A unix socket for a proxy on the same machine; otherwise a loopback
   address. Never a wildcard address the internet reaches without a proxy.
3. **Terminate TLS**, in the proxy or with `--tls-cert` and `--tls-key`.
4. **Declare the name** clients use with `--public-url`, so the host guard accepts it.
5. **Name the proxy** with `--trusted-proxies` and `--trusted-proxy-header` together, and only
   the proxy, so per-caller limits and telemetry see callers rather than the proxy.
6. **Decide who may call.** The server authenticates nobody; if the endpoint should not be open
   to everyone who reaches it, authenticate in the proxy.
7. **Leave `--trusted-origins` empty** unless a browser client needs it, then name its exact
   origin. Never `*` on a public endpoint, and never CORS in the proxy and the server both.
8. **Leave `LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES` unset.** If you need a private mirror, name it
   in `LIBGEN_MIRROR` instead, knowing that this lifts the private-address tier for every
   download URL and redirect the catalog client follows. Without a private mirror, name only
   public hosts and keep the strict tier.
9. **Leave `LIBGEN_MCP_SERVER_FETCH` at its remote default** unless the egress address is yours
   alone.
10. **Keep the limits on**, and size `--max-inflight-per-client` and `LIBGEN_MCP_ACTION_TIMEOUT`
    for the callers you expect.
11. **Keep keys out of command lines.** `LIBGEN_MCP_ANNAS_KEY`, `LIBGEN_MCP_CORE_KEY`,
    `LIBGEN_MCP_OPENALEX_KEY` and `LIBGEN_MCP_UNPAYWALL_EMAIL` belong in the environment or a dotenv file only the service
    account can read.
12. **Leave `--pprof-addr` unset** in production.
13. **Keep `LIBGEN_MCP_TELEMETRY_IDENTITY=none`** if telemetry is on, unless you have a reason
    and a lawful basis to record callers.
14. **Run the image as shipped**, as its non-root user, with the health check it declares.
