#!/usr/bin/env bash
# Update server.json (MCP Registry manifest) with the release version,
# version-pinned download URLs, and SHA256 hashes from GoReleaser's
# checksums.txt.
#
# Usage: update-server-json-sha.sh <checksums-file> <version> [mcpb-file] [oci-digest]
#
#   <checksums-file>  GoReleaser's checksums.txt
#   <version>         Release version without the leading v
#   [mcpb-file]       The .mcpb bundle, hashed here because GoReleaser does not
#                     build it and appending it to the signed checksums.txt
#                     would invalidate checksums.txt.sigstore.json
#   [oci-digest]      sha256:<64 hex> of the pushed image index, required
#                     whenever an OCI identifier is digest-pinned
#
# Both optional arguments are optional only in the sense that a caller may have
# nothing to stamp; each is required the moment server.json declares the entry
# that needs it, and the script says so rather than stamping a half-truth.

set -euo pipefail

CHECKSUMS_FILE="${1:?Usage: $0 <checksums-file> <version> [mcpb-file] [oci-digest]}"
VERSION="${2:?Usage: $0 <checksums-file> <version> [mcpb-file] [oci-digest]}"
MCPB_FILE="${3:-}"
OCI_DIGEST="${4:-}"
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

# 0. Both arguments a caller supplies are validated before anything is written.
# The rewrites below are in-place, one jq invocation per step, so a refusal
# raised halfway through would leave a manifest that is neither the old version
# nor the new one — a state nobody wrote deliberately, which the next person to
# run this, or to read the diff, has to unpick.
oci_count=$(jq '[.packages[] | select(.registryType == "oci")] | length' "$SERVER_JSON")
if [[ "$oci_count" -gt 0 ]]; then
  oci_digest_pinned=$(jq '[.packages[] | select(.registryType == "oci") | .identifier | select(contains("@"))] | length' "$SERVER_JSON")
  if [[ "$oci_digest_pinned" -gt 0 && -z "$OCI_DIGEST" ]]; then
    echo "ERROR: an OCI identifier is digest-pinned but no digest was passed as the" >&2
    echo "       fourth argument. Stamping the tag alone would leave the previous" >&2
    echo "       release's image pinned under the new version." >&2
    exit 1
  fi
  if [[ -n "$OCI_DIGEST" && ! "$OCI_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "ERROR: oci-digest must look like sha256:<64 hex chars> (got: $OCI_DIGEST)" >&2
    exit 1
  fi
fi
if [[ -n "$MCPB_FILE" && ! -f "$MCPB_FILE" ]]; then
  echo "ERROR: mcpb bundle not found: $MCPB_FILE" >&2
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

# 3b. Pin every OCI image reference. An OCI identifier carries its version in
# the tag rather than in a version field, so step 3's URL rewrite never reaches
# it. The reference is <repo>:<tag>@<digest>: the tag stays readable and matches
# every doc, while the digest is what a client actually resolves, so a retag of
# a published version cannot change what the registry serves.
#
# There is more than one such entry — ghcr.io and Docker Hub — and each keeps
# its own repository, so the rewrite happens inside jq, per entry, rather than
# by computing one identifier in the shell and assigning it to all of them,
# which would republish the ghcr.io reference under the Docker Hub entry. One
# digest serves both because the multi-platform build is pushed once and both
# registries hold the identical index (measured for v1.7.2: both answer
# sha256:fd41aaa9…). The digest is validated once, here, and applied to each.
if [[ "$oci_count" -gt 0 ]]; then
  jq --arg v "$VERSION" --arg d "$OCI_DIGEST" '
    # Drop the digest, then the tag. The tag is whatever follows the last colon;
    # a reference with no tag at all keeps its whole repository.
    def repo_of:
      (split("@")[0]) as $t
      | ($t | rindex(":")) as $i
      | if $i == null then $t else $t[0:$i] end;
    (.packages[] | select(.registryType == "oci") | .identifier) |=
      (repo_of + ":" + $v + (if $d == "" then "" else "@" + $d end))
  ' "$SERVER_JSON" >tmp.$$.json && mv tmp.$$.json "$SERVER_JSON"
  while read -r pinned; do
    echo "OCI image reference pinned to $pinned"
  done < <(jq -r '.packages[] | select(.registryType == "oci") | .identifier' "$SERVER_JSON")
fi

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

# 4b. Hash the .mcpb bundle. It is built after GoReleaser runs, so it is not in
# checksums.txt — and appending it there would invalidate the signature over
# that file. The bundle must therefore exist before this script runs.
if [[ -n "$MCPB_FILE" ]]; then
  mcpb_hash=$(sha256sum "$MCPB_FILE" | cut -d' ' -f1)
  mcpb_name=$(basename "$MCPB_FILE")
  mcpb_match=$(jq --arg name "$mcpb_name" \
    '[.packages[] | select(.identifier | endswith($name))] | length' "$SERVER_JSON")
  if [[ "$mcpb_match" -eq 0 ]]; then
    echo "ERROR: no package identifier ends with $mcpb_name" >&2
    exit 1
  fi
  jq --arg hash "$mcpb_hash" --arg name "$mcpb_name" \
    '(.packages[] | select(.identifier | endswith($name))).fileSha256 = $hash' \
    "$SERVER_JSON" >tmp.$$.json && mv tmp.$$.json "$SERVER_JSON"
  echo "SHA256 for $mcpb_name: ${mcpb_hash:0:16}..."
  ((updated++)) || true
else
  echo "NOTE: no mcpb bundle given, leaving its fileSha256 untouched"
fi

total=$(jq '.packages | length' "$SERVER_JSON")
echo "Updated $updated of $total package entries"

# Every declared artifact that carries a hash must have got one. The binaries
# this used to match were replaced by the bundle, so a run that hashes nothing
# means the checksum names no longer match any identifier and no bundle was
# passed — which would publish a manifest pointing at this release with the
# previous one's digest.
if [[ "$updated" -eq 0 ]]; then
  echo "ERROR: no package got a hash. Binary checksums no longer match any" >&2
  echo "       identifier, and no .mcpb bundle was passed as the third argument." >&2
  exit 1
fi

# 5. Stamp the version into every other version-bearing manifest.
#
# server.json is handled above because it carries far more than a version —
# per-package fields, pinned identifiers and digests. These four carry only the
# one field, and they are exactly the rest of the set `make check-manifests`
# gates against VERSION, so a tag now leaves all five consistent instead of the
# two that happened to be wired first. That asymmetry was not harmless:
# .plugin/plugin.json once spent a whole release cycle advertising a version the
# repository had already left behind.
#
# The two plugin manifests are different schemas for different directories, not
# a copy of each other: .plugin/plugin.json is the Open Plugins location and
# plugin.json at the root is the Agent Plugins one. Both are stamped here for
# the same reason, and neither is generated from the other.
#
# lhm.plugin.json is among them for a second reason: the actual publish to
# LobeHub is a manual step (`make publish-lobehub`, the CLI has no
# non-interactive auth), so stamping here is what keeps its version honest
# between the tag and that step.
for manifest in lhm.plugin.json mcpb/manifest.json .plugin/plugin.json plugin.json; do
  if [[ -f "$manifest" ]]; then
    jq --arg v "$VERSION" '.version = $v' "$manifest" >tmp.$$.json && mv tmp.$$.json "$manifest"
    echo "$manifest version set to $VERSION"
  else
    echo "NOTE: $manifest not found, skipping"
  fi
done

# 5b. CITATION.cff, the file GitHub's "Cite this repository" is built from.
#
# It names a version and a release date, and a citation that names the wrong
# release is a wrong citation, so it is stamped like the manifests above. It is
# YAML rather than JSON, so jq cannot write it: the two top-level lines are
# replaced instead, and a file that has lost either line is refused rather than
# left behind. The date is the day this runs, which is the day of the tag.
CFF="CITATION.cff"
if [[ -f "$CFF" ]]; then
  if ! grep -q '^version: ' "$CFF" || ! grep -q '^date-released: ' "$CFF"; then
    echo "ERROR: $CFF has no top-level version or date-released line to stamp" >&2
    exit 1
  fi
  released=$(date -u +%Y-%m-%d)
  sed -e "s/^version: .*/version: $VERSION/" \
    -e "s/^date-released: .*/date-released: $released/" "$CFF" >tmp.$$.cff && mv tmp.$$.cff "$CFF"
  echo "$CFF version set to $VERSION, released $released"
else
  echo "NOTE: $CFF not found, skipping"
fi

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
