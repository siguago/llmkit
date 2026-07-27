package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/siguago/llmkit/provider"
)

// OpenRouter Videos API (基于设计文档 §10.4)
//   POST {baseURL}/videos
//   GET  {baseURL}/videos/{id}  (或使用 create 响应中的 polling_url)
// 端点由 Provider.videosURL 承载，随 NewWithBaseURL 一起可指向中转站或
// httptest.Server。

// CreateVideoJob 创建一个 OpenRouter 视频任务。
func (p *Provider) CreateVideoJob(ctx context.Context, apiKey, model string, req *provider.VideoCreateRequest) (*provider.VideoJob, error) {
	body := map[string]any{
		"model":  model,
		"prompt": req.Prompt,
	}
	if req.DurationSeconds != nil {
		body["duration"] = *req.DurationSeconds
	}
	if req.AspectRatio != "" {
		body["aspect_ratio"] = req.AspectRatio
	}
	if req.Size != "" {
		body["size"] = req.Size
	}
	if req.Resolution != "" {
		body["resolution"] = req.Resolution
	}
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}
	if req.GenerateAudio != nil {
		body["generate_audio"] = *req.GenerateAudio
	}
	if req.WebhookURL != "" {
		body["callback_url"] = req.WebhookURL
	}
	if len(req.FrameImages) > 0 {
		body["frame_images"] = req.FrameImages
	}
	// OpenRouter 接受 input_references 数组（style/character/object 风格引导）。
	// 网关侧的 input_reference_image 单一图引导，需要包装成 input_references 的一个条目，
	// 不是直接发送 "input_reference_image" 这个非标字段名。
	refs := append([]provider.InputReference(nil), req.InputReferences...)
	if req.InputReferenceImage != nil {
		refs = append(refs, provider.InputReference{
			Type:     "reference",
			FileID:   req.InputReferenceImage.FileID,
			ImageURL: req.InputReferenceImage.ImageURL,
			B64JSON:  req.InputReferenceImage.B64JSON,
		})
	}
	if len(refs) > 0 {
		body["input_references"] = refs
	}
	// provider_options.openrouter.provider 强制路由
	if routing := openrouterProviderRouting(req.ProviderOptions); routing != nil {
		body["provider"] = routing
	}

	respBody, err := postJSON(ctx, p.client, apiKey, p.videosURL, body)
	if err != nil {
		return nil, err
	}
	return parseOpenRouterVideoJob(respBody, model)
}

// GetVideoJob 查询任务。优先使用 polling_url（存放在 job.Metadata）。
func (p *Provider) GetVideoJob(ctx context.Context, apiKey string, job *provider.VideoJob) (*provider.VideoJob, error) {
	if job == nil || job.ProviderJobID == "" {
		return nil, fmt.Errorf("provider_job_id required")
	}
	url := p.videosURL + "/" + job.ProviderJobID
	if job.Metadata != nil {
		if pu, ok := job.Metadata["polling_url"].(string); ok && pu != "" {
			url = pu
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
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
		return nil, provider.NewProviderErrorFromResponse(resp, "openrouter", respBody)
	}
	out, err := parseOpenRouterVideoJob(respBody, job.Model)
	if err != nil {
		return nil, err
	}
	if out.ID == "" {
		out.ID = job.ID
	}
	return out, nil
}

// OpenRouter 文档当前未给出 cancel 端点，所以本 adapter 只实现 VideoCreator，
// 不实现 VideoCanceller。厂商补上端点后，在这里加回 CancelVideoJob 方法即可，
// Client.SupportsVideoCancellation 会自动跟着变 true。
var _ provider.VideoCreator = (*Provider)(nil)

func parseOpenRouterVideoJob(raw []byte, networkModel string) (*provider.VideoJob, error) {
	var aux struct {
		ID           string   `json:"id"`
		GenerationID string   `json:"generation_id"`
		PollingURL   string   `json:"polling_url"`
		Status       string   `json:"status"`
		Progress     int      `json:"progress"`
		CreatedAt    int64    `json:"created_at"`
		CompletedAt  int64    `json:"completed_at"`
		UnsignedURLs []string `json:"unsigned_urls"`
		SignedURLs   []string `json:"signed_urls"`
		Duration     float64  `json:"duration"`
		Usage        *struct {
			Cost       float64 `json:"cost"`
			DurationMs int     `json:"duration_ms"`
			Currency   string  `json:"currency"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	job := &provider.VideoJob{
		ProviderJobID: aux.ID,
		Model:         networkModel,
		ProviderName:  "openrouter",
		Status:        normalizeOpenRouterVideoStatus(aux.Status),
		Progress:      aux.Progress,
		CreatedAt:     aux.CreatedAt,
		CompletedAt:   aux.CompletedAt,
		Metadata:      map[string]any{},
	}
	if aux.PollingURL != "" {
		job.Metadata["polling_url"] = aux.PollingURL
	}
	if aux.GenerationID != "" {
		job.Metadata["generation_id"] = aux.GenerationID
	}
	if aux.Error != nil {
		job.Error = &provider.ErrorObject{
			Message:  aux.Error.Message,
			Code:     aux.Error.Code,
			Type:     aux.Error.Type,
			Provider: "openrouter",
		}
	}
	// 完成态：把 unsigned_urls (或 signed_urls) 规范化成 MediaAsset。
	urls := aux.UnsignedURLs
	if len(urls) == 0 {
		urls = aux.SignedURLs
	}
	for _, u := range urls {
		asset := provider.MediaAsset{
			Type:     "video",
			MimeType: "video/mp4",
			URL:      u,
		}
		if aux.Duration > 0 {
			asset.DurationMs = int(aux.Duration * 1000)
		}
		job.Assets = append(job.Assets, asset)
	}
	if job.Status == provider.VideoStatusCompleted {
		usage := &provider.Usage{RequestCount: 1, MediaCount: len(job.Assets)}
		if aux.Usage != nil {
			usage.Cost = aux.Usage.Cost
			usage.DurationMs = aux.Usage.DurationMs
		}
		if usage.DurationMs == 0 && aux.Duration > 0 {
			usage.DurationMs = int(aux.Duration * 1000)
		}
		job.Usage = usage
	}
	return job, nil
}

func normalizeOpenRouterVideoStatus(s string) string {
	switch s {
	case "pending", "queued":
		return provider.VideoStatusQueued
	case "in_progress", "processing":
		return provider.VideoStatusInProgress
	case "completed", "succeeded":
		return provider.VideoStatusCompleted
	case "failed", "error":
		return provider.VideoStatusFailed
	case "cancelled":
		return provider.VideoStatusCancelled
	case "expired":
		return provider.VideoStatusExpired
	default:
		return provider.VideoStatusQueued
	}
}

func postJSON(ctx context.Context, client *http.Client, apiKey, url string, body any) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, provider.NewProviderErrorFromResponse(resp, "openrouter", respBody)
	}
	return respBody, nil
}
