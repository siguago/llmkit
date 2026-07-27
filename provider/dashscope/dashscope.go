package dashscope

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const defaultBaseURL = "https://dashscope.aliyuncs.com"

// compatSuffix is the path DashScope serves its OpenAI-compatible endpoints on
// (/chat/completions, /embeddings, /models). The native video synthesis API
// lives at a different prefix (/api/v1/services/...), which is why baseURL here
// is the host root rather than a ready-to-use OpenAI base.
const compatSuffix = "/compatible-mode/v1"

type Provider struct {
	// Chat, streaming, model listing and embeddings are plain OpenAI-compatible
	// on DashScope, so they are delegated wholesale. Qwen's thinking switch
	// rides along as the `enable_thinking` field compat already forwards.
	*compat.Provider
	baseURL string
	client  *http.Client
}

func New(baseURL string) *Provider {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		Provider: compat.New(compat.Config{
			ProviderName: "dashscope",
			BaseURL:      compatBaseURL(baseURL),
		}),
		baseURL: baseURL,
		client: &http.Client{
			Timeout:   300 * time.Second,
			Transport: httpx.NewOutbound(),
		},
	}
}

// compatBaseURL derives the OpenAI-compatible base from the host root. A
// baseURL that already points at the compatible endpoint is passed through, so
// configuring either form works instead of silently producing a doubled path.
func compatBaseURL(baseURL string) string {
	if strings.Contains(baseURL, "/compatible-mode") {
		return baseURL
	}
	return baseURL + compatSuffix
}

func (p *Provider) Name() string { return "dashscope" }

func (p *Provider) CreateVideoJob(ctx context.Context, apiKey, model string, req *provider.VideoCreateRequest) (*provider.VideoJob, error) {
	body := buildCreateBody(model, req)
	raw, err := p.doJSON(ctx, http.MethodPost, apiKey, p.baseURL+"/api/v1/services/aigc/video-generation/video-synthesis", body)
	if err != nil {
		return nil, err
	}
	return parseCreateResponse(raw, model)
}

func (p *Provider) GetVideoJob(ctx context.Context, apiKey string, job *provider.VideoJob) (*provider.VideoJob, error) {
	if job == nil || job.ProviderJobID == "" {
		return nil, fmt.Errorf("provider_job_id required")
	}
	endpoint := p.baseURL + "/api/v1/tasks/" + url.PathEscape(job.ProviderJobID)
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

// DashScope 的视频合成没有取消端点，所以本 adapter 只实现 VideoCreator，
// 不实现 VideoCanceller。对比 volcengine：它有真正的 cancel 端点，因此实现了
// 完整的 VideoProvider。
var _ provider.VideoCreator = (*Provider)(nil)

// buildCreateBody 组装 DashScope 视频合成请求体（wan 系列契约）：
// 图生视频参考图是 input.img_url，首尾帧是 input.first_frame_url /
// last_frame_url；parameters.size 是像素尺寸（"1280*720"），parameters.resolution
// 是档位（"720P"）。AspectRatio 在 DashScope 无对应参数（比例由 size 表达），
// 不透传——绝不能塞进 size（"16:9" 不是合法像素尺寸，会被上游拒绝）。
func buildCreateBody(model string, req *provider.VideoCreateRequest) map[string]any {
	input := map[string]any{"prompt": req.Prompt}
	if img := mediaRef(req.InputReferenceImage); img != "" {
		input["img_url"] = img
	}
	for _, f := range req.FrameImages {
		u := frameRef(f)
		if u == "" {
			continue
		}
		// 网关 role 取值 first/last
		switch strings.ToLower(strings.TrimSpace(f.Role)) {
		case "last", "last_frame":
			input["last_frame_url"] = u
		default:
			input["first_frame_url"] = u
		}
	}
	if req.NegativePrompt != "" {
		input["negative_prompt"] = req.NegativePrompt
	}
	parameters := map[string]any{}
	if req.Resolution != "" {
		// DashScope wan 的 resolution 是大写档位（"480P"/"720P"/"1080P"）；网关内部
		// 统一小写("720p")并据此做 capabilities 白名单校验，发上游前归一为大写，
		// 否则真实模型会以非法档位拒绝。
		parameters["resolution"] = strings.ToUpper(req.Resolution)
	}
	if req.Size != "" {
		// 网关统一 "1280x720"，DashScope 用 "*" 分隔
		parameters["size"] = strings.ReplaceAll(req.Size, "x", "*")
	}
	if req.DurationSeconds != nil {
		// DashScope wan 的 duration 是整数秒；非整数(如 5.5)会被上游拒绝，故取整。
		parameters["duration"] = int(math.Round(*req.DurationSeconds))
	}
	if req.Seed != nil {
		parameters["seed"] = *req.Seed
	}
	if req.GenerateAudio != nil {
		// wan2.5+ 的音频开关参数名是 audio
		parameters["audio"] = *req.GenerateAudio
	}
	body := map[string]any{
		"model":      model,
		"input":      input,
		"parameters": parameters,
	}
	if req.WebhookURL != "" {
		body["callback_url"] = req.WebhookURL
	}
	if req.Metadata != nil {
		body["metadata"] = req.Metadata
	}
	if extras := providerOptions(req.ProviderOptions, "dashscope"); len(extras) > 0 {
		maps.Copy(body, extras)
	}
	return body
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
	req.Header.Set("X-DashScope-Async", "enable")
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
	output := object(aux["output"])
	id := firstString(output, "task_id", "id")
	if id == "" {
		id = firstString(aux, "task_id", "id")
	}
	if id == "" {
		return nil, fmt.Errorf("dashscope video create: empty task id")
	}
	status := firstString(output, "task_status", "status")
	return &provider.VideoJob{
		ProviderJobID: id,
		Model:         model,
		ProviderName:  "dashscope",
		Status:        normalizeStatus(status),
	}, nil
}

func parseStatusResponse(raw []byte, model string) (*provider.VideoJob, error) {
	var aux map[string]any
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	output := object(aux["output"])
	if len(output) == 0 {
		output = aux
	}
	id := firstString(output, "task_id", "id")
	status := firstString(output, "task_status", "status")
	job := &provider.VideoJob{
		ProviderJobID: id,
		Model:         model,
		ProviderName:  "dashscope",
		Status:        normalizeStatus(status),
		Progress:      parseProgress(output["progress"]),
	}
	if job.Status == provider.VideoStatusFailed {
		job.Error = &provider.ErrorObject{
			Message:  firstString(output, "message", "error_message", "code"),
			Code:     firstString(output, "code", "error_code"),
			Provider: "dashscope",
		}
	}
	for _, u := range collectURLs(output) {
		job.Assets = append(job.Assets, provider.MediaAsset{Type: "video", MimeType: "video/mp4", URL: u})
	}
	// DashScope 视频合成的实际时长字段是 usage.video_duration；duration /
	// duration_seconds 留作兜底
	duration := firstFloat(output, object(output["usage"]), "video_duration", "duration", "duration_seconds")
	usage := &provider.Usage{RequestCount: 1, MediaCount: len(job.Assets)}
	if duration > 0 {
		usage.DurationMs = int(duration * 1000)
		for i := range job.Assets {
			job.Assets[i].DurationMs = usage.DurationMs
		}
	}
	if job.Status == provider.VideoStatusCompleted {
		job.Usage = usage
	}
	return job, nil
}

func normalizeStatus(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "", "PENDING", "QUEUED":
		return provider.VideoStatusQueued
	case "RUNNING", "PROCESSING", "IN_PROGRESS":
		return provider.VideoStatusInProgress
	case "SUCCEEDED", "SUCCESS", "COMPLETED", "DONE":
		return provider.VideoStatusCompleted
	case "FAILED", "ERROR":
		return provider.VideoStatusFailed
	case "CANCELED", "CANCELLED":
		return provider.VideoStatusCancelled
	case "UNKNOWN", "EXPIRED":
		// DashScope 任务结果只保留约 24h，过期/不存在的任务查询返回 UNKNOWN。
		// 必须落终态：映射成 in_progress 会让 reconcile 永远轮询、预占永不释放。
		return provider.VideoStatusExpired
	default:
		return provider.VideoStatusInProgress
	}
}

// nonVideoURLKeyParts：key 命中任意片段时整个子树跳过——这些都是输入回显
// （img_url / 首尾帧）、封面截图、回调地址等非视频产物，收进来会变成假 video
// 资产（MediaCount 虚增 + 本地存储把图片当 .mp4 落盘）。
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
