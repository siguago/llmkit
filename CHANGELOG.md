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

- `IsSafeToReplay(err)` —— 判断重放会不会重复计费。与 `IsRetryable` 正交：前者问「会不会多花钱」，后者问「重试有没有戏」，两者都为真才会重放。
- `WithMediaRetry(rc)` —— 单独设定媒体创建的重试策略。
- `WithTransport(rt)` —— 替换底层 `http.RoundTripper`，用于自定义 TLS、连接池、埋点。你的 transport 在链路最底层：凭据头和调用方附加头已经加好、客户端 IP 头已经剥掉，才轮到它，所以埋点用的 transport 无法绕开隐私处理。
- `WithLogger(l)` —— 接收 SDK 的非致命诊断。**默认全静默**，库不该往宿主程序的日志里写东西。
- `WithRequestID(fn)` / `WithRequestIDHeader(name)` —— 每个出站请求打一个自己的 ID（重试的每次尝试都是新 ID）。
- `APIError.RequestID` —— 厂商自己生成的请求 ID，从响应头解析（认 `X-Request-Id`、`Request-Id`、`Cf-Ray` 等多种写法）。报障时厂商要的就是它，而且响应一旦丢弃就再也拿不回来。
- `WithStreamTolerance(t)` / `WithMaxStreamFrameBytes(n)` —— 流式容错策略与单帧上限（默认仍是 1 MiB）。
- `provider.ImageGenerator` / `ImageEditor` / `VideoCreator` / `VideoCanceller` —— 按端点切分的能力接口。`ImageProvider` / `VideoProvider` 保留为二者的组合。
- `Client.SupportsChat()` 和 `provider.NonChatProvider` —— DashScope 和 Volcengine 在本 SDK 里只接了视频端点，`Chat` / `ChatStream` 一直返回 `ErrUnsupported`，但此前无从事先探测（chat 在 `Provider` 接口上，类型断言分辨不出来），README 的能力表还给它们标了「Chat ✅」。
- `provider.ClassifyFrame` —— SSE 帧的统一分类（`[DONE]` 哨兵 / 可跳过 / 应解析）。
- `provider.StreamPolicy` / `StreamDiagnostics` —— adapter 作者用来遵守流策略的共享组件。
- `provider` 包新增包文档，写明哪些类型稳定、哪些是厂商专属会增删、哪些是不作保证的 opaque 透传。

### 修复

- `NormalizeUsage` 只兜底了 `RequestCount == 0`，负值原样透传进按次计费的乘法。改为 `< 1` 兜底。（fuzz 发现）
- `IsRetryable` 里判断网络错误的第二个分支是死代码 —— `*net.OpError` 和 `*net.DNSError` 都实现了 `net.Error`，第一个分支总会先命中。实际行为（只重试超时）没变，但代码现在如实表达它，注释也不再描述一个不存在的行为。
- 单帧上限低于 64 KiB 时不生效：`bufio.Scanner` 的最大 token 取 `max(max, cap(buf))`，而初始缓冲固定 64 KiB。（fuzz 目标写出来后立刻暴露）
- `Chat` / `ChatStream` / `Models` / `Embed` 漏了 `ErrUnsupported` 的归一化，adapter 抛出的 `provider.ErrUnsupported` 到调用方手里 `errors.Is(err, llmkit.ErrUnsupported)` 是 false —— 而 README 的错误处理示例正是这么写的。只有媒体路径做了翻译。
- `probe` 会先用一次 chat 调用验活，对纯视频厂商必然失败，导致整份报告变成 setup 错误，连它们真正支持的视频能力都测不到；`-list` 还会给它们显示一个不存在的默认对话模型。

### 测试

- **默认测试现在真的完全离线。** `TestUntestedAdapters_DefaultBaseURLs` 此前会向 minimax / siliconflow / vercel 的官方地址发真实请求，README 里「所有默认测试都是离线的」并不成立。改为在已取消的 context 下发起请求，从 `*url.Error` 里取出目标 URL —— 不碰网络，断言反而更强（精确比对完整端点，而不只是「不是畸形 URL」）。`provider` 包测试耗时从 3.4s 降到 1.25s。可验证：`HTTPS_PROXY=http://127.0.0.1:1 go test ./...` 全绿。
- EasyRouter 的 live 测试补上 `integration` build tag（此前靠环境变量守卫，实际不会联网，但与仓库其余 live 测试的约定不一致）。
- 新增 14 个 fuzz 目标，覆盖 SSE 帧解析、错误响应解析、多模态内容转换，CI 每次 PR 跑一轮短的，崩溃输入作为 artifact 上传。
- 能力矩阵测试改为逐端点断言，并新增一条：能力探测的结果必须与实际调用行为一致。
- 新增覆盖全部 13 家 provider 的流策略测试（此前只测了 4 家）。写出来当场抓到一个自伤：`[DONE]` 哨兵和非 JSON 帧的过滤在四个 reader 里各写了一份，gemini 和 anthropic 那两份缺失，于是「流式改严格」把中转站补的 `data: [DONE]`、以及合法的空 `data:` 行也判成了厂商数据损坏。四份实现已收敛到 `provider.ClassifyFrame`。
