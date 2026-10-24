#!/usr/bin/env bash

# Generates or checks the coverage table README publishes.
#
# That table is generated, not written: tools/coverage-table renders it from a
# coverprofile and refuses to emit one that leaves a package unclaimed. That
# catches a table which *omits* something, but not a table that is simply stale —
# a coverage change nobody regenerated looks perfectly well-formed.
#
# Both modes derive a fresh profile under one exact Go version: even patch
# releases can change coverage instrumentation. CI setup and the generated
# table read that same version file. --write also preserves coverage.out for
# `go tool cover -html=coverage.out`; the default only checks the README.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

mode=${1:---check}
if [[ $# -gt 1 || ( "$mode" != --check && "$mode" != --write ) ]]; then
	printf 'usage: %s [--check|--write]\n' "$0" >&2
	exit 2
fi

version=$(cat .github/coverage-go-version)
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	printf '.github/coverage-go-version must contain an exact Go patch version\n' >&2
	exit 2
fi
# Override an inherited GOTOOLCHAIN=local (including setup-go's default), so
# local generation and CI verification always use this exact measurement tool.
export GOTOOLCHAIN="go$version"

profile=$(mktemp "${TMPDIR:-/tmp}/llmkit-cover.XXXXXX")
test_log=$(mktemp "${TMPDIR:-/tmp}/llmkit-cover-test.XXXXXX")
trap 'rm -f "$profile" "$test_log"' EXIT

# Cross-package: "did any test execute this statement", not "did this package's
# own tests". The two bases are not interchangeable, and only this one is used.
#
# The test output is kept instead of discarded: when the suite fails this script
# used to exit non-zero with nothing but "job failed" in CI. Replay the log on
# failure only, so a passing run stays quiet.
if ! go test ./... -coverpkg=./... -coverprofile="$profile" >"$test_log" 2>&1; then
	printf 'go test failed; replaying its output:\n' >&2
	cat "$test_log" >&2
	exit 1
fi
rm -f "$test_log"

if [[ "$mode" == --write ]]; then
	cp "$profile" coverage.out
	set --
else
	set -- -check
fi
go run ./tools/coverage-table -profile "$profile" "$@"
