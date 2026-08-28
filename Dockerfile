# syntax=docker/dockerfile:1

# --- Build stage ---
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

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
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
	-trimpath -buildmode=pie \
	-ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
	-o /out/libgen-mcp ./cmd/server

# --- Runtime stage ---
FROM alpine:3.24

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
	org.opencontainers.image.vendor="jmrplens"

# Default transport is stdio (no args) — the mode MCP clients use, so
# `docker run -i --rm ...` works out of the box. For the streamable HTTP
# transport, override at run time: `docker run ... --http 0.0.0.0:8080`, or
# `docker run -v ./run:/run/mcp ... --http /run/mcp/libgen.sock` to serve a
# same-host proxy over a unix socket with no published port.
ENTRYPOINT ["libgen-mcp"]
