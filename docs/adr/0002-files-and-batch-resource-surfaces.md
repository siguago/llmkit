# ADR 0002: Files 与 Batch 资源面的包布局、保真契约与范围

- 状态：接受
- 日期：2026-08-18
- 关联计划：[`V1_RELEASE_PLAN.md`](../../V1_RELEASE_PLAN.md)

## 背景

v1.0 前补上两块 GA 且被下游最常要求的资源面：OpenAI Files（Batch 的前置依赖，也服务 Responses 的 `file_id` 引用）与 Batch（OpenAI Batch + Anthropic Message Batches）。它们与 ADR 0001 的原生协议面同属"显式选择、不进统一 Chat DTO"的能力，但对象性质不同：File / Batch / MessageBatch 是**上游产生、SDK 只读**的资源对象，SDK 从不把它们重新编码发回上游（上传走 multipart，创建走独立请求 DTO）。

Anthropic Files API 于核验日（2026-08-18）仍标注 Status: Beta（`anthropic-beta: files-api-2025-04-14`）。

## 决策

1. **包布局**：OpenAI 侧新增叶子包 `protocol/openaifiles` 与 `protocol/openaibatch`（仅标准库）；Anthropic 侧的 batches 类型放进既有 `protocol/anthropic`——batch 结果行内嵌完整 `MessageResponse`，同包复用避免循环依赖和第二份 schema。
2. **保真契约刻意弱于 ADR 0001**：这批 DTO 承诺"解码不丢已建模字段 + `RawJSON()` 保留原始字节可查 + 未知枚举值透传"，不承诺 decode→encode 逐字节往返——往返保真是为"会被回传上游的对象"设计的,资源对象没有这条路径。顶层 JSON `null` 一律拒绝,不产出零值伪成功。
3. **能力接口继续按端点拆分**（`FileUploader` / `FileLister` / … / `BatchCreator` / …），不进 compat 基类；首发只有官方 OpenAI / Anthropic adapter opt in。其他 OpenAI 形状的厂商（智谱、Moonshot 等）逐家验证 wire 后在 1.x 接入。
4. **上传永不自动重试**：`io.Reader` 一次性，与 `EditImage` 同规则。下载与 batch results 返回活体流，只重试握手，body 生命周期归调用 context 管（transport 用无绝对超时的 stream client）。
5. **batch 输入/输出 JSONL 的 `body` 用 `json.RawMessage`**：调用方用自己的 DTO 组请求体,batch 包不复述八个端点的请求 schema。JSONL 读取器恒严格（坏行即错，不接 `WithStreamTolerance`——结果文件是完整工件，坏行是数据损坏不是网络抖动），行上限默认 32 MiB 可覆盖。
6. **Anthropic batch results 按配置 base URL 拼路径**，不跟随响应体里的 `results_url`——上游控回的绝对地址经中转站时等于让响应体决定出站目标，与 safehttp 的 SSRF 立场相反；`results_url` 只当"结果已可用"的信号读。
7. **Anthropic Files 不进 1.0**：beta 上游不进冻结面。待上游 GA 后在 1.x 以纯加法接入。

## 范围

本决策覆盖 OpenAI Files upload/list/retrieve/delete/content、OpenAI Batch create/retrieve/list/cancel 及输入/输出 JSONL、Anthropic Message Batches create/retrieve/list/cancel/delete/results。

不覆盖 OpenAI vector stores / assistants / fine-tuning / 分片 Uploads、batch webhook 事件、Anthropic Files。

## 后果

- 调用方获得完整的异步批处理链路（半价批量推理），且探测接口如实回答每个端点。
- 资源 DTO 的弱保真契约写进包文档；将来若出现"回传资源对象"的端点，需要升级该对象的契约而不是默认已有。
- Anthropic Files 的缺口继续挂在 README，理由从"未接"改为"上游 beta，1.x 待 GA"。
