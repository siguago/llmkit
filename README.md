# llmkit

[![CI](https://github.com/siguago/llmkit/actions/workflows/ci.yml/badge.svg)](https://github.com/siguago/llmkit/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/siguago/llmkit.svg)](https://pkg.go.dev/github.com/siguago/llmkit)
[![Go Report Card](https://goreportcard.com/badge/github.com/siguago/llmkit)](https://goreportcard.com/report/github.com/siguago/llmkit)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

一套 Go SDK，用同一套 OpenAI 兼容接口调用 13 家主流大模型厂商。**零第三方依赖**，只用标准库。

```go
client, _ := llmkit.New(llmkit.DeepSeek)          // key 取自 DEEPSEEK_API_KEY
answer, _ := client.Say(ctx, "deepseek-chat", "用一句话解释 CAP 定理。")
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

需要 Go 1.25+。

---

## 支持的厂商

| 常量 | 厂商 | 环境变量 | 实现方式 |
|------|------|----------|----------|
| `llmkit.OpenAI` | OpenAI | `OPENAI_API_KEY` | compat + reasoning 参数清洗 |
| `llmkit.Anthropic` | Anthropic Claude | `ANTHROPIC_API_KEY` | 原生 Messages API |
| `llmkit.Gemini` | Google Gemini | `GEMINI_API_KEY` | 原生 generateContent |
| `llmkit.DeepSeek` | DeepSeek | `DEEPSEEK_API_KEY` | 原生 |
| `llmkit.Moonshot` / `llmkit.Kimi` | 月之暗面 Kimi | `MOONSHOT_API_KEY` | compat + K2.6 thinking 适配 |
| `llmkit.Zhipu` | 智谱 GLM | `ZHIPU_API_KEY` | compat |
| `llmkit.MiniMax` | MiniMax | `MINIMAX_API_KEY` | compat |
| `llmkit.SiliconFlow` | 硅基流动 | `SILICONFLOW_API_KEY` | compat |
| `llmkit.DashScope` | 阿里百炼 Qwen | `DASHSCOPE_API_KEY` | 原生 |
| `llmkit.Volcengine` | 火山方舟 豆包 | `VOLCENGINE_API_KEY` | 原生 |
| `llmkit.OpenRouter` | OpenRouter（聚合） | `OPENROUTER_API_KEY` | 原生 |
| `llmkit.EasyRouter` | EasyRouter（聚合） | `EASYROUTER_API_KEY` | compat |
| `llmkit.Vercel` | Vercel AI Gateway（聚合） | `VERCEL_AI_GATEWAY_KEY` | compat |

### 能力矩阵

不是每家都实现全部能力。调用前用 `Supports*` 探测，或直接处理 `ErrUnsupported`。

| 厂商 | Chat / 流式 | 模型列表 | Embeddings | 图像 | 视频 |
|------|:---:|:---:|:---:|:---:|:---:|
| openai | ✅ | ✅ | ✅ | ✅ | — |
| anthropic | ✅ | ✅ | — | — | — |
| gemini | ✅ | ✅ | — | ✅ | ✅ |
| deepseek | ✅ | ✅ | — | — | — |
| moonshot | ✅ | ✅ | ✅ | — | — |
| zhipu | ✅ | ✅ | ✅ | — | — |
| minimax | ✅ | ✅ | ✅ | — | — |
| siliconflow | ✅ | ✅ | ✅ | — | — |
| dashscope | ✅ | — | — | — | ✅ |
| volcengine | ✅ | — | — | — | ✅ |
| openrouter | ✅ | ✅ | — | ✅ | ✅ |
| easyrouter | ✅ | ✅ | ✅ | ✅ | ✅ |
| vercel | ✅ | ✅ | ✅ | ✅ | — |

> 这张表由 `TestCapabilityMatrix` 守卫，代码变了测试会先失败。

---

## 快速上手

### 对话

```go
client, err := llmkit.New(llmkit.DeepSeek, llmkit.WithAPIKey(key))

// 一行版
answer, err := client.Say(ctx, "deepseek-chat", "你好")

// 完整版
temp := 0.3
resp, err := client.Chat(ctx, &llmkit.ChatRequest{
    Model: "deepseek-chat",
    Messages: []llmkit.Message{
        llmkit.System("你是一个简洁的技术助手。"),
        llmkit.User("Go 的 channel 和 mutex 该怎么选？"),
    },
    Temperature: &temp,
})
text := llmkit.ResponseText(resp)
```

### 流式

```go
// 回调版：流会自动 drain + close
text, usage, err := client.StreamText(ctx, "deepseek-chat", "数到五", func(delta string) {
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
    Model: "gpt-image-1", Prompt: "一只柴犬", Size: "1024x1024", Delivery: "inline",
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

三个边界值得知道：

- **流式**只重试握手阶段。一旦流建立成功，中途断开不会重试 —— 已经吐给调用方的 token 无法回滚。
- **`EditImage` 永不重试**。上传的 `io.Reader` 是一次性的，第二次尝试读不到内容。要重试请自己重开文件。
- 重试期间 context 被取消，返回的是**上游的错误**而不是 `context.Canceled` —— 你想知道的是请求为什么失败。

---

## 错误处理

```go
resp, err := client.Chat(ctx, req)
switch {
case err == nil:
case llmkit.IsAuthError(err):        // 401 / 403
case llmkit.IsRateLimited(err):      // 429，配合 llmkit.RetryAfter(err)
case llmkit.IsNotFound(err):         // 404，模型不存在
case llmkit.IsInvalidRequest(err):   // 400 / 422
case llmkit.IsServerError(err):      // 5xx
case errors.Is(err, llmkit.ErrUnsupported):  // 该厂商没有这个能力
}

llmkit.StatusCode(err)   // 上游 HTTP 状态码，非 API 错误返回 0
llmkit.RetryAfter(err)   // 上游要求的退避时长，秒数和 HTTP 日期两种格式都认
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
└── examples/            # 9 个可运行示例
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

可选能力按需实现 `ModelLister` / `Embedder` / `ImageProvider` / `VideoProvider`，Client 会自动探测。

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
```

---

## 尚未覆盖的能力

这个库覆盖的是**对话及其周边**。以下能力各家 API 有、但 SDK 目前没有，别指望：

| 缺口 | 说明 |
|---|---|
| 语音转写 (STT) / 语音合成 (TTS) | 完全没有 |
| Rerank | RAG 重排序，SiliconFlow / Jina / Cohere 有 |
| Files API | 上传文件供后续引用；目前只支持消息内联文件 |
| Batch API | 批量异步（通常半价） |
| Moderation API | 独立的内容审核端点（图像生成里的 `moderation` 参数不是这个） |
| Token 计数 | 本地 tokenizer 需要第三方库，与零依赖冲突；Anthropic 的 `count_tokens` 端点也未接 |
| 余额 / 额度查询 | 未接 |
| Gemini embeddings | Gemini 有该 API，但适配器还没实现 |
| 自定义 `http.Client` / `Transport` | mTLS、自签证书场景用不了；代理和超时可以配（见上） |
| 多 key 轮换 / 故障转移 | 一个 Client 绑定一个 key，需要自己在上层做 |

---

## 用你自己的 key 实测一遍

配一个 key，一条命令看这家厂商在你账号下**到底支持什么**：

```bash
DEEPSEEK_API_KEY=sk-... go run ./cmd/llmkit-probe deepseek
```

```
llmkit probe · deepseek · deepseek-chat
──────────────────────────────────────────────────────────────────────────────
  PASS  模型列表             64 个模型                                    412ms
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
go run ./cmd/llmkit-probe deepseek -model deepseek-reasoner
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

要验证真的能跑通，跑集成测试 —— 它会**发真实请求、产生真实费用**（每家几分之一美分，token 都封了顶）：

```bash
DEEPSEEK_API_KEY=sk-... go test -tags=integration -v -run TestLive .
```

它按环境变量里有哪些 key 决定测哪几家，覆盖非流式 / 流式 / 多轮上下文 / 工具调用 / 模型列表 / embeddings / 错误分类 / 流式取消。模型可用 `LLMKIT_TEST_MODEL_<PROVIDER>` 覆盖，媒体测试要额外加 `-media`。

覆盖率现状（`go test ./... -cover`）：

| 包 | 覆盖率 | 备注 |
|---|---|---|
| 门面层（根包） | 92% | |
| `internal/httpx` | 91% | |
| `provider/*` 适配层 | 40–88% | 迁移自一个跑在生产上的网关，路径被真实流量验证过 |
| `provider/vercel` 图像部分 | 0% | 已知空白 |

---

## 贡献

欢迎 issue 和 PR。提 PR 前请确认：

```bash
gofmt -l .          # 无输出
go vet ./...
go test ./... -race
```

**保持零依赖**是这个库的设计约束 —— CI 会检查 `go.mod` 是否混入第三方 require。

新增厂商见上面「新增一家厂商」一节；能力矩阵变化时 `TestCapabilityMatrix` 会失败，
更新它并同步 README 表格即可。

---

## License

[MIT](LICENSE)
