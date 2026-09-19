#!/usr/bin/env bash
# smoke-test-image.sh — start the container image on every platform it claims to
# support and check it prints the expected version.
#
# Usage:
#   scripts/smoke-test-image.sh <expected-version> <image>=<platform> [...]
#
# Example:
#   scripts/smoke-test-image.sh 1.7.3 \
#     libgen-mcp:smoke-amd64=linux/amd64 \
#     libgen-mcp:smoke-arm64=linux/arm64
#
# Non-native platforms need QEMU binfmt registered (docker/setup-qemu-action in
# CI, `docker run --privileged tonistiigi/binfmt --install all` locally).
#
# Why this exists: an image nothing has ever executed on the platform it
# advertises is not a tested artefact. This project published a linux/arm64
# image that could not exec at all — -buildmode=pie makes a Go binary
# dynamically linked and the linker picks its ELF interpreter by stat-ing the
# *build* host, so the cross-compiled binary asked for
# /lib/ld-linux-aarch64.so.1 while the Alpine runtime ships only musl's — and
# every gate in the pipeline passed: the end-to-end suites run the binary, not
# the image; CI built only the runner's native platform; and the release built
# both and started neither. No static Dockerfile check would have caught it.
# Only running the thing does, which is why this outlives the flag that caused
# it.
#
# --version is the whole check on purpose: it needs no network, no mirror and no
# MCP client, and it exercises the one thing in doubt, which is whether the
# kernel can exec the binary inside the image at all.
set -euo pipefail

VERSION="${1:?Usage: $0 <expected-version> <image>=<platform> [...]}"
shift
if [ "$#" -eq 0 ]; then
  echo "ERROR: name at least one <image>=<platform> pair" >&2
  exit 1
fi

failures=0
for pair in "$@"; do
  image="${pair%%=*}"
  platform="${pair#*=}"
  if [ "$image" = "$pair" ] || [ -z "$platform" ]; then
    echo "ERROR: '$pair' is not <image>=<platform>" >&2
    exit 1
  fi

  echo "==> ${image} on ${platform}"
  # --entrypoint is left alone and the argument replaces CMD, which is what a
  # caller passing a flag gets: the image's default command serves HTTP, and a
  # smoke test that started a listener would then have to stop one.
  if ! output=$(docker run --rm --platform "$platform" "$image" --version 2>&1); then
    echo "FAIL: ${image} (${platform}) did not start:" >&2
    printf '%s\n' "$output" >&2
    failures=$((failures + 1))
    continue
  fi

  if ! printf '%s' "$output" | grep -q "^libgen-mcp ${VERSION}\b"; then
    echo "FAIL: ${image} (${platform}) started but printed:" >&2
    printf '%s\n' "$output" >&2
    echo "      expected a line beginning 'libgen-mcp ${VERSION}'" >&2
    failures=$((failures + 1))
    continue
  fi

  printf '    %s\n' "$output"
done

if [ "$failures" -gt 0 ]; then
  echo "${failures} image(s) failed the smoke test" >&2
  exit 1
fi
echo "all images started and reported ${VERSION}"
