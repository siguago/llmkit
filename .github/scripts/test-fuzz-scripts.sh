#!/usr/bin/env bash

# Regression checks for discovery and its independent source guard. Fixtures
# live outside the checkout; they compile only local, no-op fuzz targets.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
workdir=$(mktemp -d "${TMPDIR:-/tmp}/llmkit-fuzz-scripts.XXXXXX")
trap 'rm -rf "$workdir"' EXIT

fail() {
	printf 'fuzz script regression: %s\n' "$1" >&2
	cat "$workdir/output" >&2
	exit 1
}

# A later discovery failure must not execute the partial list already printed.
# Exercise both go list assignments too: discover runs in command substitution,
# where errexit alone does not propagate a nested command's failure reliably.
mkdir "$workdir/bin"
cat > "$workdir/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == list && "$2" == -m ]]; then
	printf 'example.com/fuzz-fixture\n'
	[[ "$MOCK_GO_FAILURE" != module ]]
elif [[ "$1" == list ]]; then
	printf 'example.com/fuzz-fixture/a\nexample.com/fuzz-fixture/broken\nexample.com/fuzz-fixture/z\n'
	[[ "$MOCK_GO_FAILURE" != packages ]]
elif [[ "$1" == test && "$3" == -list ]]; then
	if [[ "${2##*/}" == broken && "$MOCK_GO_FAILURE" == target ]]; then
		printf 'simulated compiler error\n' >&2
		exit 1
	fi
	printf 'FuzzExample\n'
elif [[ "$1" == test ]]; then
	printf 'FUZZED %s\n' "$2"
else
	exit 90
fi
EOF
chmod +x "$workdir/bin/go"

for failure in module packages target; do
	if PATH="$workdir/bin:$PATH" MOCK_GO_FAILURE="$failure" \
		bash "$repo_root/.github/scripts/run-fuzz.sh" 1x --all > "$workdir/output" 2>&1; then
		fail "$failure discovery failure returned success"
	fi
	if grep -q '^FUZZED ' "$workdir/output"; then
		fail "$failure discovery failure executed a partial package set"
	fi
done

if PATH="$workdir/bin:$PATH" MOCK_GO_FAILURE=target \
	bash "$repo_root/.github/scripts/run-fuzz.sh" 1x ./broken > "$workdir/output" 2>&1; then
	fail 'explicit-package target listing failure returned success'
fi
if grep -q '^FUZZED ' "$workdir/output"; then
	fail 'explicit-package target listing failure executed fuzzing'
fi

PATH="$workdir/bin:$PATH" MOCK_GO_FAILURE=none \
	bash "$repo_root/.github/scripts/run-fuzz.sh" 1x --all > "$workdir/output" 2>&1
if [[ $(grep '^FUZZED ' "$workdir/output") != $'FUZZED ./a\nFUZZED ./broken\nFUZZED ./z' ]]; then
	fail 'successful discovery did not execute the complete package set'
fi

# Use the real Go build selection for the source guard, including both kinds
# of test package and files selected by tags or by their platform suffix.
fixture="$workdir/module"
mkdir -p "$fixture/.github/scripts"
cp "$repo_root/.github/scripts/run-fuzz.sh" \
	"$repo_root/.github/scripts/check-fuzz-targets.sh" "$fixture/.github/scripts/"
cat > "$fixture/go.mod" <<'EOF'
module example.com/fuzz-fixture

go 1.22
EOF

write_fuzz() {
	local file=$1 package=$2 constraint=${3:-}
	mkdir -p "$(dirname "$file")"
	{
		if [[ -n "$constraint" ]]; then
			printf '//go:build %s\n\n' "$constraint"
		fi
		printf 'package %s\n\nimport "testing"\n\n' "$package"
		printf 'func FuzzExample(f *testing.F) { f.Add("seed"); f.Fuzz(func(t *testing.T, value string) {}) }\n'
	} > "$file"
}

write_fuzz "$fixture/fuzz_test.go" root
write_fuzz "$fixture/normal/fuzz_test.go" normal
write_fuzz "$fixture/active/fuzz_test.go" active go1.22
write_fuzz "$fixture/external/fuzz_test.go" external_test
printf 'package external\n' > "$fixture/external/stub.go"
write_fuzz "$fixture/inactive/fuzz_test.go" inactive llmkit_unused_test_tag
printf 'package inactive\n' > "$fixture/inactive/stub.go"

current_os=$(go env GOOS)
other_os=windows
if [[ "$current_os" == windows ]]; then
	other_os=linux
fi
write_fuzz "$fixture/platform/fuzz_${current_os}_test.go" platform
# A second platform-only package must not appear in either discovery or truth.
write_fuzz "$fixture/otherplatform/fuzz_${other_os}_test.go" otherplatform
printf 'package otherplatform\n' > "$fixture/otherplatform/stub.go"

cd "$fixture"
bash .github/scripts/run-fuzz.sh --discover | sort > "$workdir/output"
if [[ $(cat "$workdir/output") != $'.\n./active\n./external\n./normal\n./platform' ]]; then
	fail 'normal discovery differs from the Go-selected fuzz package set'
fi
bash .github/scripts/check-fuzz-targets.sh > "$workdir/output" 2>&1 || fail 'source guard rejected the Go-selected fuzz package set'

printf 'fuzz script regressions passed\n'
