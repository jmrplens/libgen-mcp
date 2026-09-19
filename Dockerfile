# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

# --- Build stage ---
# Both bases are pinned by digest, and the frontend above with them. A tag is a
# moving target: the same Dockerfile would build different images on different
# days, so a reproducible build and an attestation that names what went into it
# both rest on this. Bumping one is a deliberate act with a diff.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:4cb7ac979db5fcc41cae44b2227ba5ab8a51e8807f40d9ba4dee20a0ad960b5b AS builder

# hadolint ignore=DL3018
RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	go mod download

COPY . .

ARG VERSION=""
ARG COMMIT=""
ARG TARGETOS
ARG TARGETARCH
# No -buildmode=pie, and that is the whole fix for a defect this image shipped:
# the published linux/arm64 image could not exec at all, dying at
# "Could not open '/lib/ld-linux-aarch64.so.1'".
#
# PIE makes a Go binary dynamically linked, and the linker picks its PT_INTERP by
# stat-ing the *build* host (cmd/link/internal/ld/elf.go): the builder runs on
# $BUILDPLATFORM, so the native architecture got Alpine's musl loader and every
# cross-compiled one got a glibc path this image does not ship. Naming the
# interpreter by hand fixes that, and dropping the flag removes the question —
# the binary then requests no interpreter at all, which is also what makes it
# runnable on a musl host, a distroless image or scratch.
#
# The flag buys nothing anywhere else: measured with Go 1.27, a windows/amd64
# build has DllCharacteristics 00008160 with and without it, and a darwin build
# is `flags:<DYLDLINK|PIE>` either way. Go already emits ASLR-capable binaries
# there. On linux it trades the standalone property for the executable's own
# address randomization, on a CGO-free binary with no FFI surface.
#
# The grep is the guard: an interpreter path is a literal string in the ELF, so
# a re-added -buildmode=pie fails here rather than at exec on one architecture.
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	set -eu; \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
	-trimpath \
	-ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
	-o /out/libgen-mcp ./cmd/server; \
	if grep -a -q "ld-linux\|ld-musl" /out/libgen-mcp; then \
	echo "built binary names an ELF interpreter: it is not standalone, and on ${TARGETARCH} it may not exec at all" >&2; \
	exit 1; \
	fi

# --- Runtime stage ---
FROM alpine:3.24@sha256:5b02b42e375f7426f8d65c3af331ca05d9878f9989230354504e0b9dfd431f60

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates tzdata && \
	addgroup -S -g 10001 appgroup && \
	adduser -S -u 10001 -G appgroup -h /home/appuser appuser

COPY --from=builder /out/libgen-mcp /usr/local/bin/libgen-mcp

USER appuser

# Port used only when the server is started in streamable HTTP mode on a TCP
# address (`--http 0.0.0.0:8080`). The default transport is stdio, which needs no
# port; nor does the other HTTP form, `--http /run/mcp/libgen.sock`, which binds a
# unix socket in a mounted directory and publishes nothing at all — the shape to
# use when a reverse proxy shares the host. The socket is created 0660, so the
# proxy's worker processes must run in the group that owns it (10001 here).
EXPOSE 8080

ARG VERSION=""
ARG COMMIT=""
ARG BUILD_DATE=""
LABEL org.opencontainers.image.title="libgen-mcp" \
	org.opencontainers.image.description="MCP server for searching and downloading from Library Genesis (libgen.li mirror family)" \
	org.opencontainers.image.source="https://github.com/jmrplens/libgen-mcp" \
	org.opencontainers.image.url="https://github.com/jmrplens/libgen-mcp" \
	org.opencontainers.image.version="${VERSION}" \
	org.opencontainers.image.revision="${COMMIT}" \
	org.opencontainers.image.created="${BUILD_DATE}" \
	org.opencontainers.image.licenses="MIT" \
	org.opencontainers.image.authors="jmrplens" \
	org.opencontainers.image.vendor="jmrplens" \
	io.modelcontextprotocol.server.name="io.github.jmrplens/libgen-mcp"

# That last label is not decoration: the MCP Registry validates ownership of an
# OCI package through it, so an image without it cannot be declared in
# server.json whatever else is right.

# The binary probes itself, rather than the image carrying a curl or wget line
# that restates the flags. Such a line is right for the default command and
# wrong for every other listener this server supports — another port, a unix
# socket, TLS this process terminates, a mount under --http-path — each of which
# would report unhealthy while serving perfectly, and an orchestrator would then
# restart a container whose restart changes nothing. --healthcheck reads the
# listener off the running instance's own command line instead.
#
# The interval budget: one attempt is bounded at 3s and the whole run at 4s, so
# the 5s timeout below is a ceiling the check answers inside rather than one it
# is killed by.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
	CMD ["libgen-mcp", "--healthcheck"]

ENTRYPOINT ["libgen-mcp"]

# The transport is decided by what stdin is, which is what `auto` reads.
#
# `docker run -i` connects a pipe and gets a stdio server, which is what an MCP
# client starts. A run without `-i` connects /dev/null — a Compose file with no
# `stdin_open`, a Kubernetes pod, anything an orchestrator starts — and used to
# get a stdio server reading /dev/null, which is a process that is up, healthy
# and unreachable. It now gets the HTTP listener the port below advertises.
#
# Two consequences worth knowing before overriding it. **Any argument replaces
# CMD wholesale**, so `docker run image --http :9000` drops `--transport auto`
# with it — which is fine, since naming a listener is deciding the transport.
# And a container binding 0.0.0.0 **with LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES set
# is refused at startup**: the default listener below is exactly the shape that
# refusal is about, so the two settings now meet by default rather than only
# when somebody asked for a port.
CMD ["--transport", "auto", "--http", "0.0.0.0:8080"]
