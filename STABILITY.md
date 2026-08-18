# 稳定性政策

自 v1.0.0 起，本项目遵循[语义化版本](https://semver.org/lang/zh-CN/)。这份文档说明**什么被冻结、什么没有、以及"破坏"的确切含义** —— 承诺写得越具体，越经得起检验。

## 冻结的是什么

**全部非 internal 包的导出 API**：根包 `llmkit`、`provider`、`provider/*`、`protocol/*`、`cmd/*`。`internal/` 下的一切不在此列，那是 Go 语言本身就不让你 import 的。

冻结的不只是签名，还包括**文档化的行为**：

- `Supports*` 的语义 —— 返回 `true` 就是这条端点真的可调用，不是"接口实现了但会返回 `ErrUnsupported`"。
- 重试语义 —— 哪些操作会自动重试、哪些永不重试（见 README「重试」一节），是契约不是实现细节。破坏它会让调用方重复计费。
- 错误分类 —— `IsAuthError` / `IsRateLimited` / `IsSafeToReplay` 等对给定上游响应的判定。
- wire 行为 —— 同一份请求结构发出的 HTTP 请求形状。上游要求变化导致的必要跟进属于修复，会在 CHANGELOG 说明。

## "破坏"的确切边界

### 算破坏（只在下一个大版本发生）

- 删除或重命名导出的标识符
- 改变函数签名、接口方法集、导出字段的类型
- 让原本成功的合法调用开始报错
- 改变上面列出的任何文档化行为

### 不算破坏

- **给导出 struct 增加字段。** 这是本项目最重要的一条例外，因为它咬过人：v0.6.0 给 `provider.Usage` 加缓存字段时，**不带字段名**的 composite literal（`provider.Usage{a, b, c}`）编译失败了。位置式 literal 不在兼容承诺内 —— 请始终带字段名构造本项目的 struct。Go 官方[兼容性文档](https://go.dev/doc/go1compat)也是这个立场。
- **给上游响应 DTO 增加字段。** 用 `DisallowUnknownFields` 或固定 JSON Schema 解码本项目类型的代码需要自己容忍新增字段。
- **增加新的导出标识符、新的可选能力接口、新的 provider。**
- **给枚举增加常量。** 本项目的枚举（`purpose`、batch status、result type…）一律是开放集合，不做本地封闭校验 —— 上游随时会加值，本地白名单只会挡掉合法的新值。
- **修 bug。** 让本就错误的行为变正确算修复。如果修复会明显改变多数调用方看到的结果，CHANGELOG 会单列并说明。

## 依赖与工具链

- **零第三方依赖是硬约束**，由 CI 强制（`go list -m all` 只能有主模块，且不能出现 `go.sum`）。1.x 期间不会引入依赖。
- **Go 最低版本可以在 minor 版本抬升**，提前一个版本在 CHANGELOG 预告。当前下限见 `go.mod`，由一个专门的 CI job 用 `GOTOOLCHAIN=local` 守着。

## 上游 beta 不进冻结面

**只接入上游已 GA 的 API。** 冻结一个上游自己都没冻结的面，等于把别人的破坏性变更变成我们的。

具体例子：Anthropic 的 Files API 至今需要 `anthropic-beta: files-api-2025-04-14`，因此不在 1.0 内；待其 GA 后以纯加法接入。作为对照，Anthropic Message Batches 已 GA，所以 v0.9.0 接了。

用 `anthropicapi.WithBetas(...)` 给单次调用传 beta header 是**调用方的**显式选择，不构成本项目对该 beta 面的支持承诺。

## 废弃流程

1. minor 版本标记 `Deprecated:`，同时提供替代品，CHANGELOG 说明原因。
2. 至少保留到下一个大版本，在大版本删除。

1.0.0 不带任何已废弃项出生：v0.7.0 清空了 v0.2.0 以来的全部废弃标记。

## 这些承诺怎么被验证

政策靠人记就会漂移，所以每条都有对应的机械守卫：

| 承诺 | 守卫 |
|---|---|
| 导出 API 不出现不兼容变更 | CI `api-compat` job：`apidiff` 对比最近的 release tag，v1 基线下不兼容即失败（[check-api-compat.sh](.github/scripts/check-api-compat.sh)） |
| `Supports*` 与实际方法集一致 | `TestCapabilityMatrix`、`TestNativeProtocolCapabilityMatrix` |
| 重试语义 | 各操作的计数 server 测试（打了几次就是几次） |
| 零依赖 | CI「Verify zero dependencies」 |
| Go 最低版本 | CI `min-go-version` job（`GOTOOLCHAIN=local`） |
| 解析器不被畸形上游数据击穿 | 24 个 fuzz 目标，每 PR 各 10 万次执行，另有每日深度任务 |

`apidiff` 通过 `go run` 以钉死的版本执行，不进 `go.mod`，因此零依赖门禁不受影响。

## 报告问题

发现本文档的承诺与实际行为不符，请提 issue —— 那是 bug，不管是代码错了还是文档错了。
