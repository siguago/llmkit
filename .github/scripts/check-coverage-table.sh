#!/usr/bin/env bash

# Guards the coverage table README publishes.
#
# That table is generated, not written: tools/coverage-table renders it from a
# coverprofile and refuses to emit one that leaves a package unclaimed. That
# catches a table which *omits* something, but not a table that is simply stale —
# a coverage change nobody regenerated looks perfectly well-formed.
#
# So this script derives a fresh profile under the same basis the table
# documents and asks the generator to verify the committed table against it.
# The basis lives in exactly one place (the generator's package comment); both
# the table and this guard call that one command, so they cannot drift apart.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

profile=$(mktemp "${TMPDIR:-/tmp}/llmkit-cover.XXXXXX")
trap 'rm -f "$profile"' EXIT

# Cross-package: "did any test execute this statement", not "did this package's
# own tests". The two bases are not interchangeable, and only this one is used.
go test ./... -coverpkg=./... -coverprofile="$profile" >/dev/null

go run ./tools/coverage-table -profile "$profile" -check
