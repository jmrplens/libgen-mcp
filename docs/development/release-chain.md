# The release chain

**Reference** — for a contributor about to change `.github/workflows/release.yml`.

A tag publishes to seven places. The workflow that does it is fourteen jobs, and
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
           └─ race ──────────┘                         │
                                                       ▼
                                                    release ─┬─ npm ───┐
                                                             ├─ pypi ──┼─ verify-published ─┬─ mcp-registry ─┐
                                                             ├─ nuget ─┘                    │                ├─ commit-manifests
                                                             ├─ homebrew ───────────────────────────────────┘
                                                             └─ winget  (skipped on a dispatch)
```

**The gate is `transport-e2e` plus `race`, and it is in front of everything a tag
sets in motion.** Nothing used to stand between a tag push and published
binaries. The live end-to-end suite cannot fill that slot — it depends on
third-party mirrors a release runner may not reach — but the transport suites
depend on nothing external, so they can. The gate cannot stop the *tag*: the
workflow triggers on the tag push, so by the time it runs the tag exists. What it
stops is everything the tag would have produced.

That gate job deliberately declares `contents: read` and takes no secrets. **A
gate that fails must not be able to leak what the jobs behind it hold.**

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
no publishing credential of its own.

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

The `.mcpb` bundle has the same problem from the other direction: its
`fileSha256` cannot come from `checksums.txt`, because the bundle is built after
GoReleaser runs and appending it to that file would invalidate the signature over
it. The stamper hashes the path passed as its third argument instead, and refuses
a run in which nothing got a hash.

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

## The release is a draft until the bundle is attached

GoReleaser creates the GitHub release with `draft: true`, and the workflow flips
it after the `.mcpb` upload — **before** the registry publish, because a draft
release's assets are not publicly downloadable and the registry fetches the
bundle `server.json` declares. With `draft: false` the release was public for the
minutes it took to build and attach that bundle.

## What a rehearsal proves, and what it cannot

```sh
gh workflow run release.yml --ref main            # rehearses the VERSION file's version
gh workflow run release.yml --ref main -f tag=v1.7.3
```

Everything that can be undone runs. GoReleaser builds the real matrix, the image
is built for both platforms and **exported to an OCI layout** so its digest is
real and the `server.json` stamp is exercised rather than skipped, the SBOMs are
generated, the `.mcpb` is packed, every publisher job runs against the
rehearsal's own build, and **both trusted-publishing exchanges run** — they mint
a credential and spend nothing, and a policy that has drifted is exactly what a
rehearsal should catch.

**What is skipped is exactly what cannot be undone**, so those steps are the
unexercised ones: the image push and its signatures and attestations, the four
publishes, `mcp-publisher`, the release un-draft, and the commit back to `main`.
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
