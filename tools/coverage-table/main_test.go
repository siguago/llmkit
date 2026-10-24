package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, file, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func readFixture(t *testing.T, file string) string {
	t.Helper()
	contents, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func sortedPackages(counts map[string]stats) []string {
	packages := make([]string, 0, len(counts))
	for pkg := range counts {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	return packages
}

// newRenderFixture builds a module root whose source tree and statement counts
// satisfy every row in rows(): every adapter with a dedicated row has a _test.go
// file (so thinAdapters does not fold it away), two adapters have none, and the
// counts cover each package a row can claim.
func newRenderFixture(t *testing.T) (string, map[string]stats) {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, ".github", "coverage-go-version"), "1.27.2\n", 0o644)

	counts := map[string]stats{
		"":                     {total: 4, covered: 3},
		"internal/logging":     {total: 10, covered: 10},
		"internal/safehttp":    {total: 14, covered: 7},
		"internal/httpx":       {total: 10, covered: 10},
		"internal/sse":         {total: 10, covered: 9},
		"internal/ipprivacy":   {total: 8, covered: 4},
		"protocol/openaifiles": {total: 10, covered: 3},
		"protocol/openaibatch": {total: 10, covered: 10},
		"protocol/anthropic":   {total: 10, covered: 5},
		"protocol/responses":   {total: 10, covered: 7},
		"provider":             {total: 10, covered: 9},
		// Deliberately outside the 40–100% adapter interval: the spread row must
		// not include the shared compat base.
		"provider/compat":      {total: 10, covered: 0},
		"provider/anthropic":   {total: 10, covered: 10},
		"provider/dashscope":   {total: 10, covered: 4},
		"provider/deepseek":    {total: 10, covered: 9},
		"provider/easyrouter":  {total: 10, covered: 8},
		"provider/gemini":      {total: 10, covered: 6},
		"provider/minimax":     {total: 10, covered: 5},
		"provider/moonshot":    {total: 10, covered: 10},
		"provider/openai":      {total: 10, covered: 7},
		"provider/openrouter":  {total: 10, covered: 5},
		"provider/vercel":      {total: 10, covered: 10},
		"provider/volcengine":  {total: 20, covered: 9},
		"provider/zhipu":       {total: 10, covered: 10},
		"provider/siliconflow": {total: 5, covered: 5},
		"provider/cerebras":    {total: 5, covered: 5},
		"cmd/llmkit-probe":     {total: 10, covered: 6},
		"tools/coverage-table": {total: 40, covered: 20},
		"examples/chat":        {total: 10, covered: 0},
	}

	// Adapters with their own row (and the compat base) carry a test file, so
	// the derived thin set is exactly the two adapters below.
	for _, pkg := range append([]string{"provider/compat"}, adapterRows...) {
		writeFixture(t, filepath.Join(root, filepath.FromSlash(pkg), filepath.Base(pkg)+"_test.go"),
			"package "+filepath.Base(pkg)+"\n", 0o644)
	}
	for _, pkg := range []string{"provider/siliconflow", "provider/cerebras"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(pkg)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, counts
}

func grandTotals(counts map[string]stats) (covered, total int) {
	for _, current := range counts {
		covered += current.covered
		total += current.total
	}
	return covered, total
}

// coverageProfile renders counts as a coverprofile. Each package becomes up to
// two blocks: one fully covered block of `covered` statements and one uncovered
// block of the rest, which readProfile folds back into the same stats.
func coverageProfile(modulePath string, counts map[string]stats) string {
	var out strings.Builder
	out.WriteString("mode: set\n")
	for _, pkg := range sortedPackages(counts) {
		file := modulePath + "/file.go"
		if pkg != "" {
			file = modulePath + "/" + pkg + "/file.go"
		}
		current := counts[pkg]
		if current.covered > 0 {
			fmt.Fprintf(&out, "%s:1.1,2.2 %d 1\n", file, current.covered)
		}
		if current.total-current.covered > 0 {
			fmt.Fprintf(&out, "%s:3.1,4.2 %d 0\n", file, current.total-current.covered)
		}
	}
	return out.String()
}

// TestCoverageScriptPinsBothModes exercises the CI entrypoint itself with a fake
// go command, so no real toolchain is installed or downloaded.
func TestCoverageScriptPinsBothModes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("coverage entrypoint regression requires bash")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", ".github", "scripts", "check-coverage-table.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		write    bool
		failTest bool
	}{
		{name: "check"},
		{name: "write", write: true},
		{name: "failed-tests", write: true, failTest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			entrypoint := filepath.Join(root, ".github", "scripts", "check-coverage-table.sh")
			writeFixture(t, entrypoint, string(script), 0o644)
			// A sentinel version proves the value comes from the shared file,
			// without installing or downloading any real toolchain.
			writeFixture(t, filepath.Join(root, ".github", "coverage-go-version"), "9.8.7\n", 0o644)
			writeFixture(t, filepath.Join(root, "coverage.out"), "existing profile\n", 0o644)
			bin := filepath.Join(root, "bin")
			writeFixture(t, filepath.Join(bin, "go"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s|%s\n' "$GOTOOLCHAIN" "$*" >> "$COVERAGE_TEST_LOG"
if [[ "$1" == test ]]; then
	if [[ ${COVERAGE_TEST_FAIL:-} == 1 ]]; then
		printf 'FAIL simulated go test output\n' >&2
		exit 92
	fi
	for argument in "$@"; do
		if [[ "$argument" == -coverprofile=* ]]; then
			printf 'mode: set\n' > "${argument#-coverprofile=}"
		fi
	done
elif [[ "$1" == run ]]; then
	previous=
	for argument in "$@"; do
		if [[ "$previous" == -profile ]]; then
			[[ -f "$argument" ]] || exit 91
		fi
		previous=$argument
	done
else
	exit 90
fi
`, 0o755)
			arguments := []string{entrypoint}
			if tc.write {
				arguments = append(arguments, "--write")
			}
			logPath := filepath.Join(root, "calls.log")
			command := exec.Command(bash, arguments...)
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GOTOOLCHAIN=local", "COVERAGE_TEST_LOG="+logPath)
			if tc.failTest {
				command.Env = append(command.Env, "COVERAGE_TEST_FAIL=1")
			}
			output, err := command.CombinedOutput()
			if (err != nil) != tc.failTest {
				t.Fatalf("coverage script: %v\n%s", err, output)
			}
			if tc.failTest {
				// C4: a failing go test must not be a bare non-zero exit.
				if !strings.Contains(string(output), "FAIL simulated go test output") {
					t.Fatalf("failed go test output was not replayed for diagnosis:\n%s", output)
				}
			} else if len(strings.TrimSpace(string(output))) != 0 {
				t.Fatalf("a passing run must stay quiet, got:\n%s", output)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			wantCalls := 2
			if tc.failTest {
				wantCalls = 1
			}
			if len(lines) != wantCalls || !strings.HasPrefix(lines[0], "go9.8.7|test ./... -coverpkg=./... -coverprofile=") {
				t.Fatalf("generation and rendering must use the pinned toolchain and basis:\n%s", calls)
			}
			if !tc.failTest {
				if !strings.HasPrefix(lines[1], "go9.8.7|run ./tools/coverage-table -profile ") ||
					strings.HasSuffix(lines[1], " -check") != !tc.write {
					t.Fatalf("wrong generator toolchain or mode: %s", lines[1])
				}
			}
			profile, err := os.ReadFile(filepath.Join(root, "coverage.out"))
			if err != nil {
				t.Fatal(err)
			}
			want := "existing profile\n"
			if tc.write && !tc.failTest {
				want = "mode: set\n"
			}
			if string(profile) != want {
				t.Fatalf("coverage.out = %q, want %q", profile, want)
			}
		})
	}
}

func TestCoverageBannerUsesSharedVersion(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"9.8.7", "8.7.6"} {
		writeFixture(t, filepath.Join(root, ".github", "coverage-go-version"), version+"\n", 0o644)
		banner, err := coverageBanner(root)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(banner, "**Go "+version+"**") || !strings.Contains(banner, "make cover-check") ||
			strings.Contains(banner, "go run ./tools/coverage-table") {
			t.Fatalf("banner does not describe the shared version and entrypoints:\n%s", banner)
		}
	}
	writeFixture(t, filepath.Join(root, ".github", "coverage-go-version"), "1.27\n", 0o644)
	if _, err := coverageBanner(root); err == nil {
		t.Fatal("coverage version must include its patch component")
	}
}

func TestReadCoverageVersion(t *testing.T) {
	root := t.TempDir()
	versionFile := filepath.Join(root, ".github", "coverage-go-version")
	for _, tc := range []struct {
		name    string
		content string
		want    string
		wantErr string
	}{
		{name: "exact", content: "1.27.2\n", want: "1.27.2"},
		{name: "trimmed", content: "  1.27.2  \n", want: "1.27.2"},
		{name: "missing patch", content: "1.27\n", wantErr: "exact Go patch version"},
		{name: "not a version", content: "devel\n", wantErr: "exact Go patch version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFixture(t, versionFile, tc.content, 0o644)
			got, err := readCoverageVersion(root)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("readCoverageVersion() = %q, %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("readCoverageVersion() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := readCoverageVersion(filepath.Join(root, "missing-root")); err == nil {
		t.Fatal("a missing version file must be an error")
	}
}

func TestReadModule(t *testing.T) {
	root := t.TempDir()
	gomod := filepath.Join(root, "go.mod")
	writeFixture(t, gomod, "// a comment\nmodule example.com/fixture\n\ngo 1.22\n", 0o644)
	modulePath, moduleRoot, err := readModule(gomod)
	if err != nil {
		t.Fatal(err)
	}
	if modulePath != "example.com/fixture" || moduleRoot != filepath.Dir(gomod) {
		t.Fatalf("readModule() = %q, %q; want example.com/fixture and %q", modulePath, moduleRoot, filepath.Dir(gomod))
	}

	writeFixture(t, gomod, "// no module line here\n", 0o644)
	if _, _, err := readModule(gomod); err == nil || !strings.Contains(err.Error(), "no module directive") {
		t.Fatalf("want a no-module-directive error, got %v", err)
	}
	if _, _, err := readModule(filepath.Join(root, "missing.mod")); err == nil {
		t.Fatal("a missing go.mod must be an error")
	}
}

func TestPackageOf(t *testing.T) {
	const modulePath = "example.com/fixture"
	for _, tc := range []struct {
		name string
		file string
		want string
	}{
		{name: "root package", file: modulePath + "/llmkit.go", want: ""},
		{name: "one level", file: modulePath + "/provider/anthropic/anthropic.go", want: "provider/anthropic"},
		{name: "two levels", file: modulePath + "/protocol/responses/responses.go", want: "protocol/responses"},
		{name: "module path alone", file: modulePath, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := packageOf(tc.file, modulePath)
			if err != nil || got != tc.want {
				t.Fatalf("packageOf(%q) = %q, %v; want %q", tc.file, got, err, tc.want)
			}
		})
	}
	for _, file := range []string{"other.com/x/x.go", "example.com/fixture2/x.go"} {
		if _, err := packageOf(file, modulePath); err == nil || !strings.Contains(err.Error(), "outside module") {
			t.Fatalf("packageOf(%q) must fail as outside the module, got %v", file, err)
		}
	}
}

func TestParseProfileLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		line    string
		file    string
		pos     string
		numStmt int
		count   int
		wantErr string
	}{
		{
			name: "plain",
			line: "example.com/fixture/a/a.go:10.2,12.3 4 1",
			file: "example.com/fixture/a/a.go", pos: "10.2,12.3", numStmt: 4, count: 1,
		},
		{
			// The file name is split at the last colon, so a filename that
			// itself carries one cannot be mistaken for the position.
			name: "colon in the file name",
			line: "example.com/fixture/odd:name/a.go:1.1,2.2 3 0",
			file: "example.com/fixture/odd:name/a.go", pos: "1.1,2.2", numStmt: 3, count: 0,
		},
		{name: "too few fields", line: "just-one-field", wantErr: "malformed profile line"},
		{name: "too many fields", line: "a.go:1.1,2.2 1 1 extra", wantErr: "malformed profile line"},
		{name: "no position", line: "example.com/fixture/a.go 1 1", wantErr: "carries no position"},
		{name: "bad numstmt", line: "example.com/fixture/a.go:1.1,2.2 nope 1", wantErr: "invalid syntax"},
		{name: "bad count", line: "example.com/fixture/a.go:1.1,2.2 1 nope", wantErr: "invalid syntax"},
		{name: "negative numstmt", line: "example.com/fixture/a.go:1.1,2.2 -1 1", wantErr: "negative"},
		{name: "negative count", line: "example.com/fixture/a.go:1.1,2.2 1 -1", wantErr: "negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, pos, numStmt, count, err := parseProfileLine(tc.line)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseProfileLine(%q) error = %v; want %q", tc.line, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProfileLine(%q): %v", tc.line, err)
			}
			if file != tc.file || pos != tc.pos || numStmt != tc.numStmt || count != tc.count {
				t.Fatalf("parseProfileLine(%q) = %q, %q, %d, %d; want %q, %q, %d, %d",
					tc.line, file, pos, numStmt, count, tc.file, tc.pos, tc.numStmt, tc.count)
			}
		})
	}
}

func TestPercentRoundsHalfUpInIntegers(t *testing.T) {
	for _, tc := range []struct {
		covered, total int
		want           float64
	}{
		{covered: 0, total: 0, want: 0},
		{covered: 5, total: 0, want: 0},
		{covered: 0, total: 100, want: 0},
		{covered: 1, total: 1, want: 100},
		{covered: 1, total: 3, want: 33.3},
		{covered: 2, total: 3, want: 66.7},
		{covered: 1, total: 2, want: 50},
		{covered: 7, total: 8, want: 87.5},
		// Exact .x5 boundaries the float64 path used to lose.
		{covered: 1001, total: 2000, want: 50.1},
		{covered: 1, total: 2000, want: 0.1},
		{covered: 3, total: 2000, want: 0.2},
		{covered: 1997, total: 2000, want: 99.9},
		{covered: 1999, total: 2000, want: 100},
		{covered: 1, total: 2001, want: 0},
	} {
		if got := percent(tc.covered, tc.total); got != tc.want {
			t.Errorf("percent(%d, %d) = %v; want %v", tc.covered, tc.total, got, tc.want)
		}
	}
}

func TestReadProfileMergesCoverPackages(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "coverage.out")
	writeFixture(t, profile, `mode: count

example.com/fixture/root.go:1.1,3.2 2 1
example.com/fixture/a/a.go:1.1,2.2 3 0
example.com/fixture/a/a.go:1.1,2.2 3 7
example.com/fixture/a/b.go:4.1,5.1 5 0
example.com/fixture/b/b.go:1.1,2.1 7 1
`, 0o644)
	counts, err := readProfile(profile, "example.com/fixture")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]stats{
		"":  {total: 2, covered: 2},
		"a": {total: 8, covered: 3},
		"b": {total: 7, covered: 7},
	}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("readProfile() = %#v; want %#v", counts, want)
	}

	// Every supported mode must parse.
	for _, mode := range []string{"set", "count", "atomic"} {
		writeFixture(t, profile, "mode: "+mode+"\nexample.com/fixture/a.go:1.1,2.2 1 1\n", 0o644)
		if _, err := readProfile(profile, "example.com/fixture"); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
}

func TestReadProfileRejectsBadInput(t *testing.T) {
	const modulePath = "example.com/fixture"
	for _, tc := range []struct {
		name     string
		contents string
		wantErr  string
	}{
		{name: "empty file", contents: "", wantErr: "empty profile"},
		{name: "no mode line", contents: "example.com/fixture/a.go:1.1,2.2 1 1\n", wantErr: "want a mode line"},
		{name: "unknown mode", contents: "mode: bogus\nexample.com/fixture/a.go:1.1,2.2 1 1\n", wantErr: "unknown coverage mode"},
		{name: "malformed line", contents: "mode: set\nonly-one-field\n", wantErr: "malformed profile line"},
		{name: "no blocks", contents: "mode: set\n\n\n", wantErr: "no coverage blocks"},
		{
			name:     "outside module",
			contents: "mode: set\nother.com/x/x.go:1.1,2.2 1 1\n",
			wantErr:  "outside module",
		},
		{
			name:     "inconsistent numstmt",
			contents: "mode: set\nexample.com/fixture/a.go:1.1,2.2 3 1\nexample.com/fixture/a.go:1.1,2.2 4 1\n",
			wantErr:  "inconsistent NumStmt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := filepath.Join(t.TempDir(), "coverage.out")
			writeFixture(t, profile, tc.contents, 0o644)
			if _, err := readProfile(profile, modulePath); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("readProfile(%q) = %v; want error containing %q", tc.contents, err, tc.wantErr)
			}
		})
	}

	t.Run("line prefix", func(t *testing.T) {
		profile := filepath.Join(t.TempDir(), "coverage.out")
		writeFixture(t, profile, "mode: set\nmalformed line\n", 0o644)
		if _, err := readProfile(profile, modulePath); err == nil || !strings.Contains(err.Error(), ":2:") {
			t.Fatalf("want the failing line number in the error, got %v", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := readProfile(filepath.Join(t.TempDir(), "absent.out"), modulePath); err == nil {
			t.Fatal("a missing profile must be an error")
		}
	})

	t.Run("line longer than the scanner buffer", func(t *testing.T) {
		profile := filepath.Join(t.TempDir(), "coverage.out")
		writeFixture(t, profile, "mode: set\n"+strings.Repeat("x", 9<<20)+"\n", 0o644)
		if _, err := readProfile(profile, modulePath); err == nil || !strings.Contains(err.Error(), "token too long") {
			t.Fatalf("want the scanner error surfaced, got %v", err)
		}
	})
}

func TestReplaceBlock(t *testing.T) {
	const staleBlock = startMarker + "\nSTALE\n" + endMarker
	const freshBlock = startMarker + "\nfresh table\n" + endMarker
	document := "intro\n\n" + staleBlock + "\n\ntrailer\n"

	updated, err := replaceBlock(document, freshBlock)
	if err != nil {
		t.Fatal(err)
	}
	want := "intro\n\n" + freshBlock + "\n\ntrailer\n"
	if updated != want {
		t.Fatalf("replaceBlock() =\n%q\nwant\n%q", updated, want)
	}

	// Replacing again with the same block is a no-op: the generator may run in
	// either mode any number of times.
	twice, err := replaceBlock(updated, freshBlock)
	if err != nil {
		t.Fatal(err)
	}
	if twice != updated {
		t.Fatalf("replaceBlock is not idempotent:\n%q\n%q", twice, updated)
	}

	// blockOf returns exactly the marker span, which is what diffBlocks diffs.
	got, err := blockOf(document)
	if err != nil {
		t.Fatal(err)
	}
	if got != staleBlock {
		t.Fatalf("blockOf() = %q; want %q", got, staleBlock)
	}
}

func TestReplaceBlockRejectsBrokenMarkerLayout(t *testing.T) {
	const block = startMarker + "\nnew\n" + endMarker
	for _, tc := range []struct {
		name     string
		document string
		wantErr  string
	}{
		{name: "missing start", document: "text\n" + endMarker + "\n", wantErr: "missing " + startMarker},
		{name: "missing end", document: startMarker + "\ntext\n", wantErr: "missing " + endMarker},
		{
			name:     "wrong order",
			document: endMarker + "\n" + startMarker,
			wantErr:  "wrong order",
		},
		{
			name:     "duplicate start marker",
			document: startMarker + "\n" + endMarker + "\n" + startMarker + "\n",
			wantErr:  "found 2 " + startMarker,
		},
		{
			name:     "duplicate end marker",
			document: startMarker + "\n" + endMarker + "\n" + endMarker + "\n",
			wantErr:  "found 2 " + endMarker,
		},
		{
			// C5: the second, stale pair used to be silently ignored, so the
			// table could read two different numbers while both modes passed.
			name:     "two marker pairs",
			document: startMarker + "\nfirst\n" + endMarker + "\n\n" + startMarker + "\nsecond\n" + endMarker,
			wantErr:  "found 2 " + startMarker,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := replaceBlock(tc.document, block); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("replaceBlock() = %v; want error containing %q", err, tc.wantErr)
			}
			if _, err := blockOf(tc.document); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("blockOf() = %v; want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestDiffBlocks(t *testing.T) {
	committed := startMarker + "\n| a | 1.0% |\n| dropped | 9.9% |\n" + endMarker
	want := startMarker + "\n| a | 1.1% |\n" + endMarker
	diff := diffBlocks(committed, want)
	if !strings.Contains(diff, "line  committed -> regenerated") {
		t.Fatalf("diff lacks a header:\n%s", diff)
	}
	if !strings.Contains(diff, "| a | 1.0% | -> | a | 1.1% |") || !strings.Contains(diff, "-> (absent)") {
		t.Fatalf("diff does not point at the changed and removed lines:\n%s", diff)
	}

	// The walk stops after 20 reported lines so a full-table mismatch does not
	// flood the CI log.
	var many strings.Builder
	many.WriteString(startMarker + "\n")
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&many, "line %d\n", i)
	}
	many.WriteString(endMarker)
	manyDiff := diffBlocks(many.String(), startMarker+"\n"+endMarker)
	if !strings.Contains(manyDiff, "(truncated)") {
		t.Fatalf("a large diff must be truncated:\n%s", manyDiff)
	}

	if got := diffBlocks(startMarker+"\nsame\n"+endMarker, startMarker+"\nsame\n"+endMarker); got != "  line  committed -> regenerated" {
		t.Fatalf("identical blocks must report no changed lines:\n%s", got)
	}
	if got := diffBlocks("no markers here", want); !strings.Contains(got, "current block: missing "+startMarker) {
		t.Fatalf("a broken committed block must be reported, got:\n%s", got)
	}
}

func TestRenderLaysOutEveryRow(t *testing.T) {
	root, counts := newRenderFixture(t)
	block, err := render(counts, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(block, startMarker+"\n") || !strings.HasSuffix(block, endMarker) {
		t.Fatalf("rendered block is not wrapped in exactly the markers:\n%s", block)
	}
	if !strings.Contains(block, "使用 **Go 1.27.2**") {
		t.Fatalf("banner does not carry the pinned version:\n%s", block)
	}

	lines := strings.Split(block, "\n")
	var tableLines []string
	for _, line := range lines {
		if strings.HasPrefix(line, "|") {
			tableLines = append(tableLines, line)
		}
	}
	// Two header lines plus one line per row.
	if want := 2 + len(rows(nil)); len(tableLines) != want {
		t.Fatalf("table has %d lines, want %d:\n%s", len(tableLines), want, block)
	}

	for _, want := range []string{
		"| 门面层（根包） | 75.0% |  |",
		"| `internal/logging` | 100.0% |  |",
		"| `protocol/responses` | 70.0% |  |",
		"| `provider` | 90.0% | 跨全部适配器共享的类型、流策略与守卫 |",
		"| `provider/compat` | 0.0% | OpenAI 兼容适配器的公共基座：chat/stream 读取、能力裁剪、rerank 方言 |",
		// The spread row covers adapters only: compat is 0% and must not widen
		// the interval down to 0.0–100.0%.
		"| `provider/*` 适配层（区间） | 40.0–100.0% |",
		"| `provider/dashscope` | 40.0% |  |",
		"| `provider/zhipu` | 100.0% |  |",
		"| `provider/siliconflow` 等 2 个薄封装 | 100.0% |",
		"| `cmd/llmkit-probe` | 60.0% |",
		"| `tools/*`（1 个开发工具） | 50.0% |",
		"| `examples/*`（1 个示例程序） | 0.0% |",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("rendered table is missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "%d") {
		t.Errorf("rendered table contains an unexpanded %%d:\n%s", block)
	}

	covered, total := grandTotals(counts)
	wantTotal := fmt.Sprintf("**总覆盖率 %.1f%%**（%d/%d 条语句）。", percent(covered, total), covered, total)
	if !strings.Contains(block, wantTotal) {
		t.Errorf("rendered total is missing %q:\n%s", wantTotal, block)
	}

	// Labels are the row identity in the README, so they must be unique.
	seen := make(map[string]bool, len(tableLines))
	for _, line := range tableLines[2:] {
		label := strings.SplitN(strings.TrimPrefix(line, "| "), " |", 2)[0]
		if seen[label] {
			t.Errorf("duplicate row label %q", label)
		}
		seen[label] = true
	}
}

func TestRenderClaimsOrRejectsPackages(t *testing.T) {
	t.Run("new adapter with tests must be claimed", func(t *testing.T) {
		root, counts := newRenderFixture(t)
		counts["provider/freshpkg"] = stats{total: 10, covered: 5}
		writeFixture(t, filepath.Join(root, "provider", "freshpkg", "freshpkg_test.go"), "package freshpkg\n", 0o644)
		_, err := render(counts, root)
		if err == nil || !strings.Contains(err.Error(), "provider/freshpkg") || !strings.Contains(err.Error(), "no row claims") {
			t.Fatalf("a new tested adapter must fail the unclaimed check, got %v", err)
		}
	})

	t.Run("new adapter without tests is folded", func(t *testing.T) {
		root, counts := newRenderFixture(t)
		counts["provider/nanopkg"] = stats{total: 10, covered: 10}
		if err := os.MkdirAll(filepath.Join(root, "provider", "nanopkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		block, err := render(counts, root)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(block, "| `provider/siliconflow` 等 3 个薄封装 | 100.0% |") {
			t.Fatalf("the new untested adapter was not folded into the thin row:\n%s", block)
		}
	})

	t.Run("row matching nothing expands its count", func(t *testing.T) {
		root, counts := newRenderFixture(t)
		delete(counts, "tools/coverage-table")
		_, err := render(counts, root)
		if err == nil || !strings.Contains(err.Error(), "`tools/*`（0 个开发工具）") || !strings.Contains(err.Error(), "matches no package") {
			t.Fatalf("want an expanded row label in the error, got %v", err)
		}
		if strings.Contains(err.Error(), "%d") {
			t.Fatalf("error leaked the raw %%d placeholder: %v", err)
		}
	})

	t.Run("specific row matching nothing", func(t *testing.T) {
		root, counts := newRenderFixture(t)
		delete(counts, "cmd/llmkit-probe")
		_, err := render(counts, root)
		if err == nil || !strings.Contains(err.Error(), "`cmd/llmkit-probe`") || !strings.Contains(err.Error(), "matches no package") {
			t.Fatalf("want a no-match error naming the row, got %v", err)
		}
	})

	t.Run("adapter directory cannot be read", func(t *testing.T) {
		root, counts := newRenderFixture(t)
		counts["provider/ghostpkg"] = stats{total: 1, covered: 1}
		_, err := render(counts, root)
		if err == nil || !strings.Contains(err.Error(), "provider/ghostpkg") || !strings.Contains(err.Error(), "reading") {
			t.Fatalf("want a directory error for the missing adapter, got %v", err)
		}
	})
}

func TestValueRendersAggregatesAndSpreads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []string
		counts  map[string]stats
		kind    rowKind
		want    string
	}{
		{
			name:    "aggregate unions its packages",
			members: []string{"a", "b"},
			counts:  map[string]stats{"a": {total: 4, covered: 2}, "b": {total: 2, covered: 1}},
			kind:    aggregate,
			want:    "50.0%",
		},
		{
			name:    "aggregate rounds half up",
			members: []string{"a"},
			counts:  map[string]stats{"a": {total: 2000, covered: 1001}},
			kind:    aggregate,
			want:    "50.1%",
		},
		{
			name:    "aggregate without statements",
			members: []string{"a"},
			counts:  map[string]stats{"a": {total: 0, covered: 0}},
			kind:    aggregate,
			want:    "—",
		},
		{
			name:    "spread reports the interval",
			members: []string{"a", "b", "c"},
			counts:  map[string]stats{"a": {total: 10, covered: 10}, "b": {total: 10, covered: 0}, "c": {total: 10, covered: 5}},
			kind:    spread,
			want:    "0.0–100.0%",
		},
		{
			name:    "spread skips packages without statements",
			members: []string{"a", "b"},
			counts:  map[string]stats{"a": {total: 0, covered: 0}, "b": {total: 10, covered: 5}},
			kind:    spread,
			want:    "50.0–50.0%",
		},
		{
			name:    "spread without statements",
			members: []string{"a"},
			counts:  map[string]stats{"a": {total: 0, covered: 0}},
			kind:    spread,
			want:    "—",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := value(tc.members, tc.counts, tc.kind); got != tc.want {
				t.Fatalf("value() = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestRowsAreWellFormed(t *testing.T) {
	all := rows(nil)
	if len(all) != 29 {
		t.Fatalf("rows() returned %d rows, want 29", len(all))
	}
	labels := make(map[string]bool, len(all))
	spreads := 0
	for _, current := range all {
		if current.match == nil {
			t.Errorf("row %q has no matcher", current.label)
		}
		if labels[current.label] {
			t.Errorf("duplicate row label %q", current.label)
		}
		labels[current.label] = true
		if current.kind == spread {
			spreads++
		}
	}
	if spreads != 1 {
		t.Fatalf("rows() has %d spread rows, want exactly the adapter interval", spreads)
	}
	for _, pkg := range adapterRows {
		if !isAdapter(pkg) {
			t.Errorf("%q is listed as an adapter but isAdapter rejects it", pkg)
		}
		if !hasDedicatedRow(pkg) {
			t.Errorf("%q has no dedicated row", pkg)
		}
	}
	if isAdapter("provider/compat") || !hasDedicatedRow("provider/compat") {
		t.Error("provider/compat is the shared base: it has a dedicated row but is not an adapter")
	}
	for _, pkg := range []string{"provider", "provider/a/b", "internal/logging", "cmd/llmkit-probe", ""} {
		if isAdapter(pkg) {
			t.Errorf("isAdapter(%q) = true; want false", pkg)
		}
	}
}

func TestThinAdaptersDerivedFromTestFiles(t *testing.T) {
	root := t.TempDir()
	for _, pkg := range []string{"provider/compat", "provider/anthropic"} {
		writeFixture(t, filepath.Join(root, filepath.FromSlash(pkg), "x_test.go"), "package "+filepath.Base(pkg)+"\n", 0o644)
	}
	for _, pkg := range []string{"provider/cerebras", "provider/siliconflow"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(pkg)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	thin, err := thinAdapters(root, []string{
		"provider/compat", "provider/anthropic", "provider/cerebras", "provider/siliconflow",
		"internal/logging", // not an adapter: no directory is read for it
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"provider/cerebras": true, "provider/siliconflow": true}
	if !reflect.DeepEqual(thin, want) {
		t.Fatalf("thinAdapters() = %#v; want %#v", thin, want)
	}

	if _, err := thinAdapters(root, []string{"provider/ghost"}); err == nil || !strings.Contains(err.Error(), "reading") {
		t.Fatalf("a missing adapter directory must be an error, got %v", err)
	}
}

func TestPkgDisplay(t *testing.T) {
	if got := pkgDisplay(""); got != "(root package)" {
		t.Fatalf("pkgDisplay(\"\") = %q", got)
	}
	if got := pkgDisplay("provider/anthropic"); got != "provider/anthropic" {
		t.Fatalf("pkgDisplay(named) = %q", got)
	}
}

// testCoverageVersion returns the running Go release in the form the tool's
// pinned-version gate expects, or skips when go test is running on a devel
// toolchain (which can never satisfy `runtime.Version() == "go"+version`).
func testCoverageVersion(t *testing.T) string {
	t.Helper()
	version := strings.TrimPrefix(runtime.Version(), "go")
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		t.Skipf("runtime.Version() = %q is not an exact release; run() cannot pass its toolchain gate", runtime.Version())
	}
	return version
}

// runTool invokes run() with its own flag set so tests do not collide with
// go test's flags or with each other.
func runTool(t *testing.T, args ...string) error {
	t.Helper()
	previousFlags, previousArgs := flag.CommandLine, os.Args
	set := flag.NewFlagSet("coverage-table", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	flag.CommandLine = set
	os.Args = append([]string{"coverage-table"}, args...)
	t.Cleanup(func() { flag.CommandLine, os.Args = previousFlags, previousArgs })
	return run()
}

func newRunFixture(t *testing.T) (root, gomod, profile, readme string, counts map[string]stats, block string) {
	t.Helper()
	version := testCoverageVersion(t)
	root, counts = newRenderFixture(t)
	writeFixture(t, filepath.Join(root, ".github", "coverage-go-version"), version+"\n", 0o644)
	gomod = filepath.Join(root, "go.mod")
	writeFixture(t, gomod, "module example.com/coverage-fixture\n\ngo 1.22\n", 0o644)
	profile = filepath.Join(root, "coverage.out")
	writeFixture(t, profile, coverageProfile("example.com/coverage-fixture", counts), 0o644)
	readme = filepath.Join(root, "README.md")
	writeFixture(t, readme, "before\n\n"+startMarker+"\nSTALE\n"+endMarker+"\n\nafter\n", 0o644)
	block, err := render(counts, root)
	if err != nil {
		t.Fatal(err)
	}
	return root, gomod, profile, readme, counts, block
}

func TestRunRewritesAndChecksTheTable(t *testing.T) {
	_, gomod, profile, readme, _, block := newRunFixture(t)
	args := func(extra ...string) []string {
		return append([]string{"-gomod", gomod, "-profile", profile, "-readme", readme}, extra...)
	}

	// The committed block is stale: --check must fail with a diff and leave the
	// file untouched.
	err := runTool(t, args("-check")...)
	if err == nil || !strings.Contains(err.Error(), "coverage table is stale") ||
		!strings.Contains(err.Error(), "committed -> regenerated") {
		t.Fatalf("want a stale-table error with a diff, got %v", err)
	}
	if got := readFixture(t, readme); got != "before\n\n"+startMarker+"\nSTALE\n"+endMarker+"\n\nafter\n" {
		t.Fatalf("--check modified the README: %q", got)
	}

	// Write mode replaces the block and preserves the surrounding text.
	if err := runTool(t, args()...); err != nil {
		t.Fatal(err)
	}
	updated := readFixture(t, readme)
	if !strings.HasPrefix(updated, "before\n\n") || !strings.HasSuffix(updated, "\n\nafter\n") {
		t.Fatalf("surrounding text was not preserved:\n%q", updated)
	}
	if got, err := blockOf(updated); err != nil || got != block {
		t.Fatalf("generated block does not match render() (err %v):\n%q\nwant\n%q", err, got, block)
	}

	// A second write is a no-op, and --check then passes.
	if err := runTool(t, args()...); err != nil {
		t.Fatal(err)
	}
	if again := readFixture(t, readme); again != updated {
		t.Fatalf("repeated write changed the README:\n%q\n%q", updated, again)
	}
	if err := runTool(t, args("-check")...); err != nil {
		t.Fatalf("--check must accept the freshly written table: %v", err)
	}
}

func TestRunRejectsBrokenMarkerLayout(t *testing.T) {
	_, gomod, profile, readme, _, _ := newRunFixture(t)
	args := func(extra ...string) []string {
		return append([]string{"-gomod", gomod, "-profile", profile, "-readme", readme}, extra...)
	}

	for _, tc := range []struct {
		name     string
		contents string
		wantErr  string
	}{
		{
			name:     "duplicate pair",
			contents: startMarker + "\nfirst\n" + endMarker + "\n\n" + startMarker + "\nsecond\n" + endMarker + "\n",
			wantErr:  "found 2 " + startMarker,
		},
		{name: "missing markers", contents: "no markers here\n", wantErr: "missing " + startMarker},
		{
			name:     "wrong order",
			contents: endMarker + "\n" + startMarker + "\n",
			wantErr:  "wrong order",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFixture(t, readme, tc.contents, 0o644)
			err := runTool(t, args("-check")...)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			if !strings.Contains(err.Error(), readme) {
				t.Fatalf("error must name the README, got %v", err)
			}
			if got := readFixture(t, readme); got != tc.contents {
				t.Fatalf("a rejected layout must not be rewritten, got %q", got)
			}
		})
	}

	t.Run("missing README", func(t *testing.T) {
		err := runTool(t, "-gomod", gomod, "-profile", profile, "-readme", filepath.Join(t.TempDir(), "absent.md"))
		if err == nil {
			t.Fatal("a missing README must be an error")
		}
	})
}

func TestRunRejectsWrongToolchainBeforeReadingProfile(t *testing.T) {
	root := t.TempDir()
	gomod := filepath.Join(root, "go.mod")
	readme := filepath.Join(root, "README.md")
	writeFixture(t, gomod, "module example.com/coverage-fixture\n\ngo 1.22\n", 0o644)
	writeFixture(t, readme, "unchanged README\n", 0o644)
	writeFixture(t, filepath.Join(root, ".github", "coverage-go-version"), "9.8.7\n", 0o644)

	err := runTool(t, "-gomod", gomod, "-readme", readme,
		"-profile", filepath.Join(root, "missing.cover"))
	if err == nil || !strings.Contains(err.Error(), "requires Go 9.8.7") {
		t.Fatalf("want an actionable toolchain mismatch before profile loading, got %v", err)
	}
	if got := readFixture(t, readme); got != "unchanged README\n" {
		t.Fatalf("toolchain mismatch changed README: %q", got)
	}
}
