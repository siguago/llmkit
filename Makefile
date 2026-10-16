.PHONY: help test test-race cover cover-check bench lint api-compat fuzz check-fuzz probe probe-all live install clean

# PROVIDER selects which vendor `make probe` targets.
PROVIDER ?= deepseek

# FUZZTIME is the per-target budget for `make fuzz`. Fixed counts, not a
# duration: Go can fail a duration-based -fuzztime spuriously (go.dev/issue/75804).
FUZZTIME ?= 20000x

# BENCHTIME is the per-benchmark budget for `make bench`.
BENCHTIME ?= 1s

help:
	@echo "llmkit"
	@echo
	@echo "  make test                    离线测试（不需要 key，不产生费用）"
	@echo "  make test-race               同上 + race 检测"
	@echo "  make cover                   重新生成覆盖率表（跨包口径，写回 README）"
	@echo "  make cover-check             只校验 README 的覆盖率表是否过期"
	@echo "  make bench                   跑基准测试（基线，不是门禁；见 docs/benchmarks.md）"
	@echo "  make lint                    gofmt + vet + 零依赖校验"
	@echo "  make api-compat              对比最近 release tag 的导出 API（见 STABILITY.md）"
	@echo "  make fuzz                    跑全部 fuzz 目标（FUZZTIME 覆盖单目标预算）"
	@echo "  make check-fuzz              校验 fuzz 包级自动发现与源码一致"
	@echo
	@echo "  make probe PROVIDER=deepseek 用你的 key 实测一家厂商的能力"
	@echo "  make probe-all               实测所有配了 key 的厂商"
	@echo "  make live PROVIDER=deepseek  跑集成测试（go test -tags=integration）"
	@echo
	@echo "  make install                 安装 llmkit-probe 到 GOBIN"
	@echo
	@echo "key 来源：环境变量或 .env 文件。make probe 会自动读 .env。"

test:
	go test ./...

test-race:
	go test ./... -race -count=1

# Generation and verification share the exact toolchain and measurement flags.
# Even Go patch releases can change coverage instrumentation.
cover:
	@bash .github/scripts/check-coverage-table.sh --write
	@echo "详细报告: go tool cover -html=coverage.out"

cover-check:
	@bash .github/scripts/check-coverage-table.sh

# Benchmarks are a recorded baseline, not a gate: no threshold fails CI.
# docs/benchmarks.md holds the last recorded numbers and the machine they
# came from, so a shift here is only interesting if it is explained there.
bench:
	go test ./... -run '^$$' -bench . -benchmem -benchtime=$(BENCHTIME)

lint:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "需要 gofmt:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	@# 零第三方依赖是这个库的硬约束
	@if grep -q '^require' go.mod; then echo "go.mod 出现了第三方依赖"; exit 1; fi
	@echo "lint 通过（含零依赖校验）"

# BASE 覆盖对比基线，默认取最近的 release tag。
api-compat:
	@bash .github/scripts/check-api-compat.sh $(BASE)

# The package list is discovered by the script, so this stays in sync with CI
# without a second hand-maintained list.
fuzz:
	bash .github/scripts/run-fuzz.sh $(FUZZTIME) --all

check-fuzz:
	bash .github/scripts/test-fuzz-scripts.sh
	bash .github/scripts/check-fuzz-targets.sh

probe:
	go run ./cmd/llmkit-probe $(PROVIDER) $(ARGS)

probe-all:
	go run ./cmd/llmkit-probe $(ARGS)

live:
	go test -tags=integration -v -run TestLive . $(ARGS)

install:
	go install ./cmd/llmkit-probe

clean:
	rm -f coverage.out
