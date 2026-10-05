# Install with npm

**How-to guide** — for anyone who already has Node and wants the server from npm.

`libgen-mcp` is published on npm as [`@jmrp.io/libgen-mcp`](https://www.npmjs.com/package/@jmrp.io/libgen-mcp). It is the same release binary every other channel ships, packaged so that `npx` can start it with no install step and `npm install -g` can put it on your `PATH`. Nothing is compiled and nothing runs at install time.

## What you get

Seven packages, published together at one version:

- **`@jmrp.io/libgen-mcp`**, the launcher. A small Node.js file (`cli.js`) registered as the `libgen-mcp` command. It finds the binary for your platform, starts it with your arguments, environment and standard streams untouched, and ends with the binary's exit code or terminating signal, so a client sees the real outcome. It never writes to stdout, because stdout is the JSON-RPC stream.
- **`@jmrp.io/libgen-mcp-<platform>`**, one per platform: `linux-x64`, `linux-arm64`, `darwin-x64`, `darwin-arm64`, `win32-x64` and `win32-arm64`. Each carries the binary, `LICENSE` and `THIRD_PARTY_NOTICES`, and is gated by `os` and `cpu`. The launcher names all six as optional dependencies pinned to its own exact version, so npm installs exactly one: the one that matches your machine.

Because the binary travels inside an ordinary package, there is no postinstall script and nothing is downloaded after the install. That is why `npm ci --ignore-scripts`, a private registry mirror and `--offline` all work.

**The launcher stops the server when it is told to stop.** From 2.1.0, on Linux and macOS it passes SIGTERM, SIGINT and SIGHUP on to the server, and when npm started it (`npx`, or an npm script) it sends the server SIGTERM within a second of npm's shell exiting: a SIGTERM sent to the `npx` process reaches only that shell, never the launcher. Up to 2.0.1 an HTTP server started with `npx` kept its port after its supervisor had stopped it. Started any other way, a server that outlives its parent is left alone, as the binary run directly would be. On Windows the console's Ctrl+C reaches the server itself, and the launcher only waits for it.

The server drains on its first SIGINT or SIGTERM and ends outright on a second one sent more than a second later, so the launcher tells it to stop once: later signals are not passed on, and a Ctrl+C from a terminal, which already reaches the server, is not passed on at all.

## Prerequisites

| Requirement | Detail                                                                                                               |
| ----------- | -------------------------------------------------------------------------------------------------------------------- |
| Node.js     | 18 or newer (`engines.node` is `>=18`). It runs the launcher only, never the server                                  |
| A platform  | Linux, macOS or Windows, on x64 or arm64. Linux on glibc or musl alike: the binary needs no C library                |
| npm         | Whatever your Node ships. pnpm and Yarn work too, as long as they install optional dependencies (they do by default) |

## Install

**npx** — nothing to install. The client starts the command, and npx keeps a copy in its cache after the first run. `-y` answers npx's "install this package?" question, which an MCP client has no way to answer:

```bash
npx -y @jmrp.io/libgen-mcp
```

**npm**, **pnpm** and **Yarn**:

```bash
npm install -g @jmrp.io/libgen-mcp
pnpm add -g @jmrp.io/libgen-mcp        # pnpm setup creates pnpm's global bin directory first
yarn dlx @jmrp.io/libgen-mcp           # Yarn 2 and later: a throwaway environment
yarn global add @jmrp.io/libgen-mcp    # Yarn 1 (Classic)
```

After a global install the command is `libgen-mcp`:

```bash
libgen-mcp --version
# libgen-mcp 2.2.1 (commit <commit>)
```

## Verify what you installed

The packages are published through an OIDC trusted publisher, so npm attaches a provenance attestation to every one of them. `npm audit signatures` checks the registry's signature over what you installed, and the provenance wherever there is one. It reads the project's lockfile, so run it in a project that depends on the package:

```bash
mkdir libgen-check && cd libgen-check
npm init -y >/dev/null
npm install @jmrp.io/libgen-mcp
npm audit signatures
# 2 packages have verified registry signatures
# 2 packages have verified attestations
```

The binary inside the platform package is the release asset byte for byte, so its build provenance answers for it too, which ties it to the commit and the workflow run that built it:

```bash
gh attestation verify node_modules/@jmrp.io/libgen-mcp-linux-x64/libgen-mcp \
  -R jmrplens/libgen-mcp \
  --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml
```

Use the directory for your platform in place of `linux-x64`. What `--signer-workflow` adds is explained on the [installation overview](overview.md#verifying-what-you-install).

## Where it lands on disk

| Install          | The command                                          | The binary                                                                     |
| ---------------- | ---------------------------------------------------- | ------------------------------------------------------------------------------ |
| `npx`            | Nothing on your `PATH`                               | npx's cache, `~/.npm/_npx/<hash>/node_modules/@jmrp.io/libgen-mcp-<platform>/` |
| `npm install -g` | `$(npm prefix -g)/bin/libgen-mcp`, which is `cli.js` | `$(npm root -g)/@jmrp.io/libgen-mcp-<platform>/libgen-mcp`                     |
| `pnpm add -g`    | pnpm's global binary directory (`pnpm bin -g`)       | pnpm's global store, behind that directory                                     |

On Windows the npm prefix is `%AppData%\npm` and the binary is `libgen-mcp.exe`. What the server itself writes once it runs (mirror caches, downloads, temp files) is the same on every channel and listed on the [installation overview](overview.md#what-every-channel-shares).

## Configure a client

The portable form installs nothing. Point the client at `npx`:

```json
{
  "mcpServers": {
    "libgen": { "command": "npx", "args": ["-y", "@jmrp.io/libgen-mcp"] }
  }
}
```

After a global install, name the command instead: `"command": "libgen-mcp"`. A client that does not inherit your shell's `PATH` may need the full path, which `command -v libgen-mcp` (or `where libgen-mcp` on Windows) prints. No `env` block is needed: every setting is optional. Where each client keeps this file, and the shapes that differ from it, are on [Connect a client](../clients.md).

## Upgrade

The server never checks for updates and never replaces its own binary. Every release moves the launcher and its six platform packages together with exact pins, so updating the launcher updates the binary.

```bash
npx -y @jmrp.io/libgen-mcp@latest            # npx reuses its cache; naming the tag resolves afresh
npm install -g @jmrp.io/libgen-mcp@latest
pnpm update -g @jmrp.io/libgen-mcp
yarn global upgrade @jmrp.io/libgen-mcp      # Yarn 1; yarn dlx resolves the newest on every run
```

A client that already has the server running keeps the old process until it restarts it. `libgen-mcp --shutdown` asks every running instance to exit, so the client starts the new one on its next call.

## Pin a version

Name the version wherever the package is named:

```bash
npx -y @jmrp.io/libgen-mcp@2.2.1
npm install -g @jmrp.io/libgen-mcp@2.2.1
```

In a client's configuration that is `"args": ["-y", "@jmrp.io/libgen-mcp@2.2.1"]`. A pinned `npx` entry never moves until you edit it, which is what you want when a team should run the same server.

## Uninstall

```bash
npm uninstall -g @jmrp.io/libgen-mcp
pnpm remove -g @jmrp.io/libgen-mcp
yarn global remove @jmrp.io/libgen-mcp       # Yarn 1
```

Under `npx` nothing was installed: remove the entry from your client's configuration, and `npm cache clean --force` if you also want npx's copy gone. The platform package goes with the launcher, because the launcher is the only thing that depends on it. Settings, caches and downloads are not touched; the overview lists [where they are](overview.md#what-every-channel-shares).

## Platform notes

- **Alpine and other musl systems** need nothing special. No package declares a `libc`, because the binary names no dynamic loader and runs on musl as it does on glibc.
- **Windows on Arm** gets the native `win32-arm64` binary, not an emulated one.
- **Under systemd**, a signal sent to the whole control group (the default `KillMode=control-group`) reaches the server twice within milliseconds: once directly, once through the launcher. The server takes the two for one request and drains. Up to 2.1.0 the second copy ended it before the drain finished, and running the binary directly or setting `KillMode=mixed` was the way around it.

## Common problems

**`the @jmrp.io/libgen-mcp-<platform> package is not installed`.** Your platform is supported but its package was skipped. The usual causes are an install with `--no-optional` (or `--omit=optional`) and a lockfile generated on another operating system. Reinstall without the flag, or delete `node_modules` and the lockfile and install again.

**`unsupported platform <os>/<arch>`.** There is no prebuilt binary for it. Build from source with [`go install`](binary.md#build-from-source), or use the [Docker image](docker.md).

**`command not found` after a global install.** The global binary directory is not on your `PATH`. `npm prefix -g` shows npm's (the command is in its `bin` subdirectory on Linux and macOS); pnpm's is set up by `pnpm setup`.

**The client never gets an answer from `npx`.** Usually the `-y` is missing, and npx is waiting for a "yes" on a pipe nobody types into. The client's log shows the question.

**Yarn reports every version as "quarantined".** Recent Yarn releases refuse a package version in its first days on the registry. Name the previous version, or wait.

Other channels are compared on the [installation overview](overview.md).
