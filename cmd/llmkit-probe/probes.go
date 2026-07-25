package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"time"

	"github.com/siguago/llmkit"
)

type outcome int

const (
	pass outcome = iota
	fail
	notApplicable
	skipped
)

type result struct {
	name    string
	outcome outcome
	detail  string // one-line summary shown in the table
	body    string // full model output, shown with -v
	elapsed time.Duration
	usage   *llmkit.Usage
}

type report struct {
	provider string
	model    string
	results  []result
	setupErr error
	tokens   int
}

func (r report) failed() int {
	n := 0
	for _, res := range r.results {
		if res.outcome == fail {
			n++
		}
	}
	return n
}

type probeOptions struct {
	model   string
	baseURL string
	media   bool
	verbose bool
	timeout time.Duration
}

// tokenMeter accumulates usage across probes so the run can report what it cost.
type tokenMeter struct {
	mu    sync.Mutex
	total int
}

func (m *tokenMeter) add(u *llmkit.Usage) {
	if u == nil {
		return
	}
	m.mu.Lock()
	m.total += u.TotalTokens
	m.mu.Unlock()
}

// probeProvider runs every probe against one provider and prints results as
// they complete, so a slow reasoning model doesn't look like a hang.
func probeProvider(ctx context.Context, t target, opts probeOptions) report {
	model := opts.model
	if model == "" {
		model = defaultChatModel(t.provider)
	}
	rep := report{provider: t.provider, model: model}

	clientOpts := []llmkit.Option{
		llmkit.WithAPIKey(t.key),
		llmkit.WithTimeout(opts.timeout),
		// One attempt: a probe should report what the provider actually did,
		// not what it did after the SDK papered over a failure.
		llmkit.WithRetry(llmkit.NoRetry()),
	}
	if opts.baseURL != "" {
		clientOpts = append(clientOpts, llmkit.WithBaseURL(opts.baseURL))
	}
	client, err := llmkit.New(t.provider, clientOpts...)
	if err != nil {
		rep.setupErr = err
		printHeader(rep)
		printSetupError(err)
		return rep
	}

	printHeader(rep)

	// Preflight. Without this, an invalid key produces a full report of
	// confident-looking nonsense: every capability probe fails or gets
	// misclassified as "not supported" when the real cause is a 401.
	if err := preflight(ctx, client, model); err != nil {
		rep.setupErr = err
		printPreflightFailure(t.provider, model, err)
		return rep
	}

	meter := &tokenMeter{}
	for _, p := range buildProbes(client, model, t, opts) {
		start := time.Now()
		res := p.run(ctx)
		res.name = p.name
		res.elapsed = time.Since(start)
		meter.add(res.usage)
		rep.results = append(rep.results, res)
		printResult(res, opts.verbose)
	}
	rep.tokens = meter.total
	printSummary(rep)
	return rep
}

// preflight makes the cheapest possible real call to confirm the key and model
// are usable, so the capability probes that follow measure capabilities rather
// than re-measuring one broken credential.
func preflight(ctx context.Context, c *llmkit.Client, model string) error {
	maxTokens := 4
	_, err := c.Chat(ctx, &llmkit.ChatRequest{
		Model:     model,
		Messages:  []llmkit.Message{llmkit.User("hi")},
		MaxTokens: &maxTokens,
	})
	return err
}

type probe struct {
	name string
	run  func(context.Context) result
}

func buildProbes(c *llmkit.Client, model string, t target, opts probeOptions) []probe {
	return []probe{
		{"模型列表", func(ctx context.Context) result { return probeModels(ctx, c) }},
		{"非流式对话", func(ctx context.Context) result { return probeChat(ctx, c, model) }},
		{"流式对话", func(ctx context.Context) result { return probeStream(ctx, c, model) }},
		{"多轮上下文", func(ctx context.Context) result { return probeMultiTurn(ctx, c, model) }},
		{"工具调用", func(ctx context.Context) result { return probeTools(ctx, c, model) }},
		{"结构化输出", func(ctx context.Context) result { return probeStructured(ctx, c, model) }},
		{"推理 / thinking", func(ctx context.Context) result { return probeThinking(ctx, c, model) }},
		{"多模态图像输入", func(ctx context.Context) result { return probeVision(ctx, c, model) }},
		{"Embeddings", func(ctx context.Context) result { return probeEmbeddings(ctx, c, t.provider) }},
		{"图像生成", func(ctx context.Context) result { return probeImage(ctx, c, t.provider, opts.media) }},
		{"视频生成", func(ctx context.Context) result { return probeVideo(ctx, c, opts.media) }},
		{"错误分类", func(ctx context.Context) result { return probeErrors(ctx, t.provider, model) }},
	}
}

// ---------------------------------------------------------------- probes

func probeModels(ctx context.Context, c *llmkit.Client) result {
	if !c.SupportsModels() {
		return result{outcome: notApplicable, detail: "该 provider 无模型列表接口"}
	}
	models, err := c.Models(ctx)
	if err != nil {
		return result{outcome: fail, detail: describeErr(err)}
	}
	if len(models) == 0 {
		return result{outcome: fail, detail: "接口返回空列表"}
	}
	names := make([]string, 0, 3)
	for i, m := range models {
		if i == 3 {
			break
		}
		names = append(names, m.ModelID)
	}
	return result{
		outcome: pass,
		detail:  fmt.Sprintf("%d 个模型", len(models)),
		body:    strings.Join(names, ", ") + " …",
	}
}

func probeChat(ctx context.Context, c *llmkit.Client, model string) result {
	maxTokens := 64
	resp, err := c.Chat(ctx, &llmkit.ChatRequest{
		Model:     model,
		Messages:  []llmkit.Message{llmkit.User("用一句话说明什么是幂等性。")},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		return result{outcome: fail, detail: describeErr(err)}
	}
	text := llmkit.ResponseText(resp)
	if strings.TrimSpace(text) == "" {
		return result{outcome: fail, detail: "回复为空", usage: resp.Usage}
	}
	detail := fmt.Sprintf("%d 字", len([]rune(text)))
	if resp.Usage != nil {
		detail += fmt.Sprintf(" · %d tokens", resp.Usage.TotalTokens)
	} else {
		detail += " · 上游未报 usage"
	}
	return result{outcome: pass, detail: detail, body: text, usage: resp.Usage}
}

func probeStream(ctx context.Context, c *llmkit.Client, model string) result {
	var chunks int
	var firstChunk time.Duration
	start := time.Now()

	text, usage, err := c.StreamText(ctx, model, "从 1 数到 5，用中文数字。", func(string) {
		chunks++
		if chunks == 1 {
			firstChunk = time.Since(start)
		}
	})
	if err != nil {
		return result{outcome: fail, detail: describeErr(err), usage: usage}
	}
	if chunks == 0 {
		return result{outcome: fail, detail: "没有收到任何 chunk（响应可能不是流式）", usage: usage}
	}
	// A single chunk carrying the whole answer means the vendor buffered it —
	// technically a valid response, but not streaming.
	if chunks == 1 {
		return result{
			outcome: fail,
			detail:  "只有 1 个 chunk，上游疑似缓冲后一次性返回",
			body:    text,
			usage:   usage,
		}
	}
	return result{
		outcome: pass,
		detail:  fmt.Sprintf("%d chunks · 首字 %s", chunks, firstChunk.Round(time.Millisecond)),
		body:    text,
		usage:   usage,
	}
}

func probeMultiTurn(ctx context.Context, c *llmkit.Client, model string) result {
	maxTokens := 64
	first, err := c.Chat(ctx, &llmkit.ChatRequest{
		Model:     model,
		Messages:  []llmkit.Message{llmkit.User("记住这个数字：42。简短确认即可。")},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		return result{outcome: fail, detail: "第 1 轮：" + describeErr(err)}
	}

	// Echoing the assistant turn back verbatim is where provider-specific
	// round-trip fields (thinking signatures, thought_signature) must survive.
	second, err := c.Chat(ctx, &llmkit.ChatRequest{
		Model: model,
		Messages: []llmkit.Message{
			llmkit.User("记住这个数字：42。简短确认即可。"),
			*first.Choices[0].Message,
			llmkit.User("我刚才让你记的数字是多少？只回答数字。"),
		},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		return result{outcome: fail, detail: "第 2 轮：" + describeErr(err), usage: first.Usage}
	}

	answer := llmkit.ResponseText(second)
	usage := sumUsage(first.Usage, second.Usage)
	if !strings.Contains(answer, "42") {
		return result{outcome: fail, detail: "未记住上下文，回答：" + oneLine(answer), body: answer, usage: usage}
	}
	return result{outcome: pass, detail: "正确记住 42", body: answer, usage: usage}
}

func probeTools(ctx context.Context, c *llmkit.Client, model string) result {
	tools := []llmkit.Tool{llmkit.NewTool("get_temperature", "查询指定城市的当前气温", map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string", "description": "城市名"}},
		"required":   []string{"city"},
	})}
	msgs := []llmkit.Message{llmkit.User("杭州现在多少度？请使用工具查询。")}

	first, err := c.Chat(ctx, &llmkit.ChatRequest{Model: model, Messages: msgs, Tools: tools})
	if err != nil {
		return result{outcome: fail, detail: describeErr(err)}
	}
	calls := llmkit.ResponseToolCalls(first)
	if len(calls) == 0 {
		return result{
			outcome: notApplicable,
			detail:  "模型未发起工具调用（可能不支持，或选择直接作答）",
			body:    llmkit.ResponseText(first),
			usage:   first.Usage,
		}
	}

	// A tool call is only half the feature — the model must also use the result.
	msgs = append(msgs, *first.Choices[0].Message)
	for _, call := range calls {
		msgs = append(msgs, llmkit.ToolResultJSON(call.ID, map[string]any{"celsius": 26}))
	}
	second, err := c.Chat(ctx, &llmkit.ChatRequest{Model: model, Messages: msgs, Tools: tools})
	if err != nil {
		return result{
			outcome: fail,
			detail:  "回传工具结果失败：" + describeErr(err),
			usage:   first.Usage,
		}
	}

	answer := llmkit.ResponseText(second)
	usage := sumUsage(first.Usage, second.Usage)
	if !strings.Contains(answer, "26") {
		return result{
			outcome: fail,
			detail:  "调用成功但未采用工具结果：" + oneLine(answer),
			body:    answer,
			usage:   usage,
		}
	}
	return result{
		outcome: pass,
		detail:  fmt.Sprintf("%s(%s) → 结果被采用", calls[0].Function.Name, oneLine(calls[0].Function.Arguments)),
		body:    answer,
		usage:   usage,
	}
}

func probeStructured(ctx context.Context, c *llmkit.Client, model string) result {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city":    map[string]any{"type": "string"},
			"celsius": map[string]any{"type": "integer"},
		},
		"required":             []string{"city", "celsius"},
		"additionalProperties": false,
	}
	resp, err := c.Chat(ctx, &llmkit.ChatRequest{
		Model:          model,
		Messages:       []llmkit.Message{llmkit.User("杭州今天 26 度。按 schema 输出。")},
		ResponseFormat: llmkit.JSONSchemaFormat("weather", schema),
	})

	// Structured output has two tiers. A vendor that refuses the strict
	// json_schema tier usually still honors plain json_object, and "supports
	// JSON but not schemas" is far more useful to know than a bare "N/A".
	tier := "json_schema"
	if err != nil && llmkit.IsInvalidRequest(err) {
		resp, err = c.Chat(ctx, &llmkit.ChatRequest{
			Model: model,
			Messages: []llmkit.Message{
				llmkit.User(`杭州今天 26 度。用 JSON 输出，字段 city（字符串）和 celsius（整数）。`),
			},
			ResponseFormat: llmkit.JSONFormat(),
		})
		tier = "json_object"
		if err != nil {
			if llmkit.IsInvalidRequest(err) {
				return result{outcome: notApplicable, detail: "不支持 json_schema，也不支持 json_object"}
			}
			return result{outcome: fail, detail: "降级 json_object 后：" + describeErr(err)}
		}
	}
	if err != nil {
		return result{outcome: fail, detail: describeErr(err)}
	}

	text := llmkit.ResponseText(resp)
	var decoded struct {
		City    string `json:"city"`
		Celsius int    `json:"celsius"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &decoded); err != nil {
		return result{
			outcome: fail,
			detail:  "返回的不是合法 JSON",
			body:    text,
			usage:   resp.Usage,
		}
	}
	if decoded.City == "" || decoded.Celsius == 0 {
		return result{outcome: fail, detail: tier + " 返回合法 JSON 但字段缺失", body: text, usage: resp.Usage}
	}
	detail := tier + " 通过"
	if tier == "json_object" {
		detail = "仅支持 json_object（不支持 json_schema）"
	}
	return result{outcome: pass, detail: detail, body: text, usage: resp.Usage}
}

func probeThinking(ctx context.Context, c *llmkit.Client, model string) result {
	maxTokens := 512
	resp, err := c.Chat(ctx, &llmkit.ChatRequest{
		Model:     model,
		Messages:  []llmkit.Message{llmkit.User("13 和 17，哪个更接近 15？只回答数字。")},
		Thinking:  llmkit.EnableThinking(1024),
		MaxTokens: &maxTokens,
	})
	if err != nil {
		if llmkit.IsInvalidRequest(err) {
			return result{outcome: notApplicable, detail: "该模型不接受 thinking 参数"}
		}
		return result{outcome: fail, detail: describeErr(err)}
	}

	reasoning := llmkit.ResponseReasoning(resp)
	if reasoning == "" {
		// Accepting the parameter without producing a trace is normal for
		// non-reasoning models — the request succeeded, the feature is absent.
		return result{
			outcome: notApplicable,
			detail:  "参数被接受但无推理内容（该模型非推理模型）",
			usage:   resp.Usage,
		}
	}
	detail := fmt.Sprintf("%d 字推理", len([]rune(reasoning)))
	if resp.Usage != nil && resp.Usage.ReasoningTokens > 0 {
		detail = fmt.Sprintf("%d reasoning tokens", resp.Usage.ReasoningTokens)
	}
	return result{outcome: pass, detail: detail, body: oneLine(reasoning), usage: resp.Usage}
}

func probeVision(ctx context.Context, c *llmkit.Client, model string) result {
	// Generated rather than embedded: a real PNG every time, no base64 blob in
	// the source, and no dependency on an external URL being reachable.
	img, err := solidPNG(color.RGBA{R: 220, G: 30, B: 30, A: 255}, 64)
	if err != nil {
		return result{outcome: fail, detail: "构造测试图失败：" + err.Error()}
	}

	maxTokens := 64
	resp, err := c.Chat(ctx, &llmkit.ChatRequest{
		Model: model,
		Messages: []llmkit.Message{llmkit.UserWith(
			llmkit.Text("这张图是什么颜色？只回答颜色名。"),
			llmkit.ImageBytes("image/png", img),
		)},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		if llmkit.IsInvalidRequest(err) || llmkit.IsNotFound(err) {
			return result{outcome: notApplicable, detail: "该模型不接受图像输入"}
		}
		return result{outcome: fail, detail: describeErr(err)}
	}

	answer := llmkit.ResponseText(resp)
	lower := strings.ToLower(answer)
	if strings.Contains(answer, "红") || strings.Contains(lower, "red") {
		return result{outcome: pass, detail: "正确识别为红色", body: answer, usage: resp.Usage}
	}
	return result{
		outcome: fail,
		detail:  "未能识别图像内容：" + oneLine(answer),
		body:    answer,
		usage:   resp.Usage,
	}
}

func probeEmbeddings(ctx context.Context, c *llmkit.Client, providerName string) result {
	if !c.SupportsEmbeddings() {
		return result{outcome: notApplicable, detail: "该 provider 无 embeddings 接口"}
	}
	model := defaultEmbedModel(providerName)
	if model == "" {
		return result{outcome: skipped, detail: "未配置默认 embedding 模型，用 -model 指定"}
	}
	resp, err := c.Embed(ctx, &llmkit.EmbeddingRequest{Model: model, Input: "hello"})
	if err != nil {
		if llmkit.IsNotFound(err) || llmkit.IsInvalidRequest(err) {
			return result{outcome: notApplicable, detail: fmt.Sprintf("模型 %s 不可用：%s", model, describeErr(err))}
		}
		return result{outcome: fail, detail: describeErr(err)}
	}
	if len(resp.Data) == 0 {
		return result{outcome: fail, detail: "返回空结果"}
	}
	vec, ok := resp.Data[0].Embedding.([]any)
	if !ok || len(vec) == 0 {
		return result{outcome: fail, detail: fmt.Sprintf("向量格式异常 %T", resp.Data[0].Embedding)}
	}
	return result{
		outcome: pass,
		detail:  fmt.Sprintf("%s · %d 维", model, len(vec)),
		usage:   resp.Usage,
	}
}

func probeImage(ctx context.Context, c *llmkit.Client, providerName string, media bool) result {
	if !c.SupportsImages() {
		return result{outcome: notApplicable, detail: "该 provider 无图像生成接口"}
	}
	if !media {
		return result{outcome: skipped, detail: "加 -media 启用（会产生较高费用）"}
	}
	model := defaultImageModel(providerName)
	if model == "" {
		return result{outcome: skipped, detail: "未配置默认图像模型"}
	}
	resp, err := c.GenerateImage(ctx, &llmkit.ImageRequest{
		Model:    model,
		Prompt:   "a single red circle on a white background, flat vector",
		Delivery: "inline",
	})
	if err != nil {
		return result{outcome: fail, detail: describeErr(err)}
	}
	if len(resp.Data) == 0 {
		return result{outcome: fail, detail: "未返回图像"}
	}
	a := resp.Data[0]
	if a.URL == "" && a.B64JSON == "" && a.DataURL == "" {
		return result{outcome: fail, detail: "返回的资产没有任何内容载荷"}
	}
	form := "b64"
	if a.URL != "" {
		form = "url"
	}
	return result{
		outcome: pass,
		detail:  fmt.Sprintf("%s · %d 张 · %s", model, len(resp.Data), form),
		usage:   resp.Usage,
	}
}

func probeVideo(ctx context.Context, c *llmkit.Client, media bool) result {
	if !c.SupportsVideo() {
		return result{outcome: notApplicable, detail: "该 provider 无视频生成接口"}
	}
	if !media {
		return result{outcome: skipped, detail: "加 -media 启用（耗时数分钟，费用较高）"}
	}
	return result{outcome: skipped, detail: "视频探测需指定模型，见 README"}
}

// probeErrors deliberately uses an invalid credential, so it costs nothing and
// verifies the SDK classifies a real upstream rejection correctly.
func probeErrors(ctx context.Context, providerName, model string) result {
	c, err := llmkit.New(providerName,
		llmkit.WithAPIKey("sk-invalid-key-for-probing"),
		llmkit.WithRetry(llmkit.NoRetry()),
		llmkit.WithTimeout(30*time.Second),
	)
	if err != nil {
		return result{outcome: fail, detail: err.Error()}
	}
	maxTokens := 8
	_, err = c.Chat(ctx, &llmkit.ChatRequest{
		Model:     model,
		Messages:  []llmkit.Message{llmkit.User("hi")},
		MaxTokens: &maxTokens,
	})
	if err == nil {
		return result{outcome: fail, detail: "无效 key 竟然被接受"}
	}
	status := llmkit.StatusCode(err)
	if status == 0 {
		return result{outcome: fail, detail: "未能捕获上游状态码：" + oneLine(err.Error())}
	}
	if !llmkit.IsAuthError(err) {
		return result{
			outcome: fail,
			detail:  fmt.Sprintf("无效 key 返回 %d，未归类为认证错误", status),
		}
	}
	return result{outcome: pass, detail: fmt.Sprintf("%d → IsAuthError", status)}
}

// ---------------------------------------------------------------- helpers

// solidPNG renders a single-color square as PNG bytes.
func solidPNG(c color.Color, size int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sumUsage(a, b *llmkit.Usage) *llmkit.Usage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &llmkit.Usage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
		ReasoningTokens:  a.ReasoningTokens + b.ReasoningTokens,
	}
}

// describeErr turns an SDK error into something actionable in one line.
func describeErr(err error) string {
	switch {
	case llmkit.IsAuthError(err):
		return "认证失败（key 无效或无权限）"
	case llmkit.IsRateLimited(err):
		if d := llmkit.RetryAfter(err); d > 0 {
			return fmt.Sprintf("限流，建议 %s 后重试", d)
		}
		return "限流"
	case llmkit.IsNotFound(err):
		return "模型不存在或未开通"
	case errors.Is(err, llmkit.ErrUnsupported):
		return "provider 不支持该能力"
	case llmkit.IsInvalidRequest(err):
		return "请求被拒绝：" + oneLine(err.Error())
	case llmkit.IsServerError(err):
		return fmt.Sprintf("上游 %d 错误", llmkit.StatusCode(err))
	case errors.Is(err, context.DeadlineExceeded):
		return "超时（可用 -timeout 调大）"
	default:
		return oneLine(err.Error())
	}
}

// oneLine collapses whitespace and truncates for table display.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 72
	if len([]rune(s)) > max {
		return string([]rune(s)[:max]) + "…"
	}
	return s
}
