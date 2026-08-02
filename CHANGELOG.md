# 变更记录

本项目尚未到 1.0，API 未冻结。破坏性变更会在这里逐条列出，并说明为什么值得破坏。

## Unreleased

新增两套**显式原生协议面**，不改变现有 `Chat` / `ChatStream` 的 DTO、路由或默认 wire 行为。没有按模型名自动切换，也没有把所有 provider 一次性声明成支持。

> 支持 OpenAI Responses 核心资源、状态生命周期与 SSE，以及 Anthropic Messages create/stream 和 token count。Batch、Conversations、WebSocket、Responses compact、云厂商 Claude transport 与部分内置工具专项类型另行提供；未知 item/block/event 可通过 Raw 形态无损保留。

### 新增

- OpenAI 官方直连 adapter 新增 Responses create、stream、retrieve、delete、cancel、input-items、input-token-count，以及根包的 `WaitResponse` 后台任务轮询 helper。请求/响应、异构 item/content、核心 stream event 与 forward-compatible Raw 类型位于 `protocol/responses`。
- Anthropic 官方直连 adapter 新增原生 Messages create、stream 与服务端 token count。请求/响应、content block、tool use/result、thinking、usage、核心 stream event、请求级 `anthropic-version` / `anthropic-beta` 选项与 Raw 类型位于 `protocol/anthropic`；默认稳定版本为 `2023-06-01`。
- provider 能力按 endpoint 拆成细粒度可选接口，根包新增对应 `Supports*` 探测。只实现 JSON create、没有 SSE 或资源生命周期的 relay 可以只声明真实存在的那一部分。
- 新增 `examples/responses` 与 `examples/anthropic-native`；两者用 `-mode=sync|stream|tokens` 一次只执行一种操作，避免示例本身无意触发两次生成。

### 行为边界

- 原生 create 使用常规重试配置的 replay-safe-only 子集。能生成内容并计费的请求没有 SDK 级幂等键；5xx、读超时或中途断链等「上游可能已经受理」的失败不会自动重放。流返回后发生的 `Recv` 错误也不会从头重放。
- SDK 不擅自覆盖 OpenAI Responses 的 `store` 默认值。示例在无需持久状态时显式使用 `store:false`；需要 retrieve、input-items 或 previous-response 状态链路时，由调用方明确决定保留策略并负责删除。
- HTTP 错误仍返回 Go error；Responses 的 `completed` / `failed` / `incomplete` 是成功解码出的资源状态，调用方检查 `Status`、`Error` 与 `IncompleteDetails`。终态事件前断流返回错误，不把部分输出伪装成完整结果。
- 原生 DTO 不承诺与统一 Chat DTO 无损互转。OpenAI Responses 首发 transport 只接官方 OpenAI，Anthropic Messages 首发 transport 只接官方 Anthropic；AWS Bedrock、Google Vertex AI 与兼容 relay 需要分别验证鉴权、路径和能力后 opt in。

### 测试与发布门禁

- 增加共享 SSE framing、两套原生 union/event/accumulator、transport、资源路径、能力探测、重试与终态语义的离线测试和 fuzz 入口。
- CI 在声明的 Go 1.22 下使用 `GOTOOLCHAIN=local`，防止静默下载更新工具链掩盖最低版本回归；零依赖检查同时验证 module graph、`go mod tidy` 后的差异和不存在 `go.sum`。

### 修复

- Anthropic 流累计在服务端 fallback 后会按最后一次跳转更新最终模型；compaction delta 同时保留续轮所需的 `encrypted_content`。对象型 DTO 不再把顶层 `null` 当作成功的零值响应。
- Responses 已建模 DTO 会保留「字段缺失、显式 `null`、空值」的线格式差异，覆盖 assistant `phase`、reasoning 加密内容、prompt、图片/文件输入与函数工具等；citation 的必填空字符串和零索引也不会在重编码或流终态克隆时丢失。type-omitted easy assistant message 新增正式 `Phase` 字段，便于多轮续接时原样回传。
- OpenAI Responses 及 Anthropic 的所有流式入口（包括统一 `Client.ChatStream`）不再被 `http.Client.Timeout` 在 900 秒整体截断；这些路径没有活跃流的 SDK 兜底超时，由调用 context 控制生命周期。其他保留既有 900 秒上限的 adapter 未改变。
- Responses 的默认单帧上限提高到公开常量 `DefaultResponsesMaxFrameBytes`（32 MiB），可容纳文档规定的 base64 图片事件；显式 `WithMaxStreamFrameBytes` 仍优先。该上限按实际帧惰性增长，但解帧与 JSON 解析可能同时持有多份数据，高并发连接不应无依据地继续调大。

## v0.4.0 — 2026-08-01

一项新能力：逐模型任务发现，让混合目录里的对话 / 向量 / 图片 / 视频模型第一次能被区分开。**没有破坏性变更，`RemoteModel` 一个字节都没动 —— 升级不需要改任何代码。**

### 从 v0.3.1 升级

**没有必须做的迁移。** 下表第一行不是本次升级造成的，但它会实实在在地咬人，所以放在这里。

| 你的代码里如果有 | 升级后会怎样 | 怎么改 |
|---|---|---|
| 硬编码 `deepseek-chat` / `deepseek-reasoner` | **和升级无关地失败** —— DeepSeek 官方已于 2026-07-24 停用这两个 ID，留在 v0.3.1 也一样调不通 | 换成 `deepseek-v4-flash` / `deepseek-v4-pro` |
| 遍历 `Models()` / `ListModels()` | 不受影响 —— 上游请求、过滤结果、顺序、空目录的 nil 语义都没变 | — |
| 用 `RemoteModel` 当 map key、比较、或不带字段名构造 | 不受影响 —— 仍是原来的三个字段 | — |
| 想按任务筛选模型 | 可选采用新增的 `ModelsWithTaskTypes()` | 缺 map key 表示**未知**，不能当成 chat |

Gemini、Vercel、OpenRouter 的混合媒体目录扩展**只发生在新方法里**：`Models()` 看到的东西和 v0.3.1 完全一致，升级不会让既有调用方突然多出一批不能用的模型。

### 新增

**新增可选的逐模型任务发现接口，且保持 `provider.RemoteModel` 完全不变。** `provider.ModelTaskLister` 在原 `ModelLister` 上增加 `ListModelsWithTaskTypes`，以独立的 `map[model_id][]task_type` 返回可靠分类；未知模型不写入 map，不能被默认为 chat。根包同步导出 `ModelTaskLister`、`Client.ModelsWithTaskTypes` 与 `Client.SupportsModelTaskTypes`。旧 adapter 自动回退为普通模型列表加 nil map。

任务名与网关 binding 对齐：`chat`、`embedding`、`image.generate`、`image.edit`、`video.generate`，并在根包与 `provider` 包通过 `RemoteModelTask*` 常量公开。Anthropic、DeepSeek、Gemini、Vercel、OpenRouter 实现任务发现：Anthropic 的 `/v1/models` 至今只出对话模型，因此按 `claude-` 前缀标成 chat，非 `claude-` 前缀的条目保持未知；DeepSeek 的目录同样没有能力字段，但混着退役别名和非对话型号，只能走白名单，只对当前 V4 Chat 模型分类，退役别名和未来 ID 保持未知。Gemini 只分类已经核验过请求/响应契约的文本、embedding、generateContent 图片和 Veo 型号，明确排除已停用型号与 TTS / Live / native-audio / Lyria / Imagen，并让未来代际保持未知；Vercel 按目录 `type` 区分 language / embedding / image；OpenRouter 使用 `architecture.output_modalities`，过滤 embeddings / rerank / speech / transcription / audio 等未实现端点，并只给能走现有 chat/completions 路由的 text+image 型号声明图片生成，纯 image 型号在专用 `/images` 路由实现前保持未知。通用 compat 目录缺少可靠类型元数据，继续只实现旧 `ModelLister`。

这是纯 additive API 变更：`RemoteModel` 仍是原来的三个字段，JSON 形状、可比较性、map key 用法和不带字段名的 composite literal 均不受影响；旧 `ListModels` / `Client.Models` 的历史过滤结果、顺序以及空目录的 nil/非 nil 语义保持不变。Gemini 的 Veo、Vercel 的扩展图片目录和 OpenRouter 的 `output_modalities=all` 只由新 `ListModelsWithTaskTypes` / `Client.ModelsWithTaskTypes` 暴露，避免升级后悄悄改变既有调用方看到的目录。唯一的旧请求编码修复是 Gemini 分页：不透明 `pageToken` 现在通过 query encoder 转义，带 `+`、`/`、`=` 的合法 token 不再被破坏。

`llmkit-probe` 新增「模型任务」探测，用真实目录跑一次分类并列出**未分类的模型**。分类靠的是硬编码白名单（Gemini、DeepSeek）或上游元数据字段（Vercel、OpenRouter），两者过时都不会报错、不会让离线测试变红 —— 厂商发新模型时，它只是安静地变成未知。未分类清单就是白名单该更新的信号；整个目录都未分类则判定为失败。

DeepSeek 的运行示例、probe 和 integration 默认模型同步更新为当前 `deepseek-v4-flash`（推理示例使用 `deepseek-v4-pro`）。官方已于 2026-07-24 停用 `deepseek-chat` / `deepseek-reasoner`；旧 ID 只留在专门验证历史请求协议的离线测试中，远程目录即使返回它们也不会自动分类。

### 已知问题

- **Gemini 和 DeepSeek 的分类白名单会随厂商发新模型而过时。** 硬编码模型 ID 是这两家目录唯一可靠的判据（都没有逐模型能力字段），代价是新模型上线后会先显示为「未知」。**失效方向是安全的** —— 过时导致漏分类，不会导致误分类成 chat 然后在 `ChatCompletion` 里丢掉媒体输出。`llmkit-probe` 的「模型任务」探测就是用来发现这种漂移的：它列出未分类模型，整个目录都未分类则判失败。
- **OpenRouter 的纯 `image` 输出型号不声明图片生成。** 本 adapter 的 `GenerateImage` 走 chat/completions + `modalities`，只有 `text+image` 型号能这么用；纯 image 型号需要 OpenRouter 专用的 `/images` 路由，尚未实现。这些型号在目录里可见但保持未分类 —— 不承诺一条会调错端点的路由。
- **`ModelsWithTaskTypes` 在多数 provider 上返回 `nil` task map。** 通用 OpenAI 兼容的 `/v1/models` 只给 ID，没有能力字段，所以 compat 层继续只实现旧 `ModelLister`。目前有逐模型分类的是 anthropic、deepseek、gemini、openrouter、vercel 五家；其余照常返回模型列表，只是 map 为空。用 `SupportsModelTaskTypes()` 可以先探测。

---

## v0.3.1 — 2026-08-01

一个性能修复。**没有破坏性变更，也没有 API 变化 —— 升级不需要改任何代码。**

### 修复

**`internal/logging` 的默认 logger 现在真的静默。** 此前 `discard` 是 `slog.NewTextHandler(io.Discard, nil)`，而 `nil` 选项把级别默认成 Info —— 于是它一边丢弃所有记录，一边对 Info/Warn/Error 回答 `Enabled() == true`。任何按 `Enabled` 的文档去跳过昂贵参数构建的守卫都因此失效：参数照常构建，格式化完再扔掉。

`provider.StreamDiagnostics.Malformed` 正是这样一处守卫（注释写着「skip it when nothing is listening — which is the default」），它的 logger 来自 `logging.From(ctx)`。没装调用方 logger 时那就是 `discard`，条件恒为真，于是容错模式下**每个畸形帧都白跑一次 `TruncateForLog(payload, 200)` 拷贝** —— 正是注释声称省掉的那次。

改为自定义 `discardHandler`，`Enabled` 在所有级别恒返回 false。Go 1.24 的 `slog.DiscardHandler` 就是干这个的，但 `go.mod` 声明 1.22，所以手写一份；将来抬高下限时可以原样换掉，行为不变。

> 只影响性能，不影响正确性 —— 记录本来就是被丢弃的，变的只是「丢弃前还构不构建」。装了 `WithLogger` 的调用方完全不受影响：`From` 返回的是他们自己的 logger，级别由他们的 handler 说了算。这一点是双向断言的：`TestWithLogger_ReceivesDiagnostics` 保证畸形帧的 warn 照常送达，把 `From` 改成忽略用户 logger 会让它立刻变红。

自 v0.2.0 的「已知问题」列表里挂了两版，现已清掉。

---

## v0.3.0 — 2026-07-31

两项新能力：gemini 的 embeddings 和一套 rerank 接口（目前 siliconflow 一家实现）。**两者都经真实 API 实测**，不是只跑通了 mock —— 上一版发布时 gemini embeddings 的 wire format 还只是「照文档理解写的」，这一版把它验了，也因此揪出三个 bug（见「修复」）。

### 从 v0.2.0 升级

**只有一条破坏性变更，且多数人碰不到。**

| 你的代码里如果有 | 升级后会怎样 | 怎么改 |
|---|---|---|
| `var p *compat.Provider = siliconflow.New("")` | **编译失败** | 改用 `p := siliconflow.New("")`，或写 `*compat.WithRerank` |
| `p := siliconflow.New("")` | 不受影响 | — |
| `llmkit.Wrap(siliconflow.New(""), ...)` | 不受影响 | — |
| 遍历 `Models()` 挑 gemini 的对话模型 | **行为变化**：现在也会返回 embedding 模型 | 按模型名过滤（含 `embedding` 的那几个） |

前三行只影响直接 import `provider/siliconflow` 的代码。第四行是 gemini 修复的必然副作用 —— 详见「修复」一节，`RemoteModel` 没有类型字段，调用方需自行区分。

用 `llmkit.New(llmkit.SiliconFlow)` 这种门面层写法的，一行都不用改。

### 破坏性变更

**`siliconflow.New` 的返回类型从 `*compat.Provider` 变为 `*compat.WithRerank`。** 只影响直接 import `provider/siliconflow` 并显式写出返回类型的代码（`var p *compat.Provider = siliconflow.New("")`）；用 `:=` 或传给 `llmkit.Wrap` 都不受影响，两个类型都实现 `provider.Provider`。这是接上 rerank 的必要代价 —— Go 的方法集没法在不换类型的前提下多长出一个方法。

### 新增

**新增 Rerank 能力**，v0.2.0「尚未覆盖」表里的第二条清掉。

重排序是 RAG 的第二段：embeddings 廉价召回候选，reranker 把 query 和 document 放在一起精确打分。新增 `Client.Rerank` / `SupportsRerank`、`provider.Reranker` 接口，以及 `RerankRequest` / `RerankResponse` / `RerankResult` 三个类型（门面层有别名）。

**这是统一接口里唯一一处刻意打破位置契约的地方。** `Embed` 保证 `Data[i]` 对应 `Input[i]`，而 `Rerank` 的结果**按相关性降序**、还可能被 `TopN` 截断 —— 重排本身就是这个操作的目的。`RerankResult.Index` 是映射回调用方 `Documents` 的唯一凭据，所以越界的 index 直接报错而不是透传：透传出去会变成调用方使用时的下标越界 panic，那时离病因已经很远了。

`RelevanceScore` 的文档里写明量纲不跨厂商也不跨模型可比（有的给 0..1 概率，有的给无界 logit）—— 拿它跨模型比较是这个 API 最容易犯的错。

实现上新增 `compat.WithRerank`，是 `compat.NoEmbeddings` 的镜像：那个类型用组合来**收窄**方法集（不提升 `Embeddings`），这个用嵌入来**扩展**（照常提升全部，再加 `Rerank`）。rerank 不属于 OpenAI API，所以 compat 默认不带它，adapter 得显式选用 `NewWithRerank` —— 否则 21 家 compat 厂商会集体声称支持一条大多数都没有的路由，正是能力探测说谎的老毛病。

目前只有 **siliconflow** 一家启用。实现的是 Cohere 定下、Jina / SiliconFlow / Together 都照抄的事实标准（`POST /rerank`，`{model, query, documents}`）。响应里 `document` 字段两种形状都收：Cohere / SiliconFlow 发 `{"text": "..."}` 对象，部分中转发裸字符串。

**能力矩阵测试当时抓不到这个新能力** —— `caps` 结构里没有 rerank 字段，加了新能力却没人守卫。已补上 `rerank` 字段与断言，补完当场就抓到了 siliconflow 的变化。README 的能力矩阵表相应多了一列。

**gemini 接上 embeddings**，手写翻译层，v0.2.0 的「尚未覆盖」表里点名的那条空白清掉。

Gemini 的路由不是 OpenAI 形状：走 `models/{model}:batchEmbedContents`，每条输入要包成 `content.parts`，模型名在 URL 和每个子请求里各出现一次（且子请求要 `models/` 全名）。但**批量语义是对得上的** —— N 条进、N 个向量出、顺序一致 —— 所以这是信封翻译，不是把一种操作硬掰成另一种。

刻意用批量端点而不是单条的 `:embedContent`：后者会把一次 `Embed` 变成 N 个 HTTP 请求，正是那道成本悬崖让 volcengine 的融合式端点整个被挡在这个接口之外。Gemini 没这个问题，批量路由存在且是位置对应的。

几处取舍：

- **`task_type` 转发，不本地校验** —— 这是与 minimax 那层刻意相反的决定。minimax 的 `type` 只有两个取值、集合封闭，所以本地拦住拼写错误；Gemini 的取值集合还在扩（`CODE_RETRIEVAL_QUERY` 是后加的），本地白名单会挡掉将来新增的合法值，所以退回本项目「让调用方看厂商自己的报错」的默认惯例。`task_type` 和 `title` 都走 `ProviderOptions["gemini"]`，忘了嵌那层 key 会报错而不是静默丢弃 —— 丢掉一个 `task_type` 是看不见的检索质量下降。
- **`EncodingFormat` 只接受 `float`**，其余报错。Gemini 没有 base64 线格式，静默给调用方 float 是他们直到下游炸掉才会发现的差异。`Dimensions` 作为 `outputDimensionality` 转发，能不能用交给模型自己答。
- **向量解码成 `[]any` 而不是 `[]float64`**，与所有 compat provider 一致（`llmkit-probe` 和调用方断言的就是 `[]any`）。
- **Gemini 在这个端点不返回 token 计数**，所以 `Usage` 只有 `RequestCount`（`NormalizeUsage` 兜底为 1），token 字段留 0 而不是估算 —— 按 token 计价的调用方拿到的是「没有数据」，不是一个编出来的数字。
- 模型名两种写法都收（`gemini-embedding-001` 与 `models/gemini-embedding-001`），URL 用裸名、子请求用全名，不会拼出 `models/models/`。

`SupportsEmbeddings()` 对 gemini 从 false 变 true，`TestCapabilityMatrix` 与 `TestEmbedModelsCoverEmbedders` 两道守卫同步更新 —— 加这个功能时它们都如期先变红，probe 的默认探测模型填了 `gemini-embedding-001`。

### 修复

**gemini 的默认 embedding 模型改为 `gemini-embedding-001`。** 原先填的 `text-embedding-004` 已从 v1beta 退役，实测 404（`is not found for API version v1beta`）。当前在线的是 `gemini-embedding-001` / `-2` / `-2-preview`。**适配器实现一行没改** —— 实测确认 `v1beta` + `batchEmbedContents` 返回 200，批量路由存在，当初「不像 volcengine 那样有 N 个请求的成本悬崖」这个判断成立。

> 排查时踩到两个假信号，记下来省得下次再走一遍。其一：`ListModels` 返回的三个 embedding 模型都只声明 `embedContent`、不声明 `batchEmbedContents`，但批量路由实测可用 —— Google 的元数据不列它，别拿它当路由是否存在的依据。其二：挂本地代理时这两条 POST 会返回 `text/html` + 0 字节的 404，还带着 Google 的 `server` 头，看着像 API 在拒绝，而 curl 直连即 200。判断路由存不存在要看 API 层的 JSON 报错，HTML 空 body 是中间层的产物。

**gemini 的无效 key 此前不被识别为认证错误。** Gemini 用 HTTP 400 `INVALID_ARGUMENT` 回应无效凭据，而不是 401 —— 于是按状态码分类把「key 错了」判成「请求格式错了」，`IsAuthError()` 返回 false，`IsRetryable()` 也就不知道这个凭据永远不会成功。真信号在响应体的 `details[].reason`（`API_KEY_INVALID`），那是 `google.rpc.ErrorInfo` 的稳定机器标识；message 是本地化文本、status 太粗，都不能用。gemini 的六处错误构造统一走新的 `geminiError`，reason 作为 `ProviderCode(err)` 保留，凭据类映射到 `ErrorCategoryAuth`。实测：`llmkit-probe gemini` 的「错误分类」一项从 FAIL 转为 `PASS 400 → IsAuthError`。

> 只映射凭据类，其余一律不分类 —— 不分类会回落到 HTTP 状态，而 Gemini 别处的状态码本来就是对的（429 限流、404 缺模型、5xx 自身故障）。**尤其不碰 `rate_limit`**：那个分类附带「上游没有计费」的断言，`IsSafeToReplay` 会据此重放图像/视频创建，是真金白银。429 自己就够格，从一个 reason 字符串猜出来的不够格。认不出的 reason 仍然通过 `ProviderCode` 交给调用方，什么都没丢。

**`ListModels` 此前会滤掉所有 embedding 模型。** 过滤条件是「支持 `generateContent`」，而 embedding 模型恰恰不支持它 —— 于是 `SupportsEmbeddings()` 对 gemini 答 true，`Models()` 却一个能用的模型都给不出来，而厂商 404 里那句「Call ModelService.ListModels」指的正是这条路。现在按「本 SDK 有没有对应路由」过滤（`generateContent` / `embedContent` / `batchEmbedContents` 三者之一）。

> **行为变化**：`Models()` 对 gemini 现在同时返回对话模型和 embedding 模型（实测 45 个，其中 3 个 embedding），而 `RemoteModel` 没有类型字段，调用方无法从返回值区分。遍历 `Models()` 挑对话模型的代码需要自己按模型名过滤。这与 vercel adapter 的既有做法一致 —— 权衡过：模型压根不可发现是更糟的失败。

### 测试

- 新增 `provider/compat/rerank_test.go` 与根包 `rerank_test.go`：请求形状、结果必须按分数降序而非输入顺序、越界 index 必须报错（两个方向都测）、`document` 字段的五种形态（对象 / 裸字符串 / 缺失 / null / 空对象）、未设的 `top_n` 必须省略（`0` 会被读成「什么都不返回」）、`ProviderOptions` 只透传本厂商命名空间、两种 token 计数位置、空结果集是合法的、以及能力面断言：**朴素的 `compat.Provider` 必须不满足 `Reranker`**，否则每家 compat 厂商都会声称支持。门面层测的是 `ErrUnsupported` 路径与探测/调用两者必须一致。写这批测试时越界守卫当场抓到了我自己测试里的不一致（响应引用 index 2，请求只发了 1 个文档），是它该有的样子。compat 包覆盖率 53.3% → 61.4%。
- 新增 `provider/gemini/embeddings_test.go`：请求形状（批量端点、鉴权头、子请求全名、输入顺序）、单条输入仍走批量信封、模型名两种写法、`Dimensions` 转发与未设时必须省略（`0` 会被 Gemini 读成「要零宽向量」）、`task_type`/`title` 透传、未知 `task_type` 必须转发而非本地拒绝、base64 拒绝、向量数不匹配、上游错误、截断响应、nil 请求、`Usage` 只有 `RequestCount`、向量元素类型是 `[]any` 的 `float64`，以及 `ProviderOptions` 的嵌套层级检查。gemini 包覆盖率 46.5% → 53.1%。
- 新增 `provider/gemini/errors_test.go`：无效 key 的分类，用的是从线上端点原样抓下来的响应体（400 + `INVALID_ARGUMENT` + `details[].reason`），不是编出来的形状。反向用例同样重要：`RATE_LIMIT_EXCEEDED` / `RESOURCE_EXHAUSTED` / `QUOTA_EXCEEDED` 断言为**不分类** —— 防止有人日后顺手把它们映射成 `rate_limit`，那会让 `IsSafeToReplay` 重放付费的图像/视频创建。
- probe 与集成测试新增 rerank 覆盖，断言落在**排序结果**而非「有没有返回」：相关文档放在候选的最后一条，一个原样返回输入顺序的坏实现无法蒙混过关。同时给两处模型表加了守卫（`TestRerankModelsCoverRerankers` / `TestLiveEmbedModelsCoverEmbedders` / `TestLiveRerankModelsCoverRerankers`）—— 写守卫时牵出集成测试的 embed 表还漏了 mistral / minimax / dashscope / together / fireworks / ollama 六家，它们的 embeddings 声明从来没被真实验证过，一并补上。

### 实测验证

这一版的每项能力声明都跑过真实 API，不只是 mock：

| 验证项 | 结果 |
|---|---|
| `llmkit-probe gemini` | 模型列表 45 个 · Embeddings `gemini-embedding-001` 3072 维 · 错误分类 `400 → IsAuthError` |
| `llmkit-probe siliconflow` | Rerank `BAAI/bge-reranker-v2-m3` · Embeddings `BAAI/bge-m3` 1024 维 · 错误分类 `401 → IsAuthError` |
| `TestLiveEmbeddings/gemini` | 3072 维 |
| `TestLiveRerank/siliconflow` | `top: index=2 score=0.9906` —— 相关文档排在输入最后一条，被正确判为最相关 |

### 已知问题

- `internal/logging.Enabled` 在默认（未装 logger）路径上对 Info/Warn/Error 返回 true，使 `provider.StreamDiagnostics.Malformed` 的性能守卫失效 —— 容错模式下每个畸形帧多一次 `TruncateForLog` 拷贝。只影响性能，不影响正确性。自 v0.2.0 起未变。**（已在 v0.3.1 修复。）**
- `Models()` 对 gemini 返回的对话模型与 embedding 模型混在一起，`RemoteModel` 无类型字段可供区分。见「修复」一节的权衡。
- rerank 只有 siliconflow 一家实现。Cohere 形状的 usage（`meta.billed_units.search_units`）已按契约实现并测试，但**没有厂商实测过** —— 接 Cohere / Jina 时值得先验一遍。

---

## v0.2.0 — 2026-07-30

这一轮针对的是「生产网关代码公开成 SDK」遗留的三类问题：花钱的调用被盲目重放、能力探测说谎、测试偷偷联网。同时厂商从 13 家扩到 21 家。

### 从 v0.1.0 升级

多数调用方不用改代码。按这张表自查，左列命中了才需要动：

| 你的代码里如果有 | 升级后会怎样 | 怎么改 |
|---|---|---|
| 依赖 `GenerateImage` / `CreateVideo` 失败后自动重试 | 只重试能证明「上游没接活」的错误 | 要旧行为：`WithMediaRetry(llmkit.DefaultRetry())` |
| 流式读取时依赖畸形帧被静默跳过 | 畸形帧直接让流失败 | 要旧行为：`WithStreamTolerance(llmkit.TolerateMalformedChunks)`，配 `WithLogger` 才看得到跳过记录 |
| `SupportsImages()` / `SupportsVideo()` | 仍可用，已废弃 | 换成 `SupportsImageGeneration()` / `SupportsImageEditing()` / `SupportsVideoGeneration()` / `SupportsVideoCancellation()` |
| `ImageGenerationRequest.Delivery`、`ImageEditRequest.Delivery` | **编译失败** | 删掉即可 —— 它本来就没有任何效果；读返回的 `MediaAsset` 判断拿到的是 URL 还是 base64 |
| `provider.Registry` | **编译失败** | `llmkit.New(name)` / `llmkit.Providers()` |
| `provider.FrameJSON` | **编译失败** | 改名为 `provider.FramePayload` |
| 自己写的 adapter 调 `provider.NewStreamReader` | **编译失败** | 加 `ctx` 作首参 |
| 依赖公共类型上的 `binding:"required"` tag | tag 没了 | 必填字段改看文档注释 |
| Go 运行时低于 1.22 | 编译失败 | `go.mod` 声明的下限是 1.22，CI 有一个 1.22 的 job 守着 |

**编译能过，就只剩三条行为变化需要留意**（媒体重试、流式严格、能力探测），而且每条都有一行开关能恢复旧行为。

三条里最该确认的是第一条：如果你的代码依赖「图像/视频创建失败会自动重试」，现在它只在能证明上游根本没受理时才重放 —— 这正是它存在的理由（旧行为会重复计费），但如果你的上游有幂等保证、原本就靠重试兜底，需要显式把它调回来。

### 破坏性变更

**媒体创建不再走通用重试策略。** `GenerateImage` 和 `CreateVideo` 成功即计费，而厂商没给幂等键，此前它们和普通对话一样默认重试 3 次 —— 上游已经受理但响应链路断掉时，会生成第二张图 / 第二个视频任务，并照单计费。现在它们只重试能证明「上游根本没接活」的错误（见新增的 `IsSafeToReplay`）。要恢复旧行为：`WithMediaRetry(llmkit.DefaultRetry())`。

**流式默认严格。** 解析不了的 SSE 帧此前被静默跳过并写进全局 `slog`，调用方会拿到一个看起来完整、实际少了一段内容或少了一次工具调用的回复。现在这种帧直接让流失败。要恢复旧行为：`WithStreamTolerance(llmkit.TolerateMalformedChunks)`，配合 `WithLogger` 才能看到跳过记录。

**能力探测拆细，旧方法标记废弃。** `SupportsImages()` 和 `SupportsVideo()` 无法表达「能生成但不能编辑」「能建任务但不能取消」，而这正是多数聚合类服务的实际情况。两者仍可用（等价于对应的 generation 方法），但请改用：

| 旧 | 新 |
|---|---|
| `SupportsImages()` | `SupportsImageGeneration()` / `SupportsImageEditing()` |
| `SupportsVideo()` | `SupportsVideoGeneration()` / `SupportsVideoCancellation()` |

相应地，几个 adapter 删掉了「只返回 `ErrUnsupported` 的空方法」—— 那种方法能满足接口，让类型断言和能力探测一起说谎。vercel / openrouter 不再实现 `EditImage`，gemini / openrouter / easyrouter / dashscope 不再实现 `CancelVideoJob`。它们本来就调不通，区别只是现在**调用前**就能问出来。

**删除 `ImageGenerationRequest.Delivery` 和 `ImageEditRequest.Delivery`。** 描述的是网关如何存储和交付资产（`proxy` 存下来给个链接 vs `inline` 塞 base64），没有任何 adapter 读它 —— 设了完全没有效果。响应里给什么由厂商决定，读返回的 `MediaAsset` 即可。

**删除 `provider.Registry`。** 零引用，且内部 map 无锁并非并发安全。SDK 侧用 `llmkit.New(name)` 和 `llmkit.Providers()`。

**`provider` 包的 `NewStreamReader` 加了 `ctx` 首参**（compat / gemini / anthropic / openrouter 四处）。只影响自己写 adapter 的人；ctx 用于取出流策略和 logger，读一次即用完，不会被持有。

**去掉公共类型上的 `binding:"required"` tag。** gin 的 validator tag，SDK 用户不用 gin。必填字段改为写在文档注释里。

**`go.mod` 从 `go 1.25.6` 降到 `go 1.22`。** 1.22 是实测的真实下限（`examples/` 用到整数 range）。CI 增加一个 1.22 的构建 job，防止将来无意中抬高门槛。

**`provider.FrameJSON` 改名为 `provider.FramePayload`。** 只影响自己写 adapter 的人（四个内建 reader 都只用 `FrameDone` / `FrameSkip`）。旧名字是错的：这个分支收的是「非空协议载荷」，包括根本不可能是 JSON 的字节 —— 正因为要让严格模式报出厂商数据损坏，而不是静默丢掉。名字暗示「已经验证过是 JSON」，会引导 adapter 作者跳过错误处理。

### 新增

**厂商从 13 家扩到 21 家，并补上 DashScope / Volcengine 缺失的对话能力。**

- **`llmkit.DashScope` 和 `llmkit.Volcengine` 不再是「仅视频」。** 通义千问和豆包是国内调用量第一梯队，此前这两个 adapter 只接了视频端点，`Chat` / `ChatStream` 一律 `ErrUnsupported` —— 名单里有、却调不了对话。现在对话和模型列表走各家的 OpenAI 兼容端点（百炼 `/compatible-mode/v1`，方舟就是 `/api/v3` 本身），视频仍走原生异步任务端点。embeddings 只有百炼跟着接了（`text-embedding-v4`）；方舟的没接，原因见「修复」一节。`SupportsChat()` 对这两家从 false 变 true，`NonChatProvider` 随之没有任何内建实现了（接口保留：下一个只接了单一端点的厂商还会用到）。
- `llmkit.XAI`（Grok）、`llmkit.Mistral` —— 两家主流前沿模型此前完全缺席。
- `llmkit.Groq` / `llmkit.Together` / `llmkit.Fireworks` / `llmkit.Cerebras` —— 托管开源权重模型的推理平台。
- `compat.NoEmbeddings` —— 与 `compat.Provider` 只差一件事的包装：对话、流式、模型列表照常代理，`Embeddings` 完全不实现，因此不满足 `provider.Embedder`。Go 的方法提升没法选择性关闭，内嵌 `compat.Provider` 就一定会把 `Embeddings` 提升上来，所以抽出这个类型专门用来「不提升」。哪家上线了 OpenAI 形状的 embeddings，把它的 `New` 从 `compat.NewNoEmbeddings` 改回 `compat.New` 即可。
- **`minimax` 的 embeddings 接上了**，手写翻译层而非白拿 compat 的实现。MiniMax 的路由不是 OpenAI 形状（请求要 `texts` 而非 `input`、必填 `type`、响应是顶层 `vectors`、还会在 HTTP 200 下用 `base_resp.status_code` 报错），但**批量语义是对得上的** —— N 条进、N 个向量出、顺序一致 —— 所以翻译是忠实的，不是把一种操作硬掰成另一种。

  几个刻意的取舍：`type`（`db`/`query`，决定这批文本是入库还是检索用，同一段文字两种用法向量不同）在统一请求里没有对应字段，走 `ProviderOptions`，默认 `db`；`GroupId` 是账号配置而不是请求参数，同样走 `ProviderOptions`，且**只在传了才拼进 query**，国际端点因此保持干净；`Dimensions` 和 `EncodingFormat` **报错而不是被忽略** —— embo-01 向量宽度固定，你要了 256 维却静默拿到 1536 维是察觉不到的；向量解码成 `[]any` 而不是 `[]float64`，与所有 compat provider 保持一致（`llmkit-probe` 和调用方断言的就是 `[]any`）；`type` 的取值在本地校验而不是转发给上游 —— 这是对本项目「让调用方看厂商自己的报错」惯例的**有意例外**，因为取值集合是封闭的两个值，万一上游把无法识别的 type 当默认值处理，一个拼写错误就会静默降低检索质量，本地响亮报错好过远端默默耸肩。

  **`volcengine` 没有跟着接**：它的现行端点把整批输入融合成单个向量，是另一种操作而非另一种拼写，硬接需要 1→N 的请求放大。原因写在「修复」一节它那条里。
- `llmkit.Ollama` / `llmkit.VLLM` —— 本地与自建运行时。
- `WithoutAPIKey()` + 本地运行时的免 key 构造 —— Ollama / vLLM 默认无鉴权，此前 `New` 一律要求凭据，本地部署根本构造不出 Client。现在这两家不给 key 也能构造，给了照发（vLLM 起了 `--api-key` 的情况）；其余厂商仍然缺 key 即 `ErrNoAPIKey` —— 对真实厂商来说空 key 是配置漏了，不是一种模式，静默放行只会换来一个离病因很远的 401。自建的无鉴权网关用 `WithoutAPIKey()` 显式声明。
- 凭据为空时不再发凭据头。此前会发出字面量 `Bearer `，那不是一个有效凭据，挡在本地运行时前面的代理可能直接拒掉。统一走新增的 `provider.SetBearer` / `provider.SetKeyHeader`，覆盖每一条路由和每种头名（含 anthropic 的 `x-api-key`、gemini 的 `x-goog-api-key`），而不只是对话路由。
- `llmkit.KeyOptional(name)` —— 问某家是否免 key 构造。导出的原因是包外工具需要它：`llmkit-probe` 的目标选择就是「这家配了 key 吗」，不导出就只能把名单硬编码一遍，或者干脆探测不了免 key 的那两家。

- `IsSafeToReplay(err)` —— 判断重放会不会重复计费。与 `IsRetryable` 正交：前者问「会不会多花钱」，后者问「重试有没有戏」，两者都为真才会重放。
- `WithMediaRetry(rc)` —— 单独设定媒体创建的重试策略。
- `WithTransport(rt)` —— 替换底层 `http.RoundTripper`，用于自定义 TLS、连接池、埋点。你的 transport 在链路最底层：凭据头和调用方附加头已经加好、客户端 IP 头已经剥掉，才轮到它，所以埋点用的 transport 无法绕开隐私处理。
- `WithLogger(l)` —— 接收 SDK 的非致命诊断。**默认全静默**，库不该往宿主程序的日志里写东西。
- `WithRequestID(fn)` / `WithRequestIDHeader(name)` —— 每个出站请求打一个自己的 ID（重试的每次尝试都是新 ID）。
- `APIError.RequestID` —— 厂商自己生成的请求 ID，从响应头解析（认 `X-Request-Id`、`Request-Id`、`Cf-Ray` 等多种写法）。报障时厂商要的就是它，而且响应一旦丢弃就再也拿不回来。
- `ProviderCode(err)` / `ErrorCategoryOf(err)` —— 保留厂商体内错误码，并将 HTTP 200 错误归一为 `auth` / `rate_limit` / `invalid_request` / `not_found` / `server`。元数据通过可解包错误附加，不改变稳定的 `APIError` / `provider.ProviderError` 结构；`StatusCode` 仍忠实返回真实 HTTP 状态，`IsAuthError` / `IsRateLimited` / `IsRetryable` 等辅助函数同时理解体内分类。
  认不出的分类会被丢弃而不是照用。分类一旦非空就会短路掉 HTTP 状态判断，所以留着一个拼错的值（`ratelimit` 少个下划线）会让上游的 500 既不算服务器错误也不可重试 —— 一个字符串常量的笔误静默关掉重试，而 `ErrorCategory` 是 string 类型，编译器不会拦。厂商码仍然保留，`ProviderCode` 照常可用，分类交回 HTTP 状态。
  写 adapter 的人要注意 `ErrorCategoryRateLimit` 比另外四类多带一层断言：它同时声明「上游在开始干活之前就拒了，没产生计费」，因为 `IsSafeToReplay` 会据此重放图像 / 视频创建 —— 那是真金白银。HTTP 429 自己就够格；一家返回 200 再在体内报限流的厂商不自动够格，除非厂商文档写明这种响应不计费。拿不准就别分类：留空则由 HTTP 状态决定，而 200 既不可重试也不可重放，是安全的那一侧。该约束写在常量的文档注释上。
- `WithStreamTolerance(t)` / `WithMaxStreamFrameBytes(n)` —— 流式容错策略与单帧上限（默认仍是 1 MiB）。
- `provider.ImageGenerator` / `ImageEditor` / `VideoCreator` / `VideoCanceller` —— 按端点切分的能力接口。`ImageProvider` / `VideoProvider` 保留为二者的组合。
- `Client.SupportsChat()` 和 `provider.NonChatProvider` —— 加这两个东西的起因是：DashScope 和 Volcengine **当时**只接了视频端点，`Chat` / `ChatStream` 一律返回 `ErrUnsupported`，却无从事先探测（chat 在 `Provider` 接口上，类型断言分辨不出来），README 的能力表还给它们标了「Chat ✅」。本次这两家都接上了对话，所以 `NonChatProvider` 现在没有任何内建实现 —— 接口保留，下一个只接单一端点的厂商还会用到。
- `provider.ClassifyFrame` —— SSE 帧的统一分类（`[DONE]` 哨兵 / 可跳过 / 应解析）。
- `provider.StreamPolicy` / `StreamDiagnostics` —— adapter 作者用来遵守流策略的共享组件。
- `provider` 包新增包文档，写明哪些类型稳定、哪些是厂商专属会增删、哪些是不作保证的 opaque 透传。
- `examples/production` —— 一份可直接拷走的生产配置。此前 `WithTimeout` / `WithRetry` / `WithMediaRetry` / `WithTransport` / `WithLogger` / `WithRequestID` 各自都有文档，但没有一处把它们装在一起，读者得自己拼。示例里每个选项注释的是「不配会出什么事」，并覆盖两个只有踩过才知道的坑：`WithTransport` 会替换掉 SDK 那个设了 `Proxy: http.ProxyFromEnvironment` 的 transport（自建 `&http.Transport{}` 不设的话 `HTTPS_PROXY` 会静默失效），以及 `RoundTripper` 测到的是响应头到达时间而非最后一字节（流式调用在这里毫秒返回，之后才流一分钟）。

### 修复

- `NormalizeUsage` 只兜底了 `RequestCount == 0`，负值原样透传进按次计费的乘法。改为 `< 1` 兜底。（fuzz 发现）
- `IsRetryable` 里判断网络错误的第二个分支是死代码 —— `*net.OpError` 和 `*net.DNSError` 都实现了 `net.Error`，第一个分支总会先命中。实际行为（只重试超时）没变，但代码现在如实表达它，注释也不再描述一个不存在的行为。
- 单帧上限低于 64 KiB 时不生效：`bufio.Scanner` 的最大 token 取 `max(max, cap(buf))`，而初始缓冲固定 64 KiB。（fuzz 目标写出来后立刻暴露）
- `Chat` / `ChatStream` / `Models` / `Embed` 漏了 `ErrUnsupported` 的归一化，adapter 抛出的 `provider.ErrUnsupported` 到调用方手里 `errors.Is(err, llmkit.ErrUnsupported)` 是 false —— 而 README 的错误处理示例正是这么写的。只有媒体路径做了翻译。
- `probe` 会先用一次 chat 调用验活，对纯视频厂商必然失败，导致整份报告变成 setup 错误，连它们真正支持的视频能力都测不到；`-list` 还会给它们显示一个不存在的默认对话模型。
- README 的「尚未覆盖」表还写着自定义 `Transport` 用不了，而同一份 README 上面就在演示 `WithTransport` —— 该行早已过时，删掉。
- `WithoutAPIKey()` 承诺「不发凭据头」，但只有走 compat 的对话路由做到了。`openai.ListModels` / openai 图像 / openrouter / easyrouter / anthropic / gemini 仍无条件拼接凭据，于是这个选项的**主要用例**（内网无鉴权网关）一调 `Models()` 就发出畸形的 `Authorization: Bearer `。
- `WithoutAPIKey()` 仍会回退读取环境变量，甚至会被后置的 `WithAPIKey` 填回凭据，导致真实厂商 key 被发送给原本声明无鉴权的内网网关。现在它是顺序无关的强抑制：环境变量和显式 key 都不会发送。这件事倒转了 Option 通常的「后者胜」读法，所以压掉一个**显式** `WithAPIKey` 时会经 `WithLogger` 报一条 warn（不含凭据本身）；环境变量被压掉不报，那是这个选项的主要用例，报了就是噪音。`New` 和 `Wrap` 两处的凭据判定也收敛到一个 `resolveCredential`：这里两个构造函数漂移就是凭据泄漏，不是风格不一致。
- 严格流模式此前只尝试解析 `{` 开头的帧，`data: garbage` 会被当成 keep-alive 静默跳过。现在只跳过空帧 / `null` / 空字符串 / `[DONE]` / 被中继重包的 SSE 注释，其余非空 payload 都进入解析并按严格/容错策略处理。
- MiniMax embeddings 的 `base_resp.status_code` 错误虽然会返回 `APIError`，但 HTTP 状态仍是 200，导致鉴权、限流和重试分类全部失效。现在同时保留真实 HTTP 状态、厂商错误码和归一分类。
- 请求 ID 提取补上 Google 的 `X-Guploader-Uploadid`、OpenRouter 的 `X-Generation-Id` 与 MiniMax 的 `trace_id` / `Trace-Id` / `Minimax-Request-Id`，MiniMax 体内 `trace_id` 也作为响应头缺失时的兜底；同步修正 xAI Live Search 和 `VideoCanceller` 的失真注释。
- 严格流模式继续拒绝未知非 JSON `data:`，但放行空白、`null`、`[DONE]` 和被中继重包成 `data:` 的 SSE 注释；避免修复 `data: garbage` 静默丢帧时，把 OpenRouter 的保活帧变成流中断。**放行条件是「载荷以 `:` 开头」，不是一张已知 ping 文案的白名单。** 白名单只认了 `: ping` 这种冒号后带空格的写法，而 SSE 语法里那个空格是可选的 —— `data: :ping` 同样合法且中继会发，却会被判成厂商数据损坏，在默认严格模式下中断整条流。改用前缀判断是完备的：合法 JSON 值不可能以 `:` 开头，所以放行注释不会吞掉真实数据，也不再让某家中继的保活措辞在不在名单上决定流能不能跑完。
- `llmkit-probe` 探测不了 ollama / vllm：具名探测强制要求非空 key，无 key 直接退出；`-list` 也永远不给它们打标记。本次新增的两家免 key provider，恰好用不了仓库里唯一的实测工具。无参数的「探测全部」模式仍然跳过它们（没法知道本地服务是否起着），但按名字探测现在能用了，`-list` 用 `○` 标出免 key。
- 集成测试的 `liveModels` 没跟着扩，8 家新 provider 全部落空 —— `liveModel()` 返回 `""`，`TestLive` 会带 `Model: ""` 发请求，再把上游的拒绝报成对话失败。同时 moonshot 的模型 ID 在 probe 那张表里已更新到 `kimi-k2.6`、在集成测试里还是下线了的 `kimi-k2-turbo-preview`。两张平行的表现在都补齐，并各有一条守卫测试。
- `.env.example` 缺 8 个新厂商的环境变量名。`llmkit-probe` 从 `.env` 读 key，这里漏了直接卡住上手路径。
- `mistral` adapter 的 `PrefillFieldName` 留空，注释还写着「Mistral 没有 prefill 字段」。实际上 Mistral 支持 assistant 消息上的 `prefix: true`（与 DeepSeek 同形），此前 `Message.Prefix` 被静默丢掉。
- `render.go` 的注释说「没配 embedding 模型的 provider 报 N/A」，实际返回的是 SKIP —— 而 probe 的输出约定里 N/A 表示「厂商不支持，不是缺陷」、SKIP 表示「没尝试」，说反了方向。
- **五家 provider 的 `SupportsEmbeddings()` 在说谎，全部改为不声明**（走 `compat.NoEmbeddings`）。逐家核对了厂商文档，原因各不相同：
  - `xai` —— API 参考只有 chat / responses / deferred-completion，没有 embeddings 路由。
  - `groq` —— API 参考有 chat / audio / models / batches / files / fine-tuning，没有 embeddings。
  - `cerebras` —— 无 `/embeddings`，实测返回 404 而非 401。
  - `moonshot` —— Kimi 开放平台只有 chat / models / tokenizers / balance / files，没有 embeddings 接口（`platform.kimi.ai` 的 API 总览与 `llms.txt` 索引都没有）。**这是既有 bug**，不是本次新增的厂商。
  - `volcengine` —— 方舟的**文本** embeddings API（`/api/v3/embeddings`，`doubao-embedding-text-*`）已进入官方「下线文档归档」，当前模型列表（2026-07-20 更新）只剩多模态 `doubao-embedding-vision-*`，服务在 `/api/v3/embeddings/multimodal`。**而这个端点不是同一个操作**：它的 `input` 是**一条内容的各个部分**（文本 / 图片 / 视频）融合成**一个**向量，响应的 `data` 是单个对象而不是数组；`Embed` 的契约却是「N 条输入 → N 个向量，`Data[i]` 对应 `Input[i]`」。硬接就得把一次 `Embed` 扇出成 N 个 HTTP 请求，100 个 chunk 变成 100 次计费 —— 这种成本悬崖不该藏在一个回答「支持」的能力探测后面。多模态向量化值得单独一套接口（融合输入本来就是它的卖点），adapter 注释里记了这个结论。本次刚把它从「仅视频」改成接 compat 时顺手把 embeddings 也带上了，是本次引入的。

  `dashscope` 的 embeddings 核对后确认可用（`/compatible-mode/v1/embeddings` + `text-embedding-v4`，标准 OpenAI 形状），保留。`minimax` 的核对结论是「形状不对但语义对得上」，所以走了手写翻译层而不是摘掉能力，见「新增」一节。

### 测试

- **默认测试现在真的完全离线。** `TestUntestedAdapters_DefaultBaseURLs` 此前会向 minimax / siliconflow / vercel 的官方地址发真实请求，README 里「所有默认测试都是离线的」并不成立。改为在已取消的 context 下发起请求，从 `*url.Error` 里取出目标 URL —— 不碰网络，断言反而更强（精确比对完整端点，而不只是「不是畸形 URL」）。`provider` 包测试耗时从 3.4s 降到 1.25s。可验证：`HTTPS_PROXY=http://127.0.0.1:1 go test ./...` 全绿。
- EasyRouter 的 live 测试补上 `integration` build tag（此前靠环境变量守卫，实际不会联网，但与仓库其余 live 测试的约定不一致）。
- 新增 14 个 fuzz 目标，覆盖 SSE 帧解析、错误响应解析、多模态内容转换，CI 每次 PR 跑一轮短的，崩溃输入作为 artifact 上传。
- 能力矩阵测试改为逐端点断言，并新增一条：能力探测的结果必须与实际调用行为一致。
- 凭据头的两个方向都钉住了：`WithoutAPIKey()` 下 openai / anthropic / openrouter / gemini / deepseek 的 `Models()` 一个凭据头都不发；给了 key 则每条都照发。只测前者是不够的 —— 把空头压掉的同时压掉真头，测试一样绿。
- 新增环境变量、显式 key 与 Option 正反顺序组合测试，保证 `WithoutAPIKey()` 始终强制不发凭据；新增非 JSON SSE 帧的严格/容错双向测试；新增 MiniMax 体内鉴权/限流错误穿过 Client 门面后的分类测试。
- 凭据抑制的 warn 也是双向断言的：压掉显式 key 必须报出来、且日志里不能出现被压掉的凭据；压掉环境变量必须完全安静。
- 被中继重包的 SSE 注释补上三条用例：冒号后无空格（`data: :ping`）、无空格且带文案（`data: :OPENROUTER PROCESSING`）、以及一条谁都没登记过的保活措辞。最后一条是防回归的重点 —— 它保证放行条件不会退回成一张文案白名单。
- `minimaxErrorCategory` 的错误码表改成双向守卫。此前实现里 11 个 `invalid_request` 码只有 3 个进了测试表。`switch` 没法反射枚举，所以反向断言用扫码空间实现：任何被分类却没登记在 `classifiedCodes` 里的码都会让测试失败（已实测这条守卫抓得住）。
- `TestUntestedAdapters_*` 从 3 个 adapter 扩到 11 个，8 家新 provider 全部覆盖默认端点、路径拼接、错误透传与能力断言。默认端点是这种薄封装最容易错的地方：groq 在 `/openai/v1`、fireworks 在 `/inference/v1`，都不是 `/v1`。
- 新增 `TestUntestedAdapters_Prefill`，双向断言：支持 prefill 的厂商必须收到字段，不支持的必须一个都收不到（多余的 key 会被严格的 compat 服务直接拒掉）。
- 新增三条防漂移守卫：`TestLiveModelsCoverAllProviders`（集成测试模型表）、`TestChatModelsCoverAllProviders` 与 `TestEmbedModelsCoverEmbedders`（probe 的模型表）。最后一条是双向的：声明了 embeddings 就必须有探测模型或在 `embedModelUnknown` 里写明原因，没声明的也不许有多余条目 —— 否则「声明了能力但从没被探测过」就是 `SupportsEmbeddings()` 开始说谎的方式。
- 新增覆盖全部 provider 的流策略测试（此前只测了 4 家）。测试遍历 `Providers()`，所以本次扩到 21 家后自动跟着覆盖。写出来当场抓到一个自伤：`[DONE]` 哨兵和非 JSON 帧的过滤在四个 reader 里各写了一份，gemini 和 anthropic 那两份缺失，于是「流式改严格」把中转站补的 `data: [DONE]`、以及合法的空 `data:` 行也判成了厂商数据损坏。四份实现已收敛到 `provider.ClassifyFrame`。
- **`internal/safehttp` 的 SSRF 拦截此前一行没测**（44.6% → 96.4%）。旧测试的三个用例全是纯函数（`isBlockedIP` / `ValidateImageBytes` / `schemeAllowed`），没有一条走过 `FetchImage`。也就是说「IP 黑名单判得对」有断言，而「黑名单真的接在了拨号路径上」没有 —— 那才是这个包存在的理由。判定函数全对但 `Dialer.Control` 没挂上去，旧测试一样全绿。新增的 `TestFetchImage_DialerBlocksLoopback` 对一个真实 `httptest` 监听器发起请求并断言 `ErrBlockedIP`，且刻意放开 scheme 限制，好让失败只可能来自 `Dialer.Control`。其余补齐：大小上限的两条路径（声明的 `Content-Length` 与 chunked 下的实际字节，含边界值恰好等于上限）、重定向跳数上限、https→http 的降级重定向、Content-Type 伪造、body 读取中断、请求构造失败。
- **`provider/vercel` 从 0% 到 99%**，README 里挂了一轮的「已知空白」清掉。243 行手写的图像层此前完全没有断言：请求字段映射、`stream=true` 必须在发 HTTP 之前就被拒（否则就是为一次被丢弃的响应付费）、201 视同成功、`ProviderOptions` 只透传 `vercel` 命名空间且返回副本。同时补上 `vercel.go` 一侧的 `normalizeBaseURL`、`ListModels` 的过滤规则与 `vercelModelImportable` 的按计费方式分流。其中 `TestVercel_CapabilitySurface` 在运行期断言该类型**不**满足 `provider.ImageEditor` —— 它嵌了 `*compat.Provider`，将来给 compat 加一个 `EditImage` 会被自动提升上来，悄悄让 `SupportsImageEditing()` 开始说谎。
- **`internal/logging` 0% → 100%。** 覆盖 ctx 往返、nil logger 不得覆盖已设值、零值 ctx 不得 panic、嵌套时内层优先，以及 discard 是共享单例（它在每个流式 chunk 上被查）。顺带钉住一处文档与实现的落差，**它现在正让一处真实的守卫失效**：`Enabled` 的注释说它可以用来「在默认静默路径上跳过构建昂贵的日志参数」，但 discard 用的是 `slog.NewTextHandler(io.Discard, nil)`，`nil` 选项把级别默认成 Info，于是 Info/Warn/Error 三档都返回 true。`provider.StreamDiagnostics.Malformed` 正是用这个条件守着它的 warn（注释写着「skip it when nothing is listening — which is the default」），而它的 logger 来自 `logging.From(ctx)`：没装调用方 logger 时那就是 discard，守卫恒为真，容错模式下每个畸形帧都会照跑一次 `TruncateForLog(payload, 200)` —— 正是注释声称省掉的那次拷贝。修法在 `internal/logging` 而不在调用点：discard 需要一个 `Enabled` 恒假的 handler（级别提到 Error 以上，或自定义一个；`slog.DiscardHandler` 可以但要 Go 1.24，而 `go.mod` 声明的是 1.22）。本轮只钉行为不改实现，测试注释里写明了修法与届时该翻哪半边断言。

### 已知问题

- `internal/logging.Enabled` 在默认（未装 logger）路径上对 Info/Warn/Error 返回 true，使 `provider.StreamDiagnostics.Malformed` 的性能守卫失效 —— 容错模式下每个畸形帧多一次 `TruncateForLog` 拷贝。只影响性能，不影响正确性；详见上面「测试」一节最后一条。
- `provider/vercel` 不实现 `EditImage`：Vercel AI Gateway 没有图像编辑端点（厂商侧确认，2026-05）。`SupportsImageEditing()` 如实返回 false。
- `provider/volcengine` 不实现 `Embeddings`：方舟现行端点是多模态融合向量，与 `Embed` 的「N 进 N 出」契约不是同一个操作。详见「修复」一节。

---

## v0.1.0 — 2026-07-25

首个公开版本：13 家厂商的统一 Go SDK，零第三方依赖，只用标准库。

对话 / 流式 / 模型列表 / embeddings / 图像 / 视频六类端点收在一套 OpenAI 兼容接口下，换厂商只改一个常量。

> 这个版本存在上面 v0.2.0「破坏性变更」一节列出的全部问题 —— 尤其是**图像和视频创建失败后会盲目重放，可能重复计费**，以及能力探测会对实际调不通的端点回答「支持」。不建议继续使用。
