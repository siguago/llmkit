#!/usr/bin/env bash

# Guards the package-level fuzz discovery in run-fuzz.sh.
#
# `run-fuzz.sh --all` is the only thing that decides which packages CI fuzzes,
# in both the per-PR job and the scheduled deep run. Deriving that set is what
# makes a new fuzz target enter CI by existing rather than by someone
# remembering to edit a list — but it also means a broken discovery rule would
# silently shrink the gate instead of failing.
#
# This script re-derives the answer from the source tree and fails if the two
# disagree: every package that declares a top-level `func FuzzXxx` in a
# compiled _test.go file must be reported by `--discover`. The reverse direction
# is checked too, so a stale hard-coded list cannot creep back in.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

discovered=$(bash .github/scripts/run-fuzz.sh --discover | sort -u)

if [[ -z "$discovered" ]]; then
	printf 'fuzz discovery returned nothing; the gate would fuzz no packages\n' >&2
	exit 1
fi

# The independent truth: source declarations in the test files Go selects for
# this build. Go evaluates build tags and platform suffixes; a build constraint
# can include a file as well as exclude it. External test packages count too.
test_files=$(go list -f '{{ $dir := .Dir }}{{ range .TestGoFiles }}{{ printf "%s/%s\n" $dir . }}{{ end }}{{ range .XTestGoFiles }}{{ printf "%s/%s\n" $dir . }}{{ end }}' ./...)
truth=$(
	while IFS= read -r file; do
		[[ -n "$file" ]] || continue
		if grep -q -E '^func Fuzz[A-Za-z0-9_]*\(' "$file"; then
			dir=$(dirname "$file")
			if [[ "$dir" == "$PWD" ]]; then
				printf '.\n'
			else
				printf './%s\n' "${dir#"$PWD"/}"
			fi
		else
			status=$?
			# A missing declaration is expected; a file read error must fail.
			if [[ "$status" -ne 1 ]]; then
				exit "$status"
			fi
		fi
	done <<< "$test_files" | sort -u
)

if [[ "$discovered" != "$truth" ]]; then
	printf 'fuzz discovery is out of sync with the source tree.\n' >&2
	printf '\n-- run-fuzz.sh --discover --\n%s\n' "$discovered" >&2
	printf '\n-- declared in the source tree --\n%s\n' "$truth" >&2
	printf '\ndiff (discovered vs. source):\n' >&2
	diff <(printf '%s\n' "$discovered") <(printf '%s\n' "$truth") >&2 || true
	exit 1
fi

printf 'fuzz discovery in sync: %s packages with fuzz targets\n' \
	"$(printf '%s\n' "$discovered" | wc -l | tr -d ' ')"
