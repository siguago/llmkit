package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/siguago/llmkit/provider"
)

// Gemini Veo Video generation (设计 §10.5)
//   POST {baseURL}/{model}:predictLongRunning
//   响应: {name: "operations/xxx"} —— operation name 作为 provider_job_id
//   GET https://generativelanguage.googleapis.com/v1beta/{operation.name}
//   完成: done=true，response.generateVideoResponse.generatedSamples[].video.uri
//
// 注意：video.uri 需要带 API key 才能下载，客户端不能直接拿；网关必须通过
// /v1/media/:asset_id/content 代理。本期返回 URI + 标记 storage_kind=upstream_url
// 让 handler 落库；代理本身设计 §13 二期再做。

// CreateVideoJob 提交 Veo predictLongRunning。
func (p *Provider) CreateVideoJob(ctx context.Context, apiKey, model string, req *provider.VideoCreateRequest) (*provider.VideoJob, error) {
	instance := map[string]any{
		"prompt": req.Prompt,
	}
	if req.InputReferenceImage != nil {
		img := map[string]any{}
		if req.InputReferenceImage.ImageURL != "" {
			img["imageUri"] = req.InputReferenceImage.ImageURL
		}
		if req.InputReferenceImage.B64JSON != "" {
			img["bytesBase64Encoded"] = req.InputReferenceImage.B64JSON
		}
		if len(img) > 0 {
			instance["image"] = img
		}
	}
	parameters := map[string]any{}
	if req.DurationSeconds != nil {
		parameters["durationSeconds"] = *req.DurationSeconds
	}
	if req.AspectRatio != "" {
		parameters["aspectRatio"] = req.AspectRatio
	}
	if req.Seed != nil {
		parameters["seed"] = *req.Seed
	}
	if req.GenerateAudio != nil {
		parameters["generateAudio"] = *req.GenerateAudio
	}
	body := map[string]any{
		"instances": []any{instance},
	}
	if len(parameters) > 0 {
		body["parameters"] = parameters
	}
	// provider_options.gemini 透传白名单
	if extras := geminiProviderExtras(req.ProviderOptions); len(extras) > 0 {
		maps.Copy(body, extras)
	}
	raw, err := p.callGeminiJSON(ctx, apiKey, model+":predictLongRunning", body)
	if err != nil {
		return nil, err
	}
	// 创建响应: {name: "models/<model>/operations/<id>" } 或 {name: "operations/<id>"}
	var aux struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	if aux.Name == "" {
		return nil, fmt.Errorf("gemini predictLongRunning: empty operation name")
	}
	return &provider.VideoJob{
		ProviderJobID: aux.Name,
		Model:         model,
		ProviderName:  "gemini",
		Status:        provider.VideoStatusInProgress, // long-running 创建后立刻可视为 in_progress
		Progress:      0,
	}, nil
}

// GetVideoJob 查询 long-running operation 状态。
// done=false → in_progress
// done=true 且 error → failed
// done=true 且 response → completed，从 response.generateVideoResponse.generatedSamples 提资产
func (p *Provider) GetVideoJob(ctx context.Context, apiKey string, job *provider.VideoJob) (*provider.VideoJob, error) {
	if job == nil || job.ProviderJobID == "" {
		return nil, fmt.Errorf("provider_job_id required")
	}
	// operation name 是相对于 API root 的路径（如 "models/veo-3.x/operations/abc"），
	// 拼到 baseURL（不含 /models 段）上得到绝对 URL。
	url := p.baseURL + "/" + strings.TrimPrefix(job.ProviderJobID, "/")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-goog-api-key", apiKey)
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
		return nil, provider.NewProviderErrorFromResponse(resp, "gemini", respBody)
	}
	return parseVeoOperation(respBody, job)
}

// Veo 没有 operation cancel 端点，所以本 adapter 只实现 VideoCreator，
// 不实现 VideoCanceller —— 让 Client.SupportsVideoCancellation 如实返回 false。
// 任务提交后只能等它跑到终态，或者放弃轮询。
var _ provider.VideoCreator = (*Provider)(nil)

func parseVeoOperation(raw []byte, prev *provider.VideoJob) (*provider.VideoJob, error) {
	var aux struct {
		Name  string `json:"name"`
		Done  bool   `json:"done"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
		Response *struct {
			GenerateVideoResponse *struct {
				GeneratedSamples []struct {
					Video *struct {
						URI      string `json:"uri"`
						MimeType string `json:"mimeType"`
					} `json:"video"`
				} `json:"generatedSamples"`
			} `json:"generateVideoResponse"`
		} `json:"response"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	out := &provider.VideoJob{
		ID:            prev.ID,
		ProviderJobID: prev.ProviderJobID,
		Model:         prev.Model,
		ProviderName:  "gemini",
	}
	if aux.Name != "" {
		out.ProviderJobID = aux.Name
	}
	if !aux.Done {
		out.Status = provider.VideoStatusInProgress
		return out, nil
	}
	if aux.Error != nil {
		out.Status = provider.VideoStatusFailed
		out.Error = &provider.ErrorObject{
			Message:  aux.Error.Message,
			Code:     aux.Error.Status,
			Type:     fmt.Sprintf("veo_error_code_%d", aux.Error.Code),
			Provider: "gemini",
		}
		return out, nil
	}
	// done=true 无 error → completed，收集 generatedSamples
	out.Status = provider.VideoStatusCompleted
	if aux.Response != nil && aux.Response.GenerateVideoResponse != nil {
		for _, s := range aux.Response.GenerateVideoResponse.GeneratedSamples {
			if s.Video == nil || s.Video.URI == "" {
				continue
			}
			mime := s.Video.MimeType
			if mime == "" {
				mime = "video/mp4"
			}
			out.Assets = append(out.Assets, provider.MediaAsset{
				Type:     "video",
				MimeType: mime,
				URL:      s.Video.URI,
				Metadata: map[string]any{
					"requires_provider_auth": true, // 提示 handler：客户端无法直接拉，必须代理
				},
			})
		}
	}
	out.Usage = &provider.Usage{
		RequestCount: 1,
		MediaCount:   len(out.Assets),
	}
	return out, nil
}
