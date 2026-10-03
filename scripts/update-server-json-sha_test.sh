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
# A third one is the Claude Desktop bundles. A release declares one bundle per
# operating system, and the stamper rebuilds the mcpb entries from the bundles
# it is given, so the single universal entry main declared before becomes three.
# A set that does not serve darwin, win32 and linux once each (the universal
# bundle among the per-OS ones, a system missing) would publish a listing a
# client cannot choose from by system, and is refused before anything is
# written.
#
# So all three are driven here, on a fixture, with no network and no release.
#
# Usage: scripts/update-server-json-sha_test.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Overridable so the cases can be pointed at an older or deliberately broken
# copy of the stamper, which is how they were shown to fail.
STAMPER="${STAMPER:-$REPO_ROOT/scripts/update-server-json-sha.sh}"

for tool in jq sha256sum zip unzip; do
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

# make_bundle <dir> <file-name> <platforms-json> writes a stand-in bundle: a zip
# whose manifest.json lists the given platforms, which is all the stamper reads
# of it besides its bytes.
make_bundle() {
  local stage
  stage="$(mktemp -d)"
  printf '{"name":"libgen-mcp","compatibility":{"platforms":%s}}\n' "$3" >"$stage/manifest.json"
  # A fixed timestamp, so two cases build byte-identical bundles and can be
  # compared by the file they stamp.
  touch -t 198001010000 "$stage/manifest.json"
  (cd "$stage" && zip -q -X "$1/$2" manifest.json)
  rm -rf "$stage"
}

# The three per-OS bundles a release declares, as the third argument takes them.
PER_OS="libgen-mcp-darwin.mcpb,libgen-mcp-windows.mcpb,libgen-mcp-linux.mcpb"
RELEASE_URL="https://github.com/jmrplens/libgen-mcp/releases/download"

# new_case stages a fresh working directory holding the fixture manifest, an
# empty checksums file, the three per-OS stand-in bundles and the universal
# one, and echoes its path.
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
      "transport": { "type": "stdio" },
      "environmentVariables": [{ "name": "LIBGEN_MCP_CORE_KEY", "isSecret": true }]
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
  make_bundle "$dir" libgen-mcp-darwin.mcpb '["darwin"]'
  make_bundle "$dir" libgen-mcp-windows.mcpb '["win32"]'
  make_bundle "$dir" libgen-mcp-linux.mcpb '["linux"]'
  make_bundle "$dir" libgen-mcp.mcpb '["darwin","win32","linux"]'
  # The two plugin manifests, which are different schemas for different
  # directories and are both stamped from here. A run that reaches only one of
  # them ships a listing advertising the previous release.
  mkdir -p "$dir/.plugin"
  printf '{"name":"libgen-mcp","version":"0.0.1"}\n' >"$dir/.plugin/plugin.json"
  printf '{"name":"libgen-mcp","version":"0.0.1"}\n' >"$dir/plugin.json"
  # The citation file is YAML, so it is stamped by line rather than by jq, and
  # a line that is not top-level must be left alone.
  printf 'cff-version: 1.2.0\ntitle: libgen-mcp\nversion: 0.0.1\ndate-released: 2000-01-01\nreferences:\n  - version: 0.0.1\n' >"$dir/CITATION.cff"
  echo "$dir"
}

# 1. A complete stamp rewrites every entry: the single universal bundle entry
#    becomes one entry per operating system, in its place, and each OCI entry
#    keeps its own repository.
dir="$(new_case)"
(cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 "$PER_OS" "$DIGEST" >stamp.log 2>&1) ||
  fail "a complete stamp exited non-zero: $(cat "$dir/stamp.log")"

want "top-level version" "9.9.9" "$(jq -r '.version' "$dir/server.json")"
want "npm entry version" "9.9.9" "$(jq -r '.packages[] | select(.registryType == "npm") | .version' "$dir/server.json")"
want "bundle identifiers, in order, where the universal entry was" \
  "$RELEASE_URL/v9.9.9/libgen-mcp-darwin.mcpb $RELEASE_URL/v9.9.9/libgen-mcp-windows.mcpb $RELEASE_URL/v9.9.9/libgen-mcp-linux.mcpb" \
  "$(jq -r '[.packages[:3][] | .identifier] | join(" ")' "$dir/server.json")"
for bundle in libgen-mcp-darwin.mcpb libgen-mcp-windows.mcpb libgen-mcp-linux.mcpb; do
  want "$bundle hash" \
    "$(sha256sum "$dir/$bundle" | cut -d' ' -f1)" \
    "$(jq -r --arg b "$bundle" '.packages[] | select(.identifier | endswith("/" + $b)) | .fileSha256' "$dir/server.json")"
  want "$bundle carries the universal entry's transport, environment and version" \
    'stdio LIBGEN_MCP_CORE_KEY 9.9.9 mcpb' \
    "$(jq -r --arg b "$bundle" '.packages[] | select(.identifier | endswith("/" + $b))
      | [.transport.type, (.environmentVariables[0].name), .version, .registryType] | join(" ")' "$dir/server.json")"
done
want "mcpb entries" "3" "$(jq '[.packages[] | select(.registryType == "mcpb")] | length' "$dir/server.json")"
want "every entry" "6" "$(jq '.packages | length' "$dir/server.json")"

# 1a. A re-run with the same bundles writes the same file: the entries are
#     rebuilt from the bundles, not appended to.
cp "$dir/server.json" "$dir/first.json"
(cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 "$PER_OS" "$DIGEST" >stamp.log 2>&1) ||
  fail "a second stamp exited non-zero: $(cat "$dir/stamp.log")"
cmp -s "$dir/first.json" "$dir/server.json" || fail "a second stamp with the same bundles changed server.json"

# 1c. The bundles given in another order write the identical file: the entries
#     follow a fixed platform order, so the registry job and the commit back to
#     main cannot disagree over how each lists them.
other="$(new_case)"
(cd "$other" && bash "$STAMPER" checksums.txt 9.9.9 \
  "libgen-mcp-linux.mcpb,libgen-mcp-darwin.mcpb,libgen-mcp-windows.mcpb" "$DIGEST" >stamp.log 2>&1) ||
  fail "a stamp with the bundles reordered exited non-zero: $(cat "$other/stamp.log")"
cmp -s "$dir/first.json" "$other/server.json" ||
  fail "the bundles given in another order wrote a different server.json: $(diff "$dir/first.json" "$other/server.json")"
rm -rf "$other"
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
want "the Open Plugins manifest" "9.9.9" "$(jq -r '.version' "$dir/.plugin/plugin.json")"
want "the Agent Plugins manifest" "9.9.9" "$(jq -r '.version' "$dir/plugin.json")"
want "the citation file's version" "version: 9.9.9" "$(grep '^version: ' "$dir/CITATION.cff")"
want "the citation file's release date" "date-released: $(date -u +%Y-%m-%d)" "$(grep '^date-released: ' "$dir/CITATION.cff")"
want "a nested version line" "  - version: 0.0.1" "$(grep '^  - version: ' "$dir/CITATION.cff")"
rm -rf "$dir"

# 1b. A citation file that has lost its version line is refused, not skipped.
dir="$(new_case)"
printf 'cff-version: 1.2.0\ntitle: libgen-mcp\n' >"$dir/CITATION.cff"
if (cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 "$PER_OS" "$DIGEST" >stamp.log 2>&1); then
  fail "a citation file with no version line was accepted"
else
  grep -q "no top-level version or date-released line" "$dir/stamp.log" ||
    fail "the refusal did not say why: $(cat "$dir/stamp.log")"
fi
rm -rf "$dir"

# 2. A digest-pinned identifier with no digest is refused rather than stamped.
dir="$(new_case)"
if (cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 "$PER_OS" >stamp.log 2>&1); then
  fail "a stamp with no digest was accepted; the previous release's image would stay pinned"
else
  grep -q "no digest was passed" "$dir/stamp.log" ||
    fail "the refusal did not say why: $(cat "$dir/stamp.log")"
  want "the manifest is untouched" "0.0.1" "$(jq -r '.version' "$dir/server.json")"
fi
rm -rf "$dir"

# 3. A digest that is not sha256:<64 hex> is refused.
dir="$(new_case)"
if (cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 "$PER_OS" "sha256:deadbeef" >stamp.log 2>&1); then
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

# 5. A set of bundles that does not serve each system exactly once, one system
#    per bundle, is refused, and server.json is left exactly as it was.
#    want_refused <description> <bundles> <expected message>
want_refused() {
  local dir
  dir="$(new_case)"
  cp "$dir/server.json" "$dir/before.json"
  if (cd "$dir" && bash "$STAMPER" checksums.txt 9.9.9 "$2" "$DIGEST" >stamp.log 2>&1); then
    fail "$1 was accepted"
  else
    grep -qF "$3" "$dir/stamp.log" || fail "$1: the refusal did not say '$3': $(cat "$dir/stamp.log")"
    cmp -s "$dir/before.json" "$dir/server.json" || fail "$1: server.json was written before the refusal"
  fi
  rm -rf "$dir"
}
want_refused "the universal bundle alone" "libgen-mcp.mcpb" \
  "libgen-mcp.mcpb serves 3 platforms (darwin, win32, linux), and a declared bundle serves exactly one"
want_refused "the universal bundle beside the per-OS ones" "$PER_OS,libgen-mcp.mcpb" \
  "darwin is served by 2 bundles"
want_refused "a set with no Linux bundle" "libgen-mcp-darwin.mcpb,libgen-mcp-windows.mcpb" \
  "no bundle given serves linux"
want_refused "a bundle given twice" "$PER_OS,libgen-mcp-linux.mcpb" \
  "libgen-mcp-linux.mcpb is given more than once"
want_refused "a bundle that is not there" "$PER_OS,libgen-mcp-freebsd.mcpb" \
  "mcpb bundle not found: libgen-mcp-freebsd.mcpb"

if [ "$failures" -gt 0 ]; then
  echo "$failures assertion(s) failed" >&2
  exit 1
fi
echo "update-server-json-sha.sh: 12 cases passed"
