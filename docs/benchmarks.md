# 基准测试基线

这里的数字是**记录，不是门禁**。没有任何阈值会让 CI 变红 —— 基准的作用是让一次改动带来的开销变化在 review 里看得见，而不是用一台机器的抖动去否决一次提交。

## 怎么复现

```bash
make bench                                  # 全部，每项 1s（BENCHTIME 可覆盖）
make bench BENCHTIME=200x                   # 固定次数，快速冒烟

go test ./internal/sse -run '^$' -bench . -benchmem   # 单包
```

`-run '^$'` 是必须的：不加的话普通测试会跟着跑一遍，数字里混进测试的开销。

## 记录环境

| | |
|---|---|
| CPU | Apple M4 |
| OS | macOS 26.0.1（darwin/arm64） |
| Go | go1.27.1 |
| 采集 | `-benchmem -count=3`，下表取 3 轮中位数 |

换机器、换 Go 版本都会让数字整体平移，比较时请在同一次采集内比。

## 基线

### `retry.backoff`（根包）

`backoff` 是重试路径上唯一的纯计算 —— 其余部分（attempt 循环、定时器）被运行时调度主导。它每次失败尝试跑一次，且最容易在不经意间长出开销（一次 `time.Parse`、一次格式化、一行日志）。

本节于 2026-10-16 在 macOS 27.0.1（Apple M4、Go 1.27.1）重新采集，仍取 `-benchmem -count=3` 的中位数。HTTP-date 输入设为采集开始后 24 小时，返回等待时间由 `MaxBackoff` 截断为 30 秒；计时结束后断言这一结果，避免日期过期后静默切换到计算退避。

| 场景 | ns/op | B/op | allocs/op |
|---|---|---|---|
| 计算退避，第 3 次 | 41.98 | 8 | 1 |
| 计算退避，第 24 次（撞上 MaxBackoff） | 44.09 | 8 | 1 |
| `Retry-After: 7`（delta-seconds） | 66.81 | 8 | 1 |
| `Retry-After: <HTTP-date>` | 185.4 | 88 | 3 |
| 同上，但 `RespectRetryAfter` 关闭 | 8.43 | 0 | 0 |

### SSE 帧解析（`internal/sse`）

64 个事件一条流，事件形如 `event: response.output_text.delta` + 一行 JSON。

| 场景 | ns/op | B/op | allocs/op | MB/s |
|---|---|---|---|---|
| LF | 33560 | 54560 | 772 | 324 |
| CRLF | 33750 | 54560 | 772 | 328 |
| LF，每次只读 8 字节 | 43507 | 54592 | 773 | 250 |
| LF，单个 64 KiB 的 `data:` 字段 | 8814115 | 23000363 | 1796 | 477 |
| 单事件流（含一次 `bufio.Reader` 建立） | 945.5 | 5168 | 16 | 179 |

### Batch JSONL（`protocol/openaibatch`）

| 场景 | ns/op | B/op | allocs/op | MB/s |
|---|---|---|---|---|
| `OutputReader.Next`，128 行短行（body 64 B） | 189090 | 174032 | 1028 | 238 |
| `OutputReader.Next`，8 行大 body（256 KiB） | 3518624 | 5319839 | 86 | 600 |
| `EncodeInput`，128 行 | 28073 | 0 | 0 | 670 |

### OpenAI 兼容流（`provider/compat`）

一条流 = 1 个 role 帧 + 256 个 delta 帧 + 1 个 finish 帧 + `[DONE]`。

| 场景 | ns/op | ns/chunk | B/op | allocs/op | MB/s |
|---|---|---|---|---|---|
| `delta.content` | 499518 | 1936 | 355199 | 3359 | 78.6 |
| `delta.reasoning` | 498229 | 1931 | 355182 | 3359 | 78.8 |

### Responses Accumulator（`protocol/responses`）

事件只解析一次，计时部分只有 `Add`。

| 场景 | ns/op | ns/event | B/op | allocs/op |
|---|---|---|---|---|
| 文本流，128 个 delta | 113415 | 840.1 | 152208 | 780 |
| 文本流，512 个 delta | 302455 | 582.8 | 1099929 | 1191 |
| 推理流，128 个 delta | 60541 | 451.8 | 99129 | 500 |
| 未知 output item，128 个 delta（被有意跳过） | 33939 | 259.1 | 31894 | 309 |

## 这些数字说明了什么

- **`backoff` 不是零分配的。** 3 个尊重 `Retry-After` 的场景都是 1 次 / 8 B 分配，来自 `RetryAfter` 里的 `errors.As`；不尊重时是 0 次 / 8.43 ns。绝对值可以忽略，但"纯计算"这个说法不成立。
- **HTTP-date 形态的 `Retry-After` 比 delta-seconds 贵约 3 倍**（185 ns / 3 次分配 vs 67 ns / 1 次），日期解析是额外开销的主要来源。只有当厂商真的发日期形态时才会付。
- **SSE 的读取边界与解析成本无关，但读得碎要加钱。** 每次 8 字节读比整块读慢约 30%，而[分块无关性是有测试钉住的](../internal/sse/decoder_test.go) —— 这条记录的是常量开销，不是正确性。
- **大的 `data:` 字段按字节增长，churn 远大于载荷。** 单帧 64 KiB 时，4 MiB 的流会产生约 23 MB 分配：`readLine` 逐字节追加行长，`d.data` 再从零长一遍。不是 bug，但意味着放大 `maxEventBytes` 时要按分配量而不是按上限估内存。
- **Batch 的 `OutputReader` 会保留整行原始字节**（ADR 0002 的"逐字节原样"契约），所以 2 MiB 文件约 5.3 MB 分配。这是刻意换来的保真度，基线把它的价格钉在这里。
- **两种增量字段在当前帧尺寸下成本接近**（498 µs vs 500 µs，差在噪声里）。两条路径都会执行字段提升检查，这组数据无法单独衡量第二次 JSON 解析的成本。帧变大了要重测。
- **Accumulator 的每事件成本随流变长而下降**（840 → 583 ns/event），因为流的固定开销（4 个生命周期事件 + 终止事件对整份响应做一次 JSON 克隆）被摊薄了。看长流要看摊销后的值，看短流要看固定成本。
- **被跳过的路径也很便宜**：未知 output item 走的只是守卫检查，259 ns/event，不到文本路径的 1/3。这是"宁可不解码也不猜语义"的定价。

## 改动时怎么用

改到上面任何一个包，跑 `make bench BENCHTIME=200x` 看一眼量级；如果数字明显动了而 PR 里没有解释，那就是需要解释的地方。真要把某条变成门禁，先固定机器或改用 `-count=N` + 相对基线比较，别直接用绝对阈值。
