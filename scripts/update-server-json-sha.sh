#!/usr/bin/env bash
# Update server.json (MCP Registry manifest) with the release version,
# version-pinned download URLs, and SHA256 hashes from GoReleaser's
# checksums.txt.
#
# Usage: update-server-json-sha.sh <checksums-file> <version>

set -euo pipefail

CHECKSUMS_FILE="${1:?Usage: $0 <checksums-file> <version>}"
VERSION="${2:?Usage: $0 <checksums-file> <version>}"
SERVER_JSON="server.json"

if [[ ! -f "$CHECKSUMS_FILE" ]]; then
  echo "ERROR: checksums file not found: $CHECKSUMS_FILE" >&2
  exit 1
fi
if [[ ! -f "$SERVER_JSON" ]]; then
  echo "ERROR: $SERVER_JSON not found in current directory" >&2
  exit 1
fi
if ! command -v jq &>/dev/null; then
  echo "ERROR: jq is required but not installed" >&2
  exit 1
fi

# 1. Top-level version
jq --arg v "$VERSION" '.version = $v' "$SERVER_JSON" >tmp.$$.json && mv tmp.$$.json "$SERVER_JSON"
echo "Top-level version set to $VERSION"

# 2. Per-package version fields
jq --arg v "$VERSION" \
  '.packages |= map(if has("version") then .version = $v else . end)' \
  "$SERVER_JSON" >tmp.$$.json && mv tmp.$$.json "$SERVER_JSON"
echo "Per-package version fields set to $VERSION"

# 3. Pin identifier URLs to this release version (handles /latest/ and prior /vX.Y.Z/).
jq --arg v "$VERSION" '
  (.packages[].identifier) |=
    (sub("releases/latest/download"; "releases/download/v" + $v)
  | sub("releases/download/v[0-9]+\\.[0-9]+\\.[0-9]+(-[A-Za-z0-9.]+)?"; "releases/download/v" + $v))
' "$SERVER_JSON" >tmp.$$.json && mv tmp.$$.json "$SERVER_JSON"
echo "Identifiers pinned to v$VERSION"

# 4. Set fileSha256 for each entry matching a checksum line.
updated=0
while read -r hash filename; do
  [[ -z "${hash:-}" || -z "${filename:-}" ]] && continue
  match=$(jq --arg name "$filename" \
    '[.packages[] | select(.identifier | endswith($name))] | length' "$SERVER_JSON")
  if [[ "$match" -gt 0 ]]; then
    jq --arg hash "$hash" --arg name "$filename" \
      '(.packages[] | select(.identifier | endswith($name))).fileSha256 = $hash' \
      "$SERVER_JSON" >tmp.$$.json && mv tmp.$$.json "$SERVER_JSON"
    echo "SHA256 for $filename: ${hash:0:16}..."
    ((updated++)) || true
  fi
done <"$CHECKSUMS_FILE"

total=$(jq '.packages | length' "$SERVER_JSON")
echo "Updated $updated of $total package entries"

# 5. Stamp the version into every other version-bearing manifest.
#
# server.json is handled above because it carries far more than a version —
# per-package fields, pinned identifiers and digests. These three carry only the
# one field, and they are exactly the rest of the set `make check-manifests`
# gates against VERSION, so a tag now leaves all four consistent instead of the
# two that happened to be wired first. That asymmetry was not harmless:
# .plugin/plugin.json once spent a whole release cycle advertising a version the
# repository had already left behind.
#
# lhm.plugin.json is among them for a second reason: the actual publish to
# LobeHub is a manual step (`make publish-lobehub`, the CLI has no
# non-interactive auth), so stamping here is what keeps its version honest
# between the tag and that step.
for manifest in lhm.plugin.json mcpb/manifest.json .plugin/plugin.json; do
  if [[ -f "$manifest" ]]; then
    jq --arg v "$VERSION" '.version = $v' "$manifest" >tmp.$$.json && mv tmp.$$.json "$manifest"
    echo "$manifest version set to $VERSION"
  else
    echo "NOTE: $manifest not found, skipping"
  fi
done

# 6. Update the npm launcher package version and its optionalDependency pins.
#
# The generator owns the whole mapping — the version and all six pins move
# together — so this stays a single call rather than a jq edit that could stamp
# the version while leaving the dependency specs a release behind. The
# per-platform packages are built from the release binaries at publish time,
# not stamped here.
NPM_MAIN="npm/libgen-mcp/package.json"
if [[ -f "$NPM_MAIN" ]] && command -v node >/dev/null 2>&1; then
  node scripts/build-npm.mjs --sync-only --version "$VERSION"
else
  echo "NOTE: $NPM_MAIN not found or node unavailable, skipping npm manifest update"
fi
