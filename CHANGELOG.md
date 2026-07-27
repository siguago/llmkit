# 变更记录

本项目尚未到 1.0，API 未冻结。破坏性变更会在这里逐条列出，并说明为什么值得破坏。

## Unreleased

这一轮针对的是「生产网关代码公开成 SDK」遗留的三类问题：花钱的调用被盲目重放、能力探测说谎、测试偷偷联网。

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
- `TestUntestedAdapters_*` 从 3 个 adapter 扩到 11 个，8 家新 provider 全部覆盖默认端点、路径拼接、错误透传与能力断言。默认端点是这种薄封装最容易错的地方：groq 在 `/openai/v1`、fireworks 在 `/inference/v1`，都不是 `/v1`。
- 新增 `TestUntestedAdapters_Prefill`，双向断言：支持 prefill 的厂商必须收到字段，不支持的必须一个都收不到（多余的 key 会被严格的 compat 服务直接拒掉）。
- 新增三条防漂移守卫：`TestLiveModelsCoverAllProviders`（集成测试模型表）、`TestChatModelsCoverAllProviders` 与 `TestEmbedModelsCoverEmbedders`（probe 的模型表）。最后一条是双向的：声明了 embeddings 就必须有探测模型或在 `embedModelUnknown` 里写明原因，没声明的也不许有多余条目 —— 否则「声明了能力但从没被探测过」就是 `SupportsEmbeddings()` 开始说谎的方式。
- 新增覆盖全部 provider 的流策略测试（此前只测了 4 家）。测试遍历 `Providers()`，所以本次扩到 21 家后自动跟着覆盖。写出来当场抓到一个自伤：`[DONE]` 哨兵和非 JSON 帧的过滤在四个 reader 里各写了一份，gemini 和 anthropic 那两份缺失，于是「流式改严格」把中转站补的 `data: [DONE]`、以及合法的空 `data:` 行也判成了厂商数据损坏。四份实现已收敛到 `provider.ClassifyFrame`。
