# llmkit

[![CI](https://github.com/siguago/llmkit/actions/workflows/ci.yml/badge.svg)](https://github.com/siguago/llmkit/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/siguago/llmkit.svg)](https://pkg.go.dev/github.com/siguago/llmkit)
[![Go Report Card](https://goreportcard.com/badge/github.com/siguago/llmkit)](https://goreportcard.com/report/github.com/siguago/llmkit)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

一套 Go SDK，用同一套 OpenAI 兼容接口调用 21 家主流大模型厂商。**零第三方依赖**，只用标准库。

```go
client, _ := llmkit.New(llmkit.DeepSeek)          // key 取自 DEEPSEEK_API_KEY
answer, _ := client.Say(ctx, "deepseek-v4-flash", "用一句话解释 CAP 定理。")
```

换一家厂商只改一个常量，请求结构、响应结构、错误处理、流式读法全都不变。

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
| `llmkit.OpenAI` | OpenAI | `OPENAI_API_KEY` | compat + reasoning 参数清洗 |
| `llmkit.Anthropic` | Anthropic Claude | `ANTHROPIC_API_KEY` | 原生 Messages API |
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

> 这张表由 `TestCapabilityMatrix` 守卫，代码变了测试会先失败。
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
    llmkit.WithTimeout(60*time.Second),                     // 单次调用上限（含重试）
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

- **`WithTransport`** 只替换链路最底层。凭据头、调用方附加头照常先加上，客户端 IP 头照常先剥掉，然后才轮到你的 `RoundTrip` —— 埋点用的 transport 不该有能力悄悄绕掉隐私处理。超时仍归 `WithTimeout` 管（一次调用含全部重试共享一个预算），所以这里不接受 `*http.Client`。
- **`WithLogger`** 默认丢弃一切。库往宿主程序的全局 logger 里写东西是越界的，所以不问就不说。开启后能看到的是：被跳过的畸形流帧、被丢弃的非法工具定义这类"绕过去了但你可能想知道"的事；真正影响结果的一律走 error 返回。
- **`WithRequestID`** 每个**出站 HTTP 请求**调一次（重试的每一次尝试都是新 ID），用于和厂商日志对账。厂商自己生成的那个 ID 走另一条路：`APIError.RequestID`，报障时对方要的是它。

### 流式容错

```go
llmkit.WithStreamTolerance(llmkit.TolerateMalformedChunks)  // 跳过坏帧并记日志
llmkit.WithMaxStreamFrameBytes(4 << 20)                      // 抬高单帧上限（默认 1 MiB）
```

默认是**严格模式**：遇到解析不了的帧直接让流失败。跳过坏帧等于静默丢数据——调用方拿到一个看起来完整、实际少了一段内容或少了一次工具调用的回复，且无从察觉。报错至少可以重试或降级。

单帧超限的错误会直接告诉你该调哪个选项，而不是甩一句 bufio 的 `token too long`。

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

### 花钱的调用不会被盲目重放

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

### 另外三个边界

- **流式**只重试握手阶段。一旦流建立成功，中途断开不会重试 —— 已经吐给调用方的 token 无法回滚。
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
├── client.go            # Client：chat / embeddings / images / video
├── messages.go          # 便捷构造器与响应提取
├── options.go           # WithAPIKey / WithBaseURL / WithTimeout / ...
├── retry.go             # 退避重试
├── errors.go            # 错误分类
├── types.go             # 类型别名（对外只需 import 一个包）
├── integration_test.go  # 真实 API 测试（-tags=integration，默认不跑）
├── provider/            # 适配层 —— 直接用也行
│   ├── types.go         #   统一请求/响应/流接口
│   ├── compat/          #   OpenAI 兼容基类
│   ├── anthropic/       #   原生实现
│   ├── gemini/          #   原生实现
│   └── ...              #   其余 11 家
├── internal/
│   ├── httpx/           #   统一 transport（代理 / 连接池 / 头注入）
│   ├── ipprivacy/       #   剥离客户端 IP 泄露头
│   └── safehttp/        #   SSRF 安全的图片下载
├── cmd/llmkit-probe/    # 能力探测 CLI（配个 key 就能实测厂商支持什么）
├── examples/            # 10 个可运行示例，含一份生产配置全家桶
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
go run ./examples/multiprovider     # 跑所有配了 key 的厂商

DEEPSEEK_API_KEY=sk-... go run ./examples/production   # 生产配置全家桶
```

[examples/production](examples/production/main.go) 和其他几个不太一样：它不演示某个能力，而是把一个长期运行的服务**该配的都配上**
—— 超时预算、区分计费调用的两套重试、带埋点的 transport、`slog`、每请求 ID、以及把厂商 request ID 捞出来的错误处理。
注释写的是「不配会出什么事」而不是「这个选项叫什么」。改完直接拷进你的项目。

---

## 尚未覆盖的能力

这个库覆盖的是**对话及其周边**。以下能力各家 API 有、但 SDK 目前没有，别指望：

| 缺口 | 说明 |
|---|---|
| 语音转写 (STT) / 语音合成 (TTS) | 完全没有 |
| Files API | 上传文件供后续引用；目前只支持消息内联文件 |
| Batch API | 批量异步（通常半价） |
| Moderation API | 独立的内容审核端点（图像生成里的 `moderation` 参数不是这个） |
| Token 计数 | 本地 tokenizer 需要第三方库，与零依赖冲突；Anthropic 的 `count_tokens` 端点也未接 |
| 余额 / 额度查询 | 未接 |
| 云托管入口 | Azure OpenAI / AWS Bedrock / Google Vertex AI 都不是 Bearer 鉴权（`api-key` + `api-version`、SigV4、服务账号 OAuth2），要各自的鉴权实现，目前都没有 |
| 多 key 轮换 / 故障转移 | 一个 Client 绑定一个 key，需要自己在上层做 |

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

解析器直接吃厂商发来的原始字节，是 SDK 最大的不可信输入面。SSE 帧解析、错误响应解析、多模态内容转换都有 fuzz 目标，CI 每次 PR 都跑一轮短的：

```bash
go test ./provider/compat/ -run '^$' -fuzz FuzzStreamReader_Strict -fuzztime=30s
go test ./provider/ -run '^$' -fuzz FuzzParseDataURI -fuzztime=30s
```

它们断言的不只是「不 panic」，还有各自的不变量：容错模式下解析错误一定被跳过而不会漏出、`ParseDataURI` 接受的输入一定能还原回原串、token 计数一定非负。

要验证真的能跑通，有两条路，都会发真实请求、产生真实费用（每家几分之一美分，token 都封了顶）：

```bash
make probe PROVIDER=deepseek                              # 人看的能力报告，见上一节
go test -tags=integration -v -run TestLive .              # 机器读的断言，可进 CI
```

两者覆盖面接近，差别在用途：`probe` 给你一张「这家支持什么」的表，失败不中断、继续跑完；集成测试是标准 `go test`，可以 `-run TestLiveTools` 单点跑、失败即红。集成测试的模型用 `LLMKIT_TEST_MODEL_<PROVIDER>` 覆盖，媒体测试加 `-media`。

覆盖率现状（`make cover`）：

| 包 | 覆盖率 | 备注 |
|---|---|---|
| 门面层（根包） | 94% | |
| `internal/logging` | 100% | |
| `internal/safehttp` | 96% | SSRF 拨号拦截、大小上限、重定向降级、MIME 双重校验各有断言 |
| `internal/httpx` | 94% | |
| `internal/ipprivacy` | 80% | |
| `provider`（公共类型与流策略） | 75% | |
| `provider/*` 适配层 | 41–88% | 迁移自一个跑在生产上的网关，路径被真实流量验证过 |
| `provider/vercel` | 99% | |
| `provider/minimax` | 89% | 手写的 embeddings 翻译层有自己的测试；chat 路径仍走冒烟测试 |
| `cmd/llmkit-probe` | 20% | 参数解析 / .env / 排版有测试；探测逻辑本身要真实 key 才跑得到 |
| `provider/siliconflow` | 0% | 构造与 chat/stream 路径由 `provider` 包的冒烟测试覆盖，故本包自身显示 0%。新增的 8 家薄封装同理 |

总覆盖率 58%。缺口集中在真实网络、媒体和 CLI 路径 —— 这些要么需要真实 key（见 `-tags=integration`），要么会产生费用。

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
