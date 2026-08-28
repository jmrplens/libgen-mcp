---
name: release
description: Cut a libgen-mcp release — bump VERSION, mirror it into the version-bearing manifests, regenerate llms.txt, tag, and publish to npm and LobeHub. Use when cutting or preparing a release, bumping the version, or publishing to the MCP registry, npm or LobeHub Marketplace.
---

# Cutting a libgen-mcp release

The version lives in `VERSION` and is mirrored into five manifests. To cut a
release:

1. Bump `VERSION`.
2. Update the version in `server.json` (both `.version` and the six release-asset
   URLs), `mcpb/manifest.json`, `lhm.plugin.json` and `.plugin/plugin.json`, and
   run `make sync-npm-version` for `npm/libgen-mcp/package.json` (it moves the
   version and all six dependency pins together — never hand-edit it).
3. Run `make check-manifests`. It gates all five against `VERSION`, and CI runs
   it in the `server.json` job. Add any new version-bearing manifest to
   `VERSION_MANIFESTS` in the `Makefile` — a file that is not listed there is not
   gated, and will silently ship the previous release's number.
4. Run `make gen-llms`. `llms.txt` and `llms-full.txt` state the version in their
   opening line, so a bump leaves them stale. They are **not** covered by
   `check-manifests` — `make check-llms` is the gate that catches it, in a
   different CI job.
5. Open a PR; once merged, tag `vX.Y.Z` on main to trigger the release.

The tag is enough for the version-bearing files the workflow owns: on release,
`scripts/update-server-json-sha.sh` re-stamps `server.json`'s version, its
per-package versions, its six asset identifiers and their `fileSha256` digests,
then stamps the version into the other four manifests (`lhm.plugin.json`,
`mcpb/manifest.json`, `.plugin/plugin.json`, `npm/libgen-mcp/package.json`) — the
same set `check-manifests` gates — and commits the result back to main. The manual
bump above exists so the pre-tag CI gates pass, not because the digests need to
be right — they cannot be until the binaries exist.

**A `remotes` URL must be globally unique across the whole registry, and the
comparison is on the literal string.** The registry refuses a publish whose remote
URL any other server already claims, templates included: v1.5.2 failed to publish
because `server.json` declared `https://{host}:{port}/` as a self-hosted form,
copied from the sibling `gitlab-mcp-server`, which had claimed that exact template
first. Checking that nothing claims your *hostname* is not the check — the string
is. A self-hosted templated remote is therefore only safe if no other server of
yours already publishes the same template.

The `server.json` CI job is named for a required status check in the branch
ruleset, not for its scope — do not rename it without updating the ruleset too.

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
make publish-lobehub    # npx -y @lobehub/market-cli plugin publish
```

It cannot be automated. LobeHub's publish endpoint authenticates over OIDC PKCE
with a one-time interactive `lhm login` + `lhm github connect`; its own
documentation states there is no token-only, non-interactive path, and the
machine-to-machine credentials it does offer carry no publish permission. The
release workflow therefore only *stamps* the version into `lhm.plugin.json`
(step 5 of `scripts/update-server-json-sha.sh`, alongside the other two
manifests, committed back to main); the actual publish is a human running the
target above.

The manifest also carries the full `tools` and `prompts` arrays, and it has to:
LobeHub derives a listing's capability badges from those arrays, because its
crawler cannot introspect a server that ships as a Go binary or a Docker image.
Without them the marketplace advertises **zero tools and zero prompts** no matter
what the server registers — which is exactly what the listing showed until
v1.3.3. Never hand-edit them; `make gen-lhm-manifest` regenerates them from a
real `tools/list` + `prompts/list` round-trip and `make check-lhm-manifest` fails
CI when they drift. Re-publishing the same version merges the supplied fields
into it, so re-running the publish after a partial failure is safe.
