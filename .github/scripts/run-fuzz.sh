#!/usr/bin/env bash
#
# Run every fuzz target of the named packages with a fixed execution budget.
#
#   run-fuzz.sh --discover            print the module-relative path of every
#                                     package that declares a fuzz target
#   run-fuzz.sh FUZZTIME PACKAGE...   fuzz the named packages
#   run-fuzz.sh FUZZTIME --all        fuzz everything --discover reports
#
# The package list is discovered rather than hand-maintained: a new fuzz target
# in a new package must enter CI by existing, not by someone remembering to edit
# a second list. `check-fuzz-targets.sh` guards the discovery rule itself.
#
# Use fixed execution counts (e.g. 100000x), never a duration: Go can fail a
# duration-based -fuzztime spuriously (go.dev/issue/75804). See the comments in
# the CI workflows before switching back to durations.

set -euo pipefail

# Assumes the current directory is inside the module.
discover() {
	local module packages package output

	module=$(go list -m) || return

	packages=$(go list ./...) || return

	while IFS= read -r package; do
		# A package that fails to build must fail discovery loudly. Swallowing
		# it here would quietly shrink the gate, which is the exact failure mode
		# this discovery exists to prevent.
		if ! output=$(go test "$package" -list '^Fuzz' 2>&1); then
			printf 'discovery: go test -list failed for %s:\n%s\n' \
				"$package" "$output" >&2
			exit 1
		fi

		if awk '/^Fuzz[[:alnum:]_]+$/ { found = 1 } END { exit !found }' \
			<<< "$output"; then
			if [[ "$package" == "$module" ]]; then
				printf '.\n'
			else
				printf './%s\n' "${package#"$module"/}"
			fi
		fi
	done <<< "$packages"
}

if [[ ${1:-} == '--discover' ]]; then
	discover
	exit 0
fi

if [[ $# -lt 2 ]]; then
	printf 'usage: %s FUZZTIME PACKAGE...\n       %s --discover\n' \
		"$0" "$0" >&2
	exit 2
fi

fuzztime=$1
shift

if [[ ${1:-} == '--all' ]]; then
	# Keep discovery's status: `set -- $(discover)` would return set's success
	# even if discovery failed after printing only part of the package set.
	packages=$(discover)
	# Word splitting is intentional and safe: these are module import paths in
	# ./a/b form, which can never contain whitespace.
	# shellcheck disable=SC2086
	set -- $packages
fi

if [[ $# -eq 0 ]]; then
	printf 'no packages to fuzz; discovery returned nothing\n' >&2
	exit 1
fi

for package in "$@"; do
	targets=$(go test "$package" -list '^Fuzz' | awk '/^Fuzz[[:alnum:]_]+$/')
	if [[ -z "$targets" ]]; then
		printf 'no fuzz targets found in %s\n' "$package" >&2
		exit 1
	fi

	while IFS= read -r target; do
		printf '== %s %s\n' "$package" "$target"
		go test "$package" -run '^$' -fuzz "^${target}$" -fuzztime="$fuzztime"
	done <<< "$targets"
done
