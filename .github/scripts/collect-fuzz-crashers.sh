#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
	printf 'usage: %s DESTINATION\n' "$0" >&2
	exit 2
fi

destination=$1
mkdir -p "$destination"

count=0
while IFS= read -r -d '' path; do
	target="$destination/$path"
	mkdir -p "$(dirname "$target")"
	cp -- "$path" "$target"
	count=$((count + 1))
done < <(git ls-files --others -z -- ':(top,glob)**/testdata/fuzz/**')

printf 'collected %d new fuzz failure file(s)\n' "$count"
