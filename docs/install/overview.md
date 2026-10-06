# Installation

**How-to guide** — for anyone putting the server on a machine.

`personal-library-mcp` is published to most of the places you already get software from.
Whichever you pick you end up with the same single executable: nothing is
compiled, no script runs at install time, and no account, API key or token is
needed by any of them.

This page compares the channels and holds what they have in common. Each channel
has a page of its own with the install, verification, upgrade, pinning and
removal steps. For the shortest path to a working client, see
[Getting started](../getting-started.md).

## Pick a channel

| Channel                             | Run it without installing                               | Install it                                                       | Needs                              | Platforms                                |
| ----------------------------------- | ------------------------------------------------------- | ---------------------------------------------------------------- | ---------------------------------- | ---------------------------------------- |
| [npm](npm.md)                       | `npx -y @jmrp.io/personal-library-mcp`                            | `npm install -g @jmrp.io/personal-library-mcp`                             | Node 18 or newer                   | Linux, macOS, Windows; x64 and arm64     |
| [PyPI](pypi.md)                     | `uvx personal-library-mcp`                                        | `pipx install personal-library-mcp`                                        | Python 3.9 or newer                | Linux, macOS 13+, Windows; x64 and arm64 |
| [NuGet](nuget.md)                   | `dnx personal-library-mcp`                                        | `dotnet tool install -g personal-library-mcp`                              | .NET 10 SDK                        | Linux, macOS, Windows; x64 and arm64     |
| [Homebrew](homebrew.md)             | —                                                       | `brew install gfade/tap/personal-library-mcp`                           | Homebrew                           | macOS and Linux; x64 and arm64           |
| [Docker](docker.md)                 | `docker run -i --rm ghcr.io/gfade/personal-library-mcp:latest` | —                                                                | Docker or an OCI runtime           | `linux/amd64`, `linux/arm64` images      |
| [Release binary](binary.md)         | —                                                       | download it and put it on your `PATH`                            | Nothing                            | Linux, macOS, Windows; amd64 and arm64   |
| [Go](binary.md#build-from-source)   | —                                                       | `go install github.com/gfade/personal-library-mcp/v2/cmd/server@latest` | Go 1.27.1 or newer                 | Wherever Go builds                       |
| [Claude Desktop](claude-desktop.md) | —                                                       | open the `.mcpb` file                                            | Claude Desktop                     | macOS, Windows, Linux (beta)             |
| [Agent plugin](agent-plugin.md)     | —                                                       | your host's plugin installer                                     | A plugin host and Node 18 or newer | Linux, macOS, Windows; x64 and arm64     |
| [winget](winget.md) (pending)       | —                                                       | `winget install --id jmrplens.personal-library-mcp -e`                     | winget                             | Windows; x64 and arm64                   |

If you have no preference: **`npx` if you already have Node 18 or newer, Docker
otherwise.** Neither installs anything you have to remember to update. If you
use Claude Desktop and nothing else, the extension is one click. To try the
server before installing anything, the [hosted endpoint](../hosted.md) needs
only a client.

**The server never updates itself**, on any channel. Upgrades come from whatever
installed it: `npm`, `pipx`, `dotnet tool update`, `brew upgrade`, a newer
image, a newer bundle or a fresh download. `personal-library-mcp --version` prints what you
have.

## What every channel shares

- **The same binary.** The package managers above do not build anything. Each
  one carries the release binary for your platform and puts it on a path, so the
  bytes are the same whichever route they arrived by, and the release's build
  provenance verifies each of them. Two exceptions:
  [`go install`](binary.md#build-from-source) compiles from source, and the
  [Docker image](docker.md) builds the same source in its own build stage.
- **Nothing runs at install time.** No postinstall script, no compiler, no
  network fetch beyond the package itself.
- **No credential.** the primary catalog needs none, and neither does any of the
  open-access providers the server falls back to. Four settings are opt-in:
  a CORE key, an AA member key, an OpenAlex key and an Unpaywall
  contact email; see [Configuration](../configuration.md).
- **No libc.** The binary is built `CGO_ENABLED=0` and **without**
  `-buildmode=pie`, so it names no dynamic loader at all: the same file runs on
  glibc and on musl, in a distroless image and on `scratch`. That is also why
  the PyPI wheels can honestly carry `musllinux` tags and why no npm package
  declares a `libc`.
- **Six platforms.** Linux, macOS and Windows, on amd64 and arm64. Anything else
  has to build from source.
- **The licences travel with it.** The npm, PyPI and NuGet packages, the Claude
  Desktop bundle, the image (under `/usr/share/licenses/personal-library-mcp`) and the
  Homebrew formula carry `LICENSE` and `THIRD_PARTY_NOTICES`. A binary
  downloaded on its own, or through winget, does not; from 2.1.0 every release
  publishes `THIRD_PARTY_NOTICES` as an asset of its own.

**What it writes on your machine**, whichever channel installed it:

| Path                                           | What it holds                                                                 |
| ---------------------------------------------- | ----------------------------------------------------------------------------- |
| `<os cache dir>/personal-library-mcp/mirrors.json`       | The discovered the primary catalog mirror list, cached 24 hours. Public URLs only |
| `<os cache dir>/personal-library-mcp/annas-mirrors.json` | The same for the AA mirrors                                       |
| Your download directory                        | Whatever `download` saved, wherever `PL_MCP_DOWNLOAD_DIR` points          |
| The system temp directory                      | `read`'s working copies, evicted on a size cap and a TTL                      |
| `~/.personal-library-mcp.env`                            | Settings, if you created it. Nothing creates it for you                       |

The cache directory is `~/.cache` on Linux, `~/Library/Caches` on macOS and
`%LocalAppData%` on Windows. Deleting any of it only forces a fresh fetch.
Uninstalling through any channel leaves all of it in place.

**Upgrading a server a client is already running.** Replacing the binary under a
running process leaves the old one holding a download slot, a temp-cache entry
and, in HTTP mode, the listener the new one wants. `personal-library-mcp --shutdown` asks
every other instance of this binary on the machine to exit and kills what is
left after five seconds, so the upgrade sequence is: install, `--shutdown`, let
the client start it again.

## From an MCP registry

`personal-library-mcp` is listed in the MCP registries under the identifier
`io.github.gfade/personal-library-mcp`. A client that installs servers from one of them can add it
from there instead of a configuration written by hand:

| MCP registry          | Listing                                                                                                                     |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| Official MCP Registry | [`io.github.gfade/personal-library-mcp`](https://registry.modelcontextprotocol.io/v0/servers?search=io.github.gfade/personal-library-mcp) |
| mcp.so                | [mcp.so/servers/personal-library-mcp-d62341](https://mcp.so/servers/personal-library-mcp-d62341)                                                |
| LobeHub               | [lobehub.com/mcp/jmrplens-personal-library-mcp](https://lobehub.com/mcp/jmrplens-personal-library-mcp)                                          |

The registry entry declares the six install packages and, as `remotes`, the
[hosted endpoint](../hosted.md) and the self-hosted `--http` form, so a registry-aware client
can offer either without you copying a URL. [Docker Hub](https://hub.docker.com/r/gfade/personal-library-mcp)
and [pkg.go.dev](https://pkg.go.dev/github.com/gfade/personal-library-mcp/v2) carry the image and the
Go module, but they are distribution channels rather than MCP listings. A listing can lag
behind a release; the version these pages describe is always the one in
[`VERSION`](../../VERSION).

## Verifying what you install

Every release is signed, and what that buys you depends on the channel. Each
channel's page has the commands:

| Channel                                                       | What is verifiable                                                                              | Who checks it              |
| ------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- | -------------------------- |
| [Release binary](binary.md#verify-what-you-installed)         | A Sigstore bundle over `checksums.txt`, plus SLSA build provenance per asset                    | You                        |
| [Claude Desktop](claude-desktop.md#verify-what-you-installed) | SLSA build provenance per bundle (the bundles are not in `checksums.txt`)                       | You                        |
| [Docker](docker.md#verify-what-you-installed)                 | A keyless cosign signature on the index and both platform manifests, plus SLSA build provenance | You                        |
| [npm](npm.md#verify-what-you-installed)                       | npm provenance, attached automatically because the publish is a trusted publisher               | `npm audit signatures`     |
| [PyPI](pypi.md#verify-what-you-installed)                     | PEP 740 attestations, attached automatically for the same reason                                | Shown on the project page  |
| [NuGet](nuget.md#verify-what-you-installed)                   | nuget.org's repository signature, plus SLSA build provenance per package (from 2.1.0)           | `dotnet nuget verify`, you |
| [Homebrew](homebrew.md#verify-what-you-installed)             | A SHA256 per platform asset, pinned in the formula                                              | `brew` itself, on download |
| [`go install`](binary.md#build-from-source)                   | The Go checksum database, over the **source**                                                   | The Go toolchain           |

**The signature is the half usually skipped, and it is the half that matters.**
A `checksums.txt` fetched from the same page as the binary proves only that the
two agree with each other. The Sigstore bundle proves the manifest was minted by
this repository's release workflow, and the signing is keyless — there is no
public key to distribute, the identity **is** the workflow, which is what
`--certificate-identity-regexp` pins in the cosign commands.

**The build provenance answers a different question**: which commit and which
workflow run produced the file. Every binary the package managers install is a
release asset byte for byte, so `gh attestation verify` checks it wherever it
landed, and each channel page says where that is. Two flags make the check mean
what you want it to:

- **`--signer-workflow gfade/personal-library-mcp/.github/workflows/release.yml`.** `-R`
  alone accepts an attestation any workflow of the repository minted; this holds
  it to the release workflow. Every `gh attestation verify` in these pages takes
  it.
- **`--source-ref refs/tags/v<version>`.** This holds it to the release you
  meant, so a valid attestation for another version does not pass. Add it
  wherever you know the version.

[cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 3.x and
the [GitHub CLI](https://cli.github.com/) are the only tools the checks need. A
2.x cosign reports "no signatures found" on an image a 3.x client verifies.

## Where to go next

| For                                            | See                                        |
| ---------------------------------------------- | ------------------------------------------ |
| Wiring it into a client                        | [Connect a client](../clients.md)          |
| A first search, end to end                     | [Getting started](../getting-started.md)   |
| Every setting, with its default and range      | [Configuration](../configuration.md)       |
| Deploying it centrally over HTTP               | [HTTP server mode](../http-server-mode.md) |
| What the four tools take and return            | [Tools](../tools.md)                       |
| An install that did not go the way a page says | [Troubleshooting](../troubleshooting.md)   |
