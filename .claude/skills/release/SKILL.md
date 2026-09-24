---
name: release
description: Cut a libgen-mcp release — bump VERSION, mirror it into the version-bearing manifests, regenerate llms.txt, tag, and publish to npm and LobeHub. Use when cutting or preparing a release, bumping the version, or publishing to the MCP registry, npm or LobeHub Marketplace.
---

# Cutting a libgen-mcp release

The version lives in `VERSION` and is mirrored into six manifests and the
citation file. To cut a release:

1. Bump `VERSION`.
2. Update the version in `mcpb/manifest.json`, `lhm.plugin.json`,
   `.plugin/plugin.json` and `plugin.json`, and run `make sync-npm-version` for
   `npm/libgen-mcp/package.json` (it moves the version and all six dependency
   pins together — never hand-edit it). In `CITATION.cff`, set `version:`; leave
   `date-released:` alone, the stamper writes the tag's date.

   In `server.json`, bump **`.version` and the `version` field of the npm, PyPI
   and NuGet entries — and nothing else.** Leave the `.mcpb` identifier, its
   `fileSha256` and both OCI identifiers pointing at the **published** release:
   the stamper rewrites all three at release time, and bumping them by hand
   declares files that do not exist yet, which `make check-server-json-packages`
   fails on (the bundle URL 404s and the image tag does not resolve). The npm
   entry has to move with the launcher because the same gate holds the two in
   lockstep; an unpublished *version* is a note there, an inconsistent one is
   not.
3. Run `make check-manifests`. It gates all six and `CITATION.cff` against
   `VERSION`, and CI runs it in the `server.json` job. Add any new
   version-bearing JSON manifest to `VERSION_MANIFESTS` in the `Makefile` — a
   file that is not listed there is not gated, and will silently ship the
   previous release's number.
4. Run `make gen-llms`. `llms.txt` and `llms-full.txt` state the version in their
   opening line, so a bump leaves them stale. They are **not** covered by
   `check-manifests` — `make check-llms` is the gate that catches it, in a
   different CI job.
5. Open a PR; once merged, **rehearse it** (below), then tag `vX.Y.Z` on main to
   trigger the release.

## Rehearsing a release

```sh
gh workflow run release.yml --ref main            # rehearses the VERSION file's version
gh workflow run release.yml --ref main -f tag=v1.7.3
```

A dispatch **is** a rehearsal. There is no input that makes it publish:
`REHEARSAL` is derived from `github.event_name` alone, and every step that
cannot be undone — the npm publish, the registry publish, the image push and its
signatures, the `.mcpb` upload, the release un-draft, the commit back to `main` —
carries `if: env.REHEARSAL != 'true'`. The `tag` input only states which version
is being rehearsed; preflight refuses it if it disagrees with `VERSION`, exactly
as it refuses a pushed tag that does.

Everything else runs, and that is the point:

- **GoReleaser builds the real matrix** under `--snapshot --skip=publish,sign,announce`,
  and the binaries carry the rehearsed version rather than a `-SNAPSHOT-` suffix
  (`snapshot.version_template` in `.goreleaser.yml`), so the npm validator
  downstream accepts them.
- **The image is built for both platforms, smoke-tested, and exported to an OCI
  layout** instead of pushed. Its digest is real, so the `server.json` stamp runs
  on a real value — without that, a rehearsal could not exercise the one thing
  the stamper's refusals exist for.
- **Both trusted-publishing exchanges run**, because they mint a credential and
  spend nothing, and a policy that has drifted is exactly what a rehearsal should
  catch. NuGet's is its `NuGet/login` step; PyPI's is a step of its own, since
  the publish action performs the exchange only as part of an upload — without it
  a rehearsal would prove nothing about PyPI at all. Both fail loudly and name
  the four values the policy has to carry. The rule generalises: a credential
  exchange that *mints* runs in a rehearsal; only the upload that spends it is
  skipped.
- **The `.mcpb` is packed and `server.json` is stamped**, locally, and nothing is
  uploaded.
- **syft and oras are installed and the image SBOMs are generated**, from the two
  smoke images the docker job's daemon holds, with the package-count guard. The
  installs and the scan are the part of that feature that can rot, so they are
  not behind the rehearsal switch; only the attestations and the `oras attach`
  are, because a rehearsal pushes no index to attach them to.
- **Every publisher job runs**, fetching the rehearsal's own build through
  `scripts/fetch-release-assets.sh` instead of downloading a release, so the
  whole graph — not just the build — is exercised.

Read the log for five things: the tag preflight resolved, a `sha256:` digest read
back from the OCI export, `N packages from docker:libgen-mcp:smoke-…`,
`npm distribution valid`, and `server.json pinned to …`.

**What a rehearsal cannot prove, and why.** Everything irreversible is skipped,
so the skipped steps are exactly the unexercised ones: the twelve image
attestations and the bare SPDX referrers (there is no pushed index to attach
them to), the four publishes, `mcp-publisher`, the release un-draft and the
commit to `main`. Two job permissions ride along with them — `contents: write`
and `attestations: write` on the GoReleaser job — because every step that needs
either is skipped. `id-token: write` *is* proven, by the two mint-only exchanges.

## The shape of the release

Fourteen jobs, and the edges are the point rather than the count. The graph, the
three rules that hold it together, the ordering the registries force, and what a
rehearsal can and cannot prove are one page:
**`docs/development/release-chain.md`**. Read it before changing
`.github/workflows/release.yml`; what follows here is only what a release-cutter
needs in hand.

Three rules from it, because each one is a failure that happened:

- **The signing identity is never live while third-party build code runs.**
  `docker` keeps `packages: write` and no `id-token`; `sign-attest` runs no build
  action at all. It is also what stops a re-run from rebuilding: the image build
  is not byte-reproducible, so a retried signature used to push a new index under
  the same tags and orphan the one already signed.
- **Nothing packages a build directory.** Each publisher fetches the release and
  checks it against the cosign-signed `checksums.txt`, so what ships is what was
  published.
- **Nothing advertises what nobody checked.** `mcp-registry` and
  `commit-manifests` wait on `verify-published`, which compares what the three
  registries actually serve with that same signed manifest, from a job holding no
  publishing credential.

The tag is enough for the version-bearing files the workflow owns: on release,
`scripts/update-server-json-sha.sh` re-stamps `server.json`'s version, its
per-package versions, its identifiers and their `fileSha256` digests,
then stamps the version into the other five manifests (`lhm.plugin.json`,
`mcpb/manifest.json`, `.plugin/plugin.json`, `plugin.json`,
`npm/libgen-mcp/package.json`) and the version and release date into
`CITATION.cff` — the
same set `check-manifests` gates — and commits the result back to main. The manual
bump above exists so the pre-tag CI gates pass, not because the digests need to
be right — they cannot be until the binaries exist.

**The two plugin manifests are different schemas for different directories.**
`.plugin/plugin.json` is the Open Plugins location and validates against a
`plugin.schema.json` beside it; `plugin.json` at the repository root is the Agent
Plugins one and names the remote `https://agent-plugins.org/schemas/1.0.0/plugin.schema.json`,
so a validator can fetch it. Keep them separate rather than symlinking one to the
other — the root schema sets `additionalProperties: false` and has no `logo` or
`mcpServers`, both of which the Open Plugins file carries.

**What `server.json` declares is four real packages**, and each one's shape is
load-bearing:

- **`registryType: "mcpb"` means the bundle, not a binary.** It names
  `libgen-mcp.mcpb`, whose `fileSha256` cannot come from `checksums.txt` — the
  bundle is built after GoReleaser runs, and appending it to that file would
  invalidate the signature over it. The stamper hashes the path passed as its
  **third** argument instead, and refuses a run in which nothing got a hash.
- **The two `oci` entries carry their digest**, which only exists once the image
  index has been pushed. So the `docker` job publishes the digest as an output,
  the `release` job `needs:` it, and the stamper takes it as its **fourth**
  argument and refuses to stamp a digest-pinned identifier without one — that
  refusal is what stops a new tag from being pinned beside the previous release's
  image. Both registries hold the identical index, so one digest serves both;
  the rewrite still happens per entry, or one registry's reference lands under
  the other's.
- **An OCI entry carries no `packageArguments`**, because the image's `CMD`
  already is the argument list and any argument replaces it wholesale.
- **The `npm` entry is validated against the *published* package.** The registry
  reads `mcpName` from the `package.json` on npmjs.com, so that field has to be
  in a version that is already published. The release job publishes npm before it
  runs `mcp-publisher`, in that order, which is what makes the two agree.

**The registry re-fetches all four entries at publish time, and that race is
retried rather than failed.** `mcp-publisher publish` validates the npm entry by
reading `mcpName` off the published package, the PyPI one by GETting pypi.org's
JSON for the version, the NuGet one by reading that version's README off the v3
feed, and the OCI one by resolving the tag — all minutes after the steps above
uploaded them. nuget.org runs a validation pass of its own before a pushed
version becomes visible, which takes minutes, so its "wait for validation to
complete" is lag by construction. The step therefore retries **only** the
wordings the registry labels transient, eight times at a minute apart; a plain
404 is deliberately not among them, so a package that genuinely failed to
publish fails on the first attempt rather than eight minutes later.

`make check-stamper` drives all of this against a fixture, in CI's `server.json`
job; it needs no network and no release. `make check-server-json-packages` is the
other half and does need both: it downloads every declared artifact and checks it
is what it claims — the bundle really is a zip with a `manifest.json`, the image
tag still resolves to the pinned digest, both platform manifests carry the
ownership label, and the published npm version carries `mcpName`. It runs in the
same CI job **on pushes only**, because it moves tens of megabytes. A published
image that predates a label or a `CMD` this repository has since added is
reported as a note rather than a failure, and only while the `Dockerfile` in the
tree declares the thing that is missing.

**A `remotes` URL must be globally unique across the whole registry, and the
comparison is on the literal string.** The registry refuses a publish whose remote
URL any other server already claims, templates included: v1.5.2 failed to publish
because `server.json` declared `https://{host}:{port}/` as a self-hosted form,
copied from the sibling `gitlab-mcp-server`, which had claimed that exact template
first. Checking that nothing claims your *hostname* is not the check — the string
is. A self-hosted templated remote is therefore only safe if no other server of
yours already publishes the same template.

The `server.json` CI job keeps its narrow name for history rather than for its
scope: it gates every version-bearing manifest. It is **not** a required status
check any more — the branch ruleset requires `CI verdict` alone — so renaming it
means renaming it in `verdict`'s `needs` list, not in the ruleset. See
`docs/development/repository-settings.md`.

## The channels, and what each one's first publish needed

A tag publishes to seven places. Six are automatic; LobeHub is not.

| Channel | Auth | One-time setup |
| --- | --- | --- |
| GitHub release | `GITHUB_TOKEN` | — |
| ghcr.io + Docker Hub | `GITHUB_TOKEN`, `DOCKERHUB_*` | — |
| npm | OIDC trusted publisher | done (bootstrap publish, then the publisher) |
| PyPI | OIDC trusted publisher | done (1.7.2 uploaded by hand, then the publisher) |
| NuGet | `NuGet/login` OIDC → 1-hour key | done (1.7.2 pushed by hand, then the policy) |
| Homebrew tap | `TAP_DEPLOY_KEY_B64` | the `jmrplens/homebrew-tap` repository and its deploy key |
| winget | `WINGET_TOKEN` | the `jmrplens/winget-pkgs` fork, and one manual manifest submission accepted upstream |
| LobeHub | stored `lhm` credential, auto-renewed | `make publish-lobehub`, by hand after the tag |

**Every trusted publisher on this repository names a blank environment**, and no
publishing job declares `environment:`. npm matches on repository, workflow file
**and** environment; PyPI and NuGet match the same way. One job with an
environment and the others without is a failure that only appears during a real
release, on the one path that never runs before a tag.

**Homebrew and winget degrade rather than block.** Each checks for its secret and
warns out when it is missing, because a release must not be held up by a channel
whose external half does not exist yet. The winget job is skipped whole on a
dispatch — it opens a pull request against `microsoft/winget-pkgs`, and there is
no dry form of that.

## Publishing to npm

npm is part of the tagged release and needs no manual step. After GoReleaser
writes `dist/`, the release job validates the assembled packages
(`make validate-npm-local NPM_BINARIES=dist`) and then publishes them with
`scripts/publish-npm.sh` — the six per-platform packages first, the launcher
last, so an install racing the publish never finds a launcher pinning packages
the registry does not have yet.

Authentication is npm's **OIDC trusted publisher**: no stored token, no
`NODE_AUTH_TOKEN`, and no `--provenance` flag (trusted publishing attaches
provenance itself). The workflow already grants `id-token: write` at the top
level, and the publish step installs `npm@11.5.1` first because that is the floor
version that performs the OIDC exchange. The trusted publisher configured on
npmjs.com for each of the seven packages names the GitHub account `jmrplens`,
repository `libgen-mcp`, workflow `release.yml`, and a **blank** environment —
the release job declares no `environment:`.

Two failure modes worth knowing:

- **Re-running the release job is safe.** `publish-npm.sh` asks `npm view` first
  and skips any version already on the registry, so a job retried after a later
  step failed does not die on npm's 409.
- **A brand-new package 404s on `install` for a few minutes** after its first
  publish while the registry propagates. Verify with `npm view`, and re-check
  before concluding the publish failed.

`make publish-npm NPM_BINARIES=<dir>` is the manual fallback (it was the
bootstrap path, since a package cannot have a trusted publisher until it exists).
It takes auth from the environment and never sees a credential itself; if it is
ever needed again, put the token in a temporary `.npmrc` **outside the repo**,
point npm at it with `NPM_CONFIG_USERCONFIG`, and delete it immediately after.

## Publishing to the LobeHub Marketplace

LobeHub is the one listing that is **not** part of the tagged release, and it
needs a manual step after the tag:

```bash
make publish-lobehub    # npx -y @lobehub/market-cli@$(LOBEHUB_CLI_VERSION) plugin update
```

**The CLI version is pinned in the `Makefile`**, for the reason
`GORELEASER_VERSION` and `COSIGN_VERSION` are: an unpinned `npx -y` downloads
and runs whatever the registry serves as latest, and this target runs on the
maintainer's machine with a credential that can publish the listing. Bump
`LOBEHUB_CLI_VERSION` deliberately — 0.0.41 changed the command's signature and
broke this target until PR #119 caught up.

**Run it and walk away: it prompts for nothing.** The credential lives in
`~/.lobehub-market/user-credentials.json` and carries a `refreshToken`, so the
CLI renews itself on the first command and prints `✓ Authenticated as …` — even
when `lhm auth status` claims the token expired. Measured again on 2026-09-22
publishing v2.0.0, which took one command and no browser.

Only the **first** login on a machine is interactive: `lhm login` is OAuth2 PKCE
with a localhost callback, with no device-code flow and no token flag. That is a
one-time cost, already paid here, and it is the reason this step is not in CI —
the credential is the maintainer's machine's, not a repository secret. It is not
a reason to warn about a prompt before every release, which this page did until
now and which cost the maintainer the same correction several releases running.

So the release workflow only *stamps* the version into `lhm.plugin.json` (step 5
of `scripts/update-server-json-sha.sh`, alongside the other two manifests,
committed back to main); the publish is a human running one target that finishes
on its own.

The manifest also carries the full `tools` and `prompts` arrays, and it has to:
LobeHub derives a listing's capability badges from those arrays, because its
crawler cannot introspect a server that ships as a Go binary or a Docker image.
Without them the marketplace advertises **zero tools and zero prompts** no matter
what the server registers — which is exactly what the listing showed until
v1.3.3. Never hand-edit them; `make gen-lhm-manifest` regenerates them from a
real `tools/list` + `prompts/list` round-trip and `make check-lhm-manifest` fails
CI when they drift. Re-publishing the same version merges the supplied fields
into it, so re-running the publish after a partial failure is safe.
