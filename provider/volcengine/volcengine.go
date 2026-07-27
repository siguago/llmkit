package volcengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const defaultBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

type Provider struct {
	// Ark serves chat, streaming and model listing OpenAI-compatible on the same
	// /api/v3 base the native video endpoints use, so they are delegated
	// wholesale. Doubao models accept either a model ID or an endpoint ID (ep-...)
	// in the model field; both are opaque to us.
	//
	// Embeddings are deliberately NOT delegated, and unlike MiniMax this is not a
	// translation someone could just sit down and write.
	//
	// Ark's OpenAI-shaped text embeddings API (/api/v3/embeddings,
	// doubao-embedding-text-*) now sits under the vendor's 下线文档归档 —
	// retired-doc archive — and the current model list carries only the multimodal
	// doubao-embedding-vision-* models, served at /api/v3/embeddings/multimodal.
	// That endpoint is a different operation, not a different spelling: its `input`
	// is the parts of ONE item ({"type":"text"} / image_url / video_url) fused into
	// a single vector, and it answers with `data` as one object rather than an
	// array. The unified Embed contract is positional and plural — N inputs, N
	// vectors, Data[i] describing Input[i].
	//
	// Bridging the two would mean fanning one Embed call out into N HTTP requests,
	// turning a batch of 100 chunks into 100 billed calls. That cost cliff does not
	// belong hidden behind a capability probe that says "yes", so the honest answer
	// stays no. The multimodal surface is worth having on its own terms — a
	// MultimodalEmbedder interface where fused input is the point — not squeezed
	// through this one.
	*compat.NoEmbeddings
	baseURL string
	client  *http.Client
}

func New(baseURL string) *Provider {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		NoEmbeddings: compat.NewNoEmbeddings(compat.Config{
			ProviderName: "volcengine",
			BaseURL:      baseURL,
		}),
		baseURL: baseURL,
		client: &http.Client{
			Timeout:   300 * time.Second,
			Transport: httpx.NewOutbound(),
		},
	}
}

func (p *Provider) Name() string { return "volcengine" }

func (p *Provider) CreateVideoJob(ctx context.Context, apiKey, model string, req *provider.VideoCreateRequest) (*provider.VideoJob, error) {
	body := buildCreateBody(model, req)
	raw, err := p.doJSON(ctx, http.MethodPost, apiKey, p.baseURL+"/contents/generations/tasks", body)
	if err != nil {
		return nil, err
	}
	return parseCreateResponse(raw, model)
}

func (p *Provider) GetVideoJob(ctx context.Context, apiKey string, job *provider.VideoJob) (*provider.VideoJob, error) {
	if job == nil || job.ProviderJobID == "" {
		return nil, fmt.Errorf("provider_job_id required")
	}
	endpoint := p.baseURL + "/contents/generations/tasks/" + url.PathEscape(job.ProviderJobID)
	raw, err := p.doJSON(ctx, http.MethodGet, apiKey, endpoint, nil)
	if err != nil {
		return nil, err
	}
	out, err := parseStatusResponse(raw, job.Model)
	if err != nil {
		return nil, err
	}
	if out.ID == "" {
		out.ID = job.ID
	}
	if out.ProviderJobID == "" {
		out.ProviderJobID = job.ProviderJobID
	}
	return out, nil
}

func (p *Provider) CancelVideoJob(ctx context.Context, apiKey string, job *provider.VideoJob) (*provider.VideoJob, error) {
	if job == nil || job.ProviderJobID == "" {
		return nil, fmt.Errorf("provider_job_id required")
	}
	endpoint := p.baseURL + "/contents/generations/tasks/" + url.PathEscape(job.ProviderJobID) + "/cancel"
	raw, err := p.doJSON(ctx, http.MethodPost, apiKey, endpoint, map[string]any{})
	if err != nil {
		return nil, err
	}
	out, err := parseStatusResponse(raw, job.Model)
	if err != nil {
		return nil, err
	}
	if out.ProviderJobID == "" {
		out.ProviderJobID = job.ProviderJobID
	}
	return out, nil
}

// buildCreateBody 组装 Ark v3 contents/generations 请求体。该 API 读取顶层
// content 数组（text / image_url / video_url 项），生成参数没有独立的
// parameters 对象，而是以 "--key value" 文本命令追加在 prompt 后。
// NegativePrompt / GenerateAudio 在 Ark 没有对应参数，不透传。
func buildCreateBody(model string, req *provider.VideoCreateRequest) map[string]any {
	content := []map[string]any{{
		"type": "text",
		"text": textCommand(req),
	}}
	if img := mediaRef(req.InputReferenceImage); img != "" {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}})
	}
	for _, frame := range req.FrameImages {
		if u := frameRef(frame); u != "" {
			item := map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}}
			// 网关 role 取值 first/last；Ark 取值 first_frame/last_frame
			switch strings.ToLower(strings.TrimSpace(frame.Role)) {
			case "first", "first_frame":
				item["role"] = "first_frame"
			case "last", "last_frame":
				item["role"] = "last_frame"
			}
			content = append(content, item)
		}
	}
	for _, ref := range req.InputReferences {
		if ref.ImageURL == "" && ref.B64JSON == "" {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(ref.Type))
		if kind == "" {
			kind = "image_url"
		}
		urlValue := ref.ImageURL
		if urlValue == "" {
			urlValue = dataURI(ref.B64JSON)
		}
		switch kind {
		case "video_url":
			content = append(content, map[string]any{"type": "video_url", "video_url": map[string]any{"url": urlValue}})
		default:
			content = append(content, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": urlValue},
				"role":      "reference_image",
			})
		}
	}

	body := map[string]any{
		"model":   model,
		"content": content,
	}
	if req.WebhookURL != "" {
		body["callback_url"] = req.WebhookURL
	}
	if req.Metadata != nil {
		body["metadata"] = req.Metadata
	}
	if extras := providerOptions(req.ProviderOptions, "volcengine"); len(extras) > 0 {
		maps.Copy(body, extras)
	}
	return body
}

// arkManagedCommandRe 匹配用户 prompt 里可能注入的、由网关统一管理的 Ark 生成命令
// （--resolution / --ratio / --duration / --seed 及其值）。Ark v3 会把 content 文本里的
// "--key value" 当生成命令解析；若放任用户在 prompt 里注入这些 flag，就能让上游按更高
// 分辨率 / 更长时长出片，而网关只按结构化字段(默认档)估价与结算——系统性少计费。
// 故拼接网关自己的命令前，先把这些受管 flag 从 prompt 中剥除（fps/watermark 等不影响
// per_second 档位计费的命令保留给用户）。
var arkManagedCommandRe = regexp.MustCompile(`(?i)\s*--(?:resolution|ratio|duration|seed)\b(?:\s+\S+)?`)

// sanitizePrompt 剥除 prompt 中受网关管理的 Ark 命令 flag，防 prompt 注入绕过计费。
func sanitizePrompt(prompt string) string {
	return strings.TrimSpace(arkManagedCommandRe.ReplaceAllString(prompt, ""))
}

// textCommand 把生成参数编码为 Ark 的文本命令（--resolution / --ratio /
// --duration / --seed），追加在(已清洗的) prompt 之后。
func textCommand(req *provider.VideoCreateRequest) string {
	var b strings.Builder
	b.WriteString(sanitizePrompt(req.Prompt))
	if req.Resolution != "" {
		fmt.Fprintf(&b, " --resolution %s", req.Resolution)
	}
	if req.AspectRatio != "" {
		fmt.Fprintf(&b, " --ratio %s", req.AspectRatio)
	}
	if req.DurationSeconds != nil {
		fmt.Fprintf(&b, " --duration %g", *req.DurationSeconds)
	}
	if req.Seed != nil {
		fmt.Fprintf(&b, " --seed %d", *req.Seed)
	}
	return b.String()
}

func (p *Provider) doJSON(ctx context.Context, method, apiKey, endpoint string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	provider.SetBearer(req.Header, apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, provider.NewProviderErrorFromResponse(resp, p.Name(), respBody)
	}
	return respBody, nil
}

func parseCreateResponse(raw []byte, model string) (*provider.VideoJob, error) {
	var aux map[string]any
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	data := object(aux["data"])
	id := firstString(aux, "id", "task_id")
	if id == "" {
		id = firstString(data, "id", "task_id")
	}
	if id == "" {
		return nil, fmt.Errorf("volcengine video create: empty task id")
	}
	status := firstString(aux, "status")
	if status == "" {
		status = firstString(data, "status")
	}
	return &provider.VideoJob{
		ProviderJobID: id,
		Model:         model,
		ProviderName:  "volcengine",
		Status:        normalizeStatus(status),
		Progress:      parseProgress(firstAny(aux, data, "progress")),
		CreatedAt:     int64(firstFloat(aux, data, "created_at", "created")),
	}, nil
}

func parseStatusResponse(raw []byte, model string) (*provider.VideoJob, error) {
	var aux map[string]any
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	data := object(aux["data"])
	if len(data) == 0 {
		data = aux
	}
	id := firstString(data, "id", "task_id")
	status := firstString(data, "status")
	job := &provider.VideoJob{
		ProviderJobID: id,
		Model:         model,
		ProviderName:  "volcengine",
		Status:        normalizeStatus(status),
		Progress:      parseProgress(data["progress"]),
		CreatedAt:     int64(firstFloat(data, nil, "created_at", "created")),
		CompletedAt:   int64(firstFloat(data, nil, "completed_at", "updated_at", "finish_time")),
	}
	if job.Status == provider.VideoStatusFailed {
		// Ark 失败任务的 error 是嵌套对象 {"error":{"code","message"}}；
		// 顶层字符串字段留作兜底。
		if eo := object(data["error"]); len(eo) > 0 {
			job.Error = &provider.ErrorObject{
				Message:  firstString(eo, "message"),
				Code:     firstString(eo, "code"),
				Provider: "volcengine",
			}
		} else {
			job.Error = &provider.ErrorObject{
				Message:  firstString(data, "error_message", "fail_reason", "message"),
				Code:     firstString(data, "error_code", "code"),
				Provider: "volcengine",
			}
		}
	}
	for _, u := range collectURLs(data) {
		job.Assets = append(job.Assets, provider.MediaAsset{
			Type:     "video",
			MimeType: "video/mp4",
			URL:      u,
		})
	}
	duration := firstFloat(data, object(data["usage"]), "duration", "duration_seconds")
	if duration <= 0 {
		duration = firstFloat(object(data["video"]), nil, "duration", "duration_seconds")
	}
	usage := &provider.Usage{RequestCount: 1, MediaCount: len(job.Assets)}
	if duration > 0 {
		usage.DurationMs = int(duration * 1000)
		for i := range job.Assets {
			job.Assets[i].DurationMs = usage.DurationMs
		}
	}
	if tokens := int(firstFloat(object(data["usage"]), nil, "total_tokens")); tokens > 0 {
		usage.TotalTokens = tokens
	}
	if job.Status == provider.VideoStatusCompleted {
		job.Usage = usage
	}
	return job, nil
}

func normalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "queued", "created", "pending":
		return provider.VideoStatusQueued
	case "running", "processing", "in_progress", "generating":
		return provider.VideoStatusInProgress
	case "succeeded", "success", "completed", "done":
		return provider.VideoStatusCompleted
	case "failed", "error":
		return provider.VideoStatusFailed
	case "cancelled", "canceled":
		return provider.VideoStatusCancelled
	case "expired":
		return provider.VideoStatusExpired
	default:
		return provider.VideoStatusInProgress
	}
}

// nonVideoURLKeyParts：key 命中任意片段时整个子树跳过——这些都是输入回显
// （img_url / frame 图）、封面/末帧截图、回调地址等非视频产物，收进来会变成
// 假 video 资产（MediaCount 虚增 + 本地存储把图片当 .mp4 落盘）。
var nonVideoURLKeyParts = []string{
	"img", "image", "cover", "thumb", "frame", "audio",
	"callback", "watermark", "logo", "avatar", "icon", "input", "origin",
}

func skipURLKey(lk string) bool {
	for _, part := range nonVideoURLKeyParts {
		if strings.Contains(lk, part) {
			return true
		}
	}
	return false
}

func collectURLs(m map[string]any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				lk := strings.ToLower(k)
				if skipURLKey(lk) {
					continue
				}
				if s, ok := val.(string); ok && strings.HasPrefix(s, "http") &&
					(strings.Contains(lk, "url") || strings.Contains(lk, "uri")) {
					out = append(out, s)
					continue
				}
				walk(val)
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(m)
	return dedupe(out)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func providerOptions(opts map[string]any, key string) map[string]any {
	if opts == nil {
		return nil
	}
	m, _ := opts[key].(map[string]any)
	return m
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func firstAny(a, b map[string]any, key string) any {
	if a != nil && a[key] != nil {
		return a[key]
	}
	if b != nil {
		return b[key]
	}
	return nil
}

func firstFloat(a, b map[string]any, keys ...string) float64 {
	for _, m := range []map[string]any{a, b} {
		for _, k := range keys {
			switch v := m[k].(type) {
			case float64:
				return v
			case int:
				return float64(v)
			}
		}
	}
	return 0
}

func parseProgress(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		x = strings.TrimSuffix(strings.TrimSpace(x), "%")
		var n int
		_, _ = fmt.Sscanf(x, "%d", &n)
		return n
	default:
		return 0
	}
}

func mediaRef(ref *provider.ImageReference) string {
	if ref == nil {
		return ""
	}
	if ref.ImageURL != "" {
		return ref.ImageURL
	}
	if ref.B64JSON != "" {
		return dataURI(ref.B64JSON)
	}
	return ""
}

func frameRef(ref provider.FrameImage) string {
	if ref.ImageURL != "" {
		return ref.ImageURL
	}
	if ref.B64JSON != "" {
		return dataURI(ref.B64JSON)
	}
	return ""
}

func dataURI(b64 string) string {
	if strings.HasPrefix(b64, "data:") {
		return b64
	}
	return "data:image/png;base64," + b64
}
