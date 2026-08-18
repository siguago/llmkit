# v1.0 发布迭代计划

状态:**四个里程碑均已实施**,本文档保留为验收基线与决策依据。

核验日期:2026-08-18(所有官方 API 形状均于当日对照官方文档核对,见第 9 节)。

代码基线:`main@b987f45`(v0.6.0),工作区干净。实施在独立分支上按里程碑顺序推进;每个里程碑对应一个版本,合并与打 tag 由维护者决定,本计划不含发布动作本身。

## 交付状态

| 里程碑 | 提交 | 与计划的差异 |
|---|---|---|
| M1 · v0.7.0 行为收口 | `0c33509` | 媒体 `ResponseFormat` 由「删除」改为「取消废弃并改正文档」——实施核对发现三家 adapter 在读它,删除是功能回退。详见第 2 节倒数第二条 |
| M2 · v0.8.0 OpenAI Files | `0eefd58` | 无 |
| M3 · v0.9.0 Batch | `aef77ce` | 无。fuzz 门禁首跑抓到一个真缺陷(Anthropic 结果行缺必填成员解码成零值成功),已修并留回归种子 |
| M4 · v1.0.0 冻结工程 | `cbb846e`、`c0be4f7` | 无 |

全套离线门禁(`make lint`、`go test ./...`、`-race`、断网复跑、`go vet -tags=integration`)在每个里程碑结束时均为绿。Live 发布证据(第 8 节末)已实现为 `LLMKIT_RUN_BATCH=1` 开启的集成测试,需真实 key 执行。

## 1. 目标与准确表述

1.0 的含义是**冻结**,不是**全能**。本计划把 1.0 定义为三件事:

1. **行为收口**:清掉所有已宣布"不承诺"的临时行为(流超时双轨制)和所有已废弃 API,1.0 之后不再有计划内破坏性变更。
2. **范围补全**:在"对话及其周边"的既有定位内,补上两块 GA 且被下游最常要求的资源面 —— OpenAI Files 与 Batch(OpenAI Batch + Anthropic Message Batches)。
3. **冻结工程**:把"什么算 breaking"写成文档与 CI 门禁,让承诺可验证。

发布文档必须使用以下准确措辞:

> v1.0.0 冻结统一 Chat / 媒体能力面与两套显式原生协议面(OpenAI Responses、Anthropic Messages)的公开 API 与行为语义,并新增 OpenAI Files、OpenAI Batch 与 Anthropic Message Batches 资源生命周期。STT/TTS、Moderation、Conversations、WebSocket、云托管 transport、Anthropic Files(上游仍是 beta)等仍不在声明内;未知字段、未知枚举值与未知 JSONL 结果类型通过 Raw 形态或透传保持前向兼容。

不得宣称"完整支持 OpenAI API / Anthropic API",也不得把 1.0 说成"功能完成"——它是**契约完成**。

## 2. 自审后修正的设计缺口

初稿存在以下不够周到之处,本版已修正:

- **把 Anthropic Files 排进了 1.0。** 官方文档核验(2026-08-18)显示 Files API 至今仍是 beta(`anthropic-beta: files-api-2025-04-14`,且明确标注 Status: Beta、不适用 ZDR)。1.0 冻结一个上游未冻结的面,等于把别人的破坏性变更变成自己的。改为明确延期,待上游 GA 后在 1.x 以纯加法接入;这条理由同时落进稳定性政策(第 7.4 节):**beta 上游不进入冻结面**。
- **初稿让 Anthropic batch results 直接 GET 响应里的 `results_url`。** 结果 URL 是上游控回的绝对地址,经中转站或私有部署时盲目跟随等于让响应体决定出站目标,与本仓库 safehttp 的 SSRF 立场相反。改为:一律按配置的 base URL 拼 `/v1/messages/batches/{id}/results`,`results_url` 仅作为"结果已可用"的信号位读取。
- **初稿把文件上传设计成可重试。** 上传体是 `io.Reader`,第二次尝试读不到内容 —— 这正是 v0.2.0 给 `EditImage` 定下"永不重试"的原因。上传遵循同一规则;要重试请调用方自己重开文件。
- **初稿的 OpenAI batch 输出行解析没有行上限。** `/v1/images/generations` 走 batch 时输出行可含 base64 图片,单行可到数 MB;无上限的 `bufio.Scanner` 会直接报错或被恶意文件放大内存。JSONL 读取器按 `DefaultResponsesMaxFrameBytes`(32 MiB)设默认行上限,可显式覆盖。
- **初稿未定义 JSONL 坏行的容错语义。** SSE 的 strict/tolerant 是为"线上流随时可能掉帧"设计的;而 batch 输出/结果文件是完整工件,坏行是数据损坏不是网络抖动。JSONL 读取器**恒为严格**,不接入 `WithStreamTolerance`。
- **初稿在批量输入 helper 里做了本地校验(custom_id 唯一性、单模型约束)。** 与本仓库"让调用方看厂商自己的报错"的默认惯例冲突,且"单文件单模型"是会变的运营约束而非协议不变量。helper 只做序列化;限制写在文档注释里。
- **初稿漏了 fuzz 门禁的接线。** v0.5.0 修过"新增 fuzz 目标静默漏出 CI"的问题,靠的是 `go test -list` 目标发现,但**包清单**仍是手写的(`ci.yml` 的 run-fuzz 行与 `deep-fuzz.yml` 的矩阵)。新增 `protocol/openaibatch` 包时两处都必须加,列入 M3 的 DoD。
- **初稿未说明 usage 字段的时间边界。** OpenAI Batch 的 `usage` 仅在 2025-09-07 之后创建的 batch 上填充;解码为可选指针,`nil` 表示"上游没给",不得补零。
- **初稿把 cancel / delete 笼统归入"常规重试"。** delete 的重试在"上游已删成功但响应丢失"时会把成功变成一个 404 错误,离病因很远。修正为:cancel / delete 与既有 `CancelResponse` / `DeleteResponse` 的重试语义严格对齐(实现时核对现状并用计数 server 测试钉住),不在本计划里另立一套。
- **初稿把媒体请求的 `ResponseFormat` 字段当成死代码列入删除清单。** 实施核对发现 openai / vercel / easyrouter 三家 adapter 都在读它并向上游转发(`url` / `b64_json` 交付偏好,DALL·E 一代模型仍接受),删除是功能回退而非清理。真正错的是那条废弃注释:它把请求侧偏好和响应侧读取混为一谈。修正为:**取消废弃并改正文档**(说明它是 OpenAI 形状图像端点的请求侧偏好、不支持的 adapter 忽略、实际拿到什么以返回的 `MediaAsset` 为准);`SupportsImages` / `SupportsVideo` 是纯别名、零内部使用,照删。M1 交付物从"删四处"改为"删两处方法 + 两处字段取消废弃",`grep Deprecated:` 归零的验收不变。
- **初稿的能力矩阵守卫只提了统一面。** README 的原生协议矩阵新增三行(Files / Batch / Message Batches)后,必须有与 `TestCapabilityMatrix` 同等地位的守卫断言 `Supports*` 与 adapter 方法集一致;若现状没有原生面的矩阵守卫,M2 先补基建再加行。

## 3. 首次发布范围(按里程碑)

### 3.1 M1 · v0.7.0 · 行为收口(最后一个计划内破坏性版本)

**流超时统一。** compat、Gemini、OpenRouter、DeepSeek 四处 `streamClient` 的 900 秒 `http.Client.Timeout` 移除(`provider/compat/compat.go`、`provider/gemini/gemini.go`、`provider/openrouter/openrouter.go`、`provider/deepseek/deepseek.go`),与 v0.5.0 已改的 OpenAI Responses / Anthropic 流路径一致:**活跃流没有 SDK 兜底超时,调用 context 是唯一生命周期约束**。这是 v0.5.0 CHANGELOG"已知问题"里预告过的收口。迁移表必须重复 v0.5.0 那条警告:从"900 秒后报错"变成"不报错也不返回",无 deadline 的 context 会让 `Recv` 永久阻塞。

**清零全部废弃标记。** 四处,均自 v0.2.0 标记;处置方式经实施核对分成两类(见第 2 节最后一条):

| 项 | 位置 | 处置 |
|---|---|---|
| `Client.SupportsImages()` | client.go | **删除**(纯别名,零内部使用)→ `SupportsImageGeneration()` / `SupportsImageEditing()` |
| `Client.SupportsVideo()` | client.go | **删除** → `SupportsVideoGeneration()` / `SupportsVideoCancellation()` |
| `ImageGenerationRequest.ResponseFormat` | provider/media_types.go | **取消废弃,改正文档**(openai / vercel / easyrouter 在读并转发;删除是功能回退) |
| `ImageEditRequest.ResponseFormat` | provider/media_types.go | 同上 |

处理完后 `grep -rn "Deprecated:"` 于非测试代码必须为零 —— 1.0 不带着已废弃项出生。

**文档同步。** README 的配置、流式容错、已知问题三节及 `options.go` 注释里所有"900 秒"表述更新;CHANGELOG v0.7.0 声明:这是 1.0 前最后一个计划内破坏性版本,此后到 1.0 之间只有加法。

### 3.2 M2 · v0.8.0 · OpenAI Files(GA)

公开操作(全部为官方直连 OpenAI transport):

- `POST /v1/files`:multipart 上传(`file`、`purpose`、可选 `expires_after[anchor]` + `expires_after[seconds]`)。
- `GET /v1/files`:list,支持 `after` / `limit`(1–10000,上游默认 10000)/ `order` / `purpose` 过滤。
- `GET /v1/files/{id}`:retrieve。
- `DELETE /v1/files/{id}`:delete,返回 `{id, deleted, object}`。
- `GET /v1/files/{id}/content`:下载,返回 `io.ReadCloser` 流式读取,不整块缓冲(单文件上限 512 MB)。

DTO 要点:`File` 对象含 `id/bytes/created_at/filename/object/purpose/status(已废弃,保留解码)/expires_at/status_details(已废弃,保留解码)`;purpose 的 8 个已知值(`assistants`、`assistants_output`、`batch`、`batch_output`、`fine-tune`、`fine-tune-results`、`vision`、`user_data`)提供常量但**不做封闭校验**——上游会加新值。上传不整块缓冲(`io.Pipe` 流式 multipart)。

**Anthropic Files 明确不做**,理由见第 2 节第一条;README 在"尚未覆盖"处注明"上游 beta,待 GA 后 1.x 接入"。

### 3.3 M3 · v0.9.0 · Batch(OpenAI Batch + Anthropic Message Batches,均 GA)

**OpenAI Batch** 公开操作:

- `POST /v1/batches`:create(`input_file_id` + `endpoint` + `completion_window:"24h"` + 可选 `metadata` / `output_expires_after`)。
- `GET /v1/batches/{id}`:retrieve(轮询用)。
- `GET /v1/batches`:list(`after` / `limit` 1–100,上游默认 20)。
- `POST /v1/batches/{id}/cancel`:cancel(`cancelling` 至多 10 分钟后落 `cancelled`,部分结果仍产出到 output file)。

Batch 对象:8 状态(`validating/failed/in_progress/finalizing/completed/expired/cancelling/cancelled`)、9 个时间戳、`request_counts{total,completed,failed}`、`errors.data[]{code,line,message,param}`、可选 `model`、可选 `usage`(仅 2025-09-07 后创建的 batch 填充,`nil` 不补零;含 `input_tokens_details.cached_tokens` 与 `output_tokens_details.reasoning_tokens`)。

JSONL 帮手(`protocol/openaibatch`):输入行 `{custom_id, method, url, body}`,`body` 为 `json.RawMessage`——调用方用自己的 DTO 组请求体,batch 包不复述八个端点的请求 schema(当前支持 `/v1/responses`、`/v1/chat/completions`、`/v1/embeddings`、`/v1/completions`、`/v1/moderations`、`/v1/images/generations`、`/v1/images/edits`、`/v1/videos`);输出行 `{id, custom_id, response:{status_code, request_id, body}, error}`。输出顺序不保证与输入一致,按 `custom_id` 对账;输出文件上游 30 天自动删除。单输入文件 ≤50,000 请求且 ≤200 MB、限单一模型 —— 写文档,不做本地校验。

**Anthropic Message Batches** 公开操作:

- `POST /v1/messages/batches`:create,`requests[]{custom_id, params}` 内联(≤100,000 请求或 ≤256 MB;`params` 复用 `protocol/anthropic` 的 MessageRequest 形状;batch 内 `max_tokens` 必须 ≥1、不接受 `stream:true`,由上游校验)。
- `GET /v1/messages/batches/{id}`:retrieve(幂等,轮询用)。
- `GET /v1/messages/batches`:list(`before_id` / `after_id` / `limit` 1–1000,上游默认 20)。
- `POST /v1/messages/batches/{id}/cancel`:cancel(`canceling`;不可中断的进行中请求可能照常完成)。
- `DELETE /v1/messages/batches/{id}`:delete(仅 `ended` 后可删,进行中需先 cancel)。
- `GET /v1/messages/batches/{id}/results`:results,流式 JSONL。

MessageBatch 对象:`processing_status ∈ {in_progress, canceling, ended}`、`request_counts{processing,succeeded,errored,canceled,expired}`、RFC 3339 时间戳(`created_at`/`expires_at` 必填,`ended_at`/`archived_at`/`cancel_initiated_at` 可空)、`results_url` 可空。结果行 `{custom_id, result}`,result 为四型 union:`succeeded{message}` / `errored{error}` / `canceled` / `expired`,未知类型 Raw 保真不跳过;`expired` 不计费。结果保留 29 天。**results 请求按配置 base URL 拼路径,不跟随响应体里的 `results_url`**(第 2 节第二条)。

**轮询 helper**:`WaitBatch` 与 `WaitAnthropicMessageBatch`,对齐 `WaitResponse` 的专用 options 风格(`WaitBatchOptions` / `WaitAnthropicMessageBatchOptions`),默认轮询间隔 60 秒(batch 以小时计,10 秒级轮询只会烧配额);文档明示 batch 可达 24 小时,长任务应自建轮询或用上游 webhook(webhook 不在 1.0 范围,见 3.5)。

### 3.4 M4 · v1.0.0 · 冻结工程

无新能力。稳定性政策文档、CI API 兼容门禁、README 范围声明重写、CHANGELOG 与发布证据。详见第 7.4 节。

### 3.5 明确延期(1.x 候选)与非目标

**1.x 候选**(纯加法,不阻塞 1.0):Anthropic Files(等上游 GA)、rerank 第二家实现(顺带实测 Cohere 形状 usage)、其余厂商的 OpenAI 形状 Files/Batch opt-in(智谱、Moonshot 等,逐家验证 wire 后接入)、OpenAI batch/files 的 webhook 事件。

**非目标**(1.0 不承诺,除非另立计划):STT/TTS、Moderation 资源 API、本地 tokenizer、OpenAI Conversations / WebSocket / `/responses/compact` / vector stores / assistants / fine-tuning / 分片 Uploads、云托管 transport(Bedrock / Vertex / Azure)、多 key 轮换、入站 HTTP gateway、按模型名自动路由。

## 4. 包和公开 API 设计

新增目录(维持"协议 DTO 是只依赖标准库的叶子包"):

```text
protocol/openaifiles/     # File、UploadRequest、ListPage、FileDeleted、purpose 常量
protocol/openaibatch/     # Batch、状态常量、CreateRequest、ListPage、JSONL 输入/输出行与读取器
protocol/anthropic/       # 追加 batches.go / batches_results.go(复用既有 MessageRequest/MessageResponse)
provider/openai/files.go  # Files transport
provider/openai/batches.go
provider/anthropic/batches.go
files.go                  # 根 Client 门面(OpenAI Files)
batches.go                # 根 Client 门面(OpenAI Batch)
anthropic_batches.go      # 根 Client 门面(Anthropic Message Batches)
```

`provider/native.go` 新增小接口,继续按端点拆分、逐 adapter opt-in,**不进 compat 基类**(method promotion 会让 21 家集体虚报):

```text
FileUploader / FileLister / FileRetriever / FileDeleter / FileContentDownloader
BatchCreator / BatchRetriever / BatchLister / BatchCanceller
AnthropicMessageBatchCreator / ...Retriever / ...Lister / ...Canceller / ...Deleter / ...ResultsReader
```

根 Client 方法固定为:

```text
UploadFile / ListFiles / RetrieveFile / DeleteFile / DownloadFileContent
SupportsFileUpload / SupportsFileListing / SupportsFileRetrieval / SupportsFileDeletion / SupportsFileContentDownload
CreateBatch / RetrieveBatch / ListBatches / CancelBatch / WaitBatch
SupportsBatchCreation / SupportsBatchRetrieval / SupportsBatchListing / SupportsBatchCancellation
CreateAnthropicMessageBatch / RetrieveAnthropicMessageBatch / ListAnthropicMessageBatches
CancelAnthropicMessageBatch / DeleteAnthropicMessageBatch / ReadAnthropicMessageBatchResults / WaitAnthropicMessageBatch
SupportsAnthropicMessageBatchCreation / ...Retrieval / ...Listing / ...Cancellation / ...Deletion / ...Results
```

首发只有 `provider/openai` 与 `provider/anthropic` opt-in。根包不 mass-alias 协议子类型,方法直接使用 `protocol/openaifiles`、`protocol/openaibatch`、`protocol/anthropic` 类型。

## 5. Wire 保真和前向兼容

- 沿用既有原则:union 用 `Type string` + typed known + Raw fallback;未知 JSONL 结果类型必须 Raw 保留,不得跳过;不通过 `map[string]any` 中转需要保真的 JSON number。
- **解码单向性是刻意的**:File / Batch / MessageBatch 是"上游产生、SDK 只读"的资源对象,SDK 不会把它们重新编码发回上游(上传是 multipart,创建是独立请求 DTO),因此这批 DTO 承诺"解码不丢已建模字段 + 保留原始字节可查",不承诺 decode→encode 逐字节往返 —— 与 Responses/Messages 那套"会被回传的对象必须往返无损"的更强契约刻意区分,ADR 里写明。
- 枚举(purpose、batch status、processing_status、result type)提供常量、不做封闭校验;未知值原样保留并可读取。
- 时间语义按厂商原样:OpenAI 用 Unix 秒(`int64`),Anthropic 用 RFC 3339 字符串;SDK 不做跨厂商归一化 —— 归一是网关的事,不是协议 DTO 的事。
- JSONL 读取器:行上限默认 `DefaultResponsesMaxFrameBytes`(32 MiB)可覆盖;恒严格,坏行即错;`io.Reader` 进、逐行出,不整文件缓冲。

## 6. 错误、状态与重试

- HTTP 4xx/5xx 返回 Go error,沿用统一分类(`IsAuthError` 等);batch 的 `failed/expired/cancelled` 与 MessageBatch 的 `ended` + 全零 `succeeded` 是**成功解码出的资源状态**,不伪装成 error,由调用方查看状态与计数 —— 与 `Response.Status` / `VideoJob.Status` 同一立场。
- 重试按"能否证明上游没接活"划分:
  - `UploadFile` **永不自动重试**(`io.Reader` 一次性,同 `EditImage`)。
  - `CreateBatch` / `CreateAnthropicMessageBatch` 用 replay-safe-only 子集(创建即排队计费;同一输入文件建两个 batch 是两份钱)。
  - retrieve / list 是读操作,走常规重试;cancel / delete 与既有 `CancelResponse` / `DeleteResponse` 的重试语义严格对齐(见第 2 节),实现时核对现状并用计数 server 钉住;`DownloadFileContent` 与 `ReadAnthropicMessageBatchResults` 只重试握手,body 读取中断原样报错,不自动续传。
- Anthropic 结果行内的 `errored` 携带完整 `ErrorResponse`(9 种已知 error type);它是**数据**不是调用错误,不参与重试,不进错误分类器 —— 但提供 helper 把单行 error 映射到统一分类,便于调用方统计。
- 凭据头规则不变:跨 origin redirect 不转发;`x-api-key` / `Authorization` 不可被 `WithHeader` 覆盖。

## 7. 实施顺序

### 7.1 PR/M1:v0.7.0 行为收口

- 四 adapter 移除流 900 秒上限 + 断言测试(stream client 无 `Timeout`);删除四处废弃 API;更新受影响测试与文档;CHANGELOG v0.7.0 迁移表。

### 7.2 PR/M2:v0.8.0 OpenAI Files

- `protocol/openaifiles` + `provider/openai/files.go` + 根门面 + `Supports*`;流式 multipart;原生能力矩阵守卫(基建缺失则先补);probe `-files`(上传→retrieve→list→delete 全清理,默认不跑);README 与 CHANGELOG;ADR 0002(资源面包布局、解码单向性、Anthropic Files 延期理由)。

### 7.3 PR/M3:v0.9.0 Batch

- `protocol/openaibatch`(含 JSONL 读写与 fuzz)+ `protocol/anthropic` batches(含 results 读取器与 fuzz)+ 两家 adapter + 根门面 + Wait helper;**ci.yml run-fuzz 包清单与 deep-fuzz.yml 矩阵加入新包**;`examples/batch` 与 `examples/anthropic-batch`(按 `-mode` 拆步,避免示例意外产生费用);probe `-batch`(create 单请求 batch → 立即 cancel → retrieve,费用注明可能不为零);集成测试带独立 env 开关;README 与 CHANGELOG。

### 7.4 PR/M4:v1.0.0 冻结工程

- **稳定性政策**(`STABILITY.md` 或 README 专节,二选一后全仓引用统一):冻结面 = 全部公开包的导出 API 与文档化行为;加字段不算 breaking,位置式 composite literal 不受保护(v0.6.0 的教训成文);`Supports*` 语义承诺;wire 行为变化按 semver 对待;**beta 上游不进冻结面**;废弃流程 = minor 标记、只在 2.0 删除;Go 最低版本可在 minor 抬升(提前一版预告)。
- **CI API 兼容门禁**:`go run golang.org/x/exp/cmd/apidiff@<pinned>` 以最新 release tag 的导出面为基线(CI 内 `git worktree` 检出 tag 生成基线,或缓存基线文件),对比 HEAD,不兼容即红;工具经 `go run` 执行,不进 `go.mod`,零依赖检查不受影响;跑在最新 Go 的 job 上,不动 1.22 job。
- README:"尚未覆盖的能力"表改写为"非目标与 1.x 路线"(第 3.5 节内容);原生协议矩阵補全三行;1.0 语义化版本承诺段落。
- CHANGELOG v1.0.0:从 v0.9.0 升级零改动;并附 0.x → 1.0 的累积迁移索引(逐版链接,不复述)。
- 发布证据见第 8 节;全绿后由维护者打 tag。

## 8. 测试矩阵与 Definition of Done

### 各里程碑通用门禁

- `make lint`(gofmt + vet + 零依赖)、`go test ./... -count=1`、`go test -race ./... -count=1` 全绿;`GOTOOLCHAIN=local` 下 Go 1.22 编译含 integration tag;`go list -m all` 只含主模块,无 `go.sum`。
- 默认测试全离线(httptest);断网(`HTTPS_PROXY=http://127.0.0.1:1`)复跑仍绿。
- 每版 CHANGELOG 带升级对照表("你的代码里如果有 / 升级后会怎样 / 怎么改"),README 与实现同步。

### M1 专项

- 四 adapter 各一条断言:stream client 无 client 级 Timeout;非流式路径仍受 `WithTimeout` 约束的既有测试不回归。
- 删除项的编译失败在 CHANGELOG 表格中逐条给出替代写法;`grep Deprecated:` 零命中(测试与 CHANGELOG 引文除外)。

### M2 专项

- transport:method/path/query/multipart 字段(`file`/`purpose`/`expires_after[anchor]`/`expires_after[seconds]`)、鉴权头、base URL 尾斜杠、context 取消、body 一定关闭。
- 上传:filename 与 content-type 正确进 multipart;大内容不整块缓冲(以计数 Reader 断言流式);reader 消费后错误不重试(计数 server 证明只打了一次)。
- 下载:握手 4xx/5xx 分类正确;成功后 body 中断报错不重试;`io.ReadCloser` 由调用方关闭的契约有文档测试。
- list:分页参数透传、空列表合法、`has_more` 语义;purpose 过滤只在设置时出现于 query。
- File DTO:已废弃字段(`status`/`status_details`)仍解码;未知 purpose 值保留可读;`expires_at` 缺失为零值不误判。
- 能力面:朴素 `compat.Provider` 必须不满足任一 Files 接口;`Supports*` 与方法集一致由矩阵守卫断言。

### M3 专项

- JSONL:输入行序列化字节精确断言;输出/结果行的 `body` 保持 RawMessage 无损;行上限边界(恰好 32 MiB、超 1 字节);坏行报错并带行号;空文件、尾部无换行、CRLF 均正确;fuzz(不 panic、上限恒生效、未知 result type Raw 保真)。
- OpenAI Batch:8 状态解码;`usage` 缺失为 `nil` 不补零;`errors.data` 的 `line` 可空;cancel 后 `cancelling` 状态轮询继续;`WaitBatch` 在 8 状态的终态集合上停机,context 取消即退。
- Anthropic:create 内联 `params` 与 MessageRequest 字节级一致(防止两份 schema 漂移);results 读取器按 base URL 拼路径(测试断言请求打到 httptest 而不是响应里伪造的 `results_url`);四型 result union + 未知型 Raw;`request_counts` 五桶;delete 在 `in_progress` 时的 4xx 分类。
- 重试:create 只在 replay-safe 错误上重放(计数 server);429 `Retry-After` 被尊重;5xx 不重放。
- probe/examples:`-batch` 默认不跑;examples 每步幂等可单独执行,含清理(删除测试文件、cancel 后确认)。

### M4 专项

- apidiff 门禁:对 v0.9.0 tag 的对比为零不兼容;人为引入一处签名变更的演练必须让 job 变红(验完撤销)。
- 稳定性文档中的每条政策都有出处链接(对应 CHANGELOG 或 ADR);README 三处矩阵与 `Supports*` 实际方法集一致。

### Live release evidence(需真实 key,发布前执行,无 key 时 suite 显式 skip)

- OpenAI:upload→list→retrieve→download→delete 全链路;create batch(单请求)→cancel→retrieve→(如产出)下载 output 并解析;invalid key 分类。
- Anthropic:create batch(两请求)→poll→results 逐行解析(succeeded 至少一条)→delete;cancel 路径;invalid key 分类。
- 由 `LLMKIT_RUN_BATCH=1` 显式开启,默认集成冒烟不包含(batch 最长 24 小时,不进常规 CI);费用与数据保留(30 天输出文件 / 29 天结果)在测试注释中写明,测试自行清理创建的文件与 batch。

## 9. 官方协议基线(核验日期 2026-08-18)

- [OpenAI Batch create](https://developers.openai.com/api/reference/resources/batches/methods/create) —— endpoint 枚举 8 个(含 `/v1/responses` 与 `/v1/videos`)、`completion_window` 仅 `24h`、`output_expires_after{anchor,seconds}`。
- [OpenAI Batch retrieve](https://developers.openai.com/api/reference/resources/batches/methods/retrieve) —— 8 状态、`usage` 仅 2025-09-07 后填充、可选 `model`。
- [OpenAI Batch list/cancel](https://developers.openai.com/api/reference/resources/batches/methods/list) —— list `limit` 1–100 默认 20;cancel 至多 10 分钟落定、部分结果照常产出。
- [OpenAI Batch guide](https://developers.openai.com/api/docs/guides/batch) —— 输入/输出 JSONL 行形状、50,000 行 / 200 MB / 单模型、输出乱序按 `custom_id` 对账、输出文件 30 天删除。
- [OpenAI Files upload](https://developers.openai.com/api/reference/resources/files/methods/create) —— multipart 字段、purpose 8 值、512 MB / 项目 2.5 TB、上传限速 1000 rpm。
- [OpenAI Files list/retrieve/delete/content](https://developers.openai.com/api/reference/resources/files) —— list `limit` 1–10000 默认 10000、`GET /files/{id}/content`。
- [Anthropic Message Batches create](https://platform.claude.com/docs/en/api/messages/batches/create) —— `requests[]{custom_id, params}` 内联。
- [Anthropic Message Batches retrieve/list/cancel/delete/results](https://platform.claude.com/docs/en/api/messages/batches/retrieve) —— `processing_status` 三态、五桶计数、RFC 3339、list `limit` 1–1000 默认 20、delete 仅 `ended` 后、results 四型 union。
- [Anthropic batch guide](https://platform.claude.com/docs/en/build-with-claude/batch-processing) —— 100,000 请求 / 256 MB、24 小时到期、结果保留 29 天、`expired` 不计费、batch 内 `max_tokens ≥ 1`。
- [Anthropic Files(beta,不进 1.0)](https://platform.claude.com/docs/en/build-with-claude/files) —— Status: Beta、`anthropic-beta: files-api-2025-04-14`。

协议会演进。实现时以官方文档当前 schema 为准;发现与本计划不符时,更新本计划的对应条目并在 PR 描述中注明,Raw fallback 不是省略解析错误的借口。
