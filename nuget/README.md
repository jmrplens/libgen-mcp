# libgen-mcp

Books and papers for your AI assistant: a [Model Context Protocol](https://modelcontextprotocol.io) server that searches, cites, downloads and reads across Library Genesis and the open-access web — arXiv, OpenAlex, Crossref, OpenLibrary, Unpaywall, Europe PMC, the RFC Editor, Zenodo and more. Four tools (`search`, `get_details`, `download`, `read`), no account, no API key.

This is a .NET tool whose entry point is the native `libgen-mcp` binary (written in Go). The package you install is a pointer: it names one package per runtime identifier, and the SDK downloads only the one your host needs. Nothing here is .NET code and nothing is compiled at install time.

mcp-name: io.github.jmrplens/libgen-mcp

## Quick start

Run it without installing anything:

```bash
dnx libgen-mcp
```

Or install it on your `PATH`:

```bash
dotnet tool install -g libgen-mcp
```

Typical MCP client configuration (stdio):

```json
{
  "mcpServers": {
    "libgen": {
      "command": "dnx",
      "args": ["libgen-mcp"]
    }
  }
}
```

Arguments for the server go **after `--`**, because everything before it belongs to `dnx`:

```bash
dnx libgen-mcp -- --http 127.0.0.1:8080
```

## Platforms

`linux-x64`, `linux-arm64`, `osx-x64`, `osx-arm64`, `win-x64` and `win-arm64`. The Linux binary is fully static — it needs no C library — so the same package runs on Debian, on Alpine and in a distroless container.

## Verify what you run

The binary the SDK runs is the release asset byte for byte, so the build provenance GitHub holds for it verifies it with the [GitHub CLI](https://cli.github.com/), from the NuGet cache `dnx` runs it out of:

```bash
gh attestation verify ~/.nuget/packages/libgen-mcp.linux-x64/<version>/tools/any/linux-x64/libgen-mcp \
  -R jmrplens/libgen-mcp --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml
```

From 2.1.0, releases also attest the packages themselves, as they were before nuget.org added its repository signature: remove `.signature.p7s` from a downloaded copy with `zip -d` and run the same `gh attestation verify` on what is left. The steps are in the [NuGet installation guide](https://jmrp.io/docs/libgen-mcp/install/nuget/).

## Configuration

Everything is optional. `LIBGEN_MCP_DOWNLOAD_DIR` chooses where downloads land, `LIBGEN_MCP_UNPAYWALL_EMAIL` enables the Unpaywall source, `LIBGEN_MCP_EXTRA_SOURCES` decides when the open-access searchers are consulted. The full list is in the [configuration reference](https://jmrp.io/docs/libgen-mcp/configuration/).

## Links

- Documentation: <https://jmrp.io/docs/libgen-mcp/>
- Source and releases: <https://github.com/jmrplens/libgen-mcp>
- Issues: <https://github.com/jmrplens/libgen-mcp/issues>

## Responsible use

This tool reaches third-party mirrors of Library Genesis. You are responsible for respecting the copyright and intellectual-property laws that apply where you live; use it only for content you are legally entitled to access.
