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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/siguago/llmkit"
	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	responsesapi "github.com/siguago/llmkit/protocol/responses"
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
	//
	// Skipped for an adapter covering only non-chat endpoints: its chat methods
	// return ErrUnsupported by design, so preflighting with a chat call would
	// abort the whole report over a capability it never claimed — including the
	// probes that are the only reason to run it.
	if client.SupportsChat() {
		if err := preflight(ctx, client, model); err != nil {
			rep.setupErr = err
			printPreflightFailure(t.provider, model, err)
			return rep
		}
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
		{"模型任务", func(ctx context.Context) result { return probeModelTaskTypes(ctx, c) }},
		{"OpenAI Responses", func(ctx context.Context) result { return probeResponses(ctx, c, model) }},
		{"Anthropic Messages", func(ctx context.Context) result { return probeAnthropicMessages(ctx, c, model) }},
		{"非流式对话", func(ctx context.Context) result { return probeChat(ctx, c, model) }},
		{"流式对话", func(ctx context.Context) result { return probeStream(ctx, c, model) }},
		{"多轮上下文", func(ctx context.Context) result { return probeMultiTurn(ctx, c, model) }},
		{"工具调用", func(ctx context.Context) result { return probeTools(ctx, c, model) }},
		{"结构化输出", func(ctx context.Context) result { return probeStructured(ctx, c, model) }},
		{"推理 / thinking", func(ctx context.Context) result { return probeThinking(ctx, c, model) }},
		{"多模态图像输入", func(ctx context.Context) result { return probeVision(ctx, c, model) }},
		{"Embeddings", func(ctx context.Context) result { return probeEmbeddings(ctx, c, t.provider) }},
		{"Rerank", func(ctx context.Context) result { return probeRerank(ctx, c, t.provider) }},
		{"图像生成", func(ctx context.Context) result { return probeImage(ctx, c, t.provider, opts.media) }},
		{"图像编辑", func(context.Context) result { return probeImageEdit(c) }},
		{"视频生成", func(ctx context.Context) result { return probeVideo(ctx, c, opts.media) }},
		{"视频取消", func(context.Context) result { return probeVideoCancel(c) }},
		{"错误分类", func(ctx context.Context) result { return probeErrors(ctx, t.provider, model) }},
	}
}

// ---------------------------------------------------------------- probes

func probeResponses(ctx context.Context, c *llmkit.Client, model string) result {
	if !c.SupportsResponses() {
		return result{outcome: notApplicable, detail: "无原生 Responses transport"}
	}
	if !c.SupportsResponseStreaming() || !c.SupportsResponseRetrieval() ||
		!c.SupportsResponseCancellation() || !c.SupportsResponseDeletion() ||
		!c.SupportsResponseInputItems() || !c.SupportsResponseTokenCount() {
		return result{outcome: fail, detail: "Responses 能力声明不完整"}
	}
	count, err := c.CountResponseInputTokens(ctx, &responsesapi.TokenCountRequest{
		Model: model,
		Input: responsesapi.NewTextInput("hello"),
	})
	if err != nil {
		return result{outcome: fail, detail: describeErr(err)}
	}
	if count == nil || count.InputTokens <= 0 {
		return result{outcome: fail, detail: "input token count 为空或为零"}
	}
	return result{outcome: pass, detail: fmt.Sprintf("原生端点可用 · input=%d tokens", count.InputTokens)}
}

func probeAnthropicMessages(ctx context.Context, c *llmkit.Client, model string) result {
	if !c.SupportsAnthropicMessages() {
		return result{outcome: notApplicable, detail: "无原生 Messages transport"}
	}
	if !c.SupportsAnthropicMessageStreaming() || !c.SupportsAnthropicTokenCount() {
		return result{outcome: fail, detail: "Messages 能力声明不完整"}
	}
	count, err := c.CountAnthropicMessageTokens(ctx, &anthropicapi.TokenCountRequest{
		Model: model,
		Messages: []anthropicapi.MessageParam{{
			Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent("hello"),
		}},
	})
	if err != nil {
		return result{outcome: fail, detail: describeErr(err)}
	}
	if count == nil || count.InputTokens <= 0 {
		return result{outcome: fail, detail: "input token count 为空或为零"}
	}
	return result{outcome: pass, detail: fmt.Sprintf("原生端点可用 · input=%d tokens", count.InputTokens)}
}

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

// probeModelTaskTypes is the only check that runs the per-model classification
// against a live catalog. The adapters that implement it classify by hardcoded
// model-ID allowlists (gemini, deepseek) or by upstream metadata fields whose
// vocabulary can grow (vercel, openrouter), and neither failure mode is visible
// offline: when a vendor ships a new model, nothing errors and no unit test
// breaks — the model just goes quietly unclassified. So the unclassified list
// is the actual output worth reading here, not the pass mark.
func probeModelTaskTypes(ctx context.Context, c *llmkit.Client) result {
	if !c.SupportsModelTaskTypes() {
		return result{outcome: notApplicable, detail: "该 provider 无逐模型任务分类"}
	}
	models, taskTypes, err := c.ModelsWithTaskTypes(ctx)
	if err != nil {
		return result{outcome: fail, detail: describeErr(err)}
	}
	if len(models) == 0 {
		return result{outcome: fail, detail: "接口返回空列表"}
	}

	byTask := make(map[string][]string)
	var unknown []string
	for _, m := range models {
		tasks := taskTypes[m.ModelID]
		if len(tasks) == 0 {
			unknown = append(unknown, m.ModelID)
			continue
		}
		key := strings.Join(tasks, " + ")
		byTask[key] = append(byTask[key], m.ModelID)
	}

	// Align on the widest label actually present. A fixed budget would feed
	// padDisplay a value it truncates, and "image.generate + image.edit" is
	// already 27 columns — silently clipping the task name would defeat the
	// point of printing it.
	labels := sortedKeys(byTask)
	unknownLabel := fmt.Sprintf("未分类 (%d)", len(unknown))
	width := 0
	for _, label := range append(append([]string{}, labels...), unknownLabel) {
		if w := displayWidth(label); w > width {
			width = w
		}
	}

	var body strings.Builder
	for _, key := range labels {
		fmt.Fprintf(&body, "%s  %s\n", padDisplay(key, width), previewModelIDs(byTask[key]))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&body, "%s  %s\n",
			padDisplay(unknownLabel, width), previewModelIDs(unknown))
		body.WriteString("\n未分类不是错误 —— adapter 宁可留空也不猜。但如果这里出现了本该识别的当前模型，\n就是分类白名单该更新的信号。\n")
	}

	// Every model unclassified means the allowlist no longer matches anything
	// the vendor is serving, which is exactly the silent drift this probe exists
	// to catch. A partially-unclassified catalog is normal and stays a pass.
	if len(unknown) == len(models) {
		return result{
			outcome: fail,
			detail:  fmt.Sprintf("%d 个模型全部未分类", len(models)),
			body:    body.String(),
		}
	}
	return result{
		outcome: pass,
		detail:  fmt.Sprintf("%d/%d 已分类 · %d 未知", len(models)-len(unknown), len(models), len(unknown)),
		body:    body.String(),
	}
}

// previewModelIDs keeps the -v body scannable on catalogs with hundreds of
// entries, while still naming enough models to recognize a family.
func previewModelIDs(ids []string) string {
	const max = 6
	if len(ids) <= max {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:max], ", ") + fmt.Sprintf(", …（另 %d 个）", len(ids)-max)
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func probeChat(ctx context.Context, c *llmkit.Client, model string) result {
	if !c.SupportsChat() {
		return result{outcome: notApplicable, detail: "该 provider 无对话接口"}
	}
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
	if !c.SupportsChat() {
		return result{outcome: notApplicable, detail: "该 provider 无对话接口"}
	}
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
	if !c.SupportsChat() {
		return result{outcome: notApplicable, detail: "该 provider 无对话接口"}
	}
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
	if !c.SupportsChat() {
		return result{outcome: notApplicable, detail: "该 provider 无对话接口"}
	}
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
	if !c.SupportsChat() {
		return result{outcome: notApplicable, detail: "该 provider 无对话接口"}
	}
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
	if !c.SupportsChat() {
		return result{outcome: notApplicable, detail: "该 provider 无对话接口"}
	}
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
	if !c.SupportsChat() {
		return result{outcome: notApplicable, detail: "该 provider 无对话接口"}
	}
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

// probeRerank asks the reranker to pick the one document that answers the
// query out of three, then checks that it actually did.
//
// A reranker that returns results at all is not proof it works — a broken
// integration can return the input order with flat scores and look fine. So the
// assertion is on the outcome: the relevant document must come back first.
func probeRerank(ctx context.Context, c *llmkit.Client, providerName string) result {
	if !c.SupportsRerank() {
		return result{outcome: notApplicable, detail: "该 provider 无 rerank 接口"}
	}
	model := defaultRerankModel(providerName)
	if model == "" {
		return result{outcome: skipped, detail: "未配置默认 rerank 模型"}
	}
	// The panda sentence is last, so a passthrough implementation that ignores
	// the query cannot accidentally pass.
	docs := []string{
		"苹果是一种常见的水果。",
		"汽车通常有四个轮子。",
		"熊猫是中国特有的哺乳动物，以竹子为食。",
	}
	const wantIdx = 2
	resp, err := c.Rerank(ctx, &llmkit.RerankRequest{
		Model:     model,
		Query:     "什么是熊猫？",
		Documents: docs,
	})
	if err != nil {
		if llmkit.IsNotFound(err) || llmkit.IsInvalidRequest(err) {
			return result{outcome: notApplicable, detail: fmt.Sprintf("模型 %s 不可用：%s", model, describeErr(err))}
		}
		return result{outcome: fail, detail: describeErr(err)}
	}
	if len(resp.Results) == 0 {
		return result{outcome: fail, detail: "返回空结果"}
	}
	top := resp.Results[0]
	if top.Index != wantIdx {
		return result{
			outcome: fail,
			detail:  fmt.Sprintf("%s · 最相关判为第 %d 条，应为第 %d 条", model, top.Index, wantIdx),
		}
	}
	return result{
		outcome: pass,
		detail:  fmt.Sprintf("%s · %d 条候选，命中第 %d 条（%.3f）", model, len(docs), top.Index, top.RelevanceScore),
		usage:   resp.Usage,
	}
}

func probeImage(ctx context.Context, c *llmkit.Client, providerName string, media bool) result {
	if !c.SupportsImageGeneration() {
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
		Model:  model,
		Prompt: "a single red circle on a white background, flat vector",
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

// probeImageEdit reports the editing endpoint separately from generation: the
// aggregator gateways forward one and not the other, which a single "images"
// row would hide.
func probeImageEdit(c *llmkit.Client) result {
	if !c.SupportsImageEditing() {
		return result{outcome: notApplicable, detail: "该 provider 无图像编辑接口"}
	}
	// Editing needs a source image to upload, which the probe has no business
	// inventing — report that the endpoint exists and stop there.
	return result{outcome: skipped, detail: "需自备源图，见 README"}
}

func probeVideo(ctx context.Context, c *llmkit.Client, media bool) result {
	if !c.SupportsVideoGeneration() {
		return result{outcome: notApplicable, detail: "该 provider 无视频生成接口"}
	}
	if !media {
		return result{outcome: skipped, detail: "加 -media 启用（耗时数分钟，费用较高）"}
	}
	return result{outcome: skipped, detail: "视频探测需指定模型，见 README"}
}

// probeVideoCancel is a pure capability check — submitting a job just to cancel
// it would cost real money for no information.
func probeVideoCancel(c *llmkit.Client) result {
	if !c.SupportsVideoGeneration() {
		return result{outcome: notApplicable, detail: "该 provider 无视频生成接口"}
	}
	if !c.SupportsVideoCancellation() {
		return result{outcome: notApplicable, detail: "有视频生成但无取消端点：任务提交后无法中止"}
	}
	return result{outcome: pass, detail: "有 cancel 端点"}
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
	if !c.SupportsChat() {
		// This probe asserts that a rejected credential is classified correctly,
		// and it uses a chat call to provoke the rejection. Without chat there is
		// nothing to provoke it with.
		return result{outcome: notApplicable, detail: "该 provider 无对话接口"}
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
	usage := &llmkit.Usage{
		PromptTokens:          a.PromptTokens + b.PromptTokens,
		CompletionTokens:      a.CompletionTokens + b.CompletionTokens,
		TotalTokens:           a.TotalTokens + b.TotalTokens,
		CachedTokens:          a.CachedTokens + b.CachedTokens,
		ReasoningTokens:       a.ReasoningTokens + b.ReasoningTokens,
		PromptCacheHitTokens:  a.PromptCacheHitTokens + b.PromptCacheHitTokens,
		PromptCacheMissTokens: a.PromptCacheMissTokens + b.PromptCacheMissTokens,
		CacheCreationTokens:   a.CacheCreationTokens + b.CacheCreationTokens,
		Cost:                  a.Cost + b.Cost,
		ImageCount:            a.ImageCount + b.ImageCount,
		DurationMs:            a.DurationMs + b.DurationMs,
		RequestCount:          a.RequestCount + b.RequestCount,
		MediaCount:            a.MediaCount + b.MediaCount,
	}
	if a.CacheCreationTokensDetails != nil && b.CacheCreationTokensDetails != nil {
		details := &llmkit.CacheCreationTokensDetails{
			Ephemeral5mTokens: a.CacheCreationTokensDetails.Ephemeral5mTokens + b.CacheCreationTokensDetails.Ephemeral5mTokens,
			Ephemeral1hTokens: a.CacheCreationTokensDetails.Ephemeral1hTokens + b.CacheCreationTokensDetails.Ephemeral1hTokens,
		}
		if details.Ephemeral5mTokens+details.Ephemeral1hTokens == usage.CacheCreationTokens {
			usage.CacheCreationTokensDetails = details
		}
	}
	if usage.CachedTokens > 0 {
		usage.PromptTokensDetails = &llmkit.PromptTokensDetails{CachedTokens: usage.CachedTokens}
	}
	if usage.ReasoningTokens > 0 {
		usage.CompletionTokensDetails = &llmkit.CompletionTokensDetails{ReasoningTokens: usage.ReasoningTokens}
	}
	return usage
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
