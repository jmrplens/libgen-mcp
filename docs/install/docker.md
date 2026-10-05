# Run with Docker

**How-to guide** — for anyone who would rather run the server in a container.

`libgen-mcp` is published as a multi-architecture container image, on the GitHub Container Registry and mirrored to Docker Hub. The same image serves a desktop client over stdio and a team over HTTP; which one you get depends on how you start it.

## What you get

| Registry                  | Image                           |
| ------------------------- | ------------------------------- |
| GitHub Container Registry | `ghcr.io/jmrplens/libgen-mcp`   |
| Docker Hub                | `docker.io/jmrplens/libgen-mcp` |

Both receive the same image index on every release, with the same digest, under two tags:

| Tag         | Moves                       |
| ----------- | --------------------------- |
| `<version>` | Never. `2.2.1`, with no `v` |
| `latest`    | To each new release         |

There is no `2` or `2.1` tag: pin the full version, or a digest.

The image is `linux/amd64` and `linux/arm64`, on Alpine, and runs as **`appuser`, UID and GID `10001`**. The entry point is the binary, `/usr/local/bin/libgen-mcp`. The binary in the image is built in the image's own build stage rather than copied from the release, so its bytes differ from the release asset while its source and version are the same.

| Image setting | Value                                                                            |
| ------------- | -------------------------------------------------------------------------------- |
| `ENTRYPOINT`  | `libgen-mcp`                                                                     |
| `CMD`         | `--transport auto --http 0.0.0.0:8080`                                           |
| `EXPOSE`      | `8080`                                                                           |
| `USER`        | `appuser` (`10001:10001`), home `/home/appuser`                                  |
| `HEALTHCHECK` | `libgen-mcp --healthcheck` every 30 s, 5 s timeout, 3 retries, 10 s start period |
| Licences      | `/usr/share/licenses/libgen-mcp/LICENSE` and `THIRD_PARTY_NOTICES`               |

## Prerequisites

| Requirement | Detail                                                                                           |
| ----------- | ------------------------------------------------------------------------------------------------ |
| A runtime   | Docker, or any OCI runtime (Podman, containerd). On macOS and Windows, Docker Desktop or similar |
| A platform  | A host that runs `linux/amd64` or `linux/arm64` containers                                       |

## Install

```bash
docker pull ghcr.io/jmrplens/libgen-mcp:latest
# or from Docker Hub
docker pull docker.io/jmrplens/libgen-mcp:latest
```

Pulling is optional: `docker run` pulls a missing image itself.

### The transport follows standard input

The default command is `--transport auto`, which reads what standard input is:

- **`docker run -i`** connects a pipe and gets the **stdio** server. That is what an MCP client starts:

  ```bash
  docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest
  ```

  The `--http 0.0.0.0:8080` of the default command is then unused, and the server says so in its first log line.

- **A run without `-i`** connects `/dev/null`, reads that as nobody speaking to it, and starts the **streamable HTTP** listener on port 8080. That is what a compose service or a Kubernetes pod gets:

  ```bash
  docker run -d --name libgen-mcp -p 127.0.0.1:8080:8080 ghcr.io/jmrplens/libgen-mcp:latest
  curl -s http://127.0.0.1:8080/health
  ```

  The MCP endpoint is `http://127.0.0.1:8080/` and its alias `/mcp`. Bind the published port to `127.0.0.1` as above unless something in front of it is meant to reach it; [HTTP server mode](../http-server-mode.md) has what a shared listener needs.

> **Any argument replaces the whole default command.**
> `docker run image --log-level debug` drops `--transport auto --http 0.0.0.0:8080`
> with it, and starts a stdio server. That is harmless when the argument names a
> listener (`--http :9000` is itself the choice of transport), but a flag you add
> is the whole command line, not an addition to it. Repeat the default when you
> want both: `docker run image --transport auto --http 0.0.0.0:8080 --log-level debug`.
> Settings that have an environment variable are simpler passed with `-e`, which
> leaves the command alone, except the listener: the default command types `--http`, which
> wins over `LIBGEN_MCP_HTTP_ADDR`, and the server logs a `WARN` naming the variable and the
> flag that overrode it
> ([Containers](../deploy/containers.md#configuring-through-the-environment)).

### Downloads and the volume

The image writes downloads to `/home/appuser/Downloads` unless `LIBGEN_MCP_DOWNLOAD_DIR` says otherwise, and that directory goes with the container. To keep files, mount a host directory and point the variable at it:

```bash
docker run -i --rm \
  -v "$HOME/Downloads/libgen:/downloads" \
  -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads \
  ghcr.io/jmrplens/libgen-mcp:latest
```

**The mounted directory has to be writable by UID `10001`.** The server checks it at startup and refuses to start otherwise, logging `LIBGEN_MCP_DOWNLOAD_DIR "/downloads" is not writable: … permission denied`. Either give the directory to that UID, or run the container as your own:

```bash
mkdir -p "$HOME/Downloads/libgen"
sudo chown 10001:10001 "$HOME/Downloads/libgen"
# or keep your ownership and run as yourself
docker run -i --rm --user "$(id -u):$(id -g)" -v "$HOME/Downloads/libgen:/downloads" \
  -e LIBGEN_MCP_DOWNLOAD_DIR=/downloads ghcr.io/jmrplens/libgen-mcp:latest
```

Run under another UID, the process has no home directory in the image (`HOME` is `/`), so the mirror cache cannot be written and the mirror list is fetched on every start; `-e HOME=/tmp` gives it somewhere to go. Docker Desktop on macOS and Windows maps ownership for bind mounts itself, so the `chown` is a Linux concern.

## Verify what you installed

**The signature.** The image index and both platform manifests are signed keylessly with cosign. Verify with a **cosign 3.x** client: a 2.x client reports "no signatures found" on an image a 3.x client verifies.

```bash
cosign verify ghcr.io/jmrplens/libgen-mcp:2.2.1 \
  --certificate-identity-regexp '^https://github.com/jmrplens/libgen-mcp/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The same command verifies `docker.io/jmrplens/libgen-mcp:2.2.1`.

**The provenance**, which answers which commit and which workflow run produced it:

```bash
gh attestation verify oci://ghcr.io/jmrplens/libgen-mcp:2.2.1 -R jmrplens/libgen-mcp \
  --signer-workflow jmrplens/libgen-mcp/.github/workflows/release.yml \
  --source-ref refs/tags/v2.2.1
```

The image also carries an SBOM and the BuildKit provenance attached at build time. What `--signer-workflow` and `--source-ref` add is explained on the [installation overview](overview.md#verifying-what-you-install).

## Where it lands on disk

Nothing lands on the host but the image layers, in your runtime's storage. Inside the container:

| Path                               | What it holds                                                  |
| ---------------------------------- | -------------------------------------------------------------- |
| `/usr/local/bin/libgen-mcp`        | The binary                                                     |
| `/home/appuser/.cache/libgen-mcp/` | The mirror caches. Gone with the container unless you mount it |
| `/home/appuser/Downloads`          | Downloads, unless `LIBGEN_MCP_DOWNLOAD_DIR` points elsewhere   |
| `/usr/share/licenses/libgen-mcp/`  | `LICENSE` and `THIRD_PARTY_NOTICES`                            |

A container started with `--rm` for each client session fetches the mirror list again each time; that is one request, and harmless.

## Configure a client

Over stdio, the client starts the container itself. Keep `-i`, and keep `--rm` so each session leaves nothing behind:

```json
{
  "mcpServers": {
    "libgen": {
      "command": "docker",
      "args": [
        "run", "-i", "--rm",
        "-v", "/home/you/Downloads/libgen:/downloads",
        "-e", "LIBGEN_MCP_DOWNLOAD_DIR=/downloads",
        "ghcr.io/jmrplens/libgen-mcp:latest"
      ]
    }
  }
}
```

A client does not expand `$HOME` in `args`, so write the full path. Over HTTP, start the container as above and give the client the URL, `http://127.0.0.1:8080/mcp`; the shape of an HTTP entry differs from client to client and is on [Connect a client](../clients.md).

## Upgrade

```bash
docker pull ghcr.io/jmrplens/libgen-mcp:latest
```

`docker run` pulls only when the image is missing, so a client entry naming `latest` keeps running the copy it pulled first until you pull again. An HTTP container needs recreating after the pull: `docker rm -f libgen-mcp`, then the same `docker run`. Recreating, not restarting, is what picks up the new image.

## Pin a version

Name the version tag, or the digest for bytes that can never change under you:

```bash
docker run -i --rm ghcr.io/jmrplens/libgen-mcp:2.2.1
docker run -i --rm ghcr.io/jmrplens/libgen-mcp@sha256:<digest>
```

The digest is the image index's, the same on both registries. For any tag, `docker buildx imagetools inspect ghcr.io/jmrplens/libgen-mcp:<tag>` prints it without a pull, and `docker image inspect --format '{{index .RepoDigests 0}}' ghcr.io/jmrplens/libgen-mcp:<tag>` after one. The latest release's is also in the repository's [`server.json`](../../server.json), beside the version tag it was published under.

## Uninstall

```bash
docker rm -f libgen-mcp                     # an HTTP container, if you started one
docker image rm ghcr.io/jmrplens/libgen-mcp:latest
```

Remove each tag you pulled. A mounted download directory is yours and is left as it is.

## Platform notes

- **Apple Silicon and arm64 Linux** pull the `linux/arm64` image natively.
- **A unix socket instead of a port.** `--http /run/mcp/libgen.sock` binds a socket in a mounted directory and publishes nothing, the shape for a reverse proxy on the same host. The socket is created `0660`, so the proxy needs the socket's group; between two containers the simpler way is to run the server with the proxy's group, as [Containers](../deploy/containers.md#nginx-in-front-over-a-shared-unix-socket) does.
- **`LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES` with the default command is refused at startup**, because the default listener binds `0.0.0.0` and that combination is exactly what the refusal is about. [Troubleshooting](../troubleshooting.md#the-server-will-not-start-the-private-address-hatch-on-an-open-listener) explains it.
- **Compose and Kubernetes** are on [Containers](../deploy/containers.md): a service definition, the health check, volumes and the resources the server needs.

## Common problems

**The client waits forever for the server.** The `-i` is missing. Without it the container starts an HTTP listener and never reads the client's pipe.

**The container exits at once with `is not writable`.** The download directory you mounted is not writable by UID `10001`. See [Downloads and the volume](#downloads-and-the-volume).

**The health check after you changed the command.** It keeps working: `--healthcheck` reads the listener off the running process's own command line, so another port, a socket, a path prefix or TLS this process terminates are all probed where they are. A stdio container has nothing to probe and reports healthy while the process is alive. If it reports `unhealthy`, the listener really is not answering; `docker logs` says why.

**`cosign verify` says "no signatures found".** The client is cosign 2.x. Use 3.x.

Other channels are compared on the [installation overview](overview.md).
