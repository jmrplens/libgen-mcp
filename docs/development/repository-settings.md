# Repository settings

**Reference** — for the maintainer, and for anyone wondering why a job is shaped
the way it is.

Some of what this repository depends on is not in this repository. A branch
ruleset, three trusted publishers, a handful of secrets and two Actions defaults
are configured in GitHub's settings, and a workflow that contradicts one of them
fails in a place that does not name the cause — sometimes only during a real
release, on the path that never runs before a tag.

This page is the written-down copy. **The settings themselves are the source of
truth**, so read a surprising row against the real thing before acting on it;
every value below was read from the GitHub API on **2026-09-21** and is recorded
with the reason it is that way rather than as a value to restore blindly.

## The branch ruleset on `main`

One ruleset, `Protect main`, active, targeting `refs/heads/main`:

| Rule                   | What it does                                                                        |
| ---------------------- | ----------------------------------------------------------------------------------- |
| Restrict deletions     | `main` cannot be deleted                                                            |
| Block force pushes     | No non-fast-forward push                                                            |
| Require a pull request | **Zero** required approvals, **squash** the only allowed merge method               |
| Required status checks | Exactly one: **`CI verdict`**. Not strict, so a branch need not be rebased to merge |

**Zero approvals is not an absent rule.** It is what makes a solo-maintained
repository still go through a pull request, which is where every check runs and
where a squash commit gets its message. The interesting part of that rule is the
one beside it: **an extra approval is required for unattributed changes** — a
commit whose author GitHub cannot match to an account. That is the failure mode
`CLAUDE.md` § *Commit identity* describes, where a commit authored with an
address that belongs to no account is signed and still shows as unverified. Here
it also stops the merge.

**Squash is the only merge method, which is why a pull request body matters.** A
squash merge uses the body as the commit message, so anything left in a
description — including a block a review bot appended after it was written —
lands permanently in `main`'s history, and editing the pull request afterwards
does not remove it.

## Who bypasses it

Two actors, both `always`:

- **Deploy keys.** This is what lets the release's `commit-manifests` job push
  the stamped manifests back to `main` over SSH with `RELEASE_DEPLOY_KEY_B64`.
  That job holds the deploy key **alone** and runs no third-party code, which is
  the reason it is a job of its own rather than a step of the release job.
- **Repository admins.** The maintainer, for the cases a ruleset cannot
  anticipate.

A deploy key bypassing branch protection is a real grant, so the narrowness of
the job holding it is the control: no build action, no third-party step, nothing
that could be made to push something else.

## The one required check

`CI verdict` is the only required status check, and it is a job that does nothing
but read the others: it `needs:` every job in `ci.yml` and runs
`.github/scripts/needs-verdict.sh`, which fails unless each one reported
`success`.

**That is one edit in one file instead of a settings change nobody remembers.**
Naming every job in the ruleset would mean a job added later is not a gate until
somebody adds it there too — a protection that weakens each time the pipeline
grows. Adding a job to `verdict`'s `needs` list makes it required, in the file
the job was added to.

Two details keep it honest. The job carries `if: always()`, without which it is
skipped the moment anything it needs fails — and a skipped check is not a failing
one as far as a ruleset is concerned. And the script **refuses `skipped` as well
as `failure`**, with one paired exception: `docker` is pull-request-only, so its
skip is legitimate on a push and only there.

## Trusted publishers, and the blank environment

npm, PyPI and NuGet each publish with no stored token. Each matches an OIDC
trusted publisher configured on its own registry, and all three match on the same
three things:

| Registry | Matches on                                                                                                   |
| -------- | ------------------------------------------------------------------------------------------------------------ |
| npm      | the account `jmrplens`, the repository `libgen-mcp`, the workflow `release.yml`, and a **blank** environment |
| PyPI     | the repository, the workflow file, and a **blank** environment                                               |
| NuGet    | the repository, the workflow file, and a **blank** environment                                               |

**No publishing job may declare `environment:`.** All three publishers name a
blank one, so adding an environment to any of those jobs breaks all three at
once — during a real release, on the one path that never runs before a tag.

**An environment named `release` used to make that easy to trip** — created
2026-07-25 with no protection rules and used by no job, so `environment: release`
on a publishing job would have looked entirely reasonable, resolved without
error, and broken every trusted publisher. It was **deleted on 2026-09-22**, so
the name no longer resolves and a job that names it fails at once instead of
publishing nothing. Recording it because the trap is worth recognising if anyone
recreates the environment.

The only environments left are `github-pages`, which the deploy job in
`pages.yml` declares because that is how GitHub Pages deployments are addressed
at all, and `copilot`, which exists for GitHub's coding agent and is unrelated to
any workflow here.

## Immutable releases

**Immutable releases are on** (`gh api repos/jmrplens/libgen-mcp/immutable-releases`
answers `enabled: true`; v1.7.3 carries `immutable: true`). Once a release leaves
draft, its assets and its tag are fixed.

It is the one setting on this page that a gate reads rather than merely depends
on: `scripts/validate-server-json-packages.sh` checks each release's `immutable`
flag and **warns on a mutable one**, because every other artefact `server.json`
declares is pinned by a digest or held by a registry that does not allow a
version to be replaced, and a mutable GitHub release is the one place where an
asset and the `checksums.txt` that vouches for it can be swapped together.

**Draft-then-publish is its precondition, not a separate nicety.** An immutable
release cannot grow an asset afterwards, and the `.mcpb` bundle is uploaded after
GoReleaser has created the release — so GoReleaser creates it with `draft: true`
and the workflow flips it only once every asset is attached. Reversing those two
steps does not produce a release with a late asset; it produces a release that
cannot accept one.

## Secrets

| Secret                                       | Held by                                   | If it is missing                                        |
| -------------------------------------------- | ----------------------------------------- | ------------------------------------------------------- |
| `SONAR_TOKEN`                                | `ci.yml`, the SonarCloud job              | The quality-gate check cannot report                    |
| `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN`     | `release.yml`, `docker` and `sign-attest` | The Docker Hub mirror is skipped; ghcr.io is unaffected |
| `RELEASE_DEPLOY_KEY_B64`                     | `release.yml`, `commit-manifests`         | The stamped manifests are not committed back to `main`  |
| `TAP_DEPLOY_KEY_B64`                         | `release.yml`, `homebrew`                 | The tap job warns and exits; the release continues      |
| `WINGET_TOKEN`                               | `release.yml`, `winget`                   | The winget job warns and exits; the release continues   |
| `GITLAB_API_TOKEN` / `GITLAB_MIRROR_SSH_KEY` | `gitlab-mirror.yml`                       | The mirror does not run                                 |

**Homebrew and winget degrading rather than failing is deliberate**: a release
must not be held up by a channel whose external half does not exist yet.

`WINGET_TOKEN` is a personal access token against a fork of
`microsoft/winget-pkgs`, and **its scope is `public_repo` and nothing else** —
anything wider is a credential in a job that opens a pull request against
somebody else's repository. A token's scope is not readable through the API, so
this one is stated here from the maintainer rather than measured; treat replacing
it as a scope change to be recorded on this line.

**No secret on this repository is unread by a workflow.** `FLY_API_TOKEN` was the
exception and was **deleted on 2026-09-22**: nothing referenced it, and a
credential nothing uses still grants what it grants while producing no log line
here when it is used somewhere else. The same test applies to the next one —
a secret no `uses:` or `env:` in `.github/workflows/` names is a standing grant
with no audit trail, not a spare.

## Actions defaults

- **The default `GITHUB_TOKEN` permission is `read`**, and Actions may not
  approve pull requests. Every workflow here also declares `permissions: {}` at
  the top and states what each job needs, so the repository default is a second
  floor rather than the only one.
- **Every `uses:` is pinned to a commit SHA** with its version in a trailing
  comment, and `make check-supply-chain` enforces it — including inside a
  commented-out block, because it reads the raw text for the pins. The trailing
  `# v7` is not decoration: Dependabot reads it to know which version the SHA
  stands for, and without it an action is pinned **and** frozen.

## Code scanning

**CodeQL runs as advanced setup**, defined in `.github/workflows/codeql.yml`, and
GitHub's default setup is **not configured** — deliberately. Default setup pins
`GOTOOLCHAIN=local` to whatever Go the CodeQL runtime ships, so the Go job breaks
every time `go.mod` moves to a release the runtime has not picked up yet. The
workflow installs the toolchain with `setup-go` and uses `build-mode: manual`
instead. **The two cannot coexist**, so enabling default setup in the repository
settings disables the workflow.

## What is deliberately in the repository instead

Not everything that could be a setting is one:

- **Dependabot's cooldown** (`cooldown: {default-days: 3}` per ecosystem) is in
  `.github/dependabot.yml`, where it is reviewable and has a reason beside it.
  Use `default-days` alone — the SemVer sub-keys are rejected outright for the
  docker ecosystem, and a configuration file Dependabot refuses stops **every**
  update rather than that one.
- **The required-check list** is `verdict`'s `needs:`, for the reason above.
- **Job permissions** are declared per job rather than granted repository-wide.

## Where the rest of it is

| For                                             | See                                   |
| ----------------------------------------------- | ------------------------------------- |
| The release workflow's shape and its invariants | [The release chain](release-chain.md) |
| What each automated check asserts               | [The gates](gates.md)                 |
| The steps to cut a release                      | `.claude/skills/release/SKILL.md`     |
