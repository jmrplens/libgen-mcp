# Release binaries and go install

**How-to guide** — for anyone who wants the executable itself, or to build it from source.

Every [GitHub release](https://github.com/jmrplens/libgen-mcp/releases) carries the server as a single executable per platform. It is the file every package manager on the other pages delivers; downloading it yourself skips the package manager and leaves nothing installed but that file.

## What you get

| Asset                          | For                                                            |
| ------------------------------ | -------------------------------------------------------------- |
| `libgen-mcp-linux-amd64`       | Linux x86_64, glibc or musl                                    |
| `libgen-mcp-linux-arm64`       | Linux aarch64, glibc or musl                                   |
| `libgen-mcp-darwin-arm64`      | macOS on Apple Silicon                                         |
| `libgen-mcp-darwin-amd64`      | macOS on Intel                                                 |
| `libgen-mcp-darwin-all`        | macOS, both architectures in one universal binary              |
| `libgen-mcp-windows-amd64.exe` | Windows x64                                                    |
| `libgen-mcp-windows-arm64.exe` | Windows on Arm                                                 |
| `<asset>.sbom.json`            | The SBOM of each binary                                        |
| `THIRD_PARTY_NOTICES`          | The licence texts of the modules the binary links (from 2.1.0) |
| `checksums.txt`                | SHA256 of every file above                                     |
| `checksums.txt.sigstore.json`  | The Sigstore bundle signing `checksums.txt`                    |

**The binaries are standalone.** They are built `CGO_ENABLED=0` and without `-buildmode=pie`, so they name no dynamic loader: the Linux files run on glibc and on musl, in a distroless image and on `scratch`. Nothing else needs installing.

The Claude Desktop bundles (`.mcpb`) are on the same release page and have [their own page](claude-desktop.md).

## Prerequisites

None to run it. To follow the steps below: `curl` (or a browser), and for verification [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) and, optionally, the [GitHub CLI](https://cli.github.com/). Building from source needs Go, under [Build from source](#build-from-source).

## Install

The `releases/latest/download/` address always serves the newest release's file under the same name.

**Linux** (on arm64, download `libgen-mcp-linux-arm64`; without root, `~/.local/bin` works as well, if it is on your `PATH`):

```bash
curl -fL -o libgen-mcp \
  https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-linux-amd64
chmod +x libgen-mcp
sudo mv libgen-mcp /usr/local/bin/
```

**macOS** (`libgen-mcp-darwin-all` runs on both architectures; `-darwin-arm64` and `-darwin-amd64` are the same program at about half the size):

```bash
curl -fL -o libgen-mcp \
  https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-darwin-all
chmod +x libgen-mcp
sudo mv libgen-mcp /usr/local/bin/
```

**Windows** (on Windows on Arm, download `libgen-mcp-windows-arm64.exe`):

```powershell
$dir = "$env:LOCALAPPDATA\Programs\libgen-mcp"
New-Item -ItemType Directory -Force $dir | Out-Null
Invoke-WebRequest -OutFile "$dir\libgen-mcp.exe" `
  https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-windows-amd64.exe
# put the directory on your user PATH, for new terminals
[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$dir", "User")
```

Then:

```bash
libgen-mcp --version
# libgen-mcp 2.1.0 (commit <commit>)
```

## Verify what you installed

Check the signature first, then the file against the manifest it signs:

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
# Verified OK

# 2. The file you downloaded is the one that manifest names.
sha256sum --ignore-missing -c checksums.txt
# libgen-mcp-linux-amd64: OK
```

On macOS use `shasum -a 256 --ignore-missing -c checksums.txt`; on Windows, compare `(Get-FileHash libgen-mcp.exe).Hash` with the line in `checksums.txt`. `gh release download` is a convenience: any download of the three files works, and `--pattern` takes the name of the asset you want. Why the first step is the one that matters is on the [installation overview](overview.md#verifying-what-you-install).

Every binary also carries SLSA build provenance in GitHub's attestation store, which answers which commit and which workflow run produced it:

```bash
gh attestation verify libgen-mcp-linux-amd64 -R jmrplens/libgen-mcp \
  --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml \
  --source-ref refs/tags/v2.1.0
```

`--source-ref` holds it to the release you meant; use the version you downloaded.

## Where it lands on disk

Wherever you put it: the binary is the whole installation. The usual places are `/usr/local/bin` or `~/.local/bin` on Linux and macOS, and a directory of your own under `%LOCALAPPDATA%\Programs` on Windows. What the server writes once it runs (mirror caches, downloads, temp files) is listed on the [installation overview](overview.md#what-every-channel-shares).

**Licences.** The binary links third-party modules whose licences ask for their texts to travel with it. A binary downloaded on its own does not carry them, so keep `THIRD_PARTY_NOTICES` from the same release beside it if you redistribute it. Every package manager channel installs that file for you.

## Configure a client

```json
{
  "mcpServers": {
    "libgen": { "command": "/usr/local/bin/libgen-mcp" }
  }
}
```

Use the full path: a desktop client does not always inherit your shell's `PATH`. On Windows, escape the backslashes, `"C:\\Users\\you\\AppData\\Local\\Programs\\libgen-mcp\\libgen-mcp.exe"`. Where each client keeps this file is on [Connect a client](../clients.md).

## Upgrade

Download the new file over the old one, and verify it the same way. If a client already has the server running, ask it to stop first, or the old process keeps its download slots and, in HTTP mode, its listener:

```bash
libgen-mcp --shutdown
```

`--shutdown` asks every other instance of this binary on the machine to exit, and kills what is left after five seconds. The client starts the new one on its next call.

## Pin a version

Every release keeps its files at a fixed address. Replace `latest/download` with `download/v<version>`:

```bash
curl -fL -o libgen-mcp \
  https://github.com/jmrplens/libgen-mcp/releases/download/v2.1.0/libgen-mcp-linux-amd64
```

A downloaded binary never updates itself, so it stays pinned until you replace it.

## Uninstall

Delete the file. Nothing else was installed; settings, caches and downloads are where the overview [lists them](overview.md#what-every-channel-shares).

## Build from source

`go install` compiles the server with your own toolchain, from the module's source. It needs **Go 1.27.1 or newer**, the version `go.mod` names; an older Go from 1.21 on downloads that toolchain by itself unless `GOTOOLCHAIN=local` is set.

```bash
go install github.com/jmrplens/libgen-mcp/v2/cmd/server@latest
```

> **The command is named `server`.** `go install` names a binary after its
> package directory, so this produces `$(go env GOPATH)/bin/server`, not
> `libgen-mcp`. Rename it, or build it with an explicit name:
>
> ```bash
> git clone https://github.com/jmrplens/libgen-mcp
> cd libgen-mcp
> go build -o libgen-mcp ./cmd/server
> ```

The `/v2` in the path is required: from major version 2, Go resolves a module only under its suffixed path, and the unsuffixed one stops at the last 1.x release. Pin a version with `@v2.1.0` in place of `@latest`; upgrade by running the command again; uninstall by deleting the file from `$(go env GOPATH)/bin`.

**Verify.** The Go toolchain checks every module it downloads against the public checksum database. That verifies the **source** it built from, not a binary this project published: the result is your build, which is why it carries no release signature. A `go install` build reports its version from the module (`libgen-mcp 2.1.0 (commit none)`), since no commit is stamped into it.

## Platform notes

- **macOS quarantine.** A file downloaded with a browser gets the quarantine attribute, and Gatekeeper then refuses to run a binary that is not notarized. `curl` sets no such attribute. For a browser download, `xattr -d com.apple.quarantine /usr/local/bin/libgen-mcp` clears it.
- **Windows SmartScreen** can warn about an executable downloaded with a browser. `Unblock-File "$env:LOCALAPPDATA\Programs\libgen-mcp\libgen-mcp.exe"` clears the mark; `Invoke-WebRequest` does not set it.
- **Any other platform** (FreeBSD, 32-bit, Linux on another architecture) has no prebuilt file. [Build from source](#build-from-source) with `GOOS` and `GOARCH` set.

## Common problems

**`cannot execute binary file: Exec format error`.** The file is for another architecture. `uname -m` says which you need: `x86_64` is `amd64`, `aarch64` and `arm64` are `arm64`.

**`Permission denied` when you run it.** The execute bit is missing: `chmod +x`.

**`sha256sum: checksums.txt: no file was verified`.** No file in the directory has a name `checksums.txt` lists. Keep the asset's own name until the check is done, and rename it afterwards.

**`go install` reports `libgen-mcp 1.7.3`, or fails with `unknown revision cmd/server/v2.1.0`.** The `/v2` is missing from the path: without it `@latest` resolves the last 1.x release, and a 2.x version is not found at all.

Other channels are compared on the [installation overview](overview.md).
