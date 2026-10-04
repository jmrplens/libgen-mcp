# Compatibility

**Reference** — for anyone checking whether the server runs where they need it.

What each release is built for, what each install channel needs on top, which MCP protocol
versions and capabilities the server speaks, and what a client or a gateway in front of it
can rely on. How to install on each platform is in the
[installation overview](install/overview.md).

## Platforms

Every release is built for six targets, plus one universal macOS binary that fuses the two
macOS ones:

| System  | amd64 (x86-64) | arm64 (AArch64) | Oldest version          |
| ------- | -------------- | --------------- | ----------------------- |
| Linux   | yes            | yes             | kernel 3.2              |
| macOS   | yes            | yes             | macOS 13 Ventura        |
| Windows | yes            | yes             | Windows 10, Server 2016 |

The oldest versions are the Go 1.27 toolchain's own floors rather than a choice this project
made. The macOS one is measured: the linker stamps `minos 13.0` into every macOS binary's
`LC_BUILD_VERSION`, and macOS refuses to start a binary that asks for a newer system than it
is. Anything outside this table (FreeBSD, 32-bit, RISC-V) has to be built from source with
`go build`, which works because the server has no cgo.

### Glibc and musl

The binaries name **no C library and no dynamic loader**. Every build in this repository —
the release binaries, the image and `make build` — is `CGO_ENABLED=0` and deliberately
**without** `-buildmode=pie`, so the ELF files carry no `PT_INTERP` and run unchanged on
glibc, on musl (Alpine), in a distroless image and on `scratch`.

That is a guarded property, not a hope. PIE makes a Go binary dynamically linked against the
build host's loader, and it once shipped a `linux/arm64` image that could not start at all and
`linux/amd64` release assets that asked for `/lib64/ld-linux-x86-64.so.2`. So the
`Dockerfile` greps the binary it just built for a loader path and fails the build, and the npm
release validator does the same on the packed bytes. On the other two systems the flag would
change nothing: Go already emits ASLR-capable binaries for Windows and macOS.

The consequences show up in the packages:

- The PyPI linux wheels carry `musllinux` tags beside the `manylinux` ones, and install under
  `python:3.13-alpine`.
- No npm package declares a `libc`, so npm does not skip the linux package on Alpine.
- The Docker image is built on Alpine and runs the same static binary.

## Install channels

Every channel except `go install` carries the same release binary; none of them compiles
anything or runs a script at install time.

| Channel                                                    | Linux amd64 | Linux arm64 | macOS amd64 | macOS arm64 | Windows amd64 | Windows arm64 | Needs on the machine                     |
| ---------------------------------------------------------- | ----------- | ----------- | ----------- | ----------- | ------------- | ------------- | ---------------------------------------- |
| [Release binary](install/binary.md)                        | yes         | yes         | yes         | yes         | yes           | yes           | Nothing                                  |
| [npm](install/npm.md) (`@jmrp.io/libgen-mcp`)              | yes         | yes         | yes         | yes         | yes           | yes           | Node.js 18 or newer                      |
| [PyPI](install/pypi.md) (`libgen-mcp`)                     | yes         | yes         | yes         | yes         | yes           | yes           | Python 3.9 or newer, for pip, pipx or uv |
| [NuGet](install/nuget.md) (`libgen-mcp`)                   | yes         | yes         | yes         | yes         | yes           | yes           | .NET 10 SDK (`dotnet tool` or `dnx`)     |
| [Homebrew](install/homebrew.md) (`jmrplens/tap`)           | yes         | yes         | yes         | yes         | no            | no            | Homebrew                                 |
| [Docker](install/docker.md) (`linux/amd64`, `linux/arm64`) | yes         | yes         | in a VM     | in a VM     | in a VM       | in a VM       | Any OCI runtime                          |
| [Claude Desktop `.mcpb`](install/claude-desktop.md)        | yes         | yes         | yes         | yes         | yes           | emulated      | Claude Desktop (beta on Linux)           |
| [`go install`](install/binary.md)                          | yes         | yes         | yes         | yes         | yes           | yes           | Go, which fetches 1.27.1 itself          |
| [winget](install/winget.md)                                | —           | —           | —           | —           | pending       | pending       | Not yet published                        |

What each channel carries per platform:

| Channel  | Per-platform artifact                                                                                                                                                                    |
| -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Release  | `libgen-mcp-{linux,darwin,windows}-{amd64,arm64}` (`.exe` on Windows), plus the universal `libgen-mcp` for macOS, all listed in the cosign-signed `checksums.txt`                        |
| npm      | The launcher `@jmrp.io/libgen-mcp` and one optional package per platform: `-linux-x64`, `-linux-arm64`, `-darwin-x64`, `-darwin-arm64`, `-win32-x64`, `-win32-arm64`                     |
| PyPI     | One `py3-none` wheel per platform: `manylinux_2_17` + `manylinux2014` + `musllinux_1_1` for each Linux architecture, `macosx_13_0_x86_64`, `macosx_13_0_arm64`, `win_amd64`, `win_arm64` |
| NuGet    | The pointer package `libgen-mcp` and one package per runtime identifier: `linux-x64`, `linux-arm64`, `osx-x64`, `osx-arm64`, `win-x64`, `win-arm64`                                      |
| Homebrew | One formula that picks the release asset by `on_macos`/`on_linux` and `on_arm`/`on_intel`                                                                                                |
| Docker   | One multi-architecture image, `ghcr.io/jmrplens/libgen-mcp` and `docker.io/jmrplens/libgen-mcp`, for `linux/amd64` and `linux/arm64`                                                     |
| `.mcpb`  | `libgen-mcp-darwin.mcpb` (universal binary), `libgen-mcp-windows.mcpb` (amd64), `libgen-mcp-linux.mcpb` (both Linux binaries and a launcher), and the universal `libgen-mcp.mcpb`        |

Notes that matter when a platform is on the edge:

- **The PyPI macOS wheels need macOS 13.** They are tagged `macosx_13_0`, the minimum the Go
  toolchain writes into the binary. Up to 2.1.0 they were tagged `macosx_11_0`, so pip on
  macOS 11 or 12 installed a wheel whose command then failed to start.
- **Windows on Arm and Claude Desktop.** The `.mcpb` bundle for Windows carries the amd64
  executable, which Windows on Arm runs under emulation. Every other channel that serves
  Windows has a native `arm64` build.
- **Docker on macOS and Windows** runs the Linux image in the virtual machine Docker Desktop (or
  an equivalent) provides, on the architecture of that VM.
- **Linux `.mcpb`.** A bundle manifest can choose a file per operating system but not per
  architecture, so the Linux bundle starts a small `/bin/sh` launcher that picks the binary by
  `uname -m`. The per-OS bundles first ship with 2.2.0. For 2.1.0 and earlier the universal
  `libgen-mcp.mcpb` is the one to download.
- **`go install`** compiles from source with your toolchain. `go.mod` declares Go 1.27.1, and any
  Go from 1.21 on downloads that toolchain by itself unless `GOTOOLCHAIN=local` forbids it. The
  binary it produces is named `server`, after the package `cmd/server`.
- **winget** is pending review in
  [microsoft/winget-pkgs#437507](https://github.com/microsoft/winget-pkgs/pull/437507).

## Transports

| Transport       | How it is chosen                                        | For                                                                    |
| --------------- | ------------------------------------------------------- | ---------------------------------------------------------------------- |
| stdio           | The default, or `--transport stdio`                     | A client that starts the server as a subprocess, which is most of them |
| Streamable HTTP | `--http <addr>`, or `--transport http`                  | A client that connects to a URL, or several clients sharing one server |
| Either          | `--transport auto`: stdin decides (the image's default) | One container image that serves both                                   |

The HTTP transport is **stateless by default** and serves `POST` on the endpoint;
`--stateless=false` brings back the session-based transport for a client that needs it. The
withdrawn HTTP+SSE transport of protocol `2024-11-05` is not served: a client on that revision
speaks it over streamable HTTP or stdio. Every flag is in [Command-line flags](cli.md), and the HTTP deployment guide is
[HTTP server mode](http-server-mode.md).

## MCP protocol versions

The versions come from the Go SDK the server is built with (`modelcontextprotocol/go-sdk`
v1.8.0) and are read from it rather than copied, so an SDK upgrade moves them. What a deployment
negotiates depends on the transport:

| Revision     | stdio | HTTP, stateless (default) | HTTP, `--stateless=false` |
| ------------ | ----- | ------------------------- | ------------------------- |
| `2026-07-28` | no    | yes                       | no                        |
| `2025-11-25` | yes   | yes                       | yes                       |
| `2025-06-18` | yes   | yes                       | yes                       |
| `2025-03-26` | yes   | yes                       | yes                       |
| `2024-11-05` | yes   | yes                       | yes                       |

- **`2026-07-28` is stateless-only.** That revision has no `initialize` and no session
  ([SEP-2575](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2575)): a client
  states the version on every request, so only a stateless HTTP deployment can serve it.
- **`initialize` negotiates `2025-11-25` at most.** A client that asks for `2026-07-28` through
  `initialize`, on any transport, is answered with `2025-11-25`, which is the newest revision
  that still has the handshake.
- **An unsupported `MCP-Protocol-Version` header gets `400` with JSON-RPC error `-32022`**,
  whose `data.supported` lists exactly the column above for that deployment and `data.requested`
  echoes what was asked. The SDK answers older revisions in plain text, which a client reads as
  "this is a pre-2025 server" and falls back to the withdrawn SSE transport; the structured
  answer is what lets it retry with a version that works instead.
- **The cards agree with the handshake.** The discovery card's `remotes` entry declares the same
  list, so a stateful deployment never advertises the one revision it would refuse.

## Advertised capabilities

The handshake's capabilities are **pinned**, and every field is a promise the server keeps:

```json
{ "capabilities": { "tools": {}, "prompts": {} } }
```

- **Tools and prompts, with `listChanged: false`** (which serializes as the empty object above).
  The catalog is fixed when the server starts and only changes with a release, so the server
  never sends a list-changed notification. Left to its defaults the SDK would declare
  `listChanged: true`, and a client that believes it opens a `subscriptions/listen` stream that
  is never answered and never closed.
- **No `logging`.** MCP logging is deprecated as of `2026-07-28`, and the server logs to stderr
  instead. The SDK would otherwise advertise it by default.
- **No resources.** `resources/list`, `resources/templates/list` and `resources/read` answer
  JSON-RPC `-32601` (method not found) rather than an empty listing that would suggest resources
  exist here. `resources/subscribe`, `resources/unsubscribe` and `completion/complete` answer the
  same.
- **Four tools**: `search`, `get_details`, `download` and `read`. A deployment that may not fetch
  file bodies, which is every HTTP deployment unless `LIBGEN_MCP_SERVER_FETCH` says otherwise,
  registers three: `read` is absent. See [Tools](tools.md).
- **Four prompts**: `acquire_book`, `research_topic`, `get_paper` and `download_troubleshoot`.
- **Listings carry a cache hint** ([SEP-2549](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549)):
  `tools/list` and `prompts/list` answer with `ttlMs: 3600000` and `cacheScope: "public"`.

Two tests hold the pin: `TestAdvertisedCapabilitiesAreWhatThisServerServes` in `cmd/server`,
and an end-to-end test that drives `subscriptions/listen` over HTTP.

## Icons

The server, each tool and each prompt carry three icons, always in this order:

| Index | Format | Size    | Theme    | For                                   |
| ----- | ------ | ------- | -------- | ------------------------------------- |
| 0     | SVG    | `any`   | *(none)* | Clients that accept `image/svg+xml`   |
| 1     | WebP   | `16x16` | `light`  | Clients that refuse SVG, light themes |
| 2     | WebP   | `16x16` | `dark`   | Clients that refuse SVG, dark themes  |

The SVG uses `currentColor`, so one entry follows any theme. The WebP pair exists because a
client can support icons and still refuse SVG: VS Code Copilot's allowlist admits
`image/webp` but not `image/svg+xml`, and an SVG-only icon rendered nothing there. A raster
cannot inherit a color, so it needs one image per theme. The order is a published contract —
the server card republishes the arrays verbatim, and a consumer may read `icons[0]` — and
[Architecture → Icons](architecture.md#icons) has the details.

## Clients

Any MCP client that speaks stdio or streamable HTTP can use the server. A few behaviours are
worth knowing before you pick a transport for one:

- **Most clients start a local process over stdio**: Claude Code, Claude Desktop, Cursor,
  VS Code, LM Studio and Kiro all do, and the README's install buttons write that configuration.
  Configurations per client are in [Connect a client](clients.md).
- **A client that only takes a URL** needs an HTTP deployment: yours, or the
  [hosted endpoint](hosted.md). There `download` returns a link rather than writing a file,
  because a remote server cannot write to your disk.
- **Browser-based clients** send an `Origin` header and are refused cross-origin until
  `--trusted-origins` names that origin. Desktop and command-line clients send none.
- **A base URL ending in `/mcp`** works: the endpoint answers at its `/mcp` alias as well, under
  whatever `--http-path` mounts.
- **Elicitation on a stateless deployment.** A legacy client (protocol `2025-11-25` or older) on
  the default stateless transport is not seen as supporting elicitation, so the download-consent
  question takes its default answer. Clients on `2026-07-28` ask and answer inside the tool
  result and are unaffected. `--stateless=false` restores the old path for legacy clients.
- **Desktop clients do not inherit your shell's `PATH`.** Give them an absolute `command`, or
  install through a channel that puts the binary where they look.

## Gateways

Everything a client receives from `tools/list` and `prompts/list` — each description, title,
schema description and prompt argument — is **plain ASCII prose with no semicolon**.

The reason is a gateway: one refused a sibling project's onboarding with
`Description contains unsafe characters: ';'`, over ordinary English punctuation. A validator
that talks about "unsafe characters" matches a character class, so holding the served text to
a class (ASCII, minus a short list) is the only kind of clean the next gateway cannot surprise.
`make check-gateway-chars` reads a real round-trip and fails on any character outside it, so a
change cannot reintroduce one.

The rule covers the served surface, not results: a tool's output is data the caller asked for,
and a citation truncated with `…` stays as it is.

## Go toolchain

The module is `github.com/jmrplens/libgen-mcp/v2` and needs Go 1.27.1 to build, which is the
`go` line in `go.mod`. The `/v2` suffix is required: `go install
github.com/jmrplens/libgen-mcp/cmd/server@latest` without it resolves the last v1 release.
