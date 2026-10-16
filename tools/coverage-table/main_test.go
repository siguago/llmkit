package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
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

func TestRunRejectsWrongToolchainBeforeReadingProfile(t *testing.T) {
	root := t.TempDir()
	gomod := filepath.Join(root, "go.mod")
	readme := filepath.Join(root, "README.md")
	writeFixture(t, gomod, "module example.com/coverage-fixture\n\ngo 1.22\n", 0o644)
	writeFixture(t, readme, "unchanged README\n", 0o644)
	writeFixture(t, filepath.Join(root, ".github", "coverage-go-version"), "9.8.7\n", 0o644)

	// run uses the command's flag set; isolate it from go test's own flags.
	previousFlags, previousArgs := flag.CommandLine, os.Args
	flag.CommandLine = flag.NewFlagSet("coverage-table", flag.ContinueOnError)
	os.Args = []string{"coverage-table", "-gomod", gomod, "-readme", readme,
		"-profile", filepath.Join(root, "missing.cover")}
	t.Cleanup(func() { flag.CommandLine, os.Args = previousFlags, previousArgs })

	if err := run(); err == nil || !strings.Contains(err.Error(), "requires Go 9.8.7") {
		t.Fatalf("want an actionable toolchain mismatch before profile loading, got %v", err)
	}
	contents, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "unchanged README\n" {
		t.Fatalf("toolchain mismatch changed README: %q", contents)
	}
}
