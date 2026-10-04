#!/usr/bin/env bash
#
# verify-vex-attestation.sh holds an image's OpenVEX attestation to the
# document it was made from, reading it the way a scanner does.
#
#   verify-vex-attestation.sh <image@sha256:digest> <expected.openvex.json> <tag>
#
#   e.g. verify-vex-attestation.sh ghcr.io/jmrplens/libgen-mcp@sha256:... \
#          dist/libgen-mcp.openvex.json v2.2.1
#
# The attestation is read from the registry's referrers (--bundle-from-oci),
# never from GitHub's attestation store: Trivy's `--vex oci` reads the
# registry, so an attestation present in the store and missing from the
# registry is one no scanner sees. The bundle must verify against this
# repository's release workflow run from refs/tags/<tag>, the statement must
# name the digest, and its predicate must be the expected document once both
# are normalized by jq: a copy that verifies and says something else is the
# failure this exists for.
#
# It needs no credential. A public image's referrers and the Sigstore trust root
# are both readable anonymously, so the job that runs it can hold nothing.
set -euo pipefail

if [ "$#" -ne 3 ]; then
	echo "usage: verify-vex-attestation.sh <image@sha256:digest> <expected.openvex.json> <tag>" >&2
	exit 2
fi
ref="$1"
expected="$2"
workflow="jmrplens/libgen-mcp/.github/workflows/release.yml"
source_ref="refs/tags/$3"

case "$ref" in
*@sha256:*) ;;
*)
	echo "::error::${ref} is not pinned by digest; a tag can move between the attestation and this check" >&2
	exit 2
	;;
esac

predicate_type="https://openvex.dev/ns/v0.2.0"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT

gh attestation verify "oci://${ref}" \
	--bundle-from-oci \
	--repo jmrplens/libgen-mcp \
	--signer-workflow "${workflow}" \
	--source-ref "${source_ref}" \
	--predicate-type "${predicate_type}" \
	--format json >"$out"

digest="${ref##*@sha256:}"
# At least one verified bundle must name this digest and carry exactly the
# expected document. Others may legitimately exist beside it: a re-run of the
# signing job writes the statement again, with the document the re-run's vex
# job wrote, and an attestation cannot be withdrawn. Those are reported, not
# failed on, because requiring every bundle to match would make a release that
# was re-run fail this check forever.
count=$(jq 'length' "$out")
if [ "${count:-0}" -lt 1 ]; then
	echo "::error::no OpenVEX attestation on ${ref}" >&2
	exit 1
fi
want="$(jq -S . "$expected")"
matches=0
for i in $(seq 0 $((count - 1))); do
	if ! jq -e --arg d "$digest" ".[$i].verificationResult.statement.subject | any(.digest.sha256 == \$d)" "$out" >/dev/null; then
		echo "::warning::OpenVEX attestation $i on ${ref} does not name that digest as its subject"
		continue
	fi
	got="$(jq -S ".[$i].verificationResult.statement.predicate" "$out")"
	if [ "$got" = "$want" ]; then
		matches=$((matches + 1))
		continue
	fi
	echo "::notice::OpenVEX attestation $i on ${ref} is another document (@id $(jq -r ".[$i].verificationResult.statement.predicate[\"@id\"]" "$out"), timestamp $(jq -r ".[$i].verificationResult.statement.predicate.timestamp" "$out"))"
done
if [ "$matches" -lt 1 ]; then
	echo "::error::none of the ${count} OpenVEX attestation(s) on ${ref} is ${expected}" >&2
	exit 1
fi
echo "${ref}: ${matches} of ${count} OpenVEX attestation(s) verified against ${workflow} at ${source_ref} and equal to ${expected}"
