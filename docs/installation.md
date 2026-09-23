# Installation

**How-to guide** — for anyone putting the server on a machine.

`libgen-mcp` is published to most of the places you already get software from.
Whichever you pick you end up with the same single executable: nothing is
compiled, no script runs at install time, and no account, API key or token is
needed by any of them.

This page is the long form — per channel, what you get, how to install it, how
to check it is what this project published, how to upgrade and how to remove it.
For the shortest path to a working client, see
[Getting started](getting-started.md).

## Pick a channel

| Channel                                          | Run it without installing                               | Install it                                                       |
| ------------------------------------------------ | ------------------------------------------------------- | ---------------------------------------------------------------- |
| [npm](#npm)                                      | `npx @jmrp.io/libgen-mcp`                               | `npm install -g @jmrp.io/libgen-mcp`                             |
| [PyPI](#pypi)                                    | `uvx libgen-mcp`                                        | `pipx install libgen-mcp`                                        |
| [Homebrew](#homebrew)                            | —                                                       | `brew install jmrplens/tap/libgen-mcp`                           |
| [NuGet](#nuget)                                  | `dnx libgen-mcp`                                        | `dotnet tool install -g libgen-mcp`                              |
| [Docker](#docker)                                | `docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest` | —                                                                |
| [Release binary](#release-binary)                | —                                                       | download it and put it on your `PATH`                            |
| [Claude Desktop extension](#claude-desktop-mcpb) | —                                                       | open the `.mcpb` file                                            |
| [Go](#go-install)                                | —                                                       | `go install github.com/jmrplens/libgen-mcp/v2/cmd/server@latest` |

If you have no preference: **`npx` if you already have Node 18 or newer, Docker
otherwise.** Neither installs anything you have to remember to update.

## What every channel shares

- **The same binary.** The package managers above do not build anything. Each
  one carries the release binary for your platform and puts it on a path, so the
  bytes are the same whichever route they arrived by. The one exception is
  [`go install`](#go-install), which compiles from source.
- **Nothing runs at install time.** No postinstall script, no compiler, no
  network fetch beyond the package itself.
- **No credential.** Library Genesis needs none, and neither does any of the
  open-access providers the server falls back to. Keys exist for two optional
  sources and are opt-in; see [Configuration](configuration.md).
- **No libc.** The binary is built `CGO_ENABLED=0` and **without**
  `-buildmode=pie`, so it names no dynamic loader at all: the same file runs on
  glibc and on musl, in a distroless image and on `scratch`. That is also why
  the PyPI wheels can honestly carry `musllinux` tags and why no npm package
  declares a `libc`.
- **Six platforms.** Linux, macOS and Windows, on amd64 and arm64. Anything else
  has to build from source.

**What it writes on your machine**, whichever channel installed it:

| Path                                           | What it holds                                                                 |
| ---------------------------------------------- | ----------------------------------------------------------------------------- |
| `<os cache dir>/libgen-mcp/mirrors.json`       | The discovered Library Genesis mirror list, cached 24 hours. Public URLs only |
| `<os cache dir>/libgen-mcp/annas-mirrors.json` | The same for the Anna's Archive mirrors                                       |
| Your download directory                        | Whatever `download` saved, wherever `LIBGEN_MCP_DOWNLOAD_DIR` points          |
| The system temp directory                      | `read`'s working copies, evicted on a size cap and a TTL                      |
| `~/.libgen-mcp.env`                            | Settings, if you created it. Nothing creates it for you                       |

The cache directory is `~/.cache` on Linux, `~/Library/Caches` on macOS and
`%LocalAppData%` on Windows. Deleting any of it only forces a fresh fetch.

**Upgrading a server a client is already running.** Replacing the binary under a
running process leaves the old one holding a download slot, a temp-cache entry
and, in HTTP mode, the listener the new one wants. `libgen-mcp --shutdown` asks
every other instance of this binary on the machine to exit and kills what is
left after five seconds, so the upgrade sequence is: install, `--shutdown`, let
the client start it again.

## Verifying what you install

Every release is signed, and what that buys you depends on the channel:

| Channel                 | What is verifiable                                                                              | Who checks it              |
| ----------------------- | ----------------------------------------------------------------------------------------------- | -------------------------- |
| Release binary, `.mcpb` | A Sigstore bundle over `checksums.txt`, plus SLSA build provenance per asset                    | You, with the recipe below |
| Docker                  | A keyless cosign signature on the index and both platform manifests, plus SLSA build provenance | You, with the recipe below |
| npm                     | npm provenance, attached automatically because the publish is a trusted publisher               | `npm audit signatures`     |
| PyPI                    | PEP 740 attestations, attached automatically for the same reason                                | Shown on the project page  |
| NuGet                   | The author and repository signature nuget.org requires                                          | `dotnet nuget verify`      |
| Homebrew                | A SHA256 per platform asset, pinned in the formula                                              | `brew` itself, on download |
| `go install`            | The Go checksum database, over the **source**                                                   | The Go toolchain           |

**The signature is the half usually skipped, and it is the half that matters.**
A `checksums.txt` fetched from the same page as the binary proves only that the
two agree with each other. The Sigstore bundle proves the manifest was minted by
this repository's release workflow, and the signing is keyless — there is no
public key to distribute, the identity **is** the workflow, which is what
`--certificate-identity-regexp` pins below.

## npm

**What you get.** [`@jmrp.io/libgen-mcp`](https://www.npmjs.com/package/@jmrp.io/libgen-mcp),
a thin launcher package, plus one per-platform package carrying the binary. npm
installs only the platform package whose `os`/`cpu` match, so nothing is
compiled and no script runs at install time. Because the binary travels inside
the package npm downloads anyway, `npx`, `npm ci --ignore-scripts`, a private
registry mirror and `--offline` all work. Needs Node 18 or newer.

**Install.**

```bash
npx @jmrp.io/libgen-mcp              # run it, no install
npm install -g @jmrp.io/libgen-mcp   # or install it globally
pnpm add -g @jmrp.io/libgen-mcp      # with pnpm
```

Most MCP clients can launch it through `npx` directly, which leaves nothing to
install or keep updated by hand:

```json
{
  "mcpServers": {
    "libgen": { "command": "npx", "args": ["-y", "@jmrp.io/libgen-mcp"] }
  }
}
```

A global install puts `libgen-mcp` in your package manager's global binary
directory. If the command is not found afterwards, that directory is not on your
`PATH`: `npm config get prefix` shows npm's (the binaries are in its `bin`
subdirectory), and pnpm's is set up by `pnpm setup`.

**Verify.** The packages are published through an OIDC trusted publisher, so npm
attaches provenance to every one of them. `npm audit signatures` checks the
registry's signature over what you installed and the provenance attestation
where there is one:

```bash
npm install -g @jmrp.io/libgen-mcp
npm audit signatures
```

**Upgrade.** `npm install -g @jmrp.io/libgen-mcp@latest`. `npx` fetches the
latest on its own, subject to its cache.

**Uninstall.** `npm uninstall -g @jmrp.io/libgen-mcp`. The per-platform package
goes with it, because the launcher is the only thing that depends on it.

On a platform with no prebuilt binary the launcher exits with a message pointing
at the release binaries and at building from source.

## PyPI

**What you get.** [`libgen-mcp`](https://pypi.org/project/libgen-mcp/), one wheel
per platform with the binary inside. The wheel puts it on the scripts path, so
`libgen-mcp` is the command afterwards and **no Python runs when you use it**.
The distribution deliberately declares no console script: the entry point is the
native executable itself.

**Install.**

```bash
uvx libgen-mcp            # run it without installing
pipx install libgen-mcp   # or: pip install libgen-mcp
```

The Linux wheels carry both `manylinux` and `musllinux` tags, so the same file
installs on Debian and on Alpine — measured end to end, under
`python:3.13-alpine`.

**Verify.** The upload is an OIDC trusted publisher, so PyPI records a PEP 740
attestation for every file. It is shown on the project page beside each file. No
installer verifies it for you yet, so if the chain matters to your deployment,
prefer the [release binary](#release-binary) and the cosign recipe there.

**Upgrade.** `pipx upgrade libgen-mcp`, or `pip install --upgrade libgen-mcp`.
`uvx libgen-mcp@latest` pins the run to the newest release rather than uv's
cached one.

**Uninstall.** `pipx uninstall libgen-mcp`, or `pip uninstall libgen-mcp`.

## Homebrew

**What you get.** A formula from this project's own tap that downloads the
release asset for your platform.

**Install.**

```bash
brew install jmrplens/tap/libgen-mcp
```

**Verify.** The formula pins each platform's asset by SHA256 and `brew` refuses a
download that does not match, so the check is already done by the time the
command returns.

**Upgrade.** `brew upgrade libgen-mcp`. The tap is updated by the release
workflow, so a new version arrives with the next `brew update`.

**Uninstall.** `brew uninstall libgen-mcp`, and `brew untap jmrplens/tap` if you
have nothing else from it.

## NuGet

**What you get.** A .NET tool whose entry point is the native executable —
nothing in the packages is .NET code. A pointer package names one package per
runtime identifier, and the host's package carries the binary.

**Install.**

```bash
dnx libgen-mcp                       # run it without installing
dnx libgen-mcp -- --http :8080       # with arguments for the server
dotnet tool install -g libgen-mcp    # or install it
```

**Arguments for the server go after `--`**, because everything before it belongs
to `dnx`. That is the one thing about this channel that surprises people.

**Verify.** nuget.org requires a signature on every package it accepts, and
`dotnet nuget verify` checks it:

```bash
dotnet nuget verify ~/.nuget/packages/libgen-mcp/*/libgen-mcp.*.nupkg
```

**Upgrade.** `dotnet tool update -g libgen-mcp`.

**Uninstall.** `dotnet tool uninstall -g libgen-mcp`.

## Docker

**What you get.** A multi-arch image on the GitHub Container Registry, mirrored
to Docker Hub, running as a non-root user (UID `10001`).

**Install.**

```bash
docker pull ghcr.io/jmrplens/libgen-mcp:latest
```

**The image decides its transport from what standard input is.** `docker run -i`
connects a pipe and gets the stdio server, which is what an MCP client starts:

```bash
docker run -i --rm \
  -v "$HOME/Downloads:/downloads" \
  -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads \
  ghcr.io/jmrplens/libgen-mcp:latest
```

A run **without** `-i` connects `/dev/null` and gets the streamable HTTP
listener on port 8080 instead, which is what a compose service or a Kubernetes
pod gets:

```bash
docker run --rm -p 8080:8080 ghcr.io/jmrplens/libgen-mcp:latest
```

The host directory you mount has to be writable by UID `10001`. And note that
**any argument replaces the image's default command wholesale**, `--transport
auto` included — harmless, since naming a listener is deciding the transport,
but a flag you add is the whole command line rather than an addition to it. What
each shape needs behind a proxy is in
[HTTP server mode](http-server-mode.md).

**Verify.** The index and both platform manifests are signed keylessly with
cosign and carry SLSA build provenance. Verify the signature with a **cosign
3.x** client — a 2.x client reports "no signatures found" on an image a 3.x
client verifies:

```bash
cosign verify ghcr.io/jmrplens/libgen-mcp:latest \
  --certificate-identity-regexp '^https://github.com/jmrplens/libgen-mcp/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

And the provenance, which answers the other question — which commit and which
run produced it:

```bash
gh attestation verify oci://ghcr.io/jmrplens/libgen-mcp:latest -R jmrplens/libgen-mcp
```

**Upgrade.** `docker pull ghcr.io/jmrplens/libgen-mcp:latest`, or pin a version
tag and move it deliberately.

**Uninstall.** `docker image rm ghcr.io/jmrplens/libgen-mcp:latest`.

## Release binary

**What you get.** A prebuilt executable from the
[releases page](https://github.com/jmrplens/libgen-mcp/releases), named
`libgen-mcp-<os>-<arch>` — for example `libgen-mcp-linux-amd64` or
`libgen-mcp-darwin-arm64`.

**Install.**

```bash
# Example: Linux amd64
curl -L -o libgen-mcp \
  https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-linux-amd64
chmod +x libgen-mcp
sudo mv libgen-mcp /usr/local/bin/
```

**Verify.** Every release ships a `checksums.txt` and a Sigstore bundle signing
it. Check the signature first, then the file against the manifest:

```bash
cd "$(mktemp -d)"
gh release download --repo jmrplens/libgen-mcp \
  --pattern 'checksums.txt' --pattern 'checksums.txt.sigstore.json' \
  --pattern 'libgen-mcp-linux-amd64'

# 1. The manifest was signed by this repository's release workflow.
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/jmrplens/libgen-mcp/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 2. The file you downloaded is the one that manifest names.
sha256sum --ignore-missing -c checksums.txt
```

[cosign](https://docs.sigstore.dev/cosign/installation/) is the only extra tool
needed; `gh` can be replaced with any download you like.

Every asset also carries SLSA build provenance in GitHub's attestation store,
which answers which commit and which workflow run produced it:

```bash
gh attestation verify libgen-mcp-linux-amd64 -R jmrplens/libgen-mcp
```

**Upgrade.** Download the new asset over the old one. Run `libgen-mcp
--shutdown` first if a client already has one running, or the old process keeps
its slots and its listener.

**Uninstall.** Delete the file. Nothing else was installed.

## Claude Desktop (`.mcpb`)

**What you get.** A `.mcpb` extension bundle that Claude Desktop installs on its
own, on macOS (universal), Windows (amd64, which Windows on Arm runs under
emulation) and Linux (amd64 and arm64, for the Claude Desktop Linux beta). No
Docker, no Node, no `PATH` to edit. On Linux the bundle starts a small launcher
that picks the binary for the machine by `uname -m`, because the manifest can
choose a file per operating system but not per architecture.

**Install.** Download
[`libgen-mcp.mcpb`](https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp.mcpb),
open it with Claude Desktop, and confirm. On Linux, install it from Claude
Desktop's **Extensions > Install Extension…** instead: the Linux app registers
no handler for `.mcpb` files, so opening the file does nothing.

**Verify.** The bundle carries SLSA build provenance like every other release
asset:

```bash
gh attestation verify libgen-mcp.mcpb -R jmrplens/libgen-mcp
```

**Upgrade.** Download the new bundle and open it again.

**Uninstall.** Remove the extension from Claude Desktop's settings.

## `go install`

**What you get.** A binary you compiled, from source, with your own toolchain.
Needs Go 1.27 or newer.

**Install.**

```bash
go install github.com/jmrplens/libgen-mcp/v2/cmd/server@latest
```

This produces a binary named `server` in `$(go env GOPATH)/bin`, because the
command's package is `cmd/server`. To invoke it as `libgen-mcp`, build it with
an explicit name instead:

```bash
git clone https://github.com/jmrplens/libgen-mcp
cd libgen-mcp
go build -o libgen-mcp ./cmd/server
```

**Verify.** The Go toolchain checks every module it downloads against the public
checksum database. That verifies the **source** it built from, not a binary this
project published — the result is your build, which is exactly why it is worth
having and also why it carries no release signature.

**Upgrade.** Run the same command again; `@latest` re-resolves.

**Uninstall.** Delete the binary from `$(go env GOPATH)/bin`.

## Where to go next

| For                                                | See                                     |
| -------------------------------------------------- | --------------------------------------- |
| Wiring it into a client and running a first search | [Getting started](getting-started.md)   |
| Every setting, with its default and range          | [Configuration](configuration.md)       |
| Deploying it centrally over HTTP                   | [HTTP server mode](http-server-mode.md) |
| What the four tools take and return                | [Tools](tools.md)                       |
| An install that did not go the way this page says  | [Troubleshooting](troubleshooting.md)   |
