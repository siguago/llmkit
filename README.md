# llmkit

[![CI](https://github.com/siguago/llmkit/actions/workflows/ci.yml/badge.svg)](https://github.com/siguago/llmkit/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/siguago/llmkit.svg)](https://pkg.go.dev/github.com/siguago/llmkit)
[![Go Report Card](https://goreportcard.com/badge/github.com/siguago/llmkit)](https://goreportcard.com/report/github.com/siguago/llmkit)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

一套 Go SDK，用同一套 OpenAI 兼容接口调用 21 家主流大模型厂商。需要保留厂商语义时，还可显式使用 OpenAI Responses 与 Anthropic Messages 的原生协议面。**零第三方依赖**，只用标准库。

```go
client, _ := llmkit.New(llmkit.DeepSeek)          // key 取自 DEEPSEEK_API_KEY
answer, _ := client.Say(ctx, "deepseek-v4-flash", "用一句话解释 CAP 定理。")
```

统一 Chat 接口里，换一家厂商只改一个常量，请求结构、响应结构、错误处理、流式读法全都不变。原生协议面不会按模型名自动启用，也不与 Chat DTO 混用。

国内厂商（DeepSeek / Kimi / 智谱 / MiniMax / 硅基流动 / 通义 / 豆包）与海外厂商
（OpenAI / Anthropic / Gemini）以及聚合渠道（OpenRouter / Vercel AI Gateway）都在一套接口下，
`HTTPS_PROXY` 开箱可用。

> 这套适配层来自一个跑在生产上的自用网关，被抽出来独立维护 —— 各家的 thinking 开关差异、
> Anthropic 的 thinking signature 回传、Gemini 的 thought_signature、Kimi K2.6 的
> `chat_template_kwargs`、OpenAI reasoning 模型的参数禁用清单等等，都是真实撞过的坑。

---

## 安装

```bash
go get github.com/siguago/llmkit
```

需要 Go 1.22+。

---

## 支持的厂商

| 常量 | 厂商 | 环境变量 | 实现方式 |
|------|------|----------|----------|
| `llmkit.OpenAI` | OpenAI | `OPENAI_API_KEY` | compat Chat + 显式原生 Responses |
| `llmkit.Anthropic` | Anthropic Claude | `ANTHROPIC_API_KEY` | Chat 翻译层 + 显式原生 Messages |
| `llmkit.Gemini` | Google Gemini | `GEMINI_API_KEY` | 原生 generateContent |
| `llmkit.XAI` | xAI Grok | `XAI_API_KEY` | compat |
| `llmkit.Mistral` | Mistral AI | `MISTRAL_API_KEY` | compat |
| `llmkit.DeepSeek` | DeepSeek | `DEEPSEEK_API_KEY` | 原生 |
| `llmkit.Moonshot` / `llmkit.Kimi` | 月之暗面 Kimi | `MOONSHOT_API_KEY` | compat + K2.6 thinking 适配 |
| `llmkit.Zhipu` | 智谱 GLM | `ZHIPU_API_KEY` | compat |
| `llmkit.MiniMax` | MiniMax | `MINIMAX_API_KEY` | compat |
| `llmkit.SiliconFlow` | 硅基流动 | `SILICONFLOW_API_KEY` | compat |
| `llmkit.DashScope` | 阿里百炼 Qwen | `DASHSCOPE_API_KEY` | 原生视频 + compat 对话 |
| `llmkit.Volcengine` | 火山方舟 豆包 | `VOLCENGINE_API_KEY` | 原生视频 + compat 对话 |
| `llmkit.Groq` | Groq | `GROQ_API_KEY` | compat |
| `llmkit.Together` | Together AI | `TOGETHER_API_KEY` | compat |
| `llmkit.Fireworks` | Fireworks AI | `FIREWORKS_API_KEY` | compat |
| `llmkit.Cerebras` | Cerebras | `CEREBRAS_API_KEY` | compat |
| `llmkit.Ollama` | Ollama（本地） | `OLLAMA_API_KEY`（可选） | compat |
| `llmkit.VLLM` | vLLM（自建） | `VLLM_API_KEY`（可选） | compat |
| `llmkit.OpenRouter` | OpenRouter（聚合） | `OPENROUTER_API_KEY` | 原生 |
| `llmkit.EasyRouter` | EasyRouter（聚合） | `EASYROUTER_API_KEY` | compat |
| `llmkit.Vercel` | Vercel AI Gateway（聚合） | `VERCEL_AI_GATEWAY_KEY` | compat |

**本地部署**：Ollama 和 vLLM 默认无鉴权，`llmkit.New(llmkit.Ollama)` 不给 key 也能构造；给了就照发（vLLM 起了 `--api-key` 的情况）。其余厂商仍然「缺 key 即报错」——对真实厂商来说，空 key 是配置漏了，不是一种模式。自建的无鉴权网关用 `WithoutAPIKey()`，任何一条路由都不会发凭据头（包括 anthropic 的 `x-api-key`、gemini 的 `x-goog-api-key`）。用 `llmkit.KeyOptional(name)` 可以问某家是否免 key。

实测这两家：`go run ./cmd/llmkit-probe ollama`。**按名字探测**——「探测所有已配置厂商」的无参数模式会跳过它们，因为没有任何迹象能说明本地服务是否起着。

### 能力矩阵

不是每家都实现全部能力。调用前用 `Supports*` 探测，或直接处理 `ErrUnsupported`。

能力按**端点**而不是按功能划分，因为厂商的支持就是按端点参差的：聚合类服务能生成图像却没有编辑端点，五家能建视频任务但只有一家能取消。

| 厂商 | Chat / 流式 | 模型列表 | 模型任务 | Embeddings | Rerank | 图像生成 | 图像编辑 | 视频生成 | 视频取消 |
|------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| openai | ✅ | ✅ | — | ✅ | — | ✅ | ✅ | — | — |
| anthropic | ✅ | ✅ | ✅ | — | — | — | — | — | — |
| gemini | ✅ | ✅ | ✅ | ✅ | — | ✅ | ✅ | ✅ | — |
| xai | ✅ | ✅ | — | — | — | — | — | — | — |
| mistral | ✅ | ✅ | — | ✅ | — | — | — | — | — |
| deepseek | ✅ | ✅ | ✅ | — | — | — | — | — | — |
| moonshot | ✅ | ✅ | — | — | — | — | — | — | — |
| zhipu | ✅ | ✅ | — | ✅ | — | — | — | — | — |
| minimax | ✅ | ✅ | — | ✅ | — | — | — | — | — |
| siliconflow | ✅ | ✅ | — | ✅ | ✅ | — | — | — | — |
| dashscope | ✅ | ✅ | — | ✅ | — | — | — | ✅ | — |
| volcengine | ✅ | ✅ | — | — | — | — | — | ✅ | ✅ |
| groq | ✅ | ✅ | — | — | — | — | — | — | — |
| together | ✅ | ✅ | — | ✅ | — | — | — | — | — |
| fireworks | ✅ | ✅ | — | ✅ | — | — | — | — | — |
| cerebras | ✅ | ✅ | — | — | — | — | — | — | — |
| ollama | ✅ | ✅ | — | ✅ | — | — | — | — | — |
| vllm | ✅ | ✅ | — | ✅ | — | — | — | — | — |
| openrouter | ✅ | ✅ | ✅ | — | — | ✅ | — | ✅ | — |
| easyrouter | ✅ | ✅ | — | ✅ | — | ✅ | ✅ | ✅ | — |
| vercel | ✅ | ✅ | ✅ | ✅ | — | ✅ | — | — | — |

对应的探测方法：`SupportsChat` / `SupportsModels` / `SupportsModelTaskTypes` / `SupportsEmbeddings` / `SupportsRerank` / `SupportsImageGeneration` / `SupportsImageEditing` / `SupportsVideoGeneration` / `SupportsVideoCancellation`。

模型列表可能混合对话、向量、图片和视频模型。对能可靠分类的目录，用 `ModelsWithTaskTypes` 一次请求同时取得列表和逐模型任务；缺失的 map key 表示“未知”，不能默认成 chat：

```go
models, tasks, err := client.ModelsWithTaskTypes(ctx)
for _, model := range models {
    fmt.Println(model.ModelID, tasks[model.ModelID])
}
```

`Models` 仍保留原来的上游请求和过滤结果；混合媒体目录的扩展只发生在 `ModelsWithTaskTypes`。不实现 `ModelTaskLister` 的 adapter 会由 `ModelsWithTaskTypes` 返回普通列表和 `nil` task map。Anthropic 与 DeepSeek 的目录都没有逐模型能力字段，只能按 ID 判断，但两家的目录形状不同：Anthropic 至今只出对话模型，按 `claude-` 前缀分类即可，非 `claude-` 条目保持未知；DeepSeek 的目录里混着退役别名和非对话型号，因此走白名单，只认当前官方 Chat ID，退役和未来 ID 保持未知。OpenRouter 中显式属于 SDK 未实现端点的目录项会被过滤；纯 `image` 输出型号虽然保留在目录中，但在专用 `/images` 路由实现前不会声明图片生成，只有可走现有 chat/completions 的 `text+image` 型号才会声明。Gemini 的 `generateContent` 也用于图片、TTS、Live/native-audio 和音乐，不能单凭方法名或 `gemini-*` 前缀推断为 chat；只有逐个核验过的当前 model code 会分类，其他条目保持未知。

### 原生协议能力矩阵

原生协议是独立、显式的能力面；这里的 ✅ 不会让 `Chat` / `ChatStream` 改走另一条 endpoint。

| 协议面 | 已接入 transport | 同步 create | SSE stream | 资源生命周期 | Token count |
|---|---|:---:|:---:|:---:|:---:|
| OpenAI Responses | 直连 OpenAI | ✅ | ✅ | ✅ retrieve / delete / cancel / input items | ✅ input tokens |
| Anthropic Messages | 直连 Anthropic | ✅ | ✅ | — | ✅ input tokens |

Responses 的能力按端点分别探测：`SupportsResponses`、`SupportsResponseStreaming`、`SupportsResponseRetrieval`、`SupportsResponseCancellation`、`SupportsResponseDeletion`、`SupportsResponseInputItems`、`SupportsResponseTokenCount`。Anthropic 对应 `SupportsAnthropicMessages`、`SupportsAnthropicMessageStreaming`、`SupportsAnthropicTokenCount`。这能让只实现部分路由的 relay 如实声明能力；当前首发 transport 仍只保证两家官方直连端点。

原生**资源面**同样按端点探测：

| 资源面 | 已接入 transport | 端点 |
|---|---|---|
| OpenAI Files | 直连 OpenAI | upload / list / retrieve / delete / content 全 ✅ |
| OpenAI Batch | 直连 OpenAI | create / retrieve / list / cancel ✅ + JSONL 输入输出 helper |
| Anthropic Message Batches | 直连 Anthropic | create / retrieve / list / cancel / delete / results ✅ |

对应 `SupportsFileUpload` / `SupportsFileListing` / `SupportsFileRetrieval` / `SupportsFileDeletion` / `SupportsFileContentDownload`、`SupportsBatchCreation` / `SupportsBatchRetrieval` / `SupportsBatchListing` / `SupportsBatchCancellation`，以及 `SupportsAnthropicMessageBatchCreation` / `...Retrieval` / `...Listing` / `...Cancellation` / `...Deletion` / `...Results`。Anthropic 的 Files API 上游仍是 beta（`files-api-2025-04-14`），按[1.0 计划](V1_RELEASE_PLAN.md)不接 beta 面，待 GA 后接入。

Anthropic 原生响应按官方 schema 严格校验稳定身份字段、`type=message`、`role=assistant` 和必填 usage 计数器。某些中转站或私有部署会精简这些字段；这种载荷会返回 `anthropicapi.ErrInvalidWire`，不会被补成看似成功的零值消息。非官方端点应先验证同步响应和流式 `message_start` 的实际 wire shape，再声明原生 Messages 能力。

> 支持 OpenAI Responses 核心资源、状态生命周期与 SSE，Anthropic Messages create/stream 和 token count，以及 OpenAI Files、OpenAI Batch 与 Anthropic Message Batches 的资源生命周期。Conversations、WebSocket、Responses compact、云厂商 Claude transport 与部分内置工具专项类型不在声明内；未知 item/block/event 可通过 Raw 形态无损保留。

> 前面的统一 Chat / 媒体能力表由 `TestCapabilityMatrix` 守卫，代码变了测试会先失败。
>
> **dashscope 和 volcengine 一个 adapter 接两套上游 API**：对话和模型列表走各家的 OpenAI 兼容端点（百炼是 `/compatible-mode/v1`，方舟就是 `/api/v3` 本身），视频走各自的原生异步任务端点。给 `WithBaseURL` 传的是**主机根**，兼容路径由 adapter 自己拼。百炼的 embeddings 一并走兼容端点（`text-embedding-v4`）；方舟的不走，原因见下。
>
> **五家不实现 `Embedder`**，都走 `compat.NoEmbeddings`（该类型的用途就是不提升 `Embeddings` 方法），`SupportsEmbeddings()` 如实返回 false，而不是让你调用后才撞上 404。原因各不相同，全部核对过厂商文档：
>
> | 厂商 | 上游实际情况 |
> |---|---|
> | xai | API 参考只有 chat / responses / deferred-completion，没有 embeddings 路由 |
> | groq | API 参考有 chat / audio / models / batches / files / fine-tuning，没有 embeddings |
> | cerebras | 无 `/embeddings`，实测返回 404 而非 401 |
> | moonshot | Kimi 开放平台只有 chat / models / tokenizers / balance / files，无 embeddings |
> | volcengine | 见下，**这一家不是「没有」，是「不是同一个操作」** |
>
> **volcengine 的 embeddings 不接，是因为语义对不上，不是因为字段名对不上。** 方舟的文本 embeddings API（`/api/v3/embeddings`，`doubao-embedding-text-*`）已进入官方「下线文档归档」；当前在线的是多模态 `doubao-embedding-vision-*`，走 `/api/v3/embeddings/multimodal`，它的 `input` 是**一条内容的各个部分**（文本 / 图片 / 视频）融合成**一个**向量，响应的 `data` 是单个对象而不是数组。而 `Embed` 的契约是「N 条输入 → N 个向量，`Data[i]` 对应 `Input[i]`」。硬接就得把一次 `Embed` 扇出成 N 个 HTTP 请求 —— 100 个 chunk 变成 100 次计费。这种成本悬崖不该藏在一个回答「支持」的能力探测后面，所以如实回答不支持。多模态向量化值得单独一套接口（融合输入本来就是它的卖点），不该硬塞进这一个。
>
> **minimax 和 gemini 的 embeddings 是手写的，不是白拿的。** 两家的路由都不是 OpenAI 形状 —— minimax 要 `texts` 而非 `input`、必填 `type`、响应是顶层 `vectors`、还会在 HTTP 200 下用 `base_resp` 报错；gemini 走 `:batchEmbedContents`，每条输入要包成 `content.parts`。但**两家的批量语义都是对得上的**（N 条进、N 个向量出、顺序一致），所以各写了一层翻译，用法见本节下面的「minimax embeddings」和「gemini embeddings」。这正是 volcengine 的反面 —— 那一家不是形状不对，是操作本身就不同。
>
> **vllm 的 Embeddings 是 true，但取决于你起的模型**：vLLM 的 OpenAI server 确实有 `/v1/embeddings`，可一个进程只服务一个模型，只有那是 embedding 模型时才答得上。这是部署问题，不是端点有无的问题，SDK 无从代答。
>
> 哪家上线了 OpenAI 形状的 embeddings，把它的 `New` 从 `compat.NewNoEmbeddings` 改回 `compat.New` 即可。
>
> **除 volcengine 外，视频任务一旦提交就无法中止**，会跑到终态并照常计费 —— 按这个前提设计调用方。

#### minimax embeddings

`type` 决定这批文本是「入库」还是「检索」用 —— 同一段文字两种用法算出的向量不同，配对使用才是模型训练时的意图。它在统一请求里没有对应字段，所以走 `ProviderOptions`，默认 `db`：

```go
resp, err := c.Embed(ctx, &llmkit.EmbeddingRequest{
    Model: "embo-01",
    Input: []string{"天很蓝", "海很深"},
    ProviderOptions: map[string]any{"minimax": map[string]any{
        "type":     "query",   // 省略则为 "db"
        "group_id": "182...",  // 只在你的端点要求时才需要（国内 endpoint）
    }},
})
```

`Dimensions` 和 `EncodingFormat` 会**报错而不是被忽略** —— embo-01 的向量宽度固定，你要了 256 维却拿到 1536 维是察觉不到的。忘了嵌 `"minimax"` 那层 key 同样会报错，而不是静默按默认值发出去。

#### gemini embeddings

同样是手写翻译层：Gemini 的路由不是 OpenAI 形状（走 `:batchEmbedContents`，每条输入包成 `content.parts`），但**批量语义对得上** —— N 条进、N 个向量出、顺序一致 —— 所以翻译是忠实的。

`task_type` 告诉模型这批文本的用途，取值不同向量也不同（`RETRIEVAL_QUERY` / `RETRIEVAL_DOCUMENT` / `SEMANTIC_SIMILARITY` / `CLASSIFICATION` / `CLUSTERING` 等）。统一请求里没有对应字段，走 `ProviderOptions`：

```go
resp, err := c.Embed(ctx, &llmkit.EmbeddingRequest{
    Model: "gemini-embedding-001",
    Input: []string{"天很蓝", "海很深"},
    ProviderOptions: map[string]any{"gemini": map[string]any{
        "task_type": "RETRIEVAL_DOCUMENT",
        "title":     "一篇文档",  // 只在 RETRIEVAL_DOCUMENT 下有意义
    }},
})
```

`Dimensions` 会作为 `outputDimensionality` 转发（能不能用取决于模型）；`EncodingFormat` 只接受 `float`，因为 Gemini 没有 base64 线格式，要 base64 会**报错而不是静默给你 float**。忘了嵌 `"gemini"` 那层 key 同样报错。

与 minimax 的一处刻意差异：**`task_type` 不做本地校验，直接转发**。minimax 的 `type` 只有两个取值且封闭，所以本地拦；Gemini 的取值集合还在扩（`CODE_RETRIEVAL_QUERY` 是后加的），本地白名单会挡掉将来新增的合法值，所以退回本项目的默认惯例 —— 让你看厂商自己的报错。

Gemini 在这个端点不返回 token 计数，所以 `Usage` 只有 `RequestCount`，token 字段是 0 而不是估算值。

#### rerank

重排序是 RAG 的第二段：embeddings 先廉价召回一批候选，reranker 再把 query 和 document 放在一起精确打分。两者互补而非替代 —— reranker 没法检索语料库，embedding 模型也看不到 query 和 document 的交互。

```go
topN := 3
resp, err := c.Rerank(ctx, &llmkit.RerankRequest{
    Model:     "BAAI/bge-reranker-v2-m3",
    Query:     "什么是熊猫？",
    Documents: []string{"苹果是一种水果", "汽车有四个轮子", "熊猫是中国特有的哺乳动物"},
    TopN:      &topN,
})
for _, r := range resp.Results {
    fmt.Println(r.Index, r.RelevanceScore)  // 2 0.98 / 0 0.31 ...
}
```

**返回的不是输入顺序。** 这是统一接口里唯一一处刻意打破位置契约的地方 —— 重排本身就是这个操作的目的。结果按相关性降序，还可能被 `TopN` 截断，所以要用 `Result.Index` 映射回你传进去的 `Documents`。越界的 index 会**报错而不是透传**：那本来会变成你使用时的下标越界 panic。

`RelevanceScore` 的量纲**不跨厂商也不跨模型可比** —— 有的给 0..1 概率，有的给无界 logit。用它排序、用它跟你为那个具体模型调好的阈值比较，别拿去跨模型比。

目前只有 siliconflow 一家实现。rerank 不属于 OpenAI API，所以 compat 层默认不带它，得由 adapter 显式选用 `compat.NewWithRerank` —— 否则 21 家 compat 厂商会集体声称支持一条大多数都没有的路由。厂商特有参数（比如硅基流动的 `max_chunks_per_doc`）走 `ProviderOptions["siliconflow"]`。

---

## 快速上手

### 对话

```go
client, err := llmkit.New(llmkit.DeepSeek, llmkit.WithAPIKey(key))

// 一行版
answer, err := client.Say(ctx, "deepseek-v4-flash", "你好")

// 完整版
temp := 0.3
resp, err := client.Chat(ctx, &llmkit.ChatRequest{
    Model: "deepseek-v4-flash",
    Messages: []llmkit.Message{
        llmkit.System("你是一个简洁的技术助手。"),
        llmkit.User("Go 的 channel 和 mutex 该怎么选？"),
    },
    Temperature: &temp,
})
text := llmkit.ResponseText(resp)
```

> DeepSeek 当前公开 API 模型是 `deepseek-v4-flash` / `deepseek-v4-pro`；旧 `deepseek-chat` / `deepseek-reasoner` 已于 2026-07-24 停用（[官方变更记录](https://api-docs.deepseek.com/updates/)）。

### 流式

```go
// 回调版：流会自动 drain + close
text, usage, err := client.StreamText(ctx, "deepseek-v4-flash", "数到五", func(delta string) {
    fmt.Print(delta)
})

// 原始 chunk 版：需要拿 reasoning / tool call delta / finish reason 时用
stream, err := client.ChatStream(ctx, req)
defer stream.Close()
for {
    chunk, err := stream.Recv()
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    fmt.Print(llmkit.ChunkText(chunk))
}
```

### OpenAI Responses（原生）

Responses 使用独立的 `protocol/responses` DTO，不经过 Chat Completions 转换。同步 create、SSE 和 input token count 是三次**独立调用**；下面只展开同步调用，三种可运行路径都在 [examples/responses](examples/responses/main.go)：

```go
import responsesapi "github.com/siguago/llmkit/protocol/responses"

store := false // SDK 不改写上游默认值；不需要服务端保留时请显式关闭
resp, err := client.CreateResponse(ctx, &responsesapi.CreateRequest{
    Model: "gpt-5-mini",
    Input: responsesapi.NewTextInput("用一句话解释 CAP 定理。"),
    Store: &store,
})
if err != nil { return err }
fmt.Println(resp.OutputText())
```

```bash
OPENAI_API_KEY=sk-... go run ./examples/responses -mode=sync
OPENAI_API_KEY=sk-... go run ./examples/responses -mode=stream
OPENAI_API_KEY=sk-... go run ./examples/responses -mode=tokens
```

`store` 是数据保留选择，不是本地缓存开关。llmkit 刻意不替调用方覆盖 OpenAI 的默认值；示例显式传 `false`。如果要用 `RetrieveResponse`、`ListResponseInputItems` 或 `previous_response_id` 做持久状态链路，请按产品的数据保留要求显式选择，并在不再需要时调用 `DeleteResponse`。后台任务把 `Background` 显式设为 `true` 后可交给 `WaitResponse` 轮询，最终仍要检查 `Response.Status`、`Error` 与 `IncompleteDetails`。

### Anthropic Messages（原生）

原生 Messages 保留 content block、thinking signature、tool use/result、原生 usage 与 SSE event；它不会自动和 `ChatRequest` 相互转换。同步 create、SSE 与服务端 token count 的完整示例在 [examples/anthropic-native](examples/anthropic-native/main.go)：

```go
import anthropicapi "github.com/siguago/llmkit/protocol/anthropic"

turns := []anthropicapi.MessageParam{{
    Role:    anthropicapi.RoleUser,
    Content: anthropicapi.StringContent("用一句话解释 CAP 定理。"),
}}
message, err := client.CreateAnthropicMessage(ctx, &anthropicapi.MessageRequest{
    Model: "claude-sonnet-4-5-20250929", MaxTokens: 256, Messages: turns,
})
if err != nil { return err }
fmt.Println(message.Text())
```

```bash
ANTHROPIC_API_KEY=sk-ant-... go run ./examples/anthropic-native -mode=sync
ANTHROPIC_API_KEY=sk-ant-... go run ./examples/anthropic-native -mode=stream
ANTHROPIC_API_KEY=sk-ant-... go run ./examples/anthropic-native -mode=tokens
```

默认发送稳定的 `anthropic-version: 2023-06-01`；只有确实使用对应 beta 字段时才给单次调用传 `anthropicapi.WithBetas(...)`，不要把 beta header 偷塞进全局自定义 header。

### OpenAI Files（原生）

上传一次、多处引用（Batch 输入、Responses 的 `file_id` 文件引用），DTO 在 `protocol/openaifiles`：

```go
import filesapi "github.com/siguago/llmkit/protocol/openaifiles"

f, err := client.UploadFile(ctx, &filesapi.UploadRequest{
    Filename:    "input.jsonl",
    Purpose:     filesapi.PurposeBatch,
    Content:     reader,               // 流式上传，不整块缓冲
    ContentType: "application/jsonl",
})
// ...用完记得删：文件是持久资源，按存储保留
_, _ = client.DeleteFile(ctx, f.ID)
```

三条边界：**上传永不自动重试**（`io.Reader` 一次性，重试请自己重开文件，与 `EditImage` 同规则）；**下载返回活体流**（`DownloadFileContent` 给 `io.ReadCloser`，调用方负责 Close，生命周期归 ctx 管、不受 `WithTimeout` 约束）；**purpose 不做本地校验**（上游会加新值，`filesapi.Purpose*` 常量只是当前已知集合）。文件是**持久数据**：SDK 不替你决定保留策略，示例与探测都在用完后显式删除。

### Batch（原生）

异步批处理，半价换 24 小时窗口。两家的形状不同：**OpenAI 走输入文件**（先上传 JSONL，再建 batch），**Anthropic 内联请求**（create 时直接带 `params`）。

```go
// OpenAI：JSONL helper + Files + Batch
item, _ := openaibatch.NewInputItem("req-1", openaibatch.EndpointChatCompletions, map[string]any{
    "model": "gpt-5-mini", "messages": msgs, "max_tokens": 128,
})
var input strings.Builder
_ = openaibatch.EncodeInput(&input, item)
file, _ := client.UploadFile(ctx, &filesapi.UploadRequest{
    Filename: "in.jsonl", Purpose: filesapi.PurposeBatch, Content: strings.NewReader(input.String()),
})
batch, _ := client.CreateBatch(ctx, &openaibatch.CreateRequest{
    InputFileID: file.ID, Endpoint: openaibatch.EndpointChatCompletions,
    CompletionWindow: openaibatch.CompletionWindow24h,
})

// Anthropic：请求内联，结果按行流式读
mb, _ := client.CreateAnthropicMessageBatch(ctx, &anthropicapi.MessageBatchCreateRequest{
    Requests: []anthropicapi.MessageBatchRequestItem{{CustomID: "req-1", Params: msgReq}},
})
reader, _ := client.ReadAnthropicMessageBatchResults(ctx, mb.ID)
defer reader.Close()
for {
    line, err := reader.Next()
    if errors.Is(err, io.EOF) { break }
    // line.Result.Type: succeeded / errored / canceled / expired
}
```

要点：

- **结果顺序不保证**，一律用 `custom_id` 对账 —— 与 rerank 一样，这是协议本身的语义，不是实现细节。
- **创建即排队计费**，没有幂等键，所以 `CreateBatch` / `CreateAnthropicMessageBatch` 只重试能证明「上游没接活」的错误（同 `CreateResponse`）。同一份输入建两次 batch 是两份钱。
- **JSONL 恒严格**：坏行直接报错并给出行号，不走 `WithStreamTolerance` —— 结果文件是完整工件，坏行是数据损坏而非网络抖动。单行上限默认 32 MiB，可用 `WithMaxStreamFrameBytes` 收紧。
- **请求体保持 `json.RawMessage`**：batch 包不复述被批处理端点的请求 schema，你用哪个端点就拿那个端点的 DTO 组 body。
- **Anthropic 的 results 始终按配置 base URL 拼路径**，不跟随响应里的 `results_url` —— 让响应体决定出站目标与本库的 SSRF 立场相悖；该字段只当「结果已可用」的信号读。
- **保留期**：OpenAI 输出文件 30 天后删除；Anthropic 结果 29 天后不可下载。`expired` 的请求不计费。

`WaitBatch` / `WaitAnthropicMessageBatch` 默认 60 秒轮询、26 小时上限；batch 以小时计，长任务更适合自己的调度器。完整分步示例见 [examples/batch](examples/batch/main.go) 与 [examples/anthropic-batch](examples/anthropic-batch/main.go)。

### 工具调用

```go
tools := []llmkit.Tool{
    llmkit.NewTool("get_weather", "查询天气", map[string]any{
        "type": "object",
        "properties": map[string]any{"city": map[string]any{"type": "string"}},
        "required": []string{"city"},
    }),
}

resp, _ := client.Chat(ctx, &llmkit.ChatRequest{Model: m, Messages: msgs, Tools: tools})

for _, call := range llmkit.ResponseToolCalls(resp) {
    msgs = append(msgs, *resp.Choices[0].Message)          // 回显 assistant turn
    msgs = append(msgs, llmkit.ToolResultJSON(call.ID, doWork(call)))
}
```

完整循环见 [examples/tools](examples/tools/main.go)。

### 多模态

```go
resp, _ := client.Chat(ctx, &llmkit.ChatRequest{
    Model: "gemini-2.5-flash",
    Messages: []llmkit.Message{
        llmkit.UserWith(
            llmkit.Text("这张图里有什么？"),
            llmkit.Image("https://example.com/photo.jpg"),
            // 或 llmkit.ImageBytes("image/png", data)
        ),
    },
})
```

### 结构化输出

```go
resp, _ := client.Chat(ctx, &llmkit.ChatRequest{
    Model:          "gpt-5",
    Messages:       msgs,
    ResponseFormat: llmkit.JSONSchemaFormat("recipe", schema),
})
json.Unmarshal([]byte(llmkit.ResponseText(resp)), &recipe)
```

### 推理 / thinking

```go
req := &llmkit.ChatRequest{
    Model:    "claude-sonnet-4-5-20250929",
    Messages: msgs,
    Thinking: llmkit.EnableThinking(4096),   // Anthropic 的 budget_tokens
}
resp, _ := client.Chat(ctx, req)
fmt.Println("思考过程:", llmkit.ResponseReasoning(resp))
fmt.Println("回答:", llmkit.ResponseText(resp))
```

各家的 thinking 开关形态不同（Anthropic 的 `budget_tokens`、GLM 的 `enable_thinking`、Kimi K2.6 的 `chat_template_kwargs`），adapter 会自动翻译成上游认的形状。

### Embeddings / 图像 / 视频

```go
// Embeddings
resp, _ := client.Embed(ctx, &llmkit.EmbeddingRequest{Model: "BAAI/bge-m3", Input: texts})

// 图像
img, _ := client.GenerateImage(ctx, &llmkit.ImageRequest{
    Model: "gpt-image-1", Prompt: "一只柴犬", Size: "1024x1024",
})

// 视频（异步 job）
job, _ := client.CreateVideo(ctx, &llmkit.VideoRequest{Model: "veo-3.1-generate-preview", Prompt: p})
job, _ = client.WaitVideo(ctx, job, &llmkit.WaitOptions{Interval: 10 * time.Second})
if job.Status != llmkit.VideoStatusCompleted {
    log.Fatalf("生成失败: %+v", job.Error)
}
```

---

## 配置

```go
client, err := llmkit.New(llmkit.OpenAI,
    llmkit.WithAPIKey(key),
    llmkit.WithBaseURL("https://my-relay.example.com/v1"),  // 中转站 / 私有部署
    llmkit.WithTimeout(60*time.Second),                     // 普通非流式操作上限（含重试）
    llmkit.WithRetry(llmkit.NoRetry()),                     // 关掉自动重试
    llmkit.WithHeader("HTTP-Referer", "https://myapp.com"), // 附加请求头
)
```

**代理**：所有出站请求都走 `http.ProxyFromEnvironment`，直接设 `HTTPS_PROXY` / `NO_PROXY` 即可。

**认证头保护**：`WithHeader` 不能覆盖 `Authorization` / `x-api-key` / `x-goog-api-key` 等凭据头，避免误发错 key。

> 要一份**可直接拷走的完整初始化**（超时预算、两套重试策略、带埋点的 transport、结构化日志、请求 ID、错误分类），
> 见 [examples/production](examples/production/main.go) —— 每个选项都注释了「不配会出什么事」。

### 接管传输层与可观测性

```go
client, err := llmkit.New(llmkit.OpenAI,
    llmkit.WithAPIKey(key),
    llmkit.WithTransport(myTracingRoundTripper),   // 自定义 TLS / 连接池 / 埋点
    llmkit.WithLogger(slog.Default()),             // 默认全静默
    llmkit.WithRequestID(uuidv7),                  // 每个出站请求一个 X-Request-Id
)
```

- **`WithTransport`** 只替换链路最底层。凭据头、调用方附加头照常先加上，客户端 IP 头照常先剥掉，然后才轮到你的 `RoundTrip` —— 埋点用的 transport 不该有能力悄悄绕掉隐私处理。普通非流式 provider 操作归 `WithTimeout` 管（一次操作含全部重试共享一个预算）；`WaitResponse` / `WaitVideo` 的整体轮询预算由各自 options 和调用 context 控制，`WithTimeout` 只约束其中每次查询。流式不受 `WithTimeout` 控制：**所有 adapter 的流式入口都没有活跃流的 SDK 兜底超时**，context deadline 或取消是唯一生命周期约束（v0.7.0 起统一；此前 compat、Gemini、OpenRouter、DeepSeek 保留过 900 秒兼容上限），生产代码请总是给流式调用的 context 设 deadline。默认 transport 的 `IdleConnTimeout` 只回收连接池中的空闲连接，不会终止活跃流。因此这里不接受 `*http.Client`。
- **`WithLogger`** 默认丢弃一切。库往宿主程序的全局 logger 里写东西是越界的，所以不问就不说。开启后能看到的是：被跳过的畸形流帧、被丢弃的非法工具定义这类"绕过去了但你可能想知道"的事；真正影响结果的一律走 error 返回。
- **`WithRequestID`** 每个**出站 HTTP 请求**调一次（重试的每一次尝试都是新 ID），用于和厂商日志对账。厂商自己生成的那个 ID 走另一条路：`APIError.RequestID`，报障时对方要的是它。

### 流式容错

```go
llmkit.WithStreamTolerance(llmkit.TolerateMalformedChunks)  // 跳过坏帧并记日志
llmkit.WithMaxStreamFrameBytes(llmkit.DefaultResponsesMaxFrameBytes) // 显式把此客户端的所有流统一为 32 MiB
```

默认是**严格模式**：遇到解析不了的帧直接让流失败。跳过坏帧等于静默丢数据——调用方拿到一个看起来完整、实际少了一段内容或少了一次工具调用的回复，且无从察觉。报错至少可以重试或降级。

`WithMaxStreamFrameBytes` 不设置或传非正值时使用 adapter 默认值：一般为 `DefaultMaxFrameBytes`（1 MiB），OpenAI Responses 为 `DefaultResponsesMaxFrameBytes`（32 MiB），所以只使用 Responses 时无需显式配置。显式正值会作用于这个客户端的所有流。

上限按实际帧惰性增长，不会为每条流预留整块内存；但它限制的是 SSE 帧内容，不是进程总内存。行缓冲、`data` 累积和后续 JSON 解码可能同时保留多份接近上限的数据，高并发或不可信中转站场景应谨慎调大。单帧超限的错误会直接指出 `WithMaxStreamFrameBytes`。

---

## 重试

默认策略：3 次尝试，500ms → 1s 指数退避（上限 30s），±20% jitter，尊重上游 `Retry-After`。

会重试：429、408、5xx、Anthropic 的 529 overloaded、网络超时。
不会重试：4xx 客户端错误、context 取消。

```go
llmkit.WithRetry(llmkit.RetryConfig{
    MaxAttempts:       5,
    InitialBackoff:    time.Second,
    MaxBackoff:        time.Minute,
    Multiplier:        2.0,
    Jitter:            0.3,
    RespectRetryAfter: true,
    ShouldRetry:       func(err error) bool { return llmkit.IsRateLimited(err) },
})
```

### 创建型调用不会被盲目重放

`GenerateImage` 和 `CreateVideo` 成功即计费，而厂商没有给幂等键，所以它们**不走上面这套通用策略**。默认只重试那些能证明"上游根本没接活"的错误：

```go
llmkit.IsSafeToReplay(err)   // 429、连接未建立、DNS 失败 → true
                             // 5xx、读超时、连接中断 → false（可能已经生成并计费了）
```

区别在于这两个判断回答的是不同问题，且互相正交：

| 错误 | `IsRetryable`（重试有戏吗） | `IsSafeToReplay`（会重复扣费吗） | 实际会重试 |
|---|:---:|:---:|:---:|
| 429 限流 | ✅ | ✅ | ✅ |
| 5xx | ✅ | ❌ 可能已受理 | ❌ |
| 读超时 | ✅ | ❌ 请求已上路 | ❌ |
| 连接被拒 | ❌ 配置错了 | ✅ 没花钱 | ❌ |
| 400 | ❌ | ❌ | ❌ |

想自己拿主意：

```go
llmkit.WithMediaRetry(llmkit.NoRetry())        // 媒体创建一次都不重试
llmkit.WithMediaRetry(llmkit.DefaultRetry())   // 按通用策略重试，接受可能重复扣费
```

`CreateResponse` 和 `CreateAnthropicMessage` 也是会生成内容、可能计费且没有 SDK 级幂等键的 create。它们固定从 `WithRetry` 配置中取 **replay-safe-only** 子集；读超时、5xx、连接中途断开这类「上游可能已经受理」的失败会直接返回。不要在调用方再套一个不看 `IsSafeToReplay` 的通用重试循环，否则一次模糊失败可能变成两次生成和两笔费用。Token count、retrieve 与 input-items 是读操作，仍使用常规重试策略。

### 另外三个边界

- **流式**只重试创建/握手阶段。一旦 `ChatStream`、Responses SSE 或 Anthropic Messages SSE 返回给调用方，中途断开不会重放 —— 已经吐给调用方的 event/token 无法回滚。
- **`EditImage` 永不重试**。上传的 `io.Reader` 是一次性的，第二次尝试读不到内容。要重试请自己重开文件。
- 重试期间 context 被取消，返回的是**上游的错误**而不是 `context.Canceled` —— 你想知道的是请求为什么失败。

---

## 错误处理

```go
_, err := client.Chat(ctx, req)
switch {
case err == nil:
case llmkit.IsAuthError(err):        // 401 / 403，或厂商体内鉴权错误
case llmkit.IsRateLimited(err):      // 429 或厂商体内限流，配合 RetryAfter
case llmkit.IsNotFound(err):         // 404，模型不存在
case llmkit.IsInvalidRequest(err):   // 400 / 422，或等价厂商错误
case llmkit.IsServerError(err):      // 5xx，或等价厂商错误
case errors.Is(err, llmkit.ErrUnsupported):  // 该厂商没有这个能力
}

log.Printf("status=%d retry_after=%s",
    llmkit.StatusCode(err), // 上游 HTTP 状态码，非 API 错误返回 0
    llmkit.RetryAfter(err), // 上游要求的退避时长
)

var apiErr *llmkit.APIError
if errors.As(err, &apiErr) {
    log.Printf("vendor_request_id=%s", apiErr.RequestID)
}
log.Printf("vendor_code=%s category=%s",
    llmkit.ProviderCode(err),    // 例如 MiniMax base_resp.status_code
    llmkit.ErrorCategoryOf(err), // auth / rate_limit / invalid_request / not_found / server
)
```

---

## 厂商差异怎么处理

统一请求结构上带了各家的私有字段，**认识的厂商会用，不认识的自动忽略**。所以同一个 `ChatRequest` 可以到处发，能力强的上游自然发挥得更好：

| 字段 | 归属 | 作用 |
|------|------|------|
| `Thinking` | Anthropic / GLM / Kimi / DeepSeek | 推理开关（adapter 翻译成各家形态）|
| `ReasoningEffort` | OpenAI o 系 / GPT-5 | 推理强度 |
| `CacheControl`（在 ContentPart 上）| Anthropic | prompt cache 断点 |
| `CacheID` | Kimi | 显式 cache 引用 |
| `ProviderRouting` | OpenRouter | 上游路由偏好 |
| `SafetySettings` | Gemini | 内容安全阈值 |
| `ExtraTools` | Anthropic | 服务端内置工具（web_search / code_execution）|
| `Container` | Anthropic | code_execution 沙箱续用 |
| `BotSetting` | MiniMax | 角色扮演配置 |

同样，响应里 Anthropic 的 `thinking signature`、Gemini 的 `thought_signature`、Anthropic 的 `redacted_thinking` 这些**必须原样回传给上游**的字段，都在 `Message` 上有对应位置 —— 多轮对话直接把 `resp.Choices[0].Message` 塞回 `Messages` 就行，不会丢。

---

## 直接用 adapter

门面层没覆盖的厂商专属方法，可以拿到底层 adapter：

```go
p := openrouter.NewWithBaseURL("")
raw := client.Adapter()             // 或从 Client 里取

// 也可以反过来：自己构造 adapter，再包成 Client
client, _ := llmkit.Wrap(p, llmkit.WithAPIKey(key))
```

`Wrap` 也接受任何实现了 `provider.Provider` 的类型 —— 测试用的 fake、公司内部网关都行。

---

## 项目结构

```
llmkit/
├── llmkit.go            # New / Wrap / provider 常量 / 工厂表
├── client.go            # Client：统一 chat / embeddings / images / video
├── responses.go         # OpenAI Responses 原生门面
├── anthropic_messages.go # Anthropic Messages 原生门面
├── files.go             # OpenAI Files 门面
├── batches.go           # OpenAI Batch 门面
├── anthropic_batches.go # Anthropic Message Batches 门面
├── messages.go          # 便捷构造器与响应提取
├── options.go           # WithAPIKey / WithBaseURL / WithTimeout / ...
├── retry.go             # 退避重试
├── errors.go            # 错误分类
├── types.go             # 类型别名（对外只需 import 一个包）
├── integration_test.go  # 真实 API 测试（-tags=integration，默认不跑）
├── provider/            # 适配层 —— 直接用也行
│   ├── types.go         #   统一请求/响应/流接口
│   ├── native.go        #   原生协议的细粒度可选能力接口
│   ├── compat/          #   OpenAI 兼容基类
│   ├── anthropic/       #   原生实现
│   ├── gemini/          #   原生实现
│   └── ...              #   其余 11 家
├── protocol/            # 原生 DTO、union 与 event（仅标准库的叶子包）
│   ├── responses/       #   OpenAI Responses
│   ├── anthropic/       #   Anthropic Messages 与 Message Batches
│   ├── openaifiles/     #   OpenAI Files
│   └── openaibatch/     #   OpenAI Batch 与 JSONL 输入输出
├── internal/
│   ├── sse/             #   两套原生 stream 共用的 SSE framing
│   ├── httpx/           #   统一 transport（代理 / 连接池 / 头注入）
│   ├── ipprivacy/       #   剥离客户端 IP 泄露头
│   └── safehttp/        #   SSRF 安全的图片下载
├── cmd/llmkit-probe/    # 能力探测 CLI（配个 key 就能实测厂商支持什么）
├── examples/            # 可运行示例，含原生协议与生产配置全家桶
├── Makefile             # make help 看全部命令
└── .env.example         # 复制成 .env 填 key，probe 会自动读
```

---

## 新增一家厂商

**OpenAI 兼容的**（约 20 行）：

```go
package myvendor

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.myvendor.com/v1"

func New(baseURL string) *compat.Provider {
    if baseURL == "" {
        baseURL = defaultBaseURL
    }
    return compat.New(compat.Config{ProviderName: "myvendor", BaseURL: baseURL})
}
```

然后在 `llmkit.go` 的 `factories` 和 `envVars` 里各加一行。参考 [siliconflow](provider/siliconflow/siliconflow.go)。

需要**格式转换的**：实现 `provider.Provider` 三个方法 + 自己的 `types.go` / `stream.go`。参考 [anthropic](provider/anthropic/)。

可选能力按需实现 `ModelLister` / `ModelTaskLister` / `Embedder` / `Reranker` / `ImageGenerator` / `ImageEditor` / `VideoCreator` / `VideoCanceller`，Client 会自动探测。图片和视频必须按端点实现最小接口；不要为了生成能力而实现同时要求编辑或取消的组合接口。

原生协议也按 endpoint 分接口：例如 relay 只接了 Responses JSON create，就只实现 `provider.ResponsesCreator`，不要顺手实现 stream / retrieve，更不要把这些方法加到所有 adapter 都嵌入的 compat 基类。否则 Go 的 method promotion 会让每家 provider 的 `Supports*` 都错误返回 true。Anthropic Messages 的 create / stream / token count 同理分别 opt in。

**如果这家在 HTTP 200 下报错**（国内厂商常见），用 `provider.WithErrorMetadata(err, 厂商码, 分类)` 附加元数据：`StatusCode` 仍忠实返回 200，而 `IsAuthError` / `IsRateLimited` / `IsRetryable` 会按你给的分类回答。参考 [minimax](provider/minimax/embeddings.go) 的 `base_resp` 处理。

一个约束值得单独说：`ErrorCategoryRateLimit` 比另外四类多一层断言 —— 它同时声明**上游没开始干活、没产生计费**，因为 `IsSafeToReplay` 会据此重放图像 / 视频创建。HTTP 429 自己够格；「200 + 体内限流」不自动够格，除非厂商文档写明这种响应不计费。拿不准就传 `""`：留空则由 HTTP 状态决定，而 200 既不可重试也不可重放，是安全的那一侧。

---

## 示例

```bash
DEEPSEEK_API_KEY=sk-... go run ./examples/chat
DEEPSEEK_API_KEY=sk-... go run ./examples/stream
DEEPSEEK_API_KEY=sk-... go run ./examples/tools
GEMINI_API_KEY=...     go run ./examples/vision ./photo.png
OPENAI_API_KEY=sk-...  go run ./examples/structured
SILICONFLOW_API_KEY=sk-... go run ./examples/embeddings
OPENAI_API_KEY=sk-...  go run ./examples/images "一只柴犬"
GEMINI_API_KEY=...     go run ./examples/videos
OPENAI_API_KEY=sk-...  go run ./examples/responses -mode=sync
ANTHROPIC_API_KEY=...  go run ./examples/anthropic-native -mode=stream
OPENAI_API_KEY=sk-...  go run ./examples/batch -mode=submit
ANTHROPIC_API_KEY=...  go run ./examples/anthropic-batch -mode=submit
go run ./examples/multiprovider     # 跑所有配了 key 的厂商

DEEPSEEK_API_KEY=sk-... go run ./examples/production   # 生产配置全家桶
```

[examples/production](examples/production/main.go) 和其他几个不太一样：它不演示某个能力，而是把一个长期运行的服务**该配的都配上**
—— 超时预算、区分计费调用的两套重试、带埋点的 transport、`slog`、每请求 ID、以及把厂商 request ID 捞出来的错误处理。
注释写的是「不配会出什么事」而不是「这个选项叫什么」。改完直接拷进你的项目。

---

## 稳定性承诺

自 v1.0.0 起遵循语义化版本：**1.x 不会破坏导出 API 与文档化行为**。完整政策见 [STABILITY.md](STABILITY.md)，两条最该先知道的：

- **给 struct 加字段不算破坏。** 请始终**带字段名**构造本项目的 struct —— 位置式 composite literal 不在承诺内（v0.6.0 给 `provider.Usage` 加字段时咬过人）。
- **只接上游已 GA 的 API。** 冻结一个上游自己都没冻结的面，等于把别人的破坏性变更变成我们的。

承诺不靠人记：CI 有一个 `api-compat` job 用 `apidiff` 对比最近的 release tag，v1 基线下出现不兼容变更直接失败。

---

## 不做什么 · 1.x 路线

这个库覆盖的是**对话及其周边**。下面这些各家 API 有、本库没有。

**1.x 会以纯加法接入**（不阻塞 1.0）：

| 待接 | 前提 |
|---|---|
| Anthropic Files API | 上游仍是 beta（`files-api-2025-04-14`）。OpenAI Files 已 GA，v0.8.0 接了 |
| Batch webhook 事件 | 完成通知未接；当前轮询用 `WaitBatch` / `WaitAnthropicMessageBatch` |
| rerank 第二家实现 | Cohere 形状的 usage 已按契约实现但无厂商实测，接第二家时一并验 |
| 其余厂商的 Files / Batch | 智谱、Moonshot 等 OpenAI 形状厂商，逐家验证 wire 后 opt in |

**明确的非目标**（除非另立计划，1.x 都不做）：

| 不做 | 原因 |
|---|---|
| 语音转写 (STT) / 语音合成 (TTS) | 不属于"对话及其周边"，值得单独一套接口 |
| Moderation API | 独立的内容审核端点；图像 / Responses 请求体里的 `moderation` 参数不是这个资源 API |
| 通用 / 本地 Token 计数 | 统一 Chat 接口没有本地 tokenizer；只有原生 Responses input tokens 与 Anthropic `count_tokens` 端点已接 |
| Responses 扩展产品面 | Conversations、WebSocket、`/responses/compact`；部分内置工具只有 Raw 保真，没有专项强类型 |
| OpenAI 其余资源面 | vector stores、assistants、fine-tuning、分片 Uploads |
| 余额 / 额度查询 | 各家形状差异大且与对话无关 |
| 云托管入口 | Azure OpenAI / AWS Bedrock / Google Vertex AI 各需自己的鉴权与路径；原生协议面只保证两家官方直连 transport |
| 多 key 轮换 / 故障转移 | 一个 Client 绑定一个 key，属于上层调度的职责 |
| 入站兼容网关 | 这是出站 SDK，不是 HTTP gateway |

> 自定义 `Transport` 曾经在这张表里，现在有了：见上面的 `WithTransport`，mTLS / 自签证书 / 埋点都走它。

---

## 用你自己的 key 实测一遍

配一个 key，一条命令看这家厂商在你账号下**到底支持什么**：

```bash
DEEPSEEK_API_KEY=sk-... go run ./cmd/llmkit-probe deepseek
```

```
llmkit probe · deepseek · deepseek-v4-flash
──────────────────────────────────────────────────────────────────────────────
  PASS  模型列表              2 个模型                                    412ms
  PASS  模型任务             2/2 已分类 · 0 未知                          301ms
  PASS  非流式对话           38 字 · 96 tokens                             1.2s
  PASS  流式对话             47 chunks · 首字 340ms                        2.1s
  PASS  多轮上下文           正确记住 42                                   1.8s
  PASS  工具调用             get_temperature({"city":"杭州"}) → 结果被采用  2.4s
  PASS  结构化输出           仅支持 json_object（不支持 json_schema）       1.9s
   N/A  多模态图像输入       该模型不接受图像输入                             0s
   N/A  Embeddings           该 provider 无 embeddings 接口                  0s
  PASS  错误分类             401 → IsAuthError                            380ms
──────────────────────────────────────────────────────────────────────────────
  7 通过 · 2 不适用  ·  约 480 tokens
```

四种结果：`PASS` 能用 · `FAIL` 该能用但没用成（打印原因）· `N/A` 这家/这个模型没有该能力 · `SKIP` 未尝试。

**key 从哪来**（按优先级）：`-key` 参数 → 环境变量 → `.env` 文件。所以也可以：

```bash
echo "DEEPSEEK_API_KEY=sk-..." > .env
go run ./cmd/llmkit-probe deepseek
```

常用法：

```bash
go run ./cmd/llmkit-probe                          # 所有配了 key 的厂商，末尾出汇总表
go run ./cmd/llmkit-probe -list                    # 支持的厂商与对应环境变量
go run ./cmd/llmkit-probe deepseek -v              # 打印模型的完整回复
go run ./cmd/llmkit-probe deepseek -model deepseek-v4-pro
go run ./cmd/llmkit-probe openai -media            # 额外测图像生成（更贵）
go run ./cmd/llmkit-probe openai -files            # 额外测 Files 全生命周期（上传后即删，零费用）
go run ./cmd/llmkit-probe openai -batch            # 额外测 Batch（建单请求 batch 后立即取消）
go run ./cmd/llmkit-probe deepseek -base-url https://my-relay.example/v1
```

**费用**：每项探测都封了 output token，一家跑完通常不到一美分。`-media` 会真的生成图片，明显更贵，所以默认不跑。

**先做连通性预检**：key 无效或模型不存在时立即中止并说明原因 —— 凭据不通时的能力探测结果没有意义，不会给你一屏误导性的 N/A。

模型可用环境变量覆盖，不必改代码：`LLMKIT_MODEL_<PROVIDER>` / `LLMKIT_EMBED_MODEL_<PROVIDER>` / `LLMKIT_IMAGE_MODEL_<PROVIDER>`。

---

## 测试

```bash
go test ./...          # 离线，不需要 key，不产生费用
go test ./... -race
```

**所有默认测试都是离线的**（`httptest` 打本地服务器），验证的是「请求构造得对不对、响应解析得对不对」，**不验证厂商是否接受**。

这条现在是真的、也是可验证的 —— 把出站流量堵死后整套测试照样通过：

```bash
HTTPS_PROXY=http://127.0.0.1:1 HTTP_PROXY=http://127.0.0.1:1 go test ./...
```

（会发真实请求的测试一律带 `integration` build tag，`go test ./...` 连编译都不会编到它们。）

### Fuzz

解析器直接吃厂商发来的原始字节，是 SDK 最大的不可信输入面。SSE 帧解析、错误响应解析、多模态内容转换都有 fuzz 目标。CI 每次 PR 对全部目标各跑 100,000 次，另有每日和手动触发的深度任务各跑 500,000 次：

```bash
bash .github/scripts/run-fuzz.sh 100000x \
  ./provider/compat/ ./provider/ ./internal/sse/ \
  ./protocol/responses/ ./protocol/anthropic/
```

这里暂时使用固定次数，是为了绕开 `-fuzztime=<时长>` 到期时偶发误报 `context deadline exceeded` 的[官方问题](https://go.dev/issue/75804)。修复目前只在 Go 的 master 上，截至 `go1.27rc2` 尚未进入任何发布分支，预计随 Go 1.28 发布——**升级到 1.27 不要切回按时长运行**。任何 fuzz 非零退出仍会让门禁失败，CI 只把本次新生成的 crasher 作为 artifact 上传。

它们断言的不只是「不 panic」，还有各自的不变量：容错模式下解析错误一定被跳过而不会漏出、任意网络分片不能改变 SSE 帧、未知原生 union/event 必须 Raw 保真、batch JSONL 的坏行一定报错且失败是粘性的、`ParseDataURI` 接受的输入一定能还原回原串、token 计数一定非负。

这不是形式主义：`FuzzMessageBatchResultsReader` 在门禁首次运行时就抓到一个真缺陷 —— Anthropic 结果行缺少 `custom_id` / `result` 时会解码成零值成功。修在 v0.9.0，失败输入已作为回归种子提交。

### API 兼容门禁

```bash
make api-compat            # 对比最近的 release tag；BASE=v0.9.0 可指定基线
```

`apidiff` 比对导出面，基线为 v1 及以上时出现不兼容变更即失败（见 [STABILITY.md](STABILITY.md)）。工具经 `go run` 以钉死的版本执行，不进 `go.mod`。

要验证真的能跑通，有两条路，都会发真实请求、产生真实费用（每家几分之一美分，token 都封了顶）：

```bash
make probe PROVIDER=deepseek                              # 人看的能力报告，见上一节
go test -tags=integration -v -run TestLive .              # 机器读的断言，可进 CI
```

两者覆盖面接近，差别在用途：`probe` 给你一张「这家支持什么」的表，失败不中断、继续跑完；集成测试是标准 `go test`，可以 `-run TestLiveTools` 单点跑、失败即红。集成测试的模型用 `LLMKIT_TEST_MODEL_<PROVIDER>` 覆盖，媒体测试加 `-media`。

更贵或更慢的几项要再显式开一道，避免日常冒烟意外扩大耗时和费用：`-media`（图像/视频）、`LLMKIT_RUN_BATCH=1`（Files 与 Batch 全生命周期）、`LLMKIT_RUN_NATIVE_BACKGROUND=1`、`LLMKIT_RUN_NATIVE_THINKING=1`。这些测试都会清理自己创建的资源 —— 上传的文件一律删除，创建的 batch 一律取消（被取消的 OpenAI batch 对象删不掉，会留在账号里直到上游归档）。

覆盖率现状（`make cover`）：

| 包 | 覆盖率 | 备注 |
|---|---|---|
| 门面层（根包） | 85% | |
| `internal/logging` | 100% | |
| `internal/safehttp` | 96% | SSRF 拨号拦截、大小上限、重定向降级、MIME 双重校验各有断言 |
| `internal/httpx` | 94% | |
| `internal/sse` | 92% | |
| `internal/ipprivacy` | 80% | |
| `protocol/openaifiles` | 92% | |
| `protocol/openaibatch` | 90% | JSONL 编解码含 fuzz |
| `protocol/anthropic` | 80% | |
| `protocol/responses` | 70% | |
| `provider`（公共类型与流策略） | 76% | |
| `provider/*` 适配层 | 48–98% | 迁移自一个跑在生产上的网关，路径被真实流量验证过 |
| `provider/vercel` | 98% | |
| `provider/minimax` | 90% | 手写的 embeddings 翻译层有自己的测试；chat 路径仍走冒烟测试 |
| `cmd/llmkit-probe` | 22% | 参数解析 / .env / 排版有测试；探测逻辑本身要真实 key 才跑得到 |
| `provider/siliconflow` 等薄封装 | 0% | 构造与 chat/stream 路径由 `provider` 包的冒烟测试覆盖，故本包自身显示 0% |

总覆盖率 66%。缺口集中在真实网络、媒体和 CLI 路径 —— 这些要么需要真实 key（见 `-tags=integration`），要么会产生费用。

---

## 贡献

欢迎 issue 和 PR。提 PR 前跑一遍 CI 跑的那套：

```bash
make lint        # gofmt + vet + 零依赖校验
make test-race
```

**保持零依赖**是这个库的设计约束 —— `make lint` 和 CI 都会检查 `go.mod` 是否混入第三方 require。

改动涉及某家厂商时，最好用你自己的 key 实测一次：

```bash
make probe PROVIDER=<厂商>
```

新增厂商见上面「新增一家厂商」一节。能力矩阵变化时 `TestCapabilityMatrix` 会失败 ——
更新它、同步 README 的能力矩阵表，再在 `cmd/llmkit-probe/render.go` 的模型表里补一行默认模型。

---

## License

[MIT](LICENSE)
