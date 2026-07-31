# OpenAI Responses 与 Anthropic Messages 原生协议支持计划

状态：已自审修订，计划作为下一次 minor release 的实施与验收基线。

核验日期：2026-08-01。

代码基线：`main@e5213e41ae3ee9954e17347c5df0bdf3459db662`。制定计划时主工作区存在 13 个未提交文件，其中包含 `provider/types.go`、`types.go` 和 Anthropic adapter；这些改动不属于本计划，实施必须从上述干净提交创建独立 branch/worktree，不得 stash、reset、提交或夹带现有改动。

## 1. 目标与准确表述

本次交付增加两套显式选择、不会先压缩为 Chat Completions 的原生出站 SDK 协议面：

1. OpenAI Responses 核心资源、状态生命周期与 SSE。
2. Anthropic Messages create/stream 核心契约，以及官方 token-count endpoint。

现有 `Chat`、`ChatStream` 和全部第三方 `provider.Provider` 保持源码兼容，不自动改走 Responses，也不改变既有默认 wire 行为。

发布文档必须使用以下准确措辞：

> 支持 OpenAI Responses 核心资源、状态生命周期与 SSE，以及 Anthropic Messages create/stream 和 token count。Batch、Conversations、WebSocket、Responses compact、云厂商 Claude transport 与部分内置工具专项类型另行提供；未知 item/block/event 可通过 Raw 形态无损保留。

不得宣称“完整支持 OpenAI API”或“完整支持 Anthropic API”。

仓库是出站 Go SDK，不是 HTTP gateway。本次不增加接收入站 `/v1/responses`、`/v1/messages` 的 handler/router。

## 2. 自审后修正的设计缺口

上一版计划方向正确，但存在以下不够周到之处，本版已修正：

- 将核心协议与 Batch、Files、WebSocket、Conversations、Bedrock/Vertex 等独立产品面混在同一完成定义中，导致范围不可收敛。本版冻结首发范围并列出明确延期项。
- 可选 provider 能力接口拆分不足。create/stream、Responses 生命周期和 token count 必须分别声明，避免只支持部分端点的 relay 虚报完整能力。
- 未明确 SSE 在终态前断流的行为。首版要求 Responses 必须看到 completed/failed/incomplete/error，Claude 必须看到 message_stop/error；否则返回明确的 unexpected-EOF/transport error。
- 未把 dirty tree 隔离写成发布阻断。本次开发必须在独立 worktree，从记录的 clean base SHA 开始。
- 未把 Go 1.22 和零第三方依赖落实为可防假绿的 CI 门禁。本版增加 `GOTOOLCHAIN=local`、module-list、integration-tag compile 等检查。
- 未充分区分 HTTP error、Responses `status=failed`、`status=incomplete`、流内 `error` 和用户取消。这五类结果必须保持不同语义。
- 未明确 known typed + unknown Raw 的 round-trip 契约。首版所有 union/event 都必须保留 discriminator 与原始 JSON，未知类型不得跳过。
- 未明确创建型请求的重放风险。Responses create 与 Claude create 默认使用 replay-safe-only retry；流式只重试收到首个 event 前的握手。
- 未明确 Responses 默认存储行为。SDK 不擅自覆写上游 `store` 默认值；示例和 live tests 默认显式 `store:false`，并提示数据保留。
- 未明确 protocol package 的依赖方向。协议 DTO 必须是只依赖标准库的叶子包，避免 provider/compat/openai 循环依赖。

## 3. 首次发布范围

### 3.1 OpenAI Responses

公开操作：

- `POST /v1/responses`：同步 create。
- `POST /v1/responses` + `stream:true`：SSE create。
- `GET /v1/responses/{id}`：retrieve，支持 include 查询。
- `DELETE /v1/responses/{id}`：delete。
- `POST /v1/responses/{id}/cancel`：cancel。
- `GET /v1/responses/{id}/input_items`：分页 list。
- `POST /v1/responses/input_tokens`：官方 input token count。

请求 P0 字段：

- model、instructions、input string/items。
- include、metadata、store、background、service tier。
- previous_response_id、max_output_tokens、max_tool_calls。
- tools、tool_choice、parallel_tool_calls。
- reasoning、text.format、temperature、top_p、truncation。
- prompt/cache、安全标识和 stream options 使用 typed common + Raw extension。

响应 P0：

- 完整 response id/object/timestamps/model/status。
- output 的 message、reasoning、function_call、function_call_output。
- output text、refusal、图片/文件引用、annotations。
- error、incomplete_details、usage details、previous_response_id。
- built-in tool output 保留公共字段；未专项建模的内容保留 Raw。

SSE typed core：

- 生命周期：created、queued、in_progress、completed、failed、incomplete、error。
- 边界：output_item added/done、content_part added/done。
- 文本/拒绝：output_text delta/done、refusal delta/done。
- function arguments delta/done。
- 每个事件保留 `sequence_number`、`output_index`、`content_index`、`item_id`、Raw。
- reasoning、search、MCP、code interpreter、image/audio/custom tool 专项事件在首版可仅 Raw 返回，但不能跳过。

background 首版允许使用，因为 retrieve/cancel 同期实现。提供轮询 helper，但不隐藏状态迁移或失败响应。

### 3.2 Anthropic Messages

公开操作：

- `POST /v1/messages`：同步 create。
- `POST /v1/messages` + `stream:true`：SSE create。
- `POST /v1/messages/count_tokens`：官方 token count。

请求/响应 P0：

- model、max_tokens、messages、顶层 system。
- text、image、document、tool_use、tool_result（含 `is_error`）。
- thinking、redacted_thinking、每个 signature/data 原样保留。
- tools、tool choice、并行工具语义。
- stop sequences、service tier、metadata、container、cache control。
- output_config、adaptive/manual/disabled thinking。
- 完整 native usage、stop_reason、stop_sequence、stop_details。
- 未知 block、enum、字段使用 Raw fallback。

SSE typed core：

- message_start、content_block_start/delta/stop、message_delta、message_stop、ping、error。
- text、partial input JSON、thinking、signature、citation delta。
- 工具 partial JSON 只在 block 完成后解析；仍保留原始片段。
- 多个并行 block 按 index 聚合。

`anthropic-version` 默认 `2023-06-01`，同时提供专用配置覆盖；`anthropic-beta` 允许显式字符串列表，不能用封闭枚举阻挡未来 beta。认证仍由 provider 管理，不允许 ExtraFields 覆盖 URL、Authorization 或认证 header。

首版只保证 direct Anthropic transport。Bedrock、Vertex 与任意 Claude-compatible relay 的鉴权/路径差异不在本次声明内。

### 3.3 明确延期

- OpenAI Conversations API、Batch API、Responses WebSocket、`/responses/compact`。
- 全部 Responses 内置工具事件的专项强类型；首版 Raw 保真。
- Anthropic Message Batches、Files API、云厂商 Claude transport。
- 入站 OpenAI/Claude-compatible HTTP gateway。
- 自动跨供应商路由、按模型名自动选择 Chat/Responses。
- 通用的 Responses ↔ Claude 全自动转换。首版只提供原生协议 helper 与现有 Chat compatibility view。

## 4. 包和公开 API 设计

建议目录：

```text
protocol/responses/       # 仅标准库：DTO、union、event、stream、accumulator
protocol/anthropic/       # 仅标准库：DTO、block、event、stream、accumulator
internal/sse/             # 仅标准库：真正的 SSE event decoder
provider/native.go        # 可选能力接口，不修改 provider.Provider
provider/openai/responses.go
provider/anthropic/messages.go
responses.go              # 根 Client façade
anthropic_messages.go     # 根 Client façade
```

基础 `provider.Provider` 保持不变。新增小接口：

```text
ResponsesCreator
ResponsesRetriever
ResponsesCanceller
ResponsesDeleter
ResponsesInputItemLister
ResponsesTokenCounter
AnthropicMessagesCreator
AnthropicTokenCounter
```

根 Client 方法固定为：

```text
SupportsResponses
CreateResponse / CreateResponseStream
RetrieveResponse / CancelResponse / DeleteResponse
ListResponseInputItems / CountResponseInputTokens
SupportsAnthropicMessages
CreateAnthropicMessage / CreateAnthropicMessageStream
CountAnthropicMessageTokens
```

根包不 mass-alias 所有协议子类型；方法直接使用 `protocol/responses` 和 `protocol/anthropic` 类型。避免继续污染旧 `types.go` 和 `provider.ChatCompletionRequest`。

不得把 Responses 方法加到 `compat.Provider`，否则所有嵌入它的 adapter 会通过 method promotion 虚报能力。首版仅 `provider/openai.Provider` opt-in；其他 provider 逐家验证后再接入。

## 5. Wire 保真和前向兼容

- Native DTO 是 wire source of truth；Chat DTO 只是 compatibility view。
- union 使用 `Type string` + typed known payload + `json.RawMessage` unknown fallback。
- 未知 top-level/nested 字段必须保存，避免未来 API 增字段后往返丢失。
- request 的 ExtraFields 与已知字段冲突时返回错误，不允许 silent override。
- 不通过 `map[string]any` 中转需要保真的 JSON number，避免大整数精度损失。
- 可选 false/0/null/absent 使用指针或显式 optional type区分。
- helper 可以提取 output text、tool calls、normalized usage，但不得改变原生对象。
- stream accumulator 的终态对象与同等非流式 fixture 必须语义等价。

## 6. 错误、状态与重试

- 不修改既有 `provider.ProviderError` struct shape；通过包装和 `provider.WithErrorMetadata` 暴露 provider code/category。
- HTTP 4xx/5xx 返回 error；Responses completed/failed/incomplete 仍返回 native Response，由 caller 查看 status。便利 helper 可将 failed/incomplete 显式转换为带完整 Response 的 protocol error。
- Claude HTTP 200 + refusal 是成功 Message，不伪装成 HTTP error。
- SSE error 事件先返回给调用者；下一次 `Recv` 再返回终止 error/EOF。
- Responses 必须见 completed/failed/incomplete/error；Claude 必须见 message_stop/error。终态前连接断开返回 unexpected EOF。
- create 默认使用现有 retry 配置的 replay-safe-only 子集。只有能够证明上游未接收工作的错误才自动重放。
- stream 只允许首事件前的握手重试；首事件交付后不重放。
- 尊重 Retry-After 和 context cancellation，保证 Close 能解除阻塞。

## 7. 实施顺序

### PR 1：协议基础和发布金丝雀

- 落档本计划与 ADR。
- 为现有 OpenAI/Anthropic Chat wire 建 golden regression fixtures。
- 实现 `internal/sse`、frame limit、strict/tolerant diagnostics。
- 实现两套 protocol union/raw/stream/accumulator 基础。
- 增加外部自定义 Provider compile fixture。

### PR 2：OpenAI Responses

- optional interfaces、根 Client façade。
- create/stream/retrieve/delete/cancel/input-items/token-count。
- 生命周期、函数调用、结构化输出、状态延续、Raw events。
- transport、错误、重试、分页与 background 测试。
- Responses examples。

### PR 3：Anthropic native Messages

- native request/response/block/event 与 create/stream/token-count。
- version/beta call configuration。
- block order、tools、thinking signature、usage、stop details。
- 让旧 Anthropic Chat adapter 复用 native transport/codec；golden 保证旧行为。
- Claude examples。

### PR 4：集成、CI、文档与发布准备

- README 协议矩阵、范围说明、存储/重试/成本提醒。
- probe 协议能力输出。
- integration-tag live suites 与明确的 model/key env。
- fuzz、race、Go 1.22、零依赖和 release evidence。
- CHANGELOG 和 migration note。

实现过程可以按上述边界提交多个本地 commit；创建 PR 前最终 squash 与否按仓库历史风格决定。

## 8. 测试矩阵与 Definition of Done

### Codec 与类型

- string/items、known/unknown item/block/event。
- nil、显式 false、0、null、absent。
- Raw round-trip、ExtraFields 冲突、大整数不丢精度。
- marshal 不修改 caller-owned request。
- 所有无效组合在触网前返回清楚错误。

### Transport

- 精确 method/path/query/content-type/accept/auth/version/beta。
- base URL 尾斜杠和 path prefix。
- extra headers、request ID、自定义 transport、context cancellation。
- 2xx、截断 JSON、非 JSON、400/401/403/408/409/429/5xx。
- Retry-After 秒值和 HTTP-date；计数 server 证明无重复创建。
- response body 一定关闭，stream Close/取消及时退出。

### SSE

- LF/CRLF、comments、event/id/retry、多 data 行、任意网络分片。
- assembled event frame ceiling，而不是单行 ceiling。
- terminal event 先交付、下一次 EOF。
- terminal 前断流明确失败。
- Responses 多 output index/item/content index 交错。
- Claude 多 block index、partial JSON、ping、thinking/signature、流内 error。
- unknown event 返回 Raw 且后续事件仍可读。
- accumulator 与非流式结果语义等价。
- parser/union/accumulator fuzz：不 panic、有界终止、frame limit 恒生效。

### 兼容与 CI

- `provider.Provider` 不增加方法；v0.3 风格外部 adapter 仍编译。
- unsupported capability 触网前返回可 `errors.Is(ErrUnsupported)` 的错误。
- 旧 Chat/ChatStream 请求和公开行为 golden 不变。
- capability matrix 与实际 method set 一致。
- `gofmt`、`git diff --check`、`go vet ./...`。
- `GOTOOLCHAIN=local` 下 Go 1.22 build/test，integration tag 至少编译。
- `go test ./... -count=1`、`go test -race ./... -count=1`。
- CI 实际发现并运行新 fuzz targets。
- `go list -m all` 只包含主模块；`go mod tidy` 后 go.mod 无变化且无 go.sum。
- 默认测试全离线，仅使用 httptest；live tests 由 tag/env 显式开启。
- 最终 feature worktree `git status --porcelain` 为空，diff 不含原主工作区的 13 个既有改动。

### Live release evidence

- Responses：sync、stream、强制 function call、previous-response 状态、background retrieve/cancel、invalid key。
- Anthropic：sync、stream、强制 tool use/result、thinking signature 多轮回传、token count、invalid key。
- 默认显式 `store:false`，仅状态链路测试开启存储并在测试后删除。
- 不快照自然语言，只断言协议结构、状态、usage 和 request ID。
- 无 API key 时 live suite 正确 skip，但离线发布门禁仍须全部通过。

## 9. 官方协议基线

- [OpenAI Responses create](https://developers.openai.com/api/reference/resources/responses/methods/create)
- [OpenAI Responses migration](https://developers.openai.com/api/docs/guides/migrate-to-responses)
- [OpenAI Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses)
- [OpenAI Responses streaming events](https://developers.openai.com/api/reference/resources/responses/streaming-events)
- [Anthropic Messages create](https://platform.claude.com/docs/en/api/messages/create)
- [Anthropic streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [Anthropic thinking](https://platform.claude.com/docs/en/build-with-claude/thinking)
- [Anthropic versioning](https://platform.claude.com/docs/en/api/versioning)
- [Anthropic beta headers](https://platform.claude.com/docs/en/api/beta-headers)
- [Anthropic count tokens](https://platform.claude.com/docs/en/api/messages/count_tokens)

协议会演进。新增 typed variant 前，以官方文档和官方 SDK 的当前 schema 为准；Raw fallback 是稳定性保障，不是省略解析错误的借口。
