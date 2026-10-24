# llmkit v1.1 分支评审（`feat/v1.1-iteration`）

- 评审日期：2026-10-24
- 评审基线：分支 `feat/v1.1-iteration`，HEAD `3dc2727`（与 `origin/feat/v1.1-iteration` 一致）；对比基线 `main@bab625e`（tag `v1.0.1`）
- 评审范围（已提交与未提交一起看）：
  - **已提交**：`git diff main...HEAD` —— 2 个提交（`5ac781c` 工程门禁 + 性能基线 + Vercel rerank、`3dc2727` 固定覆盖率工具链），34 文件，+3298/−172
  - **未提交**：`git diff HEAD` —— 22 文件，+655/−156
  - **未跟踪**：8 文件，1587 行（DeepSeek vision/ErrorOrigin/probe 的实现与测试、上一轮评审稿）
- 评审方式：按 5 个分区并行评审（CI/门禁、覆盖率与基准、provider 层、probe/集成/示例、根包 API/文档/发布卫生），Lead 独立跑完所有本机门禁并逐条复核 P0/P1/P2 的证据。全程只读：未改动工作区任何文件，未调用真实厂商 API。
- 分级定义：**P0** 发布阻断；**P1** 发布/合入前必须修；**P2** 应修（可排入本迭代或紧随的 patch）；**P3** 建议修/记录。

---

## 一、结论摘要

### 已提交部分：质量高，可以合入

- 两个提交的工程内容与项目一贯标准一致：覆盖率表与 fuzz 包清单从"手工承诺"变成"生成 + 机械守卫"，基准基线建立且明确不做门禁，Vercel 成为第二家 rerank 适配器，SECURITY.md 补齐。
- **本机复跑 + 远端 CI 记录双重确认门禁成立**：HEAD `3dc2727` 的 GitHub Actions run #51 四个 job 全绿；run #50 唯一红灯是 coverage-table，正是 `3dc2727` 要修的问题（说明"Go 补丁版本改变覆盖率口径"不是理论担忧）。合并后每日深度 fuzz 的动态 matrix（run #84）也全绿。
- **API 冻结面干净**：`make api-compat`（基线 `v1.0.1`）只报告 compatible additions，无 `Incompatible changes`；Go 1.22 下限构建通过；零依赖保持。
- 未发现 P0/P1 级代码缺陷。已提交代码的 2 个 P2 都是可修边界问题（OpenRouter 多 choice 资产残留、rerank search_units 语义），不影响本迭代合入判断，但建议在发布前修掉第一个。

### 未提交部分：方向正确、测试扎实，但**当前状态不应直接提交/合入**

- 这批改动是 DeepSeek 模型名迁移（`deepseek-flash`）+ Vision 支持 + `ErrorOrigin` 诊断面 + probe 五态/退出码/流式探测加固。设计取向与项目价值观一致，测试（`probes_test.go` 577 行、`integration_validation_test.go` 175 行、DeepSeek vision/stream 测试）质量高于平均。
- 但存在 **1 个 P1 行为回归**：`probeVision` 把通用上游 400/422/404 从 `N/A` 改成 `FAIL`，会让十余家"默认 chat 模型本无视觉能力"的厂商在默认探测中误红并 `exit 1`，与 README 的五态定义直接冲突。
- 另有 4 个 P2（`isRedAnswer` 过严、`length` 空响应判 FAIL、离线验证测试因 build tag 从不执行、DeepSeek legacy 视觉模型名过度本地拒绝）与若干 P3。
- **且这批改动从未经过 CI**：未提交 = 远端分支不包含它，run #51 并未验证这些代码。本机门禁全绿只能说明"本地可用"。

### 发布判断

**不建议现在直接 tag `v1.1.0`。** 最低限度要先做三件事：修 `probeVision` 语义、按正确提交纪律把未提交批次提交并跑 CI、补全 CHANGELOG 与 README 的 v1.1 条目（详见第五节收口顺序）。代码层风险不大，主要是"文档即规格"与发布纪律问题。

### 上一轮计划完成度（`NEXT_ITERATION_REVIEW.md` 的 v1.1 八项）

**4 项达成（2/3/4/8）、2 项部分（1/7）、2 项未做且未在任何文档说明（5/6）**。详见第四节。另外，本分支还做了计划外的一批 DeepSeek/ErrorOrigin/probe 工作，评审稿没有随分支更新 —— 计划与实现已经分叉，需要一份迭代总结把这条线记录清楚。

---

## 二、门禁实跑结果

以下全部在评审环境实跑（Go 1.27.3；固定口径门禁使用 `.github/coverage-go-version` 指定的 1.27.2）：

| 门禁 | 结果 | 说明 |
|---|---|---|
| `go vet ./...` | ✅ | 全仓零输出 |
| `gofmt -l .` | ✅ | 空 |
| `go test ./...` | ✅ | 26 个包全绿（含 `cmd/llmkit-probe`、`tools/coverage-table`） |
| `go test ./... -race -count=1` | ✅ | 全绿，无 `DATA RACE` |
| `go vet -tags=integration ./...` + `go test -tags=integration ./... -run '^$'` | ✅ | 集成测试可编译（CI 同款步骤） |
| `make lint` | ✅ | gofmt + vet + 零依赖；`go list -m all` 仅主模块 |
| `make cover-check`（已提交快照 `3dc2727`） | ✅ | `README.md: coverage table is current`，总覆盖率 **69.4%（7818/11260）** |
| `make cover-check`（当前工作区） | ✅ | `current`，总覆盖率 **71.1%（8118/11423）** |
| `make check-fuzz` | ✅ | 脚本自测通过；fuzz 发现与源码对账一致：**6 包 / 24 个目标**、无重复 |
| 覆盖率门禁负向测试 | ✅ | 手改 README 数字 69.4→69.5 后按预期红并给逐行 diff |
| `make api-compat`（基线 `v1.0.1`） | ✅ | 只报告 compatible additions，无 incompatible changes |
| Go 1.22 下限 | ✅ | `GOTOOLCHAIN=go1.22.12` 构建通过；CI `min-go-version` job 亦绿 |
| 远端 CI（HEAD `3dc2727`） | ✅ | [run #51](https://github.com/siguago/llmkit/actions/runs/38857271095) 4 job 全绿；run #50 仅 coverage-table 红（由 `3dc2727` 修复）；[deep-fuzz run #84](https://github.com/siguago/llmkit/actions/runs/40026923173) 全绿 |

两条评审环境说明（不是仓库缺陷）：

1. 本机沙箱禁止写 `~/Library/Caches/go-build` 与 `~/go/pkg/mod` 的 stat cache。首次直跑 `test-fuzz-scripts.sh` 会因此失败；把 `GOCACHE`/`GOMODCACHE` 指到可写临时目录后全部通过。真实开发机/CI 不受影响。
2. `check-coverage-table.sh` 强制 `GOTOOLCHAIN=go1.27.2`（本机默认 1.27.3）。这意味着**贡献者首次跑 `make cover-check` 会使用/下载固定工具链**，README 未提示（P3）。

---

## 三、发现列表

### P0-1（条件性发布阻断）· 未跟踪文件是未提交 API 的唯一实现：只提交已跟踪改动会编译失败

- 位置：`provider/error_origin.go`（未跟踪，61 行）是 `provider.WithErrorOrigin` / `ErrorOriginOf` / `ErrorOrigin*` 的唯一实现；`errors.go`、`provider/deepseek/deepseek.go`、`provider/compat/stream.go`（已修改、已跟踪）都引用它。
- 实测复现：把工作区复制到 `/tmp` 后删除 8 个未跟踪文件（模拟 `git commit -am`），`go build ./...` 失败：
  ```
  provider/compat/stream.go:81:25: undefined: provider.WithErrorOrigin
  provider/compat/stream.go:81:62: undefined: provider.ErrorOriginUpstream
  ```
- 覆盖率耦合：当前 README 的 71.1% 依赖 6 个未跟踪测试文件。只恢复实现、不恢复测试时 `make cover-check` 变红（总覆盖率 71.1%→68.7%、`provider/deepseek` 91.4%→74.8%、`cmd/llmkit-probe` 48.9%→19.3%），CI coverage-table job 会失败。
- 影响：这不是代码 bug，而是**下一次提交的操作风险**：最容易发生的错误提交方式（`git commit -am`）会得到一个构建失败的分支。
- 建议：合入前显式 `git add` 全部 8 个未跟踪文件（实现 + 测试 + 文档），分成"实现/测试/文档"2–3 个自洽提交；提交后立刻在干净 checkout 上跑 `go build ./... && go test ./... && make cover-check`。**不要 `git add -A`**（会把过期的 `NEXT_ITERATION_REVIEW.md` 一并带入）。

### P1-1（已确认，未提交）· `probeVision` 把通用上游 400/422/404 从 N/A 改为 FAIL，十余家厂商默认探测会误红并 exit 1

- 位置：`cmd/llmkit-probe/probes.go:902-945`，关键分支 `:922-930`（只有 `DeepSeek && model=="deepseek-v4-pro" && ErrorOriginLocal && IsInvalidRequest` 才判 N/A）；旧实现（`git show HEAD:cmd/llmkit-probe/probes.go`）是 `IsInvalidRequest(err) || IsNotFound(err) → notApplicable`。
- 冲突文档：`README.md:819` 五态定义 `N/A 这家/这个模型没有该能力`；`README.md:821` 退出码定义（FAIL 即非零）；`README.md:65` 明文推荐 `llmkit-probe ollama`。
- 触发条件：默认 chat 模型不支持图像的厂商。`render.go` 的默认模型表里至少 11 家是纯文本模型（`mistral-large-latest`、`glm-4.6`、`MiniMax-M2`、`qwen-plus`、`gpt-oss-120b`、`Qwen/Qwen3-8B`、`llama3.2` 等），除 DeepSeek 外没有 adapter 在本地拦截图像输入 → 上游返回 4xx → 新代码判 FAIL。
- 已验证证据：评审用离线 fake 上游（对图像请求返回 400、其余 200）复现 —— `llmkit-probe mistral` 默认探测输出 `FAIL 多模态图像输入 来源未知：请求被拒绝：... status 400`，汇总 `8 通过 · 1 失败 · 11 不适用`，**exit 1**；同一 fake 改为接受图像后 exit 0。`deepseek -model deepseek-v4-flash`（SDK 本地拒绝）同样 FAIL/exit 1。作者自己的单测 `probes_test.go:276-306` 把"上游 400/404/422 → FAIL"固化为期望。
- 影响：工具的核心用法（"跑一遍看我这家能干什么"）在白名单外的厂商上会误报失败；按退出码做自动化/CI 的场景直接误红。这是本轮引入的行为回归（旧实现 N/A/exit 0）。
- 建议（按推荐顺序）：
  1. 把"本地拒绝"判定从硬编码模型名改为语义判定：`ErrorOriginOf(err)==ErrorOriginLocal && IsInvalidRequest(err) → N/A`。这一条自动覆盖 `deepseek-v4-pro`、`deepseek-v4-flash/未知纯文本模型`，也消除模型名改名后失效的脆弱性。
  2. 对上游 400/422/404：若坚持 FAIL，至少支持 `-vision-model`/`LLMKIT_VISION_MODEL_<PROVIDER>` 指定真正支持视觉的模型，默认模型不支持时给 `N/A（该默认模型不支持图像；用 -vision-model 指定）`；或恢复旧的 404/422→N/A、仅 400 保留 FAIL。
  3. 无论选哪种，README 五态定义、`main.go` 注释、`probes_test.go` 三处必须一起改。

### P1-2（已确认，文档）· CHANGELOG Unreleased 只覆盖约 1/3 变更：已提交的 v1.1 工作全部没有条目

- 位置：`CHANGELOG.md:5-40`（Unreleased 段）。`git diff main...HEAD -- CHANGELOG.md` 为**空**：两个已提交提交完全没有 CHANGELOG 条目。
- 未记录的已提交内容：
  - **功能**：rerank 第二家（Vercel 网关 Cohere 方言 `/v1/rerank`、能力矩阵/README/live 模型表/examples 同步）；
  - **行为修复（按 STABILITY.md 须单列）**：OpenRouter 媒体资产归一化修复（原始条目与归一化条目并存，`GenerateImage.Data` 曾混入垃圾条目）；
  - **工程**：覆盖率表机械化（生成器 + CI diff 守卫 + 固定工具链）、fuzz 包级自动发现 + 对账守卫 + deep-fuzz 动态 matrix、基准基线（7 个 benchmark + `docs/benchmarks.md`）；
  - **文档/治理**：`SECURITY.md`、`examples/rerank`、`live_models_test.go` 与 untagged 守卫、CI 主工具链 1.25→1.27。
- 关联矛盾：`README.md:771` 写 rerank"已完成"，但 `CHANGELOG.md:379`（v0.3.0 已知问题）仍写"rerank 只有 siliconflow 一家实现……没有厂商实测过"；Unreleased 段没有任何 rerank 条目把状态收口。
- 影响：项目的立身之本之一是"文档即规格"。当前分支一旦发布，CHANGELOG 无法回答"v1.1.0 到底改了什么"，用户也无法从升级说明里知道 OpenRouter 行为修复。
- 建议：在 Unreleased 段补 4 小节（新增/修复/工程/文档），或把工程类改动写进"工程与门禁"别册；在 v1.1.0 发布时把 v0.3.0 那条已知问题用一句"已在 v1.1.0 由 Vercel 实现并单测覆盖（线上待 key）"收口。

### P1-3（已确认，文档）· README 的 probe 输出示例是旧渲染格式，且汇总数字与作者自己的实测记录矛盾

- 位置：`README.md:800-816`（示例）vs `cmd/llmkit-probe/render.go:21-22`（`resultTableWidth = 84`、`grandTotalWidth = 81`、`%-10s` 对齐）。
- 证据：示例仍是旧 78 列分隔线、4 字符结果标记、`9 通过 · 1 不适用 · 约 740 tokens`；而 `CHANGELOG.md:24` 的作者实测记录是 `deepseek-flash → 10 通过、10 不适用`、`v4-pro → 9 通过、11 不适用`。同一工具 20 项探测也不可能由 10 行"节选"给出"9 通过/1 不适用"的**汇总**。另外示例里"正确识别图片颜色"也与代码 detail 文案"正确识别为红色"不一致。
- 影响：示例是用户跑之前唯一能看到的输出契约；格式与数字都过时会让用户以为工具坏了。修复成本极低。
- 建议：用固定工具链/伪上游重新生成一段真实输出（或删掉汇总数字、只保留格式示意），README 与 `main.go` 包注释同步。

### P2 · 已提交代码（建议在 v1.1.x 修掉）

| # | 问题 | 位置 | 证据/影响 | 建议 |
|---|---|---|---|---|
| C1 | **OpenRouter 多 choice 仍残留无载荷 raw `Images`**：只清 `choices[0]`，`choices[1..]` 保留解码出的原始条目；`GenerateImage` 从**所有** choice 收集 `Message.Images`，`n>1` 时 `Data` 混入空 URL 垃圾条目 | `provider/openrouter/openrouter.go:140-163`、`stream.go:356-373`、`images.go:63-68` | 已复现（两 choice 各带一张图 → `Data` 3 条，其中 1 条 `Type=image_url, URL=""`）；违反 `MediaAsset` 文档不变量（URL/B64JSON/DataURL/FileID 至少一个非空）。本次修复目标"替换 raw 条目"在多 choice 下未达成 | 按 choice 逐个 `extract→clear→attach`；至少遍历清空所有 choices 的 raw entries；补 n=2 的 chat/stream/GenerateImage 用例 |
| C2 | **rerank 把 Cohere `meta.billed_units.search_units` 映射成 `Usage.RequestCount`**，语义失真 | `provider/compat/rerank.go:153-160`；`provider/types.go:410-419`（RequestCount 是 per-call 语义） | Vercel 官方文档："One search unit covers up to 100 documents"；250 文档 = 1 次请求却报 `RequestCount=3`。≤100 文档时恰好为 1，问题被掩盖；现有测试 `rerank_test.go:248` 用的"1 文档 3 units"比例本身不真实 | 优先 (a)：纯加法新增 `Usage.BilledSearchUnits`（STABILITY 允许给 struct 加字段），`RequestCount` 保持 1；或 (b) 在 `RequestCount` 注释/README/CHANGELOG 明示该方言语义并补 >100 文档用例 |
| C3 | 覆盖率表生成器**核心逻辑零单测**，README 的"有离线回归"只对 CLI 编排成立 | `tools/coverage-table/main.go`（654 行）、`main_test.go`（165 行） | 工具自身覆盖率 11.2%；`readProfile`/`render`/`replaceBlock`/`unclaimed` 均 0.0%。README:948 备注"覆盖率表生成器及固定工具链入口有离线回归"属夸大（生成器是"承诺表"的守卫，自身反而无守卫） | 给四个核心函数各补 2–3 个表驱动用例（含跨包 block 归属、未归属包、marker 缺失/重复、幂等） |
| C4 | CI 入口脚本**吞掉 `go test` 全部输出**：测试失败时零诊断 | `.github/scripts/check-coverage-table.sh:39` | 实测：故意让测试失败 → exit 1，但 stdout/stderr 均 0 字节。CI 上只能看到"job failed" | 失败时回放输出：先写日志，失败时 `cat`；或 `go test ... 2>&1` 到临时文件、成功删/失败打印 |
| C5 | **重复的 coverage marker 对会被静默忽略**：第二份陈旧块永远"current"且写不回 | `tools/coverage-table/main.go`（`replaceBlock`） | 实测：README 里放两份 `<!-- coverage-table:start/end -->`，即使第二份数字陈旧，`--check` 也通过 | 发现多对 marker 直接报错；或要求恰好一对 |
| C6 | README 的 fuzz 命令仍是**手写 5 包清单**（漏 `protocol/openaibatch`），与"包级自动发现"的承诺/守卫自相矛盾 | `README.md:874-878` vs `STABILITY.md:64`、`run-fuzz.sh --all` | 用户照 README 跑会漏一个包；本次迭代的卖点之一就是"再也不会漏" | 改为 `bash .github/scripts/run-fuzz.sh 100000x --all`（或 `make fuzz`） |
| C7 | `docs/benchmarks.md` **采集环境自相矛盾**，另有 2 处算术小错 | `docs/benchmarks.md:21`（macOS 26.0.1）vs `:33`（macOS 27.0.1）；`:70` reasoning MB/s 78.8 应为 79.8；`:60` batch 600≈596.7 | 同一文档两处环境版本不同，削弱"可复现"可信度 | 统一环境描述；修数字；补 Benchmark 标识符与 `-count=3` 具体命令 |
| C8 | `ci.yml` 未声明 workflow 级 `permissions`，与 `deep-fuzz.yml`（`contents: read`）不一致 | `.github/workflows/ci.yml:1-8` | 最小权限缺口（默认 token 权限依赖仓库设置） | 加 `permissions: contents: read` |
| C9 | `check-api-compat.sh` 在无 tag 的仓库里**静默 exit 0** —— API 冻结门禁可无声消失 | `.github/scripts/check-api-compat.sh:36-42`（既有脚本，非本分支引入） | 实测无 tag 仓库：提示后 exit 0。CI 目前靠 `fetch-depth: 0` 兜住，但本地/浅克隆会静默放行 | 加 `--require-baseline` 或"无 tag 时 exit 2"，CI 显式传基线 |

### P2 · 未提交代码（提交前建议修）

| # | 问题 | 位置 | 证据/影响 | 建议 |
|---|---|---|---|---|
| W1 | （即 P1-1）视觉探测 N/A→FAIL 回归 | `probes.go:902-930` | 见上 | 见上 |
| W2 | **`isRedAnswer` 过窄**：只接受纯 `红/红色/red`，正确但"啰嗦"的回答被判 FAIL | `probes.go:1181-1192` | 已复现 7 类正确答案（如 `这张图片是红色的。`）→ FAIL/exit 1；`probes_test.go` 把该过严行为固化为期望 | 归一化后允许"含颜色词且不含否定/其他颜色"的句子（如正则 `\b(red|红)\b` + 排除 `not/不`），并补"啰嗦正确答案"用例 |
| W3 | **`finish_reason=length` 且无正文/思考/usage 时判 FAIL 而非 INCOMPLETE** | `probes.go:1150-1178`（`chatResponseIssue`）；流式同型 `:425-433` | 与 README 五态/`exit 2` 的"未完成"语义冲突；作者 CHANGELOG 又写"真正的空响应仍失败"——两处口径不一致 | 明确边界：`length` 且零可见输出 → INCOMPLETE（预算耗尽）；`stop` 且空 → FAIL。README/CHANGELOG 同步 |
| W4 | **`integration_validation_test.go` 的离线验证在任何常规路径都不会执行** | `integration_validation_test.go:1`（`//go:build integration`） | 默认 `go test ./...` 不编译；CI 只 `go test -tags=integration ./... -run '^$'`（只编译不跑）；`make live -run TestLive` 也不匹配。这个"完全不联网的验证"实际上从没跑过 | 去掉 build tag（它自称"不需要凭据、不联网"），或加一个不依赖 key 的 CI step 专门跑 `-run 'TestIntegrationValidation'` |
| W5 | **DeepSeek legacy 视觉模型名被过度本地拒绝**（置信度中高，待真机确认） | `provider/deepseek/deepseek.go:463-473`（只放行 `deepseek-flash`） | 官方 Vision/定价文档称旧名 `deepseek-v4-flash`、`deepseek-v4-flash-vision-exp` 仍可调用且由支持视觉的 Flash 承接；同 adapter 又把 `deepseek-v4-flash` 分类为 chat。与 README/CHANGELOG"旧名继续本地拒绝"的表述冲突（SDK 主动拒绝 vs 上游可接受） | 二选一：把两个 legacy 名加入图像放行白名单（同时改 `vision_test.go:203` 反向断言与文档）；或维持保守拒绝但在文档明确"这是 SDK 主动拒绝，上游可能接受"。若选前者，另需评估 pro 的 422 与官方 400 的差异（P3） |
| W6 | **`probeErrors` 不继承 `-base-url`/`-timeout`**（既有问题，本轮语义改造后更显眼） | `probes.go:1098`（内部另建 client） | 私有 relay/离线环境会直连官方域名 → 误 FAIL；`ollama`/`vllm` 这类免 key 的 provider 该探测必 FAIL | 把 `opts.baseURL/timeout` 传入 `probeErrors`；免 key provider 判 N/A |
| W7 | Vercel rerank **无真机验收证据** | `provider/vercel/vercel.go:75-84`、`README.md:771` | README 自己写"线上验收仍需各家 key"；CHANGELOG 实测记录只覆盖 DeepSeek。DoD 要求"第二个 rerank 有真机证据" | 发布前用 vERCEL key 跑一次 `TestLiveRerank`；或在 CHANGELOG 明确"线上未验收" |

### P2 · 发布与流程

| # | 问题 | 位置 | 建议 |
|---|---|---|---|
| R1 | **`NEXT_ITERATION_REVIEW.md` 是过期草稿**：仍写 Anthropic Files"上游仍是 beta"（分支已改成 2026-08-19 GA）、Go 下限抬 1.24 建议、`testing.Coverage()` 口径说明有误；且 8 项计划完成度未更新 | `NEXT_ITERATION_REVIEW.md:113,185` 等 | 不要按原样提交。要么更新为"上一轮评审存档 + 本轮完成度"后提交，要么留在本地。**合入前必须二选一**，否则 `git add -A` 会带进过期文档 |
| R2 | **上轮 8 项未闭环且无说明**：`Client.Close()`（第 5 项）、三处 waiter 404 策略统一（第 6 项）未做；`protocol/responses` 覆盖率 71.7% < 75% DoD（第 7 项）；第 1 项无真机验收 | 见第四节 | 在迭代总结/CHANGELOG 里写"本轮只做 A/B/C，D/E 延后到 v1.x"，比"悄悄没做"好 |
| R3 | **提交信息与内容不匹配**：`5ac781c` 标题只提"工程门禁、性能基线与 Vercel rerank"，实际还含 OpenRouter 修复、CI Go 1.25→1.27、SECURITY.md、examples/rerank、live_models 守卫等 | `git show 5ac781c --stat` | 补提交正文或在 PR 描述里列出；后续提交建议一主题一提交 |
| R4 | 未提交批次混装两个主题：DeepSeek 模型迁移/vision/ErrorOrigin 与 probe 语义改造 | `git status` | 拆成两层提交（provider/ErrorOrigin 一层、probe/integration 一层），回滚粒度更细 |

### P3（建议修，不阻断；按文件归并）

| 位置 | 问题 |
|---|---|
| `provider/doc.go:41` | 仍写 "This module is pre-1.0 and the API is not frozen"——v1.0 后已明显漂移（既有问题） |
| `cmd/llmkit-probe/main.go:63-65` | `flag.Parse` 错误分支是死代码（默认 `ExitOnError` 已 `os.Exit(2)`；`-h` 退出 0）；不实测也不影响 |
| `.github/scripts/{check-coverage-table,check-fuzz-targets,run-fuzz,test-fuzz-scripts}.sh` | 新模式 100644，其余脚本 755；建议统一 `chmod +x`（虽都经 `bash` 调用） |
| `.github/workflows/ci.yml:191` | `go-version-file: .github/coverage-go-version` 依赖 setup-go 对非 go.mod 文件的未文档化回退（实测 CI 可用） |
| `README.md:958-975` | 贡献者预检清单缺 `make check-fuzz` / `make cover-check` / `make bench`，与"跑一遍 CI 跑的那套"措辞不符 |
| `README.md:880` | fuzz 段落仍写 `截至 go1.27rc2`，CI 已升 1.27，措辞需同步 |
| `tools/coverage-table` | `percent` 在精确 .x5 边界受 float64 影响（1001/2000→50.0，应 50.1）；`NumStmt` 不一致不校验；`adapterPackages` 死代码；"row matches no package" 错误信息 `%d` 未展开；thin 折叠规则与注释/README 表述不符；`provider/*` 区间行含 `provider/compat` |
| `docs/benchmarks.md` | 复现方法不精确（无 benchmark 名、`make bench` 并行跑包、`-count=3` 未落成命令）；Retry-After 分配描述与表格不一致 |
| `integration_test.go:81` | `TestLiveModelsCoverAllProviders` 仍在 integration tag 内（CI 只编译不执行），与 `live_models_test.go` 把 embed/rerank 守卫迁到 untagged 的做法不一致 |
| `probes.go:158-176` | `probeThinking` 仍硬编码 512 且不检查 `finish_reason=length` |
| `probes.go:400-440` | 流式探测失去"上游疑似缓冲"信号（单帧正文现在计 PASS）；"没有收到任何 chunk" 文案与实际计数口径（只数正文 delta）不符 |
| `probes.go:1150+` / `main.go:26-27` | `describeErr` 对所有未标记错误加"来源未知："前缀，比旧文案更含糊；`-timeout` 帮助文案与"仅流式探测"的真实语义不符；用法错误与 INCOMPLETE 共用退出码 2 |
| `provider/compat/stream.go:81` / `provider/deepseek/*` | envelope 错误多包一层 `errorWithOrigin`：`errors.As/Is` 正常，但"直接类型断言 `*provider.ProviderError`"的调用方会失效（STABILITY 未承诺该用法，建议在 CHANGELOG 提示） |
| `provider/deepseek/deepseek.go` | 官方第三种视觉输入 `file` 块（`file_id`/`file_data`）被本地 422 拒绝；契约外 content 形状（`[]map`/`RawMessage`）可绕过模型/角色校验 |
| `provider/error_origin.go` | 仅覆盖 DeepSeek + compat SSE envelope，其余 adapter 仍 unknown（文档已自述）；"unknown 标记点"缺直接测试 |
| `examples/multiprovider` | 仍用退役 Moonshot 模型名 `kimi-k2-turbo-preview`，"every provider" 名不副实（`key` 可选的两家未覆盖） |
| `examples/vision` | 只传 `-provider` 时 `-model` 默认仍是 `gemini-2.5-flash`（DeepSeek 下会 404） |
| `README.md` / `.env.example` | 模型覆盖变量只列 chat/embed/image，缺 rerank/vision（`examples/rerank` 已存在） |
| `.gitignore` | `:26` 的 `coverage.out` 与 `:4` 既有的 `*.out` 重复；`.workbuddy/`、`.claude/worktrees/`、`.env*` 规则经核对正确 |
| `.github/workflows/*` | Actions 仅按可变 major tag 固定（`@v7`）且无 dependabot；政策问题，记录即可 |

---

## 四、上一轮 `NEXT_ITERATION_REVIEW.md` 8 项计划完成度审计

| # | 计划项 | 状态 | 证据 / 差距 |
|---|---|---|---|
| 1 | rerank 接第二家 + 实测 Cohere 形状 usage | **部分** | Vercel adapters + 能力矩阵/README/live 模型表/examples 均已同步，Cohere 方言单测有；但**无真机实测**（README 自认），`search_units→RequestCount` 语义问题（C2），v0.3.0 已知问题未在 CHANGELOG 收口 |
| 2 | 覆盖率表机械化 | **达成** | 生成器 + `make cover`/`cover-check` + CI `coverage-table` job；本机在提交快照与工作区各自复现 `current`；负向测试（手改数字）正确变红 |
| 3 | fuzz 包清单机械化 | **达成** | `run-fuzz.sh --discover` + `check-fuzz-targets.sh` 对账 + deep-fuzz 动态 matrix + 脚本自测；实测 6 包/24 目标一致，负向/边界用例齐 |
| 4 | 基准测试基线 | **达成** | 7 个 benchmark 覆盖 backoff/SSE/JSONL/compat 流/Accumulator；`ResetTimer`/`ReportAllocs`/结果断言齐；`docs/benchmarks.md` 含机器与复现说明；明确不设门禁（符合"先记录趋势"的建议） |
| 5 | `Client.Close()` | **未做** | 全仓无 `Client.Close` / `CloseIdleConnections`；计划与 README 均未说明延后 |
| 6 | 三处 waiter 404 策略统一 | **未做** | `responses.go:227-236`、`client.go:479-488` 仍"404 一律 continue 到预算耗尽"；只有 batch 有 `waitNotFoundGracePolls`（v1.0.1 的行为）。且上一轮建议里"`WaitVideo` 404 即终态"本身值得重新论证（与既有 video 测试/语义冲突），建议先收回该子项再重排 |
| 7 | 覆盖洼地补齐（compat≥70、openrouter≥60、responses≥75） | **部分** | `provider/compat` 83.2% ✅、`provider/openrouter` 81.7% ✅（新 chat_media_test）、`protocol/responses` 71.7% **< 75%** ❌ |
| 8 | SECURITY.md + examples/rerank | **达成** | SECURITY.md 披露渠道/四类范围/维护者流程齐备，README 指路；examples/rerank 可运行且断言排序结果（top index==2） |

**计划外新增**（本分支实际还做了、上轮计划没有的）：DeepSeek 模型名迁移（`deepseek-flash`/目录 metadata）、DeepSeek Vision、`ErrorOrigin` 诊断面、probe 五态/退出码/流式探测加固（均未提交）。这些是真正的 v1.1 增量之一，但计划文档没有反映。

---

## 五、建议的收口顺序

1. **修 P1-1（probeVision 语义）**：先做决策（本地拒绝→N/A；上游 4xx 保留 FAIL 还是恢复 N/A），一次性改代码 + `probes_test.go` + README 五态/退出码说明 + `main.go` 注释。修完用离线 fake 验证"纯文本模型默认探测 → exit 0"。
2. **按提交纪律提交未提交批次**（P0-1）：显式 `git add` 8 个未跟踪文件 + 22 个修改文件，建议拆 2–3 个自洽提交；提交后干净 checkout 跑 `go build ./... && go test ./... && make cover-check && make check-fuzz && make lint`，再推 CI。
3. **补 CHANGELOG / README（P1-2、P1-3）**：Unreleased 段补齐已提交功能/修复/工程条目；重生成 README probe 示例；把 rerank 线上验收状态、DeepSeek legacy 视觉拒绝策略写清楚。
4. **决定 v1.1.0 范围并记录差距（R1、R2）**：`Client.Close()`/waiter 统一要么做、要么在迭代总结写明延后；`protocol/responses` 覆盖率差距如实记录；`NEXT_ITERATION_REVIEW.md` 更新或排除。
5. **修 P2-C1（OpenRouter 多 choice）与 C2（search_units 语义）**：C1 是本次已提交修复的未闭环部分，建议随 v1.1.0 一起修；C2 若改为纯加法字段需走 api-compat + CHANGELOG。
6. **P2-C3/C4/C5**：给覆盖率生成器补核心单测、让 CI 脚本失败时有诊断、拒绝重复 marker —— 这三个都直接关系到"承诺表守卫"本身的可信度，建议紧随其后。
7. **P2-W2/W3/W4、P3 择机**：`isRedAnswer`、`length` 边界、validation 测试的 build tag 都是低风险高收益的小修。
8. **发布**：以上完成后跑全量 CI，tag `v1.1.0`（纯加法，符合 STABILITY；Go 下限维持 1.22）。

---

## 六、评审方法、限制与"已核对为非问题"

### 方法

- 5 个分区报告（CI/门禁、覆盖率/基准、provider、probe/集成/示例、API/文档）并行产出，每条发现要求 `file:line` + 证据（实测输出或代码引用）+ 分级 + 修法；Lead 独立跑完第二节全部门禁，并对所有 P0/P1 与"已实测"的 P2 做了复核/复现。
- 外部事实（DeepSeek Vision 文档、Vercel rerank 文档、GitHub Actions 运行记录、setup-go 行为）由分区评审联网核对，按"外部数据"对待；本报告已标注哪些结论依赖它们。

### 限制

- **未调用任何真实厂商 API**（评审约束）：DeepSeek Vision/legacy 名的线上有效性、Vercel rerank 真机行为、Anthropic Files GA 声明均未独立验证；DeepSeek 部分有作者 `CHANGELOG.md:22-25`（2026-10-16 真实 key 实测）作为间接证据。
- 基准 `ns/op` 未在安静机器上重新采集（只做了 fixture/算术交叉核对与 `-benchtime=1x` 冒烟），本报告不质疑 docs 数字量级。
- 分支保护/required checks 是否已包含新增的 `coverage-table`/fuzz 步骤，仓库内不可验证，需仓库设置确认。
- 未提交批次的"真机行为"结论（如 isRedAnswer 误判率、厂商 4xx 形态）基于离线 fake 上游复现，属"逻辑可复现"，不是线上统计。

### 已核对为"非问题"（避免下一轮重复排查）

- `ErrorOrigin` API：文档与行为一致（nil/未标记=unknown、包装保留 `errors.Is/As` 与状态码/分类/重试语义、本地 422 可被 `StatusCode` 读到、不凭状态码猜来源）；纯加法，api-compat 干净。
- DeepSeek Vision 请求形态：typed `[]provider.ContentPart` 与 raw `[]any` 两条路径 JSON 一致；图文顺序、`detail`、空 `text` 匿名结构体重建均正确；本地拒绝时**零上游调用**且带 local origin。
- DeepSeek 流式 usage gating：浅拷贝置空 `Usage` 安全，`GetUsage()` 仍返回完整统计，`include_usage` 开/关两态测试真实。
- `compat` SSE envelope 来源标记：只在明确 error envelope 上标记；非 envelope 错误（取消/网络/解析/EOF）保持原错误与 unknown（测试断言了错误同一性）。
- OpenRouter 单 choice 的 raw+normalized 去重修复正确（多 choice 见 C1）；Vercel rerank 路由 `/v1/rerank` 与 Cohere 方言与官方文档一致；能力矩阵同步。
- 覆盖率数字可复现：固定 Go 1.27.2 下提交快照 69.4%（7818/11260）、工作区 71.1%（8118/11423），各自 `current`；两次 profile 排序后逐行一致、渲染逐字节一致；"新包无行"会被生成器拒绝。
- fuzz 发现确定且与源码一致（6 包/24 目标）；`test-fuzz-scripts.sh` 自测通过（沙箱缓存问题除外）；篡改 README 覆盖率会被 CI 同款脚本打回。
- 示例全部 `go build`/`go vet`/`gofmt` 干净；`examples/rerank` 断言排序；examples 内无 `deepseek-v4-flash` 残留；`.env`/`coverage.out` 均被 `.gitignore` 覆盖，未跟踪敏感文件。
- `tools/coverage-table` 自身覆盖率 11.2% 与 README 表格标注一致（但核心逻辑无测试，见 C3）。

---

## 附：分区评审报告

详细的逐文件证据、复现命令与"未覆盖项"在 5 份分区报告中（评审机器上的临时目录，未写入仓库）：
`T1-ci-infra.md`、`T2-coverage-bench.md`、`T3-provider.md`、`T4-probe.md`、`T5-api-docs.md`。本报告已把其中所有 P0/P1/P2 与代表性命中纳入正文。

---

## 八、修复记录（2026-10-24）

评审后按第三节逐项修复。所有改动只在本分支工作区，尚未提交（提交由收口步骤完成，不推送）。修复后门禁复跑结果：

| 门禁 | 结果 |
|---|---|
| `gofmt -l .` / `go vet ./...` / `go build ./...` | ✅ 干净 |
| `go test ./...` / `go test ./... -race -count=1` | ✅ 27 包全绿，无 DATA RACE |
| `go vet -tags=integration ./...` + `go test -tags=integration ./... -run '^$'` | ✅ 27 包可编译 |
| `make lint` | ✅ 含零依赖校验 |
| `make check-fuzz` | ✅ 脚本自测 + 6 包/24 目标发现对账 |
| `make cover` → `make cover-check` | ✅ 重新生成后 `current`；总覆盖率 **71.1% → 73.6%（8481/11517）** |
| `make api-compat`（基线 v1.0.1） | ✅ 仅 compatible additions（含新增 `provider.Usage.BilledSearchUnits`） |
| Go 1.22 下限（`GOTOOLCHAIN=go1.22.12 go build ./...`） | ✅ |
| `llmkit-probe` 退出码实测 | ✅ 无 key/坏参数=2、`-list`=0、`-h`=0 |
| benchmark smoke（`-benchtime=1x`） | ✅ 7 组 benchmark 全部可跑 |

### 逐项状态

| 发现 | 状态 | 修复摘要 |
|---|---|---|
| P0-1 未跟踪耦合 / 提交纪律 | ✅ 已解决 | 8 个未跟踪文件全部纳入提交；未提交批次拆成 3 个自洽提交，提交后在干净树验证 build/test/cover-check |
| P1-1 `probeVision` N/A→FAIL 回归 | ✅ 已修复 | 本地拒绝与上游 400/404/422 → `N/A`（保留上游原文 + `-vision-model` / `LLMKIT_VISION_MODEL_<PROVIDER>` 提示）；401/403/429/5xx/网络 → `FAIL`；离线 fake 上游验证默认探测 `exit 0` |
| P1-2 CHANGELOG 覆盖不全 | ✅ 已修复 | Unreleased 重写为完整 v1.1 条目（新增 / 修复 / 工程与门禁 / 文档 / 已知问题），并明确「计划作为 v1.1.0 发布」 |
| P1-3 README probe 示例过时 | ✅ 已修复 | 按新 renderer 重写 20 行示例（84 列、`%-10s`、含 INCOMPLETE），汇总与 CHANGELOG 实测记录一致（10 通过 / 10 不适用） |
| C1 OpenRouter 多 choice 残留 | ✅ 已修复 | 归一化资产按 choice 归属 + 清空所有 choice 的 raw 条目；n=2 的 chat/stream/GenerateImage 回归测试，`GenerateImage.Data` 不再有空的垃圾条目 |
| C2 rerank `search_units` 语义 | ✅ 已修复 | 新增纯加法 `Usage.BilledSearchUnits`；`RequestCount` 恢复 per-call；250 文档 / 3 units 真实比例测试；README/示例/CHANGELOG 同步 |
| C3 覆盖率生成器核心无测试 | ✅ 已修复 | 21 个顶层测试，工具自身覆盖率 **11.2% → 96.3%**（readProfile/render/replaceBlock/percent/unclaimed 均 100%） |
| C4 CI 脚本吞测试输出 | ✅ 已修复 | `go test` 输出落文件，失败回放完整诊断、成功保持安静；正负路径实测 |
| C5 重复 marker 静默忽略 | ✅ 已修复 | marker 必须恰好各一个；重复/缺失/顺序错误都报错且不写 README |
| C6 README fuzz 手写清单 | ✅ 已修复 | 改为 `run-fuzz.sh 100000x --all`（包清单自动发现） |
| C7 docs/benchmarks | ✅ 已修复 | 环境描述统一（macOS 26.0.1）；reasoning MB/s 78.8→79.8、batch 600→596.7；复现命令精确到 benchmark 名 + `-count=3`；Retry-After 分配描述与表格一致 |
| C8 CI 缺 permissions | ✅ 已修复 | `ci.yml` 增加 workflow 级 `contents: read` |
| C9 api-compat 无 tag 静默放行 | ✅ 已修复 | 默认 fail-closed（exit 2）+ `--allow-missing-baseline`；默认基线排除预发布 tag |
| W2 `isRedAnswer` 过窄 | ✅ 已修复 | 支持「这张图片是红色的。」等啰嗦正确回答；排除否定与其他颜色词（「非常」豁免、`hundred`/`colored` 不误命中）；20+ 正反用例 |
| W3 `length` 空响应判 FAIL | ✅ 已修复 | `finish_reason=length` 一律 `INCOMPLETE`（流式/非流式一致）；`stop` 且空才是 FAIL |
| W4 离线验证测试从不执行 | ✅ 已修复 | 新建无 build tag 的 `live_checks_test.go`（helpers + validation），`liveModels` 守卫迁入默认构建；`go test ./...` 实际执行 |
| W5 DeepSeek legacy 视觉名 | ✅ 已修复 | 放行 `deepseek-v4-flash` / `deepseek-v4-flash-vision-exp`；pro/未知模型/非 user 仍拒绝；正反测试更新 |
| W6 `probeErrors` 不继承 `-base-url` | ✅ 已修复 | 使用 `opts.baseURL/timeout`；免 key provider（Ollama/vLLM）→ `N/A`；httptest 断言 401 走 relay、超时生效 |
| W7 Vercel rerank 无真机证据 | ⚠️ 无法修复（环境限制） | 无 key 不能真机验证；README/CHANGELOG 已如实标注，CI 保留 `TestLiveRerank` 供有 key 时补证据 |
| R1 NEXT_ITERATION_REVIEW 过期草稿 | ✅ 已修复 | 加历史存档横幅（列明已被更新的事实）+ v1.1 落地情况表；作为历史文档提交 |
| R2 计划未闭环且无说明 | ✅ 已记录 | 明确 `Client.Close()`、waiter 统一延后及理由；`protocol/responses` 覆盖率差距记入 CHANGELOG「已知问题与未完成」 |
| R3 提交信息与内容不匹配 | ➖ 不修（历史） | 已推送提交不改写；新提交按主题拆分并在正文写清内容 |
| R4 未提交批次混装 | ✅ 已修复 | 拆为 provider / probe+集成 / 工具链+文档 3 个提交 |
| 其余 P3（可修项） | ✅ 已修复 | `probeThinking` 预算+length、单帧正文标注、`describeErr` 前缀、`-timeout` 文案、死代码；examples vision/multiprovider；覆盖变量与 `.env.example`；`provider/doc.go` v1.0 冻结表述；`error_origin` unknown 测试；README 贡献清单/rc2 措辞/性能基线补 compat；`.gitignore` 冗余；脚本可执行位；setup-go 注释 |
| P3 未修项（有意保留） | ➖ 决策 | Actions SHA pin / dependabot（仓库策略，需单独决定）；DeepSeek 本地 422 vs 官方 400（既有文档化语义，改动属行为变更）；契约外 content 形状透传（provider 内部 API 既有行为，非本轮引入） |

### 修复后遗留（不阻塞）

- `Client.Close()`、三处 waiter 404 策略统一（明确延后，见 CHANGELOG）。
- `protocol/responses` 覆盖率 71.7%，未达上轮 75% DoD。
- Vercel rerank / DeepSeek Vision 线上真机验收需要各厂商 key。
- 本轮新增的 `-vision-model` CLI flag 与 `LLMKIT_VISION_MODEL_*` / `LLMKIT_RERANK_MODEL_*` 环境变量已写入 README 与 `.env.example`。

### 评审后补充（2026-10-25）

- 二次评审发现 `NEXT_ITERATION_REVIEW.md:176-178` 行尾空格会触发 CI 的 patch whitespace 门禁（`git diff --check` / `git show --check` 均 exit 2）。已清理，并复跑两项检查均为 exit 0；全仓其它文件无行尾空白。
