# Agent plugin

**How-to guide** — for anyone whose agent host installs MCP servers as plugins.

A plugin host can install an MCP server from a repository when the repository describes itself in a manifest the host understands. This repository carries three files for that, in the two formats hosts read today. They do not contain the server: they tell a host how to start it, and what they start is the [npm launcher](npm.md), run through `npx`.

## What you get

| File                  | Format                                                                  | What it says                                                                                          |
| --------------------- | ----------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `plugin.json`         | [Agent Plugins](https://agent-plugins.org/) 1.0, at the repository root | The plugin's name, version, description, author, licence and keywords. Its schema allows nothing else |
| `.plugin/plugin.json` | Open Plugins, validated by `.plugin/plugin.schema.json` beside it       | The same identity, plus a `logo` and `"mcpServers": "./mcp.json"`, the pointer to the server entry    |
| `mcp.json`            | Agent Plugins 1.0 MCP configuration, which both lead to                 | One stdio entry, `libgen`, that runs `npx -y @jmrp.io/libgen-mcp`                                     |

The two manifests are separate files rather than one copy, because the formats disagree: Agent Plugins 1.0 sets `additionalProperties: false` and has no field for a logo or for the server entry, while Open Plugins expects both. An Agent Plugins host finds `mcp.json` by its fixed place at the plugin root; an Open Plugins host follows the pointer. Both manifests carry the release version, stamped by the release workflow.

## Which hosts read them

A host that implements **Agent Plugins** reads `plugin.json` at the root of the plugin directory and `mcp.json` beside it. A host that implements **Open Plugins** reads `.plugin/plugin.json` and follows its `mcpServers` pointer. Neither specification defines how a plugin is distributed or installed, so the command belongs to your host and its version: typically you give it the repository, `jmrplens/libgen-mcp`, through its plugin installer, and it copies the directory into its own plugin store.

## Prerequisites

| Requirement | Detail                                                                                                          |
| ----------- | --------------------------------------------------------------------------------------------------------------- |
| A host      | One that installs Agent Plugins or Open Plugins packages with MCP servers                                       |
| Node.js     | 18 or newer, with `npx` on the `PATH` the host starts servers with. Node runs the launcher only, not the server |

## What the plugin runs

The whole of `mcp.json`:

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "libgen": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "@jmrp.io/libgen-mcp"]
    }
  }
}
```

It is written to the Agent Plugins 1.0 rules, which are the strictest a host applies, so a host that accepts less strict files accepts it too:

- **`$schema` and `type` are there because the specification requires them.** A conformant host disables an `mcp.json` without `$schema`, and skips a server entry without `type`.
- **There is no `env` block.** A conformant host expands exactly two placeholders, `${PLUGIN_ROOT}` and `${PLUGIN_DATA}`, and passes any other `${...}` to the server as text, so an entry cannot default a variable the way a shell would. Every setting is optional, so the entry sets none, and the server reads its configuration from [`~/.libgen-mcp.env`](../configuration.md) and from whatever environment the host passes on.
- **It runs a process on your machine, not a container.** `npx` fetches the launcher and the binary for your platform on the first start and starts the binary. Files `download` saves therefore land on your disk, in `~/Downloads` unless you set `LIBGEN_MCP_DOWNLOAD_DIR`, and `read` can open them and any other file in the directories it allows.
- **`-y`** answers npx's "install this package?" question, which a host has no way to answer.

## Verify what you installed

The manifests are configuration, so there is nothing signed in them. What runs is the npm package, and the [npm page](npm.md#verify-what-you-installed) has the provenance and signature checks.

## Where it lands on disk

The host copies the plugin directory into its own store, at a place it chooses (the Agent Plugins specification uses `~/.agents/plugins/<name>/` as its example). npx keeps the launcher and the binary in its cache, as the [npm page](npm.md#where-it-lands-on-disk) shows. What the server writes once it runs is listed on the [installation overview](overview.md#what-every-channel-shares).

## Configure a client

For this channel, the plugin's `mcp.json` is the configuration: the host reads it and starts the entry. Settings go in `~/.libgen-mcp.env`, one `NAME=value` per line, which the server reads at startup:

```bash
LIBGEN_MCP_DOWNLOAD_DIR=/home/you/Books
LIBGEN_MCP_UNPAYWALL_EMAIL=you@example.com
```

Every variable is described in [Configuration](../configuration.md). On a host without plugin support, put the same entry in the client's own configuration, and add an `env` object there if you prefer: a client's own file has the client's rules, not the specification's. Where each client keeps it is on [Connect a client](../clients.md).

## Upgrade

The manifest's version moves with each release, but `npx` can keep starting the copy already in its cache. Clearing that cache makes the next start fetch the newest release:

```bash
npm cache clean --force
```

A host that already has the server running keeps the old process until it restarts it. To choose the release yourself instead, pin it as below.

## Pin a version

In your local copy of the entry, name the version: `"args": ["-y", "@jmrp.io/libgen-mcp@2.2.0"]`. The [npm page](npm.md#pin-a-version) has the details.

## Uninstall

Remove the plugin the way your host documents. Nothing was installed globally; `npm cache clean --force` removes npx's copy if you want it gone. Settings, caches and downloads are not touched; the overview lists [where they are](overview.md#what-every-channel-shares).

## Platform notes

The same entry works on Linux, macOS and Windows on x64 and arm64, which matters because neither specification has per-platform variants of an entry. On Windows `npx` is a `.cmd` script, and the Agent Plugins specification lets a host start it through the command interpreter; a host that does not will fail to start it, and the workaround is the `cmd /c` form on [Connect a client](../clients.md).

## Common problems

**No MCP server appears, or the host reports `npx` not found.** The host starts servers with a `PATH` that has no Node on it. Install Node 18 or newer where that `PATH` reaches, or give the host's own configuration the absolute path of `npx`.

**The first start is slow.** npx downloads the launcher and the binary once; later starts use the cache.

**A setting in `~/.libgen-mcp.env` has no effect.** The host passed the same variable with a value of its own, which wins over the file. The precedence is on [Configuration](../configuration.md).

Other channels are compared on the [installation overview](overview.md).
