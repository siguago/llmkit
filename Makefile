.PHONY: help test test-race cover lint api-compat probe probe-all live install clean

# PROVIDER selects which vendor `make probe` targets.
PROVIDER ?= deepseek

help:
	@echo "llmkit"
	@echo
	@echo "  make test                    离线测试（不需要 key，不产生费用）"
	@echo "  make test-race               同上 + race 检测"
	@echo "  make cover                   覆盖率报告"
	@echo "  make lint                    gofmt + vet + 零依赖校验"
	@echo "  make api-compat              对比最近 release tag 的导出 API（见 STABILITY.md）"
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

cover:
	@go test ./... -coverprofile=coverage.out >/dev/null
	@go tool cover -func=coverage.out | tail -1
	@echo "详细报告: go tool cover -html=coverage.out"

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
