#!/usr/bin/env bash
# Sign a container image with cosign, retrying transient OIDC failures.
#
# Keyless signing resolves its identity from the ambient GitHub OIDC endpoint,
# which intermittently answers with a plain-text error body where cosign
# expects JSON:
#
#   fetching ambient OIDC credentials: invalid character 'u' looking for
#   beginning of value
#
# By then the image is already pushed, so failing the step leaves a published
# image nobody signed and fails the release over a blip that clears on its own.
# Retry before giving up.
#
# `--recursive` signs every manifest the index lists, not only the index. A
# consumer that resolves a tag for its own platform looks for a signature of
# THAT manifest's digest and finds nothing under an index-only signature; the
# same reason the provenance attestations cover the platform manifests too.
#
# Usage: cosign-sign-retry.sh <image-ref-with-digest>

set -euo pipefail

IMAGE_REF="${1:?Usage: $0 <image-ref-with-digest>}"
ATTEMPTS="${COSIGN_SIGN_ATTEMPTS:-3}"

for attempt in $(seq 1 "$ATTEMPTS"); do
	if cosign sign --yes --recursive "$IMAGE_REF"; then
		exit 0
	fi

	if [[ "$attempt" -eq "$ATTEMPTS" ]]; then
		echo "cosign sign failed for ${IMAGE_REF} after ${ATTEMPTS} attempts" >&2
		exit 1
	fi

	delay=$((attempt * 10))
	echo "cosign sign failed for ${IMAGE_REF} (attempt ${attempt}/${ATTEMPTS}); retrying in ${delay}s" >&2
	sleep "$delay"
done
