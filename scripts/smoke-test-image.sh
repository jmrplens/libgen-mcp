#!/usr/bin/env bash
# smoke-test-image.sh — start the container image on every platform it claims to
# support, check it prints the expected version, check its binary records that
# version in its build information, and check it carries the licence and the
# third-party notices generated for its own binary.
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
#
# The binary's build information has to carry -ldflags with
# `-X main.version=<version>`, which is what syft reads to give the server a
# versioned purl in the image SBOM: the build context has no .git, so the main
# module line says (devel), and without that setting no advisory against this
# module can be matched to the image. -trimpath drops -ldflags from the build
# information while --version still answers with the right version, so the
# version check above cannot tell, and the Dockerfile builds without it for
# that reason. The binary is copied out with cat and the -ldflags line of its
# build information read with grep -a, so the check needs no Go toolchain on
# the runner.
#
# The licence and THIRD_PARTY_NOTICES sit in /usr/share/licenses/libgen-mcp. The
# licence must be this repository's, and the notices, which the image's builder
# generates from the binary it just built, must name the platform the image was
# built for: notices left over from another build, or a step that stopped
# writing them, would otherwise ship without anything noticing.
set -euo pipefail

VERSION="${1:?Usage: $0 <expected-version> <image>=<platform> [...]}"
shift
if [ "$#" -eq 0 ]; then
  echo "ERROR: name at least one <image>=<platform> pair" >&2
  exit 1
fi

BINARY=/usr/local/bin/libgen-mcp
LDFLAGS_PREFIX=$'build\t-ldflags='
LICENSES_DIR=/usr/share/licenses/libgen-mcp
REPO_LICENSE="$(cd "$(dirname "$0")/.." && pwd)/LICENSE"
NOTICES_HEADER="Third-party notices for libgen-mcp"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

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

  binfile="${workdir}/binary"
  if ! docker run --rm --platform "$platform" --entrypoint /bin/cat "$image" "$BINARY" > "$binfile" 2> "${binfile}.err"; then
    echo "FAIL: ${image} (${platform}) carries no ${BINARY} to read:" >&2
    cat "${binfile}.err" >&2
    failures=$((failures + 1))
    continue
  fi
  ldflags=$(grep -a "^${LDFLAGS_PREFIX}" "$binfile" || true)
  if [[ "$ldflags" != *"-X main.version=${VERSION}"[\ \"]* ]]; then
    echo "FAIL: ${image} (${platform}) has a binary whose build information does not record -X main.version=${VERSION}" >&2
    echo "      (a build with -trimpath leaves -ldflags out of it); its -ldflags line reads:" >&2
    printf '      %s\n' "${ldflags:-(none)}" >&2
    failures=$((failures + 1))
    continue
  fi
  echo "    build information records -X main.version=${VERSION}"

  if ! licence=$(docker run --rm --platform "$platform" --entrypoint /bin/cat "$image" "$LICENSES_DIR/LICENSE" 2>&1); then
    echo "FAIL: ${image} (${platform}) carries no ${LICENSES_DIR}/LICENSE:" >&2
    printf '%s\n' "$licence" >&2
    failures=$((failures + 1))
    continue
  fi
  if [ "$licence" != "$(cat "$REPO_LICENSE")" ]; then
    echo "FAIL: ${image} (${platform}) carries a ${LICENSES_DIR}/LICENSE that is not this repository's" >&2
    failures=$((failures + 1))
    continue
  fi

  if ! notices=$(docker run --rm --platform "$platform" --entrypoint /bin/cat "$image" "$LICENSES_DIR/THIRD_PARTY_NOTICES" 2>&1); then
    echo "FAIL: ${image} (${platform}) carries no ${LICENSES_DIR}/THIRD_PARTY_NOTICES:" >&2
    printf '%s\n' "$notices" >&2
    failures=$((failures + 1))
    continue
  fi
  # Here-strings rather than pipes: the notices run to hundreds of kilobytes,
  # and a printf into a grep -q or a head that stops reading early dies of
  # SIGPIPE, which pipefail would report as the check failing.
  if [ "$(head -n 1 <<< "$notices")" != "$NOTICES_HEADER" ] ||
    ! grep -qxF "Builds:    ${platform}" <<< "$notices"; then
    echo "FAIL: ${image} (${platform}) carries THIRD_PARTY_NOTICES that are not the generator's for ${platform}:" >&2
    head -n 5 <<< "$notices" >&2
    failures=$((failures + 1))
    continue
  fi
  echo "    licence and third-party notices for ${platform} present"
done

if [ "$failures" -gt 0 ]; then
  echo "${failures} image(s) failed the smoke test" >&2
  exit 1
fi
echo "all images started, reported ${VERSION}, record it in their build information and carry their licence and notices"
