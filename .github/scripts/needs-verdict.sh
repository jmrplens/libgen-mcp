#!/usr/bin/env bash
#
# needs-verdict.sh decides one verdict from the results of every job that fed it.
#
# Branch protection can then name a single required check. Without that, the
# ruleset has to list every job by name, and a job added later is not a gate
# until somebody remembers to add it there too — which is a protection that
# quietly weakens every time the pipeline grows.
#
# It reads the `needs` context as JSON on stdin, which the workflow passes with
# `toJSON(needs)`, and takes the event name as its only argument.
#
# A job counts as passing only when its result is exactly "success". That
# includes refusing "skipped": a skipped job reports no failure, and treating
# that as a pass would hand a green tick to a pipeline that did not run.
set -euo pipefail

event="${1:-}"
if [ -z "$event" ]; then
	echo "usage: needs-verdict.sh <event-name> < needs.json" >&2
	exit 2
fi

# ALLOWED_SKIPS pairs a job with the event on which it may legitimately not run.
# The event is part of the entry on purpose: a job that is meant to skip on a
# push to main must still be required on a pull request, and an allowance
# written as a bare job name would excuse it everywhere.
#
# Each entry needs a reason here; an unexplained one is how the "skipped is
# fine" hole gets reopened a job at a time.
#
#   docker:push — the image build runs on pull requests only.
ALLOWED_SKIPS="docker:push"

results="$(cat)"

failed=""
skipped=""
allowed=""

while IFS=$'\t' read -r job result; do
	[ -n "$job" ] || continue
	case "$result" in
	success) ;;
	skipped)
		if printf '%s\n' $ALLOWED_SKIPS | grep -qx "$job:$event"; then
			allowed="$allowed $job"
		else
			skipped="$skipped $job"
		fi
		;;
	*)
		failed="$failed $job($result)"
		;;
	esac
done < <(printf '%s' "$results" | jq -r 'to_entries[] | "\(.key)\t\(.value.result)"')

if [ -n "$allowed" ]; then
	echo "Skipped by design:$allowed"
fi

if [ -z "$failed" ] && [ -z "$skipped" ]; then
	echo "All required jobs succeeded."
	exit 0
fi

if [ -n "$failed" ]; then
	echo "Failed:$failed" >&2
fi
if [ -n "$skipped" ]; then
	echo "Did not run:$skipped" >&2
	echo "A job that did not run is not a job that passed." >&2
fi
exit 1
