# ADR 0001: 独立提供 OpenAI Responses 与 Anthropic Messages 原生协议面

- 状态：接受
- 日期：2026-08-01
- 关联计划：[`RESPONSES_CLAUDE_NATIVE_SUPPORT_PLAN.md`](../../RESPONSES_CLAUDE_NATIVE_SUPPORT_PLAN.md)

## 背景

llmkit 现有的统一 `Chat` / `ChatStream` 接口以 OpenAI Chat Completions 形状为公共交集。OpenAI Responses 的 item、生命周期和事件模型，以及 Anthropic Messages 的 content block、thinking signature、原生 usage 和事件模型，都无法在不丢语义的前提下先压缩成这套公共 DTO。

同时，`provider.Provider` 是公开且被第三方实现的稳定接口。直接向它增加方法会破坏源码兼容；把原生方法放到通用 compat adapter 上，又会让所有嵌入该 adapter 的 provider 通过方法提升错误地宣称能力。

## 决策

1. 保留现有统一 Chat API 及其 wire 行为；原生协议必须由调用方显式选择，不按模型名自动切换。
2. 在 `protocol/responses` 和 `protocol/anthropic` 提供只依赖标准库的原生 DTO、tagged union、事件、stream contract 与 accumulator。
3. 通过 `provider/native.go` 中按端点拆分的小接口声明可选能力，根 `Client` 用 façade 方法和独立的 `Supports*` 探测暴露它们；基础 `provider.Provider` 不变。
4. 首发 transport 只由官方 OpenAI 与 Anthropic adapter opt in。relay 或其他云 transport 必须逐一验证路径、鉴权、header 和事件语义后再声明支持。
5. 已知 union variant 使用强类型字段并保留未知成员；未知 discriminator 使用原始 JSON fallback。扩展字段不得覆盖已知字段。
6. 两套 SSE 共用真正的 event decoder。只有协议终态事件才代表正常结束；终态前 EOF 是错误，流内 error 事件先交付，再在下一次 `Recv` 返回分类后的终止错误。
7. 创建调用默认只重试可证明未被上游接收的失败；读取与 token count 使用一般重试策略。流建立后不自动重放。

## 范围

本决策覆盖 Responses create/stream/retrieve/delete/cancel/input-items/input-tokens，以及 Anthropic Messages create/stream/count-tokens。

不覆盖入站兼容网关、Responses Conversations/Batch/WebSocket/compact、Anthropic Batches/Files、Bedrock/Vertex transport，也不承诺对每种内置工具事件提供专项类型；这些事件仍必须通过原始 JSON 可见。

## 后果

- 优点：保留厂商语义、第三方 provider 源码兼容、部分 endpoint 能力可准确探测、协议演进时未知数据不被静默丢弃。
- 代价：调用方需要显式导入协议子包，Chat DTO 与两套 native DTO 不能混用；新增 provider 也必须分别实现和测试每项 native capability。
- 运维要求：文档不得宣称完整覆盖两家 API；发布前必须通过 wire、SSE、race、fuzz、Go 1.22、零第三方依赖与 tagged live-suite 编译门禁。
