# Claude Desktop extension

**How-to guide** — for anyone who uses Claude Desktop and wants the server in one click.

`libgen-mcp` ships as a Claude Desktop extension, a `.mcpb` bundle Claude Desktop installs and runs by itself. No Docker, Node or Python, no `PATH` to edit and no JSON to write: the settings are a form in Claude Desktop.

## What you get

A bundle is a zip holding a manifest, the icon, the server for one or more systems, `LICENSE` and `THIRD_PARTY_NOTICES`. Releases publish one bundle per operating system, plus the universal one:

| System                                         | Bundle                                                                                                               | Size    | Carries                                           |
| ---------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- | ------- | ------------------------------------------------- |
| macOS (Apple Silicon and Intel)                | [`libgen-mcp-darwin.mcpb`](https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-darwin.mcpb)   | ~18 MiB | The universal macOS binary                        |
| Windows (x64; Windows on Arm runs it emulated) | [`libgen-mcp-windows.mcpb`](https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-windows.mcpb) | ~10 MiB | The x64 executable                                |
| Linux (x86_64 and arm64, Claude Desktop beta)  | [`libgen-mcp-linux.mcpb`](https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp-linux.mcpb)     | ~18 MiB | Both Linux binaries and a launcher that picks one |
| All three                                      | [`libgen-mcp.mcpb`](https://github.com/jmrplens/libgen-mcp/releases/latest/download/libgen-mcp.mcpb)                 | ~45 MiB | Every server above                                |

> **The per-system bundles start with the release after 2.1.0.** Releases up to
> and including 2.1.0 publish only the universal `libgen-mcp.mcpb`, so until the
> next release the three links above it answer 404 and the universal bundle is
> the one to download. From then on every release publishes all four, and the
> universal one stays for the links that point at it.

Each per-system bundle lists only its own system, so Claude Desktop refuses one opened on another system with a message rather than installing a server that cannot start. All four install as **the same extension**, so installing one replaces another in place.

**On Linux the bundle starts a small launcher**, `server/linux/launch.sh`, run with `/bin/sh`. A manifest can choose a command per operating system but not per architecture, so the launcher reads `uname -m`, picks `libgen-mcp-linux-amd64` or `libgen-mcp-linux-arm64`, restores its execute bit if the extraction lost it, and replaces itself with the binary through `exec`. Claude Desktop therefore stops the real server when it stops the extension, and the launcher never writes to stdout.

## Prerequisites

| Requirement    | Detail                                                                                          |
| -------------- | ----------------------------------------------------------------------------------------------- |
| Claude Desktop | A version that installs extensions (Settings has an **Extensions** section). On Linux, the beta |
| A platform     | macOS on Apple Silicon or Intel, Windows x64 or on Arm, Linux on x86_64 or arm64                |

## Install

1. Download the bundle for your system from the table above, or from the [latest release](https://github.com/jmrplens/libgen-mcp/releases/latest).
2. Open it with Claude Desktop.
   - **macOS and Windows:** double-click the file, or drag it onto Claude Desktop's settings window.
   - **Linux:** in Claude Desktop, **Extensions > Install Extension…**, then pick the file. The Linux app registers no handler for `.mcpb` files, so double-clicking it does nothing.
3. Claude Desktop shows the extension's details. Confirm, fill in any [settings](#settings) you want (none is required), and enable it.

Then ask Claude for a book or a paper. The extension's four tools (`search`, `get_details`, `download`, `read`) are listed under its entry in Settings.

## Settings

Every setting is optional. Each maps to one environment variable the server reads, and an empty field leaves the setting to the dotenv files below, then to the server's default.

| Setting                       | Default             | Variable                        | What it does                                                                                      |
| ----------------------------- | ------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------- |
| Mirror                        | empty (auto-select) | `LIBGEN_MIRROR`                 | Tries this Library Genesis mirror first, e.g. `https://libgen.li`; discovery remains the fallback |
| Download directory            | `~/Downloads`       | `LIBGEN_MCP_DOWNLOAD_DIR`       | Where `download` saves files. Must be writable, or the server refuses to start                    |
| Request timeout               | `10s`               | `LIBGEN_MCP_TIMEOUT`            | Per HTTP request, as a Go duration (`10s`, `1m`), at most `10m`                                   |
| Maximum download size (bytes) | `0`                 | `LIBGEN_MCP_MAX_DOWNLOAD_BYTES` | Refuses a download larger than this. `0` means no limit                                           |
| Log level                     | `info`              | `LIBGEN_MCP_LOG_LEVEL`          | `debug`, `info`, `warn` or `error`                                                                |
| Settings file                 | empty               | `LIBGEN_MCP_ENV_FILE`           | Absolute path to a dotenv file read in addition to `~/.libgen-mcp.env`                            |
| Unpaywall email               | empty               | `LIBGEN_MCP_UNPAYWALL_EMAIL`    | Your contact address for Unpaywall, which turns that article source on                            |
| CORE API key                  | empty               | `LIBGEN_MCP_CORE_KEY`           | A free key from core.ac.uk, which turns the CORE article source on. Marked sensitive              |

**The form and the settings file share the work by which fields have a value.** The extension passes every field as its variable, and a field with a value wins over both dotenv files (the one named under Settings file, then `~/.libgen-mcp.env`). A field left blank arrives empty, and the server reads an empty variable from the files instead, so Mirror, Unpaywall email and CORE API key can live in a file as long as their fields stay empty. Download directory, Request timeout, Maximum download size and Log level start out holding their defaults, so they arrive with those values and win over a file: change them in the form. Use the file for everything the form has no field for, such as `LIBGEN_MCP_EXTRA_SOURCES` or `LIBGEN_MCP_SOURCES`. A `.env` in Claude Desktop's working directory is never read. Every variable is described in [Configuration](../configuration.md).

The CORE key is marked sensitive, so Claude Desktop stores it encrypted with a key the operating system protects (where one is available) and masks it in the form.

## Verify what you installed

Every bundle carries SLSA build provenance like every other release asset:

```bash
gh attestation verify libgen-mcp-linux.mcpb -R jmrplens/libgen-mcp \
  --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml
```

Use the file you downloaded. The bundles are attached after the release's `checksums.txt` is signed, so they are not listed in it: their provenance is the check. What `--signer-workflow` adds is explained on the [installation overview](overview.md#verifying-what-you-install).

## Where it lands on disk

Claude Desktop unpacks the bundle into its own extensions directory:

| System  | Extensions directory                                      |
| ------- | --------------------------------------------------------- |
| macOS   | `~/Library/Application Support/Claude/Claude Extensions/` |
| Windows | `%APPDATA%\Claude\Claude Extensions\`                     |
| Linux   | `~/.config/Claude/Claude Extensions/`                     |

The server runs from there. What it writes once it runs (mirror caches, downloads, temp files) is listed on the [installation overview](overview.md#what-every-channel-shares).

## Configure a client

There is nothing to write: Claude Desktop is the client, and the extension is its configuration. To use the server from another client as well, install it through another channel and see [Connect a client](../clients.md). An extension and a hand-written entry in `claude_desktop_config.json` for the same server would run two copies side by side; keep one.

## Update

The server never updates itself. Download the newer bundle and open it the same way: every bundle is the same extension, so Claude Desktop replaces the installed version rather than adding a second one. Check **Settings > Extensions** afterwards for the version it now shows.

## Pin a version

Install the bundle from a particular release, `https://github.com/jmrplens/libgen-mcp/releases/download/v<version>/<bundle>`, and do not open a newer one. It stays on that version.

## Remove

In Claude Desktop, **Settings > Extensions**, open the extension and uninstall it. That removes its directory; downloads and caches stay where the overview [lists them](overview.md#what-every-channel-shares).

## Logs

The server writes its logs to stderr, and Claude Desktop keeps them in a log file per server:

| System  | Log directory            |
| ------- | ------------------------ |
| macOS   | `~/Library/Logs/Claude/` |
| Windows | `%APPDATA%\Claude\logs\` |
| Linux   | `~/.config/Claude/logs/` |

The server's file is named `mcp-server-` followed by the extension's name; `mcp.log` beside it records the connection itself. Set **Log level** to `debug` for more detail, and see [Troubleshooting](../troubleshooting.md#raising-the-log-level).

## Platform notes

- **Windows on Arm** runs the x64 executable under emulation; the bundle carries no arm64 Windows build.
- **Linux on another architecture** gets a refusal in the MCP log naming the machine type: the bundle carries x86_64 and arm64 only.
- **A `noexec` mount** under your home directory stops the Linux binary from running. The launcher says so in the log rather than failing silently.

## Common problems

**Nothing happens when you open the file on Linux.** Install it from **Extensions > Install Extension…**, as above.

**Claude Desktop says the extension is not compatible with this system.** You opened another system's bundle. Download the one for yours, or the universal `libgen-mcp.mcpb`.

**The extension is enabled but its tools fail at once.** The server refused to start, most often because **Download directory** is not writable. The server's log file says which setting it refused.

Other channels are compared on the [installation overview](overview.md).
