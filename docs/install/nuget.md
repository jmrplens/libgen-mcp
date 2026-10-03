# Install from NuGet

**How-to guide** — for anyone with the .NET SDK who wants the server as a .NET tool.

`libgen-mcp` is published on NuGet as [`libgen-mcp`](https://www.nuget.org/packages/libgen-mcp), a .NET tool whose entry point is the native release binary. Nothing in the packages is .NET code and nothing is compiled: the SDK downloads the binary for your machine and runs it.

## What you get

Seven packages per release:

- **`libgen-mcp`**, the pointer. It is what you install or run, and it names one package per runtime identifier. It also carries `.mcp/server.json`, the MCP Registry entry for **its own** version, which is what MCP-aware tooling reads to learn how to start the server.
- **`libgen-mcp.<rid>`**, one per runtime identifier: `linux-x64`, `linux-arm64`, `osx-x64`, `osx-arm64`, `win-x64` and `win-arm64`. Each carries the binary, `LICENSE` and `THIRD_PARTY_NOTICES`. The SDK downloads only the one for your host.

The tool manifest is `DotNetCliTool Version="2"`, the format with runtime-identifier packages, which is why an older SDK cannot install it.

## Prerequisites

| Requirement | Detail                                                                                                                                        |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| .NET SDK    | 10 or newer. `dnx` ships with the .NET 10 SDK, and the runtime-identifier tool format needs it too. No .NET runtime is used to run the server |
| A platform  | One of the six runtime identifiers above. Linux on glibc or musl alike: the binary needs no C library                                         |

`dotnet --version` should print `10.` or later.

## Install

```bash
dnx libgen-mcp                     # nothing installed: dnx caches the packages on the first run
dotnet tool install -g libgen-mcp
```

The global install puts a `libgen-mcp` command in `~/.dotnet/tools`. The Windows and macOS installers put that directory on your `PATH`; on Linux, add it yourself if `libgen-mcp` is not found.

> **Arguments for the server go after `--`.** Everything before `--` belongs to
> `dnx`, and everything after it goes to the server:
> `dnx libgen-mcp -- --http 127.0.0.1:8080`. Without the separator `dnx` reads
> `--http` as its own option. An installed tool takes its arguments directly:
> `libgen-mcp --http 127.0.0.1:8080`.

Then:

```bash
dnx libgen-mcp -- --version
# libgen-mcp 2.1.0 (commit 5493887)
```

## Verify what you installed

**The repository signature.** nuget.org signs every package it accepts, and `dotnet nuget verify` checks that signature on the cached copy:

```bash
dotnet nuget verify ~/.nuget/packages/libgen-mcp/*/libgen-mcp.*.nupkg
# Signature type: Repository
#   Subject Name: CN=NuGet.org Repository by Microsoft, ...
```

That signature is nuget.org's, so it says where the package came from, not which build produced it.

**The binary's build provenance.** The binary the SDK runs is the release asset byte for byte, so its attestation answers that. `dnx` runs it from the NuGet cache, and an installed tool's command links into the tool store:

```bash
# what dnx runs
gh attestation verify ~/.nuget/packages/libgen-mcp.linux-x64/<version>/tools/any/linux-x64/libgen-mcp \
  -R jmrplens/libgen-mcp --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml

# what dotnet tool install put on your PATH (Linux and macOS)
gh attestation verify "$(readlink -f "$(command -v libgen-mcp)")" \
  -R jmrplens/libgen-mcp --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml
```

Use your runtime identifier in place of `linux-x64`.

**The packages themselves (from 2.1.0).** The release workflow attests all seven packages as they were before nuget.org added its repository signature. Remove that one entry from a downloaded copy and verify what is left:

```bash
version=2.1.0
curl -sSLO https://api.nuget.org/v3-flatcontainer/libgen-mcp/$version/libgen-mcp.$version.nupkg
zip -q -d libgen-mcp.$version.nupkg .signature.p7s
gh attestation verify libgen-mcp.$version.nupkg -R jmrplens/libgen-mcp \
  --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml \
  --source-ref refs/tags/v$version
```

The same works for any `libgen-mcp.<rid>` package. Info-ZIP's `zip -d` leaves every other byte where it was, which is NuGet's own definition of the unsigned package; a tool that rewrites the archive gives other bytes, and `gh` then finds no attestation. What `--signer-workflow` and `--source-ref` add is explained on the [installation overview](overview.md#verifying-what-you-install).

## Where it lands on disk

| Install                  | The command                                              | The binary                                                                                |
| ------------------------ | -------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| `dnx`                    | Nothing on your `PATH`                                   | `~/.nuget/packages/libgen-mcp.<rid>/<version>/tools/any/<rid>/libgen-mcp`                 |
| `dotnet tool install -g` | `~/.dotnet/tools/libgen-mcp`, a link (a shim on Windows) | `~/.dotnet/tools/.store/libgen-mcp/<version>/libgen-mcp.<rid>/<version>/tools/any/<rid>/` |

On Windows `~` is `%USERPROFILE%` and the binary is `libgen-mcp.exe`. What the server writes once it runs is listed on the [installation overview](overview.md#what-every-channel-shares).

## Configure a client

```json
{
  "mcpServers": {
    "libgen": { "command": "dnx", "args": ["libgen-mcp"] }
  }
}
```

With a server argument, keep the separator: `"args": ["libgen-mcp", "--", "--log-level", "debug"]`. After a global install, name the command instead, `"command": "libgen-mcp"`, with no separator. Where each client keeps this file is on [Connect a client](../clients.md).

## Upgrade

`dnx` resolves the newest version when it starts, and uses its cache when that version is already there, so there is nothing to do. An installed tool:

```bash
dotnet tool update -g libgen-mcp
```

A client that already has the server running keeps the old process until it restarts it; `libgen-mcp --shutdown` asks every running instance to exit.

## Pin a version

```bash
dnx libgen-mcp@2.1.0
dotnet tool install -g libgen-mcp --version 2.1.0
```

In a client's configuration that is `"args": ["libgen-mcp@2.1.0"]`. For a repository that should run one version for everybody, a local tool manifest pins it in the tree: `dotnet new tool-manifest`, then `dotnet tool install libgen-mcp --version 2.1.0`, and `dotnet tool run libgen-mcp` starts it.

## Uninstall

```bash
dotnet tool uninstall -g libgen-mcp
```

Under `dnx` nothing was installed: remove the client entry, and delete `~/.nuget/packages/libgen-mcp` and `~/.nuget/packages/libgen-mcp.<rid>` to drop the cached packages.

## Platform notes

- **Alpine** works with the SDK's Alpine build: it resolves the `linux-x64` package, whose binary runs on musl unchanged.
- **The NuGet cache can be moved** with `NUGET_PACKAGES`; the paths above move with it.

## Common problems

**`dnx: command not found`.** The SDK is older than 10, or only a runtime is installed. `dotnet --list-sdks` shows what is there.

**The server rejects an option it should know.** It never received it: `dnx` took it. Put `--` before the server's arguments.

**`Settings file 'DotnetToolSettings.xml' was not found in the package`, then "Contact the tool author".** The package is fine: the SDK is older than 10 and cannot read runtime-identifier tool packages (the .NET 9 SDK says exactly this). Install the .NET 10 SDK.

Other channels are compared on the [installation overview](overview.md).
