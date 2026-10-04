# Install with winget

**How-to guide** — for anyone on Windows who manages software with winget.

> **Not available yet.** The first manifest for `jmrplens.libgen-mcp` is under
> review in [microsoft/winget-pkgs#437507](https://github.com/microsoft/winget-pkgs/pull/437507),
> so `winget install` does not find the package today. Everything below
> describes what the package will be once that pull request is merged. Until
> then, use the [release binary](binary.md), [npm](npm.md), [PyPI](pypi.md) or
> [NuGet](nuget.md) on Windows.

## What you get

A **portable** package: winget downloads the release executable for your architecture, `libgen-mcp-windows-amd64.exe` or `libgen-mcp-windows-arm64.exe`, and registers it as the command `libgen-mcp`. No installer runs. Once the first manifest is accepted, every release submits its own version to `winget-pkgs` from the release workflow, so new versions follow the release on their own after Microsoft's review.

## Prerequisites

| Requirement | Detail                                                                                         |
| ----------- | ---------------------------------------------------------------------------------------------- |
| winget      | The Windows Package Manager, part of App Installer on Windows 10 1809 and later and Windows 11 |
| A platform  | Windows on x64 or arm64                                                                        |

## Install

```powershell
winget install --id jmrplens.libgen-mcp -e
```

Open a new terminal afterwards, so it picks up the `PATH` entry winget added, and check:

```powershell
libgen-mcp --version
```

## Verify what you installed

winget checks the file it downloads against the SHA256 in the manifest, computed from the release asset when the manifest was submitted, and refuses a mismatch. The executable is the release asset byte for byte, so the checks on the [release binary page](binary.md#verify-what-you-installed) apply to it as well, including its build provenance:

```powershell
gh attestation verify (Get-Command libgen-mcp).Source -R jmrplens/libgen-mcp `
  --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml
```

## Where it lands on disk

winget keeps portable packages under `%LOCALAPPDATA%\Microsoft\WinGet\Packages\`, in a directory named after the package, and puts a link to the command in `%LOCALAPPDATA%\Microsoft\WinGet\Links`, which is on your `PATH`. The package carries the executable only: `LICENSE` and `THIRD_PARTY_NOTICES` are not in it, and the release that produced it is where to find them. What the server writes once it runs is listed on the [installation overview](overview.md#what-every-channel-shares).

## Configure a client

```json
{
  "mcpServers": {
    "libgen": { "command": "libgen-mcp" }
  }
}
```

If the client does not find it, give it the full path that `(Get-Command libgen-mcp).Source` prints, with each backslash doubled. Where each client keeps this file is on [Connect a client](../clients.md).

## Upgrade

```powershell
winget upgrade --id jmrplens.libgen-mcp -e
```

A client that already has the server running keeps the old process until it restarts it; `libgen-mcp --shutdown` asks every running instance to exit.

## Pin a version

```powershell
winget install --id jmrplens.libgen-mcp -e --version 2.2.0
winget pin add --id jmrplens.libgen-mcp
```

`--version` installs a version the repository holds; `winget pin add` keeps `winget upgrade --all` from moving it.

## Uninstall

```powershell
winget uninstall --id jmrplens.libgen-mcp -e
```

## Platform notes

- **Windows on Arm** gets the native arm64 executable.
- **Two channels, two commands.** An npm, PyPI or NuGet install is a separate copy that winget does not know about; uninstall the one you no longer use, or two `libgen-mcp` commands end up on your `PATH`.

## Common problems

**`No package found matching input criteria`.** The package is not published yet; see the note at the top of this page.

**`libgen-mcp` is not recognised right after the install.** The terminal predates the `PATH` change. Open a new one.

Other channels are compared on the [installation overview](overview.md).
