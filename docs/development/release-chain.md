# The release chain

**Reference** — for a contributor about to change `.github/workflows/release.yml`.

A tag publishes to seven places. The workflow that does it is fifteen jobs, and
almost every edge between them is a failure somebody already had: the shape is
not convenience, and a `needs:` removed to make a re-run faster puts one of those
failures back.

This page is the **shape and its invariants**. The procedure — bump the version,
mirror it into the manifests, rehearse, tag — is a skill,
`.claude/skills/release/SKILL.md`, because it is a sequence to follow rather than
a page to read. Three rules from it that have each cost a publish are repeated in
`CLAUDE.md` § *Release Process*.

## What starts it

Two events, and only one of them publishes:

- **A tag push matching `v*`.** This is a real release.
- **A `workflow_dispatch`.** This is **always** a rehearsal. `REHEARSAL` is
  derived from `github.event_name` alone and there is no input that turns it off;
  the optional `tag` input only states which version is being rehearsed.

`preflight` runs before anything else spends a runner, and its one job is to
resolve the release tag and **hold it to the `VERSION` file**. It exists because
the `docker` job used to tag `ghcr.io` from `VERSION` while everything else used
the git tag, and nothing compared them: a tag pushed without the bump would have
published an image at one version and a `server.json` pinning another, each
internally consistent.

## The shape

```text
preflight ─┬─ transport-e2e ─┬─ docker ── sign-attest ─┐
           ├─ race ──────────┤                         │
           └─ binary-vulns ──┘                         │
                                                       ▼
                                                    release ─┬─ npm ───┐
                                                             ├─ pypi ──┼─ verify-published ─┬─ mcp-registry ─┐
                                                             ├─ nuget ─┘                    │                ├─ commit-manifests
                                                             ├─ homebrew ───────────────────────────────────┘
                                                             └─ winget  (skipped on a dispatch)
```

**The gate is `transport-e2e`, `race` and `binary-vulns`, and it is in front of
everything a tag sets in motion.** Nothing used to stand between a tag push and published
binaries. The live end-to-end suite cannot fill that slot — it depends on
third-party mirrors a release runner may not reach — but the transport suites
depend on nothing external, so they can. The gate cannot stop the *tag*: the
workflow triggers on the tag push, so by the time it runs the tag exists. What it
stops is everything the tag would have produced.

Each gate job deliberately declares `contents: read` and takes no secrets. **A
gate that fails must not be able to leak what the jobs behind it hold.**

**`binary-vulns` is the third gate, and it asks the scanners' question.**
`make govulncheck` asks whether this module's code reaches a vulnerable symbol.
Trivy, Grype, osv-scanner and every consumer of the SBOMs this release attaches
ask whether any module a binary's build information names carries an advisory,
reached or not. `make check-binary-vulns` builds the six targets `.goreleaser.yml`
declares from the tagged tree and asks that, failing on a finding
`cmd/audit_binary_vulns/declarations.go` does not accept and on a declaration no
finding needs any more. CI runs it on every pull request; it runs again here
because an advisory published after the commit's CI passed is exactly what a tag
cut days later would ship. It declares `contents: read` and takes no secrets,
like the other two.

## What the GoReleaser job checks before anything leaves the draft

Two steps run right after GoReleaser, on the bytes it wrote, while the release is
still a draft and before the bundle, the attestations, the un-drafting and every
publisher:

- **Every linux binary is standalone.** `scripts/check_elf_standalone.py` reads
  the program headers of every ELF file under `dist/` and refuses any that is not
  ELF64 or carries a `PT_INTERP`, which is what `-buildmode=pie` adds (see
  `CLAUDE.md`, *The binaries are standalone*). The Dockerfile, `validate-npm.mjs`
  and `validate_pypi.py` each grep their own channel's bytes for a loader name;
  this is the one check every channel inherits, the loose assets, the `.mcpb`,
  NuGet and Homebrew included.
- **The module-grain gate again, on these exact binaries.** `make
  check-binary-vulns BINARIES='dist/libgen-mcp_*/libgen-mcp*'` scans GoReleaser's
  output in place. The binaries are named by the platform their build information
  records and must be the configuration's six targets, each once, so a glob that
  missed one, or caught the universal darwin binary, fails instead of passing
  short.

## Three rules that hold it together

**The signing identity is never live while third-party build code runs.**
`docker` holds `packages: write` and no `id-token`; `sign-attest` runs no build
action at all. Splitting them is also what stops a re-run from rebuilding: the
image build is not byte-reproducible, so a retried signature used to push a new
index under the same tags and orphan the one already signed.

**Nothing packages a build directory.** Each publisher fetches the release and
checks it against the cosign-signed `checksums.txt` before packing anything, so
what ships is what was published rather than an equally-configured rebuild of it.
That is also why `scripts/publish-npm.sh` runs with `--no-assemble`: the
directory the validator examined is the one that goes out.

**Nothing advertises what nobody checked.** `mcp-registry` and
`commit-manifests` wait on `verify-published`, which compares what the three
registries actually serve against that same signed manifest — from a job holding
no publishing credential of its own. The NuGet packages are also held whole to
the digests the `nuget` job attested before it pushed them (its `nupkg_sha256`
output): nuget.org adds a repository signature to everything it serves, so the
check removes that entry the way NuGet defines an unsigned package, which is the
lookup a verifier makes, and a layout the signing rearranges fails there instead
of leaving an attestation nobody can find.

## The licence and the third-party notices travel with every binary

The binaries link the Go standard library and modules under BSD-3-Clause,
Apache-2.0 (pdfcpu) and MPL-2.0, whose terms ask for their texts to accompany a
binary redistribution, and MIT asks the same of this project's own notice. The
SBOMs name each licence and carry none of the texts. So two files go into every
artifact that hands somebody a binary:

- **`LICENSE`**, read from the repository and held byte for byte to it.
- **`THIRD_PARTY_NOTICES`**, every license, notice and patent file of each module
  the six binaries link, read from their build information and the module cache
  by `cmd/gen_third_party_notices`: at each module's root, and in the directory
  of each package the binaries link (listed per target with `go list -deps`),
  which is where pdfcpu keeps the MIT licence of the pkcs7 code it vendors.
  GoReleaser runs it as an `sboms` entry with
  `artifacts: any`, the one hook after the builds and before `checksums.txt` is
  computed and signed, so the file is a release asset covered by the signature
  and the build-provenance attestation like a binary, and a generation that
  fails stops the release.

`fetch-release-assets.sh` requires the notices by name whenever it takes its
default patterns. Where each one lands, and what refuses a package without it:

- **npm**: all seven tarballs carry both at the package root.
  `build-npm.mjs` holds the notices to `checksums.txt` and records their digest
  in `verified-binaries.json`; `validate-npm.mjs` requires both in the exact file
  set, LICENSE equal to the repository's and the notices equal to the recorded
  digest.
- **PyPI**: every wheel declares core metadata 2.4 (`License-Expression: MIT`, a
  `License-File` per text, no legacy `License` field or licence classifier) and
  carries both under `.dist-info/licenses/`, with `Issues` and `Security`
  project URLs. `validate_pypi.py` checks all of it, and runs
  `twine check --strict` where twine is installed.
- **NuGet**: all seven packages carry both at the root, beside the nuspec's MIT
  expression and its `licenseUrl`; `validate_nuget.py` checks them.
- **Claude Desktop bundles**: `build-mcpb.sh` packs both into each of the four
  bundles and refuses a `dist/` whose notices are missing or do not open with
  the generator's header; `make check-mcpb` drives those refusals.
- **Image**: the Dockerfile's builder generates notices from the image's own
  binary and installs them with LICENSE in `/usr/share/licenses/libgen-mcp`;
  `scripts/smoke-test-image.sh` requires both, the notices naming the platform
  the image was built for.
- **Homebrew**: the formula installs both into the keg's prefix as resources,
  the notices pinned to `checksums.txt` and LICENSE to the tagged tree's hash;
  `make check-homebrew-tap` renders it from a fixture.

winget delivers the binary alone; the notices are the release asset beside it.

## The digest, and why `release` waits for `docker`

`server.json` declares two OCI packages, and an OCI identifier carries a digest
that only exists once the index has been pushed. So:

1. `docker` publishes the index digest as a job **output**.
2. `release` names `docker` in its `needs:` and passes that digest to
   `scripts/update-server-json-sha.sh` as its fourth argument.
3. The stamper **refuses** to stamp a digest-pinned identifier without one.

That refusal is the whole point: stamping the tag alone would leave the previous
release's image pinned under the new version — a manifest whose identifier reads
correctly and resolves to the wrong bytes. `make check-stamper` drives the
refusal against a fixture, offline, in CI's `server.json` job.

The `.mcpb` bundles have the same problem from the other direction: their
`fileSha256` cannot come from `checksums.txt`, because the bundles are built after
GoReleaser runs and appending them to that file would invalidate the signature
over it. The stamper hashes the bundles passed as its third argument instead,
and refuses a run in which nothing got a hash.

## One bundle per operating system

`build-mcpb.sh` packs four bundles from the same binaries:
`libgen-mcp-darwin.mcpb` (the universal Mach-O), `libgen-mcp-windows.mcpb` (the
amd64 executable), `libgen-mcp-linux.mcpb` (the launcher and both Linux
binaries) and the universal `libgen-mcp.mcpb`, which carries all of them. Each
per-OS manifest is derived from `mcpb/manifest.json` by `mcpb/platform.jq` and
lists only its own platform, so Claude Desktop refuses it on another system. All
four are uploaded and attested; **`server.json` declares only the three per-OS
ones**, and the reasons are a registry rule and a directory's:

- **A registry entry has no platform field.** A client offered the universal
  bundle beside the three per-OS ones could not tell which to install, so each
  system must be served by exactly one declared bundle. The stamper refuses a
  set that is not one bundle per system before it writes anything (the
  universal bundle, given among them, serves three), and
  `validate-server-json-packages.sh` holds the declared entries to the same
  rule. Several `mcpb` entries are otherwise unremarkable to the registry.
- **Directories stop reading a bundle past 50 MiB or 256 MiB unpacked.** The
  single bundle of 2.1.0 was 45 MiB with five binaries, within 5 MiB of the
  first limit, and every user downloaded four servers they do not run. The
  packer reports each bundle's size in the job summary and warns when a
  declared one passes either figure.

The universal bundle stays a release asset because links to
`releases/latest/download/libgen-mcp.mcpb` exist outside this repository.

The entries are **written by the stamp, not by hand**: entries naming per-OS
assets of a release that does not carry them would fail
`check-server-json-packages` on every push to `main`, and their hashes cannot be
known before the bundles are built. The stamper rebuilds the `mcpb` entries from
the bundles it is given, copying the first entry's transport and environment,
so the first release that builds per-OS bundles turns `main`'s single universal
entry into three, and a re-run writes the same file. The `mcp-registry` and
`commit-manifests` jobs fetch the three by name, each held to its own attestation,
and the default patterns of `fetch-release-assets.sh` are one per operating
system (`libgen-mcp-linux-*` and the rest), because `libgen-mcp-*` would also
match the per-OS bundles and hand the npm, PyPI and NuGet jobs files they never
use.

## Ordering: who has to publish before whom

- **npm, PyPI and NuGet publish before `mcp-publisher`.** The registry validates
  each entry by fetching what it declares: it reads `mcpName` off the published
  `package.json` on npmjs.com, GETs pypi.org's JSON for the version, and reads
  that version's README off NuGet's v3 feed. Run the registry publish first and
  all three fail on a package that does not exist yet.
- **npm publishes its six platform packages before the launcher.** An install
  racing the publish would otherwise find a launcher pinning packages the
  registry does not have.
- **NuGet pushes its six runtime packages before the pointer**, for the same
  reason: `dotnet tool install` resolves the pointer and then the host's package.
- **`verify-published` retries only the wordings the registry labels transient.**
  nuget.org runs its own validation pass before a pushed version becomes visible,
  which takes minutes, so lag is by construction. A plain `404` is deliberately
  not retried: a package that genuinely failed to publish should fail on the
  first attempt rather than eight minutes later.

**Homebrew and winget degrade rather than block.** Each checks for its secret and
warns out when it is missing, because a release must not be held up by a channel
whose external half does not exist yet. The winget job is skipped whole on a
dispatch: it opens a pull request against `microsoft/winget-pkgs`, and there is
no dry form of that.

## The release is a draft until the bundles are attached

GoReleaser creates the GitHub release with `draft: true`, and the workflow flips
it after the `.mcpb` upload — **before** the registry publish, because a draft
release's assets are not publicly downloadable and the registry fetches the
bundles `server.json` declares. With `draft: false` the release was public for the
minutes it took to build and attach them.

## What a rehearsal proves, and what it cannot

```sh
gh workflow run release.yml --ref main            # rehearses the VERSION file's version
gh workflow run release.yml --ref main -f tag=v1.7.3
```

Everything that can be undone runs. GoReleaser builds the real matrix, the image
is built for both platforms and **exported to an OCI layout** so its digest is
real and the `server.json` stamp is exercised rather than skipped, the SBOMs are
generated, the four `.mcpb` bundles are packed, every publisher job runs against the
rehearsal's own build, and **both trusted-publishing exchanges run** — they mint
a credential and spend nothing, and a policy that has drifted is exactly what a
rehearsal should catch.

**What is skipped is exactly what cannot be undone**, so those steps are the
unexercised ones: the image push and its signatures and attestations, the
attestations over the release assets and the NuGet packages (an attestation in a
rehearsal would name bytes that are never published), the four publishes, `mcp-publisher`, the release un-draft, and the commit back to `main`.
Two job permissions ride along with them. `id-token: write` *is* proven, by the
two mint-only exchanges.

The generalisable rule: **a credential exchange that mints runs in a rehearsal;
only the upload that spends it is skipped.**

## Pinning the action is not pinning the tool

Every `uses:` is pinned to a commit SHA with its version in a trailing comment,
and `make check-supply-chain` enforces it. But `goreleaser-action` and
`cosign-installer` download a binary at run time, so pinning the action leaves
the code that actually runs unpinned. Each is given an exact version through a
top-level `env` entry — `GORELEASER_VERSION`, `COSIGN_VERSION` — which is the one
indirection the audit allows. `syft` and `oras` are fetched by hand in jobs that
hold a signing identity, so they are pinned there with a SHA256 beside each
install; Dependabot does not see a `curl`, so those move when somebody moves
them.

**The cosign major is load-bearing beyond the pin.** It decides how a signature
is attached, and a 2.x client reports "no signatures found" on an image a 3.x
client verifies. Bump it deliberately, and with the verification recipe in
[Installation](../installation.md#docker).

## Adding a job

Three things, and the second is the one that gets forgotten:

1. Give it the narrowest `permissions:` that works. The workflow declares
   `permissions: {}` at the top, so a job inherits nothing.
2. **Do not give it an `environment:`.** See
   [Repository settings](repository-settings.md#trusted-publishers-and-the-blank-environment).
3. Put it in the `needs:` of whatever must not run before it, and — if it is a
   gate — in the `needs:` of what it gates. For a **CI** job the equivalent step
   is `verdict`'s `needs` list in `ci.yml`; see
   [The gates](gates.md).

## Where the rest of it is

| For                                                   | See                                           |
| ----------------------------------------------------- | --------------------------------------------- |
| The steps to cut a release                            | `.claude/skills/release/SKILL.md`             |
| The settings this chain depends on and CI cannot see  | [Repository settings](repository-settings.md) |
| What each automated check asserts                     | [The gates](gates.md)                         |
| The three rules that have each already cost a publish | `CLAUDE.md` § *Release Process*               |
| What a user does with what this publishes             | [Installation](../installation.md)            |
