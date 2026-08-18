#!/usr/bin/env bash

# Compares the exported API of HEAD against the most recent release tag.
#
# The 1.0 promise is that a v1.x release never breaks source compatibility for
# code that uses the exported API (see STABILITY.md). This script is that
# promise made mechanical: once the baseline tag is v1.0.0 or later, an
# incompatible change fails the build. While the baseline is still a 0.x tag
# the same comparison runs and prints its findings, but does not fail — 0.x
# breaks intentionally, and a gate that has to be muted for every planned
# break is a gate nobody trusts by the time it matters.
#
# apidiff runs via `go run` with a pinned version so the tool never enters
# go.mod: this module has zero third-party dependencies and the CI gate that
# enforces that must keep passing.

set -euo pipefail

APIDIFF_VERSION="golang.org/x/exp/cmd/apidiff@v0.0.0-20260813180055-c1d0aacb2297"
MODULE_PATH="github.com/siguago/llmkit"

baseline_tag=${1:-}
if [[ -z "$baseline_tag" ]]; then
	baseline_tag=$(git tag --list 'v[0-9]*' --sort=-version:refname | head -n 1)
fi
if [[ -z "$baseline_tag" ]]; then
	printf 'no release tag found; nothing to compare against\n'
	exit 0
fi
if ! git rev-parse -q --verify "refs/tags/${baseline_tag}" >/dev/null; then
	printf 'baseline tag %s does not exist\n' "$baseline_tag" >&2
	exit 2
fi

# A v1+ baseline means the freeze is in force and any incompatible change is a
# build failure. A 0.x baseline runs the same check for visibility only.
enforcing=false
if [[ "$baseline_tag" =~ ^v([0-9]+)\. ]] && (( ${BASH_REMATCH[1]} >= 1 )); then
	enforcing=true
fi

workdir=$(mktemp -d)
worktree="${workdir}/baseline"
baseline_api="${workdir}/baseline.api"
cleanup() {
	git worktree remove --force "$worktree" >/dev/null 2>&1 || true
	rm -rf "$workdir"
}
trap cleanup EXIT

git worktree add -q --detach "$worktree" "$baseline_tag"

# apidiff resolves a module path against the go.mod in the current directory,
# so the baseline export data must be written from inside the baseline tree.
stderr_file="${workdir}/apidiff.err"
(cd "$worktree" && go run "$APIDIFF_VERSION" -m -w "$baseline_api" "$MODULE_PATH")

printf '== exported API: %s -> HEAD\n' "$baseline_tag"
report=$(go run "$APIDIFF_VERSION" -m "$baseline_api" "$MODULE_PATH" 2>"$stderr_file")
# apidiff announces every internal package it skips, on stderr, once per side.
# Drop that noise but keep anything else it had to say.
grep -v '^Ignoring internal package ' "$stderr_file" >&2 || true
printf '%s\n' "$report"

if ! printf '%s\n' "$report" | grep -q '^Incompatible changes:'; then
	printf '\nno incompatible changes against %s\n' "$baseline_tag"
	exit 0
fi

if [[ "$enforcing" == true ]]; then
	printf '\nincompatible changes against %s break the v1 compatibility promise (STABILITY.md).\n' "$baseline_tag" >&2
	printf 'add the new API instead of changing the existing one, or ship it in a new major module path.\n' >&2
	exit 1
fi

printf '\nincompatible changes against %s reported, not enforced: the baseline predates v1.0.0.\n' "$baseline_tag"
printf 'every one of them must be listed in the CHANGELOG upgrade table for the release that ships it.\n'
