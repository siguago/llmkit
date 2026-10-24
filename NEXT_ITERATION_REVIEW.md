# llmkit 全面评审与下一次迭代建议

> **存档说明（2026-10-24 追加）**：本文是 2026-10-11 对 `main@bab625e`（v1.0.1）的历史评审，保留原文以记录当时的判断依据，**不代表当前状态**。其中部分事实与建议已被后续工作更新：
> - Anthropic Files API 已于 2026-08-19 GA（本文写作时仍按 beta 处理）；本库尚未接入，属排期而非上游 beta 限制。
> - Go 下限抬到 1.24 的建议尚未落地，`go.mod` 仍为 1.22，当前策略见 STABILITY.md。
> - 本文 P2-2 对三处 waiter 404 策略的建议中，「`WaitVideo` 404 即终态」子项经复核与既有语义/测试冲突，已在 v1.1 评审中撤回，仅保留「三处策略不一致」的观察。
> v1.1 的实际落地情况见文末「七、v1.1 落地情况（2026-10-24）」。

- 评审日期：2026-10-11
- 评审基线：`main@bab625e`（tag `v1.0.1`），工作区干净
- 代码规模：202 个 `.go` 文件；生产 30,443 行 / 测试 25,822 行
- 评审方式：全量门禁实跑 + 源码逐层通读 + 覆盖率实测 + 文档与实现交叉核对

---

## 一、结论摘要

这是一份成熟度远高于同期个人开源项目的代码库。**工程质量没有"明显缺陷"级别的发现**；问题集中在三类：

1. **有承诺、无守卫**：项目已确立"每条承诺配一条机械守卫"的原则（能力矩阵、重试语义、零依赖都有守卫），但**有两处承诺表仍是手工维护且正在漂移** —— README 覆盖率表与 CI 的 fuzz 包清单。
2. **工程基线缺失**：零 benchmark；无连接池释放入口；Go 下限停留在已 EOL 的 1.22。
3. **路线图待办未动**：README「1.x 纯加法」表四项全部未开始，其中「rerank 第二家」附带清掉一处悬了两版未验证的代码。

下一次迭代建议按 `v1.1.0`（纯加法、不触碰冻结面）组织，见第五节。

---

## 二、门禁实跑结果

| 门禁       | 命令                             | 结果                          |
| -------- | ------------------------------ | --------------------------- |
| 静态检查     | `go vet ./...`                 | 通过，零输出                      |
| 格式       | `gofmt -l .`                   | 空                           |
| 单元测试     | `go test ./...`                | 全绿（含 `cmd/llmkit-probe`）    |
| 竞态       | `go test ./... -race -count=1` | 全绿，无 `DATA RACE`            |
| 零依赖      | `go list -m all` / `go.sum`    | 仅主模块；无 `go.sum`             |
| 总覆盖率     | `go test ./... -coverprofile`  | **66.3%**（README 声称 66%，一致） |
| Fuzz 目标  | `grep '^func Fuzz'`            | 24 个                        |
| **基准测试** | `grep '^func Benchmark'`       | **0 个**                     |

单包覆盖率实测（与 README 表格的偏差见第四节第 1 条）：

```
根包 87.2% | internal/logging 100% | internal/safehttp 96.8% | internal/httpx 94.3%
internal/sse 92.7% | internal/ipprivacy 80.0% | protocol/openaifiles 92.1%
protocol/openaibatch 89.2% | protocol/anthropic 79.9% | protocol/responses 70.6%
provider 77.5% | provider/vercel 98.4% | provider/minimax 90.6% | provider/moonshot 87.5%
provider/anthropic 82.8% | provider/easyrouter 74.3% | provider/dashscope 72.5%
provider/openai 68.3% | provider/volcengine 66.8% | provider/gemini 66.6%
provider/compat 63.4% | provider/deepseek 56.9% | provider/zhipu 50.0%
provider/openrouter 47.7% | cmd/llmkit-probe 21.3%
```

---

## 三、值得保留的差异化优势

这些是评审中反复确认"确实做到了、不是自夸"的部分，下一轮不应为任何新功能牺牲它们：

- **承诺机械化。** `STABILITY.md` 每条政策都指向一条守卫：`apidiff` 门禁、`TestCapabilityMatrix` / `TestNativeProtocolCapabilityMatrix`、各操作的计数 server 重试断言、零依赖三重校验（`go list -m all` + `go mod tidy` 后 diff + 拒绝 `go.sum`）。这是本项目与多数同类库的分水岭。
- **"扣费安全"是第一性语义，不是补丁。** `IsRetryable`（重试有没有戏）与 `IsSafeToReplay`（会不会重复扣费）被刻意设计成正交两问，并在 `ErrorCategoryRateLimit` 的文档注释上写明"它同时断言上游没开始干活"，因为 `IsSafeToReplay` 会据此重放付费操作。把成本语义写进类型契约、而不是散落在调用点，是正确的层次。
- **`Supports*` 不说谎。** 能力按**端点**而非按功能切分，且严格由方法集决定；`compat.NoEmbeddings` / `compat.WithRerank` 用组合来收窄/扩张方法集，避免 Go 方法提升导致 21 家集体虚报。
- **Fuzz 断言的是不变量而非"不 panic"。** 24 个目标断言"容错模式下错误必被跳过""任意分片不改变 SSE 帧""未知 union 必须 Raw 保真""JSONL 失败粘性"等，且已在首跑抓到过真缺陷（v0.9.0）。
- **v1.0.1 自审揪出的四条都是真问题。** 尤其是 `WaitBatch` 空等 26 小时 —— 这是从 `WaitResponse` 抄规则时漏抄前提的典型，能自己发现说明自审有效。
- **文档即规格。** README / CHANGELOG / STABILITY / ADR 之间没有互相矛盾之处，且对"不做什么"（STT/TTS、Moderation、云托管 transport、多 key 轮换）给出了成立的理由而非敷衍。

---

## 四、发现的问题（按优先级）

### P1-1 · README 覆盖率表已漂移 5 处，且是全仓唯一没有守卫的承诺表

**证据.** README `886–907` 行的覆盖率表 vs 本次实测：

| 包                      | README 写的 | 实测         | 偏差             |
| ---------------------- | --------- | ---------- | -------------- |
| 门面层（根包）                | 85%       | 87.2%      | +2.2           |
| `protocol/openaibatch` | 90%       | 89.2%      | **−0.8（低于声称）** |
| `protocol/anthropic`   | 80%       | 79.9%      | **−0.1（低于声称）** |
| `provider/*` 区间        | 48–98%    | 47.7–98.4% | **下限被突破**      |
| `cmd/llmkit-probe`     | 22%       | 21.3%      | −0.7           |

**根因.** 这张表是手写的。项目的通行做法是"承诺靠人记就会漂移，所以每条都有机械守卫"（STABILITY.md 第 53 行原话），能力矩阵有 `TestCapabilityMatrix`、Go 下限有 `min-go-version` job、零依赖有专门 step —— 唯独覆盖率表靠人记得更新，于是它漂移了，而且漂移方向包含"实际低于声称"，这比数字过时更糟：它让读者以为有 90% 的地方实际只有 89.2%。

**建议.** 增加 `make cover-table` 从 `go test -cover` 生成 README 表格片段，CI 里 regenerating + `git diff --exit-code` 守卫（与既有零依赖 step 同型）。若嫌 CI 变慢，退一步：加一条守卫测试，读 README 表格与 `testing.Coverage()` 比对，允许 ±1 的表述误差但拒绝"实际低于声称"。

### P1-2 · fuzz 包清单在 CI 两处手写，无守卫；新增包会静默漏出

**证据.** `.github/workflows/ci.yml:85–91` 的 `run-fuzz.sh` 包参数列表与 `.github/workflows/deep-fuzz.yml` 的 `matrix.include` 都是静态列表；全仓 `grep` 无任何测试或脚本引用这两份文件。`V1_RELEASE_PLAN.md` 第 44 行（M3 DoD）自己点过这个风险，但处置只是"两处都必须加"。

**当前巧合成立.** 全仓含 fuzz 目标的包共 6 个（`internal/sse`、`protocol/anthropic`、`protocol/openaibatch`、`protocol/responses`、`provider`、`provider/compat`），恰好都在这两份清单里 —— 也就是说**今天没有漏**，但没有任何东西阻止明天漏。

**触发路径.** 给 `protocol/openaifiles` 加一个"文件对象解码保真"的 fuzz 目标（这是该项目自己会做的事，因为文件 DTO 有"顶层 null 必须拒绝"的不变量），目标会编译、`go test` 会跑种子语料、CI 会全绿 —— 而 10 万/50 万次的门禁永远不会执行它。这正是 v0.5.0 修过的"新增 fuzz 目标静默漏出 CI"的**同型回归**，只是当时修的是包内目标发现（`go test -list`），包级清单还是手写的。

**建议.** 一条守卫测试：遍历 `go list ./...`，对每个包跑 `go test -list '^Fuzz' <pkg>`，非空者必须出现在两份清单中；或更彻底 —— 让 `run-fuzz.sh` 接受"自动发现含 fuzz 目标的包"，CI 只写一次包范围。

### P1-3 · 零基准测试

**证据.** `grep -c '^func Benchmark'` 全仓 = 0。

**为什么这在**这个**项目里是缺口.** 本项目把性能写进契约级别的地方比比皆是：`internal/logging` 整个包的存在理由就是"跳过昂贵参数构建"（`Enabled` 的注释明写 `StreamDiagnostics.Malformed` 依赖它避免每帧一次 `TruncateForLog` 拷贝）；`WithMaxStreamFrameBytes` 的文档讨论"行缓冲、data 累积、JSON 解码可能同时持有多份接近上限的数据"；`protocol/openaibatch` 的行上限讨论 32 MiB 单行。**这些都是可测量的主张，却没有任何测量。** 更实际的风险是：SSE 帧解析、JSONL 行解码、responses accumulator 都处在"被 fuzz 反复覆盖、会被持续改动"的路径上，没有基线就无法察觉重构引入的分配/吞吐回归。

**建议.** 先建立基线（不设阈值门禁，避免 flaky）：`internal/sse` 帧解析、`protocol/openaibatch` `OutputReader` 逐行解码、`provider/compat` 流读取、`protocol/responses` `Accumulator.Add`、`retry.backoff` 各一组 `BenchmarkXxx`，含 `-benchmem`。等基线稳定后再考虑进 CI（建议只记录、由人看趋势，不做硬阈值）。

### P1-4 · `Client` 无 `Close()`，连接池无法主动释放

**证据.** 一、每个 provider 构造时各自建一个独立 transport：`provider/compat/compat.go:48`、`provider/openai/openai.go:104`、`provider/anthropic/anthropic.go:62`、`provider/gemini/gemini.go:48`、`provider/deepseek/deepseek.go:34`、`provider/openrouter/openrouter.go:39`、`provider/easyrouter/easyrouter.go:38`、`provider/vercel/vercel.go:41`、`provider/volcengine/volcengine.go:66`、`provider/dashscope/dashscope.go:51`、`provider/minimax/minimax.go:48` —— 全部调 `httpx.NewOutbound()` / `httpx.NewClient(0)`，各返回一个**新建的** `*http.Transport`。二、`internal/httpx/httpx.go:35–37` 设 `MaxIdleConns:100`、`MaxIdleConnsPerHost:20`、`IdleConnTimeout:120s`。三、全仓无 `Client.Close` / `CloseIdleConnections` 导出（仅 stream 与 logger/reader 有 `Close`）。

**影响.** 连接数是有界的（120 秒后回收），所以不是泄漏；但在"多租户 / 多 key 轮换 / 测试里反复建 Client"这类真实用法下，每个被丢弃的 Client 会持有最多 20 条/主机的 idle 连接直到自然过期，且调用方**没有任何手段提前释放**。`WithTransport` 的文档还专门承诺"自定义 transport 可用于调优连接池"，但调优出来的池子同样关不掉，语义上是自相矛盾的。

**建议.** 纯加法：`func (c *Client) Close() error`，内部对底层 transport 调 `CloseIdleConnections()`（不打断进行中的请求，与 `http.Client` 的既有语义一致）。需要 `provider.Provider` 增加一个**可选**接口（如 `interface{ CloseIdleConnections() }`），按端点 opt-in 探测，不进 `provider.Provider` 本体 —— 与项目既有的"可选能力按端点切分"一致。

### P2-1 · Go 下限 1.22 已 EOL，抬高到 1.24 可直接删除一处手写垫片

**证据.** `internal/logging/logging.go:25–27` 注释原文："Go 1.24 ships slog.DiscardHandler, which does exactly this. go.mod declares 1.22, so it is spelled out here; **when the floor rises this can become slog.DiscardHandler unchanged in behaviour**."

**为什么现在值得动.** 政策允许（`STABILITY.md`：「Go 最低版本可以在 minor 版本抬升，提前一个版本在 CHANGELOG 预告」），且这是项目自己**预先写好注释、指明了收口方式**的技术债 —— 属于"账已经记好，只差还"。1.24 同时带来 `slog.DiscardHandler`、更完整的泛型别名等，能减少手写代码而非增加。

**建议.** v1.1.0 的 CHANGELOG 里预告「v1.2.0 起 Go 下限抬至 1.24」，v1.2.0 落地并删除 `discardHandler` + 同步 `min-go-version` job 的版本号。注意 CI 主 job 是 1.25，抬到 1.24 后仍保留"下限 job 用 `GOTOOLCHAIN=local`"的全部既有价值。

### P2-2 · 三处 waiter 的 404 容忍策略不一致，`WaitVideo` / `WaitResponse` 与 v1.0.1 的修复方向相反

**证据.** v1.0.1 给 `WaitBatch` / `WaitAnthropicMessageBatch` 增加了「只在**头三次轮询**内吸收 404」，理由是 26 小时预算下空等一天不可接受。但另外两处仍是"404 一律 continue 到预算耗尽"：`responses.go:227–236`（`WaitResponse`，默认 30 分钟）、`client.go:479–488`（`WaitVideo`，默认 30 分钟）。

**判断.** 30 分钟确实比 26 小时轻，当时的取舍（CHANGELOG v1.0.1 原文："`WaitResponse` 的预算是 30 分钟"）站得住，**不是 bug**。但三处 waiter 现在对同一个问题有三种心态，且 `WaitVideo` 有一个额外情形值得单独说：**视频任务的 404 是终态信号**（任务已过期/被清理），继续轮询 30 分钟既无意义也掩盖了病因。

**建议.** 把"有限次数吸收 404"抽成一个共享的轮询策略（`provider` 或根包内部），三处 waiter 统一；对 `WaitVideo` 额外处理"404 即终态返回"，对齐 `IsTerminalVideoStatus` 的既有立场。

### P2-3 · 覆盖率洼地集中在手写翻译层

**证据.** `provider/openrouter` 47.7%（全仓最低，且是少数走**原生** chat 路径的 adapter）、`provider/zhipu` 50.0%、`provider/deepseek` 56.9%、`provider/compat` 63.4%、`protocol/responses` 70.6%。

**判断.** 这些正是"请求构造 + 响应解析"最密集的地方，也是 wire 行为被冻结（STABILITY.md 明确把 wire 行为列入冻结面）的区域 —— 换句话说，**它们是最不该低的**。`provider/compat` 是全仓被复用最广的基类，63.4% 意味着大量错误分支与流分支未被执行。

**建议.** 下一轮针对性补：`provider/compat` 的流错误分支与响应解析边界、`provider/openrouter` 的原生路径与模型任务分类、`protocol/responses` 的 item/content 解码分支。

### P2-4 · 治理文件缺席：无 `SECURITY.md` / `CONTRIBUTING.md`

**证据.** 仓库根目录只有 `LICENSE` / `README.md` / `CHANGELOG.md` / `STABILITY.md` / 两份计划文档 与 `docs/adr/`。无 `SECURITY.md`、`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`；README 第 911–930 行有一节"贡献"，实际承担了 CONTRIBUTING 的职责。

**为什么这个项目特别需要 SECURITY.md.** 它处理 API key、实现了 `internal/safehttp` 的 SSRF 拨号拦截、有 `internal/ipprivacy` 主动剥离客户端 IP 头、并有"跨 origin redirect 不转发凭据头"的明确规则。这些正是需要**私下披露**而非公开 issue 的漏洞类型。README 只说"欢迎 issue 和 PR"，等于引导安全研究者把细节贴进公开 issue。

**建议.** 加 `SECURITY.md`：披露邮箱（或 GitHub Security Advisories 入口）、范围（凭据处理 / SSRF 绕过 / IP 隐私 / redirect 凭据转发）、以及"不要在公开 issue 里贴 PoC"。`CONTRIBUTING.md` 可把 README 的「贡献」一节搬过去（README 保留一句指路），顺带把"改厂商能力矩阵要同步哪三处"写得更显眼。

### P2-5 · `examples/` 缺 rerank 可运行示例

**证据.** `examples/` 现有 14 个目录，覆盖 chat / stream / tools / structured / vision / embeddings / images / videos / responses / anthropic-native / batch / anthropic-batch / multiprovider / production，**无 rerank**。而 rerank 恰好是能力矩阵里唯一"只有一家实现"、且行为最反直觉（打破位置契约）的能力，README 只有一段内联代码。

**建议.** 加 `examples/rerank`，断言落在**排序结果**（相关文档放在候选最后一条）而非"有没有返回"，与既有 `TestLiveRerank` 的立场一致。

### 注记 · 已核对为"非问题"

以下几处经核对**不是缺陷**，记录以免下一轮重复排查：

- 根目录存在 `.env`（4.3 KB，含真实 key）与 `coverage.out`：均被 `.gitignore` 覆盖，`git ls-files` 确认未跟踪，`git status --ignored` 显示为 `!!`。合规。
- `WaitResponse` 30 分钟容忍 404：见 P2-2，是有意识的取舍。
- `provider/volcengine` 不实现 Embeddings、`provider/vercel` 不实现 EditImage：理由已逐条核对厂商文档，判断成立。
- 若干薄封装包覆盖率显示 0%（`siliconflow`、`groq`、`together`、`fireworks`、`cerebras`、`xai`、`mistral`、`ollama`、`vllm`）：这些包的构造与 chat/stream 路径由 `provider` 包的冒烟测试覆盖，README 已如实说明，不是掩盖。

---

## 五、下一次迭代建议：v1.1.0

**定位.** 纯加法，不触碰任何冻结面（不改签名、不删标识符、不改变文档化行为），完全符合 `STABILITY.md`。目标是**把工程基线补齐 + 清掉路线图上最划算的一项功能债**。

### 建议纳入

| # | 项                                    | 类型 | 依据                                                                            | DoD                                                                                                 |
| - | ------------------------------------ | -- | ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| 1 | **rerank 接第二家 + 实测 Cohere 形状 usage** | 功能 | README「1.x 纯加法」表；CHANGELOG v0.3.0「已知问题」明说 Cohere 形状 usage"已按契约实现但**没有厂商实测过**" | 新增一家 rerank adapter；`TestLiveRerank` 覆盖第二家；清掉该条已知问题；能力矩阵与 probe 模型表同步                               |
| 2 | **覆盖率表机械化**                          | 工程 | P1-1，已漂移 5 处                                                                  | `make cover-table` + CI diff 守卫，或至少守卫测试；README 表格数字归位                                               |
| 3 | **fuzz 包清单机械化**                      | 工程 | P1-2，V1 计划自己点名的风险                                                             | 自动发现或守卫测试；含"新包新增 fuzz 目标必须出现在两份清单"的反向用例                                                             |
| 4 | **基准测试基线**                           | 工程 | P1-3                                                                          | `internal/sse` / `openaibatch` / `compat` 流 / `responses` accumulator 各 1 组 `-benchmem`；只记录趋势，不设硬阈值 |


| 5 | **`Client.Close()`** | API（加法） | P1-4 | 可选接口 + 门面方法 + 测试断言进行中请求不被中断 |
| 6 | **三处 waiter 404 策略统一** | 修复 | P2-2 | 共享策略 + `WaitVideo` 404 即终态；计数 server 测试三处方向都钉住 |
| 7 | **覆盖洼地补齐** | 测试 | P2-3 | `provider/compat` ≥70%、`provider/openrouter` ≥60%、`protocol/responses` ≥75% |
| 8 | **SECURITY.md + examples/rerank** | 治理/文档 | P2-4、P2-5 | 有披露入口与范围声明；示例断言排序结果 |

### 建议在 v1.1.0 预告、v1.2.0 落地

| 项                                                       | 依据                                                     |
| ------------------------------------------------------- | ------------------------------------------------------ |
| Go 下限抬至 1.24，删除 `internal/logging` 的手写 `discardHandler` | P2-1；`logging.go:25–27` 已写明收口方式；政策允许 minor 抬升 + 提前一版预告 |

### 建议明确不做（维持现状）

| 不做                                                                                                         | 理由                                                                                         |
| ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| Anthropic Files                                                                                            | 上游仍是 beta（`files-api-2025-04-14`）。`STABILITY.md` 的"beta 上游不进冻结面"是**正确的政策**，不应该因为"想要功能全"而破例 |
| STT / TTS、Moderation、Conversations / WebSocket、云托管 transport（Bedrock / Vertex / Azure）、多 key 轮换、入站 gateway | README 已列为非目标，逐条理由成立                                                                       |
| Batch webhook 事件                                                                                           | 有价值但成本高（需公网回调接收面），且当前 `WaitBatch` 已可用；建议等有真实需求再立计划，不要为了凑齐路线图而动                             |
| 其余厂商的 Files / Batch opt-in                                                                                 | 需要逐家验证真实 wire，属于"验证密度"工作，适合在有该厂商 key 且确有需求时逐家做，不适合一次性铺开                                    |

### 迭代节奏建议

本轮 8 项里，第 2、3 项（两处机械化）本质是**同一件事**：把"手工维护的承诺表"变成"生成 + 守卫"。建议合并为一个 PR 先做 —— 它们是本项目自己已经解决过两次的问题模式（能力矩阵、Go 下限），做起来最快、收益最确定，且能立刻锁住后续所有迭代不再引入同类漂移。第 1 项（rerank 第二家）紧随其后，因为它是唯一同时清掉"功能缺口"和"未验证代码"的一项。

---

## 六、评审方法说明

- 所有结论均基于本机实跑，未依赖文档自述：`go vet` / `gofmt -l` / `go test ./...` / `go test -race` / `go list -m all` / `go test -coverprofile` 均在 `main@bab625e` 上执行。
- 覆盖率与门禁数字取自本次执行输出；文档偏差以实测为准。
- 源码结论均给出行号或文件路径，可逐条复核。
- 未执行 `-tags=integration` 的真实 API 测试与 `make probe`（需真实 key 且产生费用），因此**本评审不覆盖线上 wire 的实测有效性** —— 那部分依据的是项目已有的发布证据与 LIVE 测试设计。

---

## 七、v1.1 落地情况（2026-10-24 补充）

分支 `feat/v1.1-iteration`（HEAD `3dc2727`）对上文 v1.1 建议的落地核对。已提交 = 分支上两个提交；未提交 = 该分支工作区当时尚未提交的 DeepSeek Vision / `ErrorOrigin` / probe 加固批次（后已并入 v1.1 提交）。

| # | 上文建议 | 状态 | 说明 |
|---|---|---|---|
| 1 | rerank 接第二家 + 实测 Cohere 形状 usage | 部分达成 | 已接 Vercel（Cohere 方言 `/v1/rerank`），能力矩阵/README/live 模型表/examples 同步；**线上验收仍需真实 key**；`search_units` 的语义在 v1.1 评审中被修正为独立的 `Usage.BilledSearchUnits`（此前误映射到 `RequestCount`） |
| 2 | 覆盖率表机械化 | 达成 | 生成器 + `make cover`/`cover-check` + CI `coverage-table` job；固定工具链口径可复现，手改表格会被打回 |
| 3 | fuzz 包清单机械化 | 达成 | `run-fuzz.sh --discover` + `check-fuzz-targets.sh` 对账 + deep-fuzz 动态 matrix；新包新增目标不再需要改两份清单 |
| 4 | 基准测试基线 | 达成 | 7 个 benchmark 覆盖 backoff / SSE / Batch JSONL / compat 流 / Responses Accumulator，`docs/benchmarks.md` 记录机器与复现命令；按建议只记录、不设阈值 |
| 5 | `Client.Close()` | **延后** | 未实现。这是新增导出 API 面（可选能力接口 + 各 adapter 的 idle-connection 释放），需要单独设计并同步 API 冻结审查；不是本轮 bug 修复范围。已记入 v1.2 候选。 |
| 6 | 三处 waiter 404 策略统一 | **延后（子项撤回）** | 未实现。重新评估后认为 `WaitResponse`（30 分钟）与 `WaitVideo`（30 分钟）容忍 404 属文档化取舍，统一会改变已在用的行为；「`WaitVideo` 404 即终态」与既有 video 语义/测试冲突，撤回该子项。保留「三处心态不一致」的记录，等有真实需求再动。 |
| 7 | 覆盖洼地补齐 | 部分达成 | `provider/compat` 83.2% ✅、`provider/openrouter` 81.7% ✅；`protocol/responses` 71.7% 仍未达 75% 的 DoD，记入待办 |
| 8 | SECURITY.md + examples/rerank | 达成 | 披露渠道与四类范围齐备；示例可运行并断言排序结果 |

**计划外增量**：本分支还完成了 DeepSeek 模型名迁移（`deepseek-flash`）、DeepSeek Vision（含 legacy 模型名）、`ErrorOrigin` 上游/本地来源标记、`llmkit-probe` 五态结果与流式探测加固。这些不在上文建议内，发布说明以 CHANGELOG 为准。

**已知遗留**（不阻塞 v1.1.0）：`Client.Close()`（见第 5 项）、三处 waiter 策略（见第 6 项）、`protocol/responses` 覆盖率（见第 7 项）、Vercel rerank 与 DeepSeek Vision 的线上真机验收（需各厂商 key）。
