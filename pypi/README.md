# libgen-mcp

Books and papers for your AI assistant: a [Model Context Protocol](https://modelcontextprotocol.io) server that searches, cites, downloads and reads across Library Genesis and the open-access web — arXiv, Crossref, OpenLibrary, Unpaywall, Europe PMC, the RFC Editor, Zenodo and more. Four tools (`search`, `get_details`, `download`, `read`), no account, no API key.

This package wraps the native `libgen-mcp` binary (written in Go) in a platform wheel, the same distribution model `uv`, `ruff` and `ziglang` use: `pip` selects the wheel for your OS and architecture and installs the binary onto your `PATH`. No Go toolchain, no runtime downloads, no install scripts, and nothing runs at install time.

mcp-name: io.github.jmrplens/libgen-mcp

## Quick start

Run it directly with [uv](https://docs.astral.sh/uv/):

```bash
uvx libgen-mcp
```

Or install it on your `PATH`:

```bash
pipx install libgen-mcp   # or: pip install libgen-mcp
```

Either way the installed command is `libgen-mcp`, which is the native binary itself.

Typical MCP client configuration (stdio):

```json
{
  "mcpServers": {
    "libgen": {
      "command": "uvx",
      "args": ["libgen-mcp"]
    }
  }
}
```

## Platforms

Linux, macOS and Windows, on x86-64 and arm64. The Linux wheels carry both `manylinux` and `musllinux` tags because the binary is fully static — it needs no C library, so the same file runs on Debian, on Alpine and in a distroless container.

## Configuration

Everything is optional. `LIBGEN_MCP_DOWNLOAD_DIR` chooses where downloads land, `LIBGEN_MCP_UNPAYWALL_EMAIL` enables the Unpaywall source, `LIBGEN_MCP_EXTRA_SOURCES` decides when the open-access searchers are consulted. The full list is in the [configuration reference](https://jmrp.io/docs/libgen-mcp/configuration/).

## Links

- Documentation: <https://jmrp.io/docs/libgen-mcp/>
- Source and releases: <https://github.com/jmrplens/libgen-mcp>
- Issues: <https://github.com/jmrplens/libgen-mcp/issues>

## Responsible use

This tool reaches third-party mirrors of Library Genesis. You are responsible for respecting the copyright and intellectual-property laws that apply where you live; use it only for content you are legally entitled to access.
