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

# 5. Stamp the LobeHub Marketplace manifest version (if present). The actual
# publish to LobeHub is a manual step (`make publish-lobehub`) — the CLI has no
# non-interactive auth — but the version is kept in sync here on every release.
LHM_JSON="lhm.plugin.json"
if [[ -f "$LHM_JSON" ]]; then
  jq --arg v "$VERSION" '.version = $v' "$LHM_JSON" >tmp.$$.json && mv tmp.$$.json "$LHM_JSON"
  echo "$LHM_JSON version set to $VERSION"
else
  echo "NOTE: $LHM_JSON not found, skipping LobeHub manifest update"
fi
