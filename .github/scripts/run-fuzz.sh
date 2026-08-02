#!/usr/bin/env bash

set -euo pipefail

if [[ $# -lt 2 ]]; then
	printf 'usage: %s FUZZTIME PACKAGE...\n' "$0" >&2
	exit 2
fi

fuzztime=$1
shift

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
