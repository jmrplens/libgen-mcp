# Install from PyPI

**How-to guide** — for anyone who would rather get the server through Python's packaging.

`libgen-mcp` is published on PyPI as [`libgen-mcp`](https://pypi.org/project/libgen-mcp/): one wheel per platform with the release binary inside. The installer puts that binary on the environment's scripts path, so `libgen-mcp` is the command afterwards and **no Python runs when you use it**. Python is only how the file reaches your machine.

## What you get

Six wheels per release, one per platform, tagged `py3-none-<platform>`:

| Platform            | Wheel tag                                                                  |
| ------------------- | -------------------------------------------------------------------------- |
| Linux x86_64        | `manylinux_2_17_x86_64`, `manylinux2014_x86_64`, `musllinux_1_1_x86_64`    |
| Linux aarch64       | `manylinux_2_17_aarch64`, `manylinux2014_aarch64`, `musllinux_1_1_aarch64` |
| macOS Intel         | `macosx_13_0_x86_64`                                                       |
| macOS Apple Silicon | `macosx_13_0_arm64`                                                        |
| Windows x64         | `win_amd64`                                                                |
| Windows arm64       | `win_arm64`                                                                |

There is no source distribution: an installer on any other platform finds nothing to install rather than something to compile.

**The binary is the command.** It rides in the wheel's `.data/scripts` directory, which the wheel format obliges the installer to place on the scripts path (`bin/`, or `Scripts\` on Windows) with its execute bit. The distribution deliberately declares **no console script**: one would be named `libgen-mcp` too, and two files competing for one path is a race whichever installer writes last wins.

The wheel also carries a small `libgen_mcp` package, so `python -m libgen_mcp` runs the same binary and `libgen_mcp.find_binary()` returns its path. Neither is needed to use the server.

**The Linux wheels carry `manylinux` and `musllinux` tags together.** That is honest only for a binary that needs no C library, and this one names no dynamic loader at all, so the same file installs and runs on Debian and on Alpine. It was measured end to end under `python:3.13-alpine`.

## Prerequisites

| Requirement  | Detail                                                                                              |
| ------------ | --------------------------------------------------------------------------------------------------- |
| Python       | 3.9 or newer (`Requires-Python: >=3.9`). The tag is `py3-none`, so any CPython or PyPy of that line |
| An installer | `uv` (for `uvx` and `uv tool`), `pipx`, or `pip` in a virtual environment                           |
| A platform   | One of the six above. macOS 11 or newer                                                             |

## Install

```bash
uvx libgen-mcp               # nothing installed: uv caches the wheel on the first run
uv tool install libgen-mcp
pipx install libgen-mcp
python -m venv ~/.venvs/libgen-mcp && ~/.venvs/libgen-mcp/bin/pip install libgen-mcp
```

With `pip`, install into a virtual environment, so the command lands in one you own. A `pip install --user` works too, and puts it in `~/.local/bin`. Then:

```bash
libgen-mcp --version
# libgen-mcp 2.1.0 (commit 5493887)
```

## Verify what you installed

The upload is an OIDC trusted publisher, so PyPI records a [PEP 740](https://peps.python.org/pep-0740/) attestation for every file, shown on the project page beside each one. No installer checks it for you yet.

What you can check yourself is the binary the wheel installed. It is the release asset byte for byte, so its build provenance answers for it:

```bash
# after uv tool, pipx or pip
gh attestation verify "$(command -v libgen-mcp)" -R jmrplens/libgen-mcp \
  --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml

# uvx keeps the binary in uv's cache, and the package says where
gh attestation verify "$(uvx --from libgen-mcp python -c 'import libgen_mcp as m; print(m.find_binary())')" \
  -R jmrplens/libgen-mcp --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml
```

`gh` follows the symbolic link pipx and uv put on your `PATH`. What `--signer-workflow` adds is explained on the [installation overview](overview.md#verifying-what-you-install).

## Where it lands on disk

| Install   | The command                                              | The binary                                                     |
| --------- | -------------------------------------------------------- | -------------------------------------------------------------- |
| `uvx`     | Nothing on your `PATH`                                   | uv's cache, under `$(uv cache dir)/archive-v0/<hash>/bin/`     |
| `uv tool` | A link in `$(uv tool dir --bin)`, usually `~/.local/bin` | `$(uv tool dir)/libgen-mcp/bin/libgen-mcp`                     |
| `pipx`    | A link in `~/.local/bin`                                 | `~/.local/share/pipx/venvs/libgen-mcp/bin/libgen-mcp` on Linux |
| `pip`     | The environment's `bin` directory (`Scripts` on Windows) | The same file                                                  |

`LICENSE` and `THIRD_PARTY_NOTICES` are in the environment's `libgen_mcp-<version>.dist-info/licenses/`. What the server writes once it runs is listed on the [installation overview](overview.md#what-every-channel-shares).

## Configure a client

The portable form installs nothing:

```json
{
  "mcpServers": {
    "libgen": { "command": "uvx", "args": ["libgen-mcp"] }
  }
}
```

After `uv tool`, `pipx` or `pip`, name the command: `"command": "libgen-mcp"`, or its full path if the client does not inherit your shell's `PATH` (`command -v libgen-mcp` prints it). No `env` block is needed. Where each client keeps this file is on [Connect a client](../clients.md).

## Upgrade

```bash
uvx libgen-mcp@latest          # uvx reuses its cached version; naming the tag resolves the newest
uv tool upgrade libgen-mcp     # a tool installed with an exact pin stays on it; uv tool install libgen-mcp@latest moves it
pipx upgrade libgen-mcp
~/.venvs/libgen-mcp/bin/pip install --upgrade libgen-mcp
```

A client that already has the server running keeps the old process until it restarts it; `libgen-mcp --shutdown` asks every running instance to exit.

## Pin a version

```bash
uvx libgen-mcp@2.1.0
uv tool install libgen-mcp==2.1.0
pipx install libgen-mcp==2.1.0
pip install libgen-mcp==2.1.0
```

In a client's configuration that is `"args": ["libgen-mcp@2.1.0"]` under `uvx`.

## Uninstall

```bash
uv tool uninstall libgen-mcp
pipx uninstall libgen-mcp
~/.venvs/libgen-mcp/bin/pip uninstall libgen-mcp
```

Under `uvx` nothing was installed: remove the client entry, and `uv cache clean libgen-mcp` to drop the cached wheel.

## Platform notes

- **Alpine** installs the same Linux wheel as Debian, through its `musllinux` tag.
- **macOS** needs 13 (Ventura) or later, the minimum the Go toolchain writes into
  the binary. The wheels are tagged `macosx_13_0`, so pip on an older system finds
  no wheel instead of installing one. Up to 2.1.0 they were tagged `macosx_11_0`,
  and pip on macOS 11 or 12 installed a command that could not start.
- **Windows** puts `libgen-mcp.exe` in the environment's `Scripts\` directory. A client started outside that environment needs the full path.
- **An unsupported platform** (32-bit, FreeBSD, Linux on another architecture) gets "no matching distribution". Build from source with [`go install`](binary.md#build-from-source).

## Common problems

**`No matching distribution found for libgen-mcp`.** Either the platform has no wheel (above), or the Python is older than 3.9. `python -m pip debug --verbose` lists the tags your interpreter accepts.

**`command not found` after `pipx` or `uv tool`.** `~/.local/bin` is not on your `PATH`. `pipx ensurepath` or `uv tool update-shell` adds it.

Other channels are compared on the [installation overview](overview.md).
