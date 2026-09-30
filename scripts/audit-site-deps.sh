#!/usr/bin/env bash
# Audit the docs site's dependencies for advisories rated high or critical, and
# tell a registry that did not answer apart from a tree that has a finding.
#
# pnpm exits non-zero for both, and a red check that means "the npm advisory
# endpoint was down" reads exactly like one that means "a dependency has a high
# advisory". A gate that fails for the registry rather than for the tree is one
# people learn to click past, so the two are told apart by what pnpm printed:
# an unanswered run is annotated and passes, and everything else pnpm refuses
# fails.
#
# Advisories already judged live in site/pnpm-workspace.yaml under
# auditConfig.ignoreGhsas, each with its reason beside it. That is the only
# place pnpm reads them from.
#
# PNPM names the pnpm command, and SITE_DIR the directory to audit. Both exist
# so the classification can be driven against a stand-in pnpm by the test
# beside this script.

set -uo pipefail

PNPM="${PNPM:-pnpm}"
SITE_DIR="${SITE_DIR:-$(cd "$(dirname "$0")/../site" && pwd)}"

output=$(cd "$SITE_DIR" && "$PNPM" audit --audit-level=high 2>&1)
status=$?
printf '%s\n' "$output"

if [[ "$status" -eq 0 ]]; then
	exit 0
fi

# A report is a verdict, whatever else the output says. pnpm prints this line
# only after it has an answer from the advisory endpoint, so it is checked
# first: nothing below may excuse a run that found something.
if grep -qE '[0-9]+ vulnerabilit(y|ies) found' <<< "$output"; then
	echo "FAIL: pnpm audit reports an advisory rated high or higher in site/." >&2
	echo "Update the dependency, or record why it does not apply under auditConfig.ignoreGhsas in site/pnpm-workspace.yaml." >&2
	exit "$status"
fi

# pnpm 12 draws its error as a box and wraps it at the terminal's width, so a
# phrase can be split across two lines with the frame between its words. The
# frame is taken out and the text joined before it is read.
flat=$(printf '%s' "$output" |
	sed -e 's/│//g' -e 's/├─▶//g' -e 's/╰─▶//g' -e 's/×//g' |
	tr '\n' ' ' | tr -s ' ')

# The endpoint not answering. pnpm 12 reports every such case as
# ERR_PNPM_AUDIT_BAD_RESPONSE, and says either that the request failed (DNS,
# refused, timed out, reset) or which status the endpoint answered with. Only a
# 5xx or a 429 is the registry's problem: a 4xx is a request this tree made
# wrongly, and that is a finding. The older names are pnpm 10's and 11's
# spelling of the same failures, kept so a pnpm downgrade does not turn an npm
# outage back into a red check.
unanswered='Failed to request the audit endpoint|responded with (5[0-9][0-9]|429)|TimeoutError|operation was aborted|ENOTFOUND|EAI_AGAIN|ECONNRESET|ECONNREFUSED|socket hang up'
if grep -qE "$unanswered" <<< "$flat"; then
	message="the npm advisory endpoint did not answer, so this run says nothing about the site's dependencies"
	if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
		echo "::warning title=Dependency audit did not run::$message"
	else
		echo "WARNING: $message." >&2
	fi
	exit 0
fi

echo "FAIL: pnpm audit exited $status without a report or a recognisable registry failure." >&2
exit "$status"
