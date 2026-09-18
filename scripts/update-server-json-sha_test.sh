#!/usr/bin/env bash
# update-server-json-sha_test.sh — exercise the release stamper against a
# fixture manifest.
#
# The stamper runs once per release, inside the job that holds this
# repository's publishing identities, and what it writes is what the MCP
# Registry serves for that version. Two of its rewrites cannot be checked by
# reading the result afterwards, because a wrong answer looks right:
#
#   - Each OCI entry keeps its own repository. Computing one identifier and
#     assigning it to every entry republishes the ghcr.io reference under the
#     Docker Hub entry, and the tag in it still reads correctly.
#   - A digest-pinned identifier stamped without a digest keeps the previous
#     release's image under the new tag. Same shape, wrong bytes.
#
# So both are driven here, on a fixture, with no network and no release.
#
# Usage: scripts/update-server-json-sha_test.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Overridable so the cases can be pointed at an older or deliberately broken
# copy of the stamper, which is how they were shown to fail.
STAMPER="${STAMPER:-$REPO_ROOT/scripts/update-server-json-sha.sh}"

for tool in jq sha256sum; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "SKIP: $tool is not installed" >&2
    exit 0
  fi
done

DIGEST="sha256:$(printf 'a%.0s' {1..64})"
failures=0

fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# want <description> <expected> <actual>
want() {
  if [ "$2" != "$3" ]; then
    fail "$1: want '$2', got '$3'"
  fi
}

# new_case stages a fresh working directory holding the fixture manifest, an
# empty checksums file and a stand-in bundle, and echoes its path.
new_case() {
  local dir
  dir="$(mktemp -d)"
  cat >"$dir/server.json" <<'JSON'
{
  "$schema": "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
  "name": "io.github.jmrplens/libgen-mcp",
  "version": "0.0.1",
  "packages": [
    {
      "registryType": "mcpb",
      "identifier": "https://github.com/jmrplens/libgen-mcp/releases/download/v0.0.1/libgen-mcp.mcpb",
      "version": "0.0.1",
      "fileSha256": "0000000000000000000000000000000000000000000000000000000000000000",
      "transport": { "type": "stdio" }
    },
    {
      "registryType": "oci",
      "identifier": "ghcr.io/jmrplens/libgen-mcp:0.0.1@sha256:1111111111111111111111111111111111111111111111111111111111111111",
      "transport": { "type": "stdio" }
    },
    {
      "registryType": "oci",
      "identifier": "docker.io/jmrplens/libgen-mcp:0.0.1@sha256:1111111111111111111111111111111111111111111111111111111111111111",
      "transport": { "type": "stdio" }
    },
    {
      "registryType": "npm",
      "identifier": "@jmrp.io/libgen-mcp",
      "version": "0.0.1",
      "runtimeHint": "npx",
      "transport": { "type": "stdio" }
    }
  ]
}
JSON
  : >"$dir/checksums.txt"
  printf 'not really a bundle\n' >"$dir/libgen-mcp.mcpb"
  echo "$dir"
}

# 1. A complete stamp rewrites the four entries, and each OCI entry keeps its
#    own repository.
dir="$(new_case)"
(cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 libgen-mcp.mcpb "$DIGEST" >stamp.log 2>&1) ||
  fail "a complete stamp exited non-zero: $(cat "$dir/stamp.log")"

want "top-level version" "9.9.9" "$(jq -r '.version' "$dir/server.json")"
want "npm entry version" "9.9.9" "$(jq -r '.packages[] | select(.registryType == "npm") | .version' "$dir/server.json")"
want "bundle identifier" \
  "https://github.com/jmrplens/libgen-mcp/releases/download/v9.9.9/libgen-mcp.mcpb" \
  "$(jq -r '.packages[] | select(.registryType == "mcpb") | .identifier' "$dir/server.json")"
want "bundle hash" \
  "$(sha256sum "$dir/libgen-mcp.mcpb" | cut -d' ' -f1)" \
  "$(jq -r '.packages[] | select(.registryType == "mcpb") | .fileSha256' "$dir/server.json")"
want "ghcr identifier" \
  "ghcr.io/jmrplens/libgen-mcp:9.9.9@$DIGEST" \
  "$(jq -r '.packages[] | select(.identifier | startswith("ghcr.io")) | .identifier' "$dir/server.json")"
want "docker hub identifier" \
  "docker.io/jmrplens/libgen-mcp:9.9.9@$DIGEST" \
  "$(jq -r '.packages[] | select(.identifier | startswith("docker.io")) | .identifier' "$dir/server.json")"
# The OCI entries declare no version field, and the stamper must not invent one:
# the registry rejects a version on an OCI package, whose version is its tag.
want "oci entries carry no version field" "0" \
  "$(jq '[.packages[] | select(.registryType == "oci") | select(has("version"))] | length' "$dir/server.json")"
rm -rf "$dir"

# 2. A digest-pinned identifier with no digest is refused rather than stamped.
dir="$(new_case)"
if (cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 libgen-mcp.mcpb >stamp.log 2>&1); then
  fail "a stamp with no digest was accepted; the previous release's image would stay pinned"
else
  grep -q "no digest was passed" "$dir/stamp.log" ||
    fail "the refusal did not say why: $(cat "$dir/stamp.log")"
  want "the manifest is untouched" "0.0.1" "$(jq -r '.version' "$dir/server.json")"
fi
rm -rf "$dir"

# 3. A digest that is not sha256:<64 hex> is refused.
dir="$(new_case)"
if (cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 libgen-mcp.mcpb "sha256:deadbeef" >stamp.log 2>&1); then
  fail "a malformed digest was accepted"
else
  grep -q "oci-digest must look like" "$dir/stamp.log" ||
    fail "the refusal did not name the expected shape: $(cat "$dir/stamp.log")"
fi
rm -rf "$dir"

# 4. A run that hashes nothing is a failure, not a quiet success. The checksum
#    file names a file no identifier ends with, and no bundle is passed.
dir="$(new_case)"
printf '%s  libgen-mcp-linux-amd64\n' "$(printf 'b%.0s' {1..64})" >"$dir/checksums.txt"
if (cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 "" "$DIGEST" >stamp.log 2>&1); then
  fail "a stamp that hashed nothing reported success"
else
  grep -q "no package got a hash" "$dir/stamp.log" ||
    fail "the refusal did not name the cause: $(cat "$dir/stamp.log")"
fi
rm -rf "$dir"

if [ "$failures" -gt 0 ]; then
  echo "$failures assertion(s) failed" >&2
  exit 1
fi
echo "update-server-json-sha.sh: 4 cases passed"
