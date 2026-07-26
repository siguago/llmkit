package easyrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const defaultBaseURL = "https://easyrouter.io/v1"

// Provider wraps the generic OpenAI-compatible chat surface and adds
// EasyRouter's OpenAI-style images plus video task APIs.
type Provider struct {
	*compat.Provider
	baseURL string
	client  *http.Client
}

// New 构造 EasyRouter provider。传空 baseURL 使用默认端点；可由 config.yaml 或
// EASYROUTER_BASE_URL 覆盖。EasyRouter 应用文档要求 endpoint 以 /v1 结尾，这里
// 对只填 host 的配置做兼容补齐，避免误打到 /chat/completions。
func New(baseURL string) *Provider {
	baseURL = normalizeBaseURL(baseURL)
	outboundTransport := httpx.NewOutbound()
	return &Provider{
		Provider: compat.New(compat.Config{
			ProviderName:   "easyrouter",
			BaseURL:        baseURL,
			PassModalities: true,
			PassAudio:      true,
		}),
		baseURL: baseURL,
		client: &http.Client{
			Timeout:   300 * time.Second,
			Transport: outboundTransport,
		},
	}
}

func normalizeBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return defaultBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if strings.HasSuffix(raw, "/v1") {
			return raw
		}
		return raw + "/v1"
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	} else if !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (p *Provider) GenerateImage(ctx context.Context, apiKey, model string, req *provider.ImageGenerationRequest) (*provider.ImageGenerationResponse, error) {
	body := map[string]any{
		"model":  model,
		"prompt": req.Prompt,
	}
	if req.N != nil {
		body["n"] = *req.N
	}
	if req.Size != "" {
		body["size"] = req.Size
	}
	if req.Background != "" {
		body["background"] = req.Background
	}
	if req.Moderation != "" {
		body["moderation"] = req.Moderation
	}
	if req.Quality != "" {
		body["quality"] = req.Quality
	}
	if req.Style != "" {
		body["style"] = req.Style
	}
	if req.OutputFormat != "" {
		body["output_format"] = req.OutputFormat
	}
	if req.OutputCompression != nil {
		body["output_compression"] = *req.OutputCompression
	}
	if req.User != "" {
		body["user"] = req.User
	}
	if req.ResponseFormat != "" {
		if req.ResponseFormat == "url" || req.ResponseFormat == "b64_json" {
			body["response_format"] = req.ResponseFormat
		}
	}
	if req.Stream != nil {
		body["stream"] = *req.Stream
	}
	if extras := easyrouterExtras(req.ProviderOptions); len(extras) > 0 {
		maps.Copy(body, extras)
	}
	if isTrue(body["stream"]) {
		return nil, provider.NewProviderError(http.StatusBadRequest, "easyrouter", []byte(`{"error":{"message":"image streaming is not supported by this gateway endpoint; set stream=false","type":"invalid_request_error","code":"unsupported_image_stream"}}`))
	}

	raw, err := p.postJSON(ctx, apiKey, p.baseURL+"/images/generations", body)
	if err != nil {
		return nil, err
	}
	resp, err := parseImagesResponse(model, raw)
	if err != nil {
		return nil, err
	}
	resp.Provider = "easyrouter"
	return resp, nil
}

func (p *Provider) EditImage(ctx context.Context, apiKey, model string, req *provider.ImageEditRequest) (*provider.ImageGenerationResponse, error) {
	if len(req.Images) == 0 {
		return nil, errors.New("at least one image is required for image edit")
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	errCh := make(chan error, 1)

	go func() {
		defer pw.Close()
		defer mw.Close()
		writeField := func(name, value string) error {
			if value == "" {
				return nil
			}
			return mw.WriteField(name, value)
		}
		if err := writeField("model", model); err != nil {
			errCh <- err
			return
		}
		if err := writeField("prompt", req.Prompt); err != nil {
			errCh <- err
			return
		}
		if req.N != nil {
			if err := writeField("n", strconv.Itoa(*req.N)); err != nil {
				errCh <- err
				return
			}
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"size", req.Size},
			{"quality", req.Quality},
			{"background", req.Background},
			{"output_format", req.OutputFormat},
			{"user", req.User},
		} {
			if err := writeField(field.name, field.value); err != nil {
				errCh <- err
				return
			}
		}
		if req.OutputCompression != nil {
			if err := writeField("output_compression", strconv.Itoa(*req.OutputCompression)); err != nil {
				errCh <- err
				return
			}
		}
		for i, img := range req.Images {
			fieldName := "image"
			if len(req.Images) > 1 {
				fieldName = "image[]"
			}
			fw, err := provider.CreateFormFileWithContentType(mw, fieldName, fileName(img.Filename, "image", i), img.ContentType)
			if err != nil {
				errCh <- err
				return
			}
			if _, err := io.Copy(fw, img.Reader); err != nil {
				errCh <- err
				return
			}
		}
		if req.Mask != nil {
			fw, err := provider.CreateFormFileWithContentType(mw, "mask", fileName(req.Mask.Filename, "mask", 0), req.Mask.ContentType)
			if err != nil {
				errCh <- err
				return
			}
			if _, err := io.Copy(fw, req.Mask.Reader); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/images/edits", pr)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if uerr := <-errCh; uerr != nil {
		return nil, uerr
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewProviderErrorFromResponse(resp, "easyrouter", body)
	}
	out, err := parseImagesResponse(model, body)
	if err != nil {
		return nil, err
	}
	out.Provider = "easyrouter"
	return out, nil
}

func (p *Provider) CreateVideoJob(ctx context.Context, apiKey, model string, req *provider.VideoCreateRequest) (*provider.VideoJob, error) {
	var body map[string]any
	if isEasyRouterVeoModel(model) {
		body = buildVeoVideoBody(model, req)
	} else {
		body = buildSeedanceVideoBody(model, req)
	}

	raw, err := p.postJSON(ctx, apiKey, p.baseURL+"/video/generations", body)
	if err != nil {
		return nil, err
	}
	return p.parseVideoCreate(raw, model)
}

func buildVeoVideoBody(model string, req *provider.VideoCreateRequest) map[string]any {
	body := map[string]any{
		"model":  model,
		"prompt": req.Prompt,
	}
	if req.Size != "" {
		body["size"] = req.Size
	}
	if req.DurationSeconds != nil {
		body["duration"] = *req.DurationSeconds
	}
	if req.GenerateAudio != nil {
		body["generate_audio"] = *req.GenerateAudio
	}
	if img := imageRefValue(req.InputReferenceImage); img != "" {
		body["input_reference"] = img
	}
	if req.NegativePrompt != "" {
		body["negative_prompt"] = req.NegativePrompt
	}
	if req.AspectRatio != "" {
		body["aspect_ratio"] = req.AspectRatio
	}
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}
	if extras := easyrouterExtras(req.ProviderOptions); len(extras) > 0 {
		maps.Copy(body, extras)
	}
	return body
}

func buildSeedanceVideoBody(model string, req *provider.VideoCreateRequest) map[string]any {
	body := map[string]any{
		"model":  model,
		"prompt": req.Prompt,
	}
	if req.DurationSeconds != nil {
		body["duration"] = *req.DurationSeconds
	}
	if img := imageRefValue(req.InputReferenceImage); img != "" {
		body["image"] = img
	}
	if imgs := imageValues(req.FrameImages, req.InputReferences); len(imgs) > 0 {
		body["images"] = imgs
	}

	metadata := map[string]any{}
	if req.Metadata != nil {
		maps.Copy(metadata, req.Metadata)
	}
	if req.Resolution != "" {
		metadata["resolution"] = req.Resolution
	}
	if req.AspectRatio != "" {
		metadata["ratio"] = req.AspectRatio
	}
	if req.DurationSeconds != nil {
		metadata["duration"] = *req.DurationSeconds
	}
	if req.Seed != nil {
		metadata["seed"] = *req.Seed
	}
	if req.GenerateAudio != nil {
		metadata["generate_audio"] = *req.GenerateAudio
	}
	if req.WebhookURL != "" {
		metadata["callback_url"] = req.WebhookURL
	}
	if content := contentValues(req.InputReferences); len(content) > 0 {
		if _, exists := metadata["content"]; !exists {
			metadata["content"] = content
		}
	}
	if extras := easyrouterExtras(req.ProviderOptions); len(extras) > 0 {
		if m, ok := extras["metadata"].(map[string]any); ok {
			maps.Copy(metadata, m)
			delete(extras, "metadata")
		}
		maps.Copy(body, extras)
	}
	if len(metadata) > 0 {
		body["metadata"] = metadata
	}
	return body
}

func (p *Provider) GetVideoJob(ctx context.Context, apiKey string, job *provider.VideoJob) (*provider.VideoJob, error) {
	if job == nil || job.ProviderJobID == "" {
		return nil, fmt.Errorf("provider_job_id required")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/video/generations/"+url.PathEscape(job.ProviderJobID), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewProviderErrorFromResponse(resp, "easyrouter", respBody)
	}
	out, err := p.parseVideoStatus(respBody, job.Model)
	if err != nil {
		return nil, err
	}
	if out.ID == "" {
		out.ID = job.ID
	}
	return out, nil
}

// EasyRouter 没有取消端点，所以本 adapter 只实现 VideoCreator，不实现
// VideoCanceller —— 让 Client.SupportsVideoCancellation 如实返回 false，
// 而不是让调用方在运行时才撞上 ErrUnsupported。
var _ provider.VideoCreator = (*Provider)(nil)

func (p *Provider) postJSON(ctx context.Context, apiKey, endpoint string, body any) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, provider.NewProviderErrorFromResponse(resp, "easyrouter", respBody)
	}
	return respBody, nil
}

func parseImagesResponse(model string, raw []byte) (*provider.ImageGenerationResponse, error) {
	var aux struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
			Size          string `json:"size"`
		} `json:"data"`
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails *struct {
				TextTokens  int `json:"text_tokens"`
				ImageTokens int `json:"image_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	resp := &provider.ImageGenerationResponse{
		Created: aux.Created,
		Model:   model,
	}
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}
	for _, d := range aux.Data {
		asset := provider.MediaAsset{Type: "image"}
		if d.URL != "" {
			asset.URL = d.URL
		}
		if d.B64JSON != "" {
			asset.B64JSON = d.B64JSON
			asset.MimeType = "image/png"
		}
		if d.RevisedPrompt != "" || d.Size != "" {
			asset.Metadata = map[string]any{}
			if d.RevisedPrompt != "" {
				asset.Metadata["revised_prompt"] = d.RevisedPrompt
			}
			if d.Size != "" {
				asset.Metadata["size"] = d.Size
			}
		}
		resp.Data = append(resp.Data, asset)
	}
	usage := &provider.Usage{
		RequestCount: 1,
		ImageCount:   len(resp.Data),
		MediaCount:   len(resp.Data),
	}
	if aux.Usage != nil {
		usage.PromptTokens = aux.Usage.InputTokens
		usage.CompletionTokens = aux.Usage.OutputTokens
		usage.TotalTokens = aux.Usage.TotalTokens
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
	}
	resp.Usage = usage
	return resp, nil
}

func (p *Provider) parseVideoCreate(raw []byte, networkModel string) (*provider.VideoJob, error) {
	var aux struct {
		ID        string `json:"id"`
		TaskID    string `json:"task_id"`
		Model     string `json:"model"`
		Status    string `json:"status"`
		Progress  int    `json:"progress"`
		CreatedAt int64  `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	taskID := aux.TaskID
	if taskID == "" {
		taskID = aux.ID
	}
	return &provider.VideoJob{
		ProviderJobID: taskID,
		Model:         networkModel,
		ProviderName:  "easyrouter",
		Status:        normalizeVideoStatus(aux.Status),
		Progress:      aux.Progress,
		CreatedAt:     aux.CreatedAt,
	}, nil
}

func (p *Provider) parseVideoStatus(raw []byte, networkModel string) (*provider.VideoJob, error) {
	var wrapper struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.Code != "" && !strings.EqualFold(wrapper.Code, "success") && len(wrapper.Data) == 0 {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusBadGateway,
			Message:    fmt.Sprintf("easyrouter api error (status %d): %s", http.StatusBadGateway, string(raw)),
		}
	}
	payload := raw
	if len(wrapper.Data) > 0 {
		payload = wrapper.Data
	}

	var aux struct {
		TaskID      string          `json:"task_id"`
		Status      string          `json:"status"`
		FailReason  string          `json:"fail_reason"`
		ResultURL   string          `json:"result_url"`
		Progress    any             `json:"progress"`
		SubmitTime  int64           `json:"submit_time"`
		FinishTime  int64           `json:"finish_time"`
		CreatedAt   int64           `json:"created_at"`
		UpdatedAt   int64           `json:"updated_at"`
		Properties  map[string]any  `json:"properties"`
		UpstreamRaw json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &aux); err != nil {
		return nil, err
	}
	job := &provider.VideoJob{
		ProviderJobID: aux.TaskID,
		Model:         networkModel,
		ProviderName:  "easyrouter",
		Status:        normalizeVideoStatus(aux.Status),
		Progress:      parseProgress(aux.Progress),
		CreatedAt:     firstNonZero(aux.CreatedAt, aux.SubmitTime),
		CompletedAt:   aux.FinishTime,
		Metadata:      map[string]any{},
	}
	if len(aux.Properties) > 0 {
		job.Metadata["properties"] = aux.Properties
	}

	var upstream struct {
		Content *struct {
			VideoURL string `json:"video_url"`
		} `json:"content"`
		Duration   float64 `json:"duration"`
		Resolution string  `json:"resolution"`
		Usage      *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(aux.UpstreamRaw, &upstream)

	if job.Status == provider.VideoStatusFailed {
		msg := aux.FailReason
		if msg == "" {
			msg = wrapper.Message
		}
		job.Error = &provider.ErrorObject{
			Message:  msg,
			Code:     "easyrouter_video_failed",
			Provider: "easyrouter",
		}
	}

	resultURL := aux.ResultURL
	if resultURL == "" && upstream.Content != nil {
		resultURL = upstream.Content.VideoURL
	}
	if job.Status == provider.VideoStatusCompleted && job.ProviderJobID != "" {
		assetURL := p.baseURL + "/videos/" + url.PathEscape(job.ProviderJobID) + "/content"
		requiresProviderAuth := true
		if isEasyRouterVeoModel(networkModel) && resultURL != "" {
			assetURL = resultURL
			requiresProviderAuth = false
		}
		asset := provider.MediaAsset{
			Type:            "video",
			MimeType:        "video/mp4",
			URL:             assetURL,
			ProviderAssetID: job.ProviderJobID,
			Metadata:        map[string]any{},
		}
		if requiresProviderAuth {
			asset.Metadata["requires_provider_auth"] = true
		}
		if resultURL != "" {
			asset.Metadata["result_url"] = resultURL
		}
		if upstream.Duration > 0 {
			asset.DurationMs = int(upstream.Duration * 1000)
		}
		if upstream.Resolution != "" {
			asset.Metadata["resolution"] = upstream.Resolution
		}
		job.Assets = append(job.Assets, asset)
	}
	if job.Status == provider.VideoStatusCompleted {
		usage := &provider.Usage{RequestCount: 1, MediaCount: len(job.Assets)}
		if upstream.Usage != nil {
			usage.PromptTokens = upstream.Usage.PromptTokens
			usage.CompletionTokens = upstream.Usage.CompletionTokens
			usage.TotalTokens = upstream.Usage.TotalTokens
			if usage.TotalTokens == 0 {
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
		}
		if len(job.Assets) > 0 {
			usage.DurationMs = job.Assets[0].DurationMs
		}
		job.Usage = usage
	}
	if len(job.Metadata) == 0 {
		job.Metadata = nil
	}
	return job, nil
}

func easyrouterExtras(opts map[string]any) map[string]any {
	if opts == nil {
		return nil
	}
	v, ok := opts["easyrouter"]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}

func isEasyRouterVeoModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "veo-")
}

func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func imageRefValue(ref *provider.ImageReference) string {
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

func imageValues(frames []provider.FrameImage, refs []provider.InputReference) []string {
	var out []string
	for _, f := range frames {
		if f.ImageURL != "" {
			out = append(out, f.ImageURL)
		} else if f.B64JSON != "" {
			out = append(out, dataURI(f.B64JSON))
		}
	}
	for _, r := range refs {
		if strings.Contains(strings.ToLower(r.Type), "video") {
			continue
		}
		if r.ImageURL != "" {
			out = append(out, r.ImageURL)
		} else if r.B64JSON != "" {
			out = append(out, dataURI(r.B64JSON))
		}
	}
	return out
}

func contentValues(refs []provider.InputReference) []map[string]any {
	var out []map[string]any
	for _, r := range refs {
		t := strings.ToLower(strings.TrimSpace(r.Type))
		switch {
		case t == "video_url" && r.ImageURL != "":
			out = append(out, map[string]any{
				"type": "video_url",
				"video_url": map[string]any{
					"url": r.ImageURL,
				},
			})
		case t == "image_url" && r.ImageURL != "":
			out = append(out, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": r.ImageURL,
				},
			})
		}
	}
	return out
}

// dataURI 把 base64 字符串包装成 data URI。
// 已经是 data: 前缀直接返回；否则按 base64 前几字节嗅探 PNG/JPEG/WebP/GIF，
// 嗅探失败回退 image/png（OpenAI 默认）。
func dataURI(b64 string) string {
	if strings.HasPrefix(b64, "data:") {
		return b64
	}
	return "data:" + sniffImageMime(b64) + ";base64," + b64
}

// sniffImageMime 按 base64 头部识别常见图片 MIME。base64 编码 3 字节为 4 字符，
// 所以前 4 字符基本能区分 PNG / JPEG / WebP / GIF。
func sniffImageMime(b64 string) string {
	switch {
	case strings.HasPrefix(b64, "iVBORw0KGgo"):
		return "image/png"
	case strings.HasPrefix(b64, "/9j/"):
		return "image/jpeg"
	case strings.HasPrefix(b64, "UklGR"):
		return "image/webp"
	case strings.HasPrefix(b64, "R0lGOD"):
		return "image/gif"
	default:
		return "image/png"
	}
}

func fileName(name, fallback string, idx int) string {
	if name != "" {
		return name
	}
	if idx > 0 {
		return fmt.Sprintf("%s-%d.png", fallback, idx)
	}
	return fallback + ".png"
}

func normalizeVideoStatus(s string) string {
	switch strings.ToUpper(s) {
	case "SUBMITTED", "QUEUED", "PENDING", "QUEUED_UP":
		return provider.VideoStatusQueued
	case "IN_PROGRESS", "PROCESSING", "RUNNING":
		return provider.VideoStatusInProgress
	case "SUCCESS", "SUCCEEDED", "COMPLETED":
		return provider.VideoStatusCompleted
	case "FAILURE", "FAILED", "ERROR":
		return provider.VideoStatusFailed
	case "CANCELLED", "CANCELED":
		return provider.VideoStatusCancelled
	case "EXPIRED":
		return provider.VideoStatusExpired
	default:
		// 与 openai/openrouter 一致：未知/空状态默认 queued，让 reconcile 继续 poll。
		return provider.VideoStatusQueued
	}
}

func parseProgress(v any) int {
	switch p := v.(type) {
	case float64:
		return int(p)
	case string:
		p = strings.TrimSpace(strings.TrimSuffix(p, "%"))
		n, _ := strconv.Atoi(p)
		return n
	default:
		return 0
	}
}

func firstNonZero(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}
