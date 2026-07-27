package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"time"

	"github.com/siguago/llmkit/provider"
)

// Gemini Image generation (设计 §10.5)
//   POST {baseURL}/{model}:generateContent
//   request.generationConfig.responseModalities = ["TEXT","IMAGE"]
//   响应 candidates[].content.parts[].inlineData = {mimeType, data: base64}
//
// 通过 generateContent + responseModalities 触发图像输出；不是独立的 images endpoint。
// EditImage 仍接受 multipart——adapter 把 image part 转成 inlineData 喂进 contents。

// GenerateImage 实现 §10.5 图像生成。
func (p *Provider) GenerateImage(ctx context.Context, apiKey, model string, req *provider.ImageGenerationRequest) (*provider.ImageGenerationResponse, error) {
	body := buildGeminiImageRequest(req, nil)
	raw, err := p.callGeminiJSON(ctx, apiKey, model+":generateContent", body)
	if err != nil {
		return nil, err
	}
	return parseGeminiImageResponse(raw, model)
}

// EditImage 把上传的 image 作为 inlineData part 注入 contents[0]。
// Gemini 没有单独的 image edit 端点；编辑通过 prompt + 输入图引导。
func (p *Provider) EditImage(ctx context.Context, apiKey, model string, req *provider.ImageEditRequest) (*provider.ImageGenerationResponse, error) {
	if len(req.Images) == 0 {
		return nil, fmt.Errorf("at least one image required for gemini image edit")
	}
	// 把 multipart 中的 image 转 base64 → inlineData
	var inputImages []map[string]any
	for _, img := range req.Images {
		buf, err := io.ReadAll(img.Reader)
		if err != nil {
			return nil, err
		}
		mime := img.ContentType
		if mime == "" {
			mime = "image/png"
		}
		inputImages = append(inputImages, map[string]any{
			"inlineData": map[string]any{
				"mimeType": mime,
				"data":     base64EncodeImage(buf),
			},
		})
	}
	gen := &provider.ImageGenerationRequest{
		Model:           req.Model,
		Prompt:          req.Prompt,
		N:               req.N,
		Size:            req.Size,
		OutputFormat:    req.OutputFormat,
		User:            req.User,
		ProviderOptions: req.ProviderOptions,
	}
	body := buildGeminiImageRequest(gen, inputImages)
	raw, err := p.callGeminiJSON(ctx, apiKey, model+":generateContent", body)
	if err != nil {
		return nil, err
	}
	return parseGeminiImageResponse(raw, model)
}

func buildGeminiImageRequest(req *provider.ImageGenerationRequest, extraParts []map[string]any) map[string]any {
	parts := []map[string]any{{"text": req.Prompt}}
	parts = append(parts, extraParts...)
	contents := []map[string]any{{"role": "user", "parts": parts}}

	genCfg := map[string]any{
		"responseModalities": []string{"TEXT", "IMAGE"},
	}
	// candidateCount 控制返回数量
	if req.N != nil && *req.N > 0 {
		genCfg["candidateCount"] = *req.N
	}
	// imageConfig 子对象（Gemini 文档：generationConfig.imageConfig.aspectRatio / outputMimeType）
	imgCfg := map[string]any{}
	if req.AspectRatio != "" {
		imgCfg["aspectRatio"] = req.AspectRatio
	}
	if req.OutputFormat != "" {
		imgCfg["outputMimeType"] = "image/" + req.OutputFormat
	}
	if len(imgCfg) > 0 {
		genCfg["imageConfig"] = imgCfg
	}

	body := map[string]any{
		"contents":         contents,
		"generationConfig": genCfg,
	}
	// provider_options.gemini 透传白名单
	if extras := geminiProviderExtras(req.ProviderOptions); len(extras) > 0 {
		maps.Copy(body, extras)
	}
	return body
}

func parseGeminiImageResponse(raw []byte, model string) (*provider.ImageGenerationResponse, error) {
	var aux struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text       string `json:"text"`
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	resp := &provider.ImageGenerationResponse{
		Created:  time.Now().Unix(),
		Model:    model,
		Provider: "gemini",
	}
	for _, cand := range aux.Candidates {
		for _, p := range cand.Content.Parts {
			if p.InlineData != nil && p.InlineData.Data != "" {
				asset := provider.MediaAsset{
					Type:     "image",
					MimeType: p.InlineData.MimeType,
					B64JSON:  p.InlineData.Data,
				}
				if asset.MimeType == "" {
					asset.MimeType = "image/png"
				}
				resp.Data = append(resp.Data, asset)
			}
		}
	}
	usage := &provider.Usage{
		RequestCount: 1,
		ImageCount:   len(resp.Data),
		MediaCount:   len(resp.Data),
	}
	if aux.UsageMetadata != nil {
		usage.PromptTokens = aux.UsageMetadata.PromptTokenCount
		usage.CompletionTokens = aux.UsageMetadata.CandidatesTokenCount
		usage.TotalTokens = aux.UsageMetadata.TotalTokenCount
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
	}
	resp.Usage = usage
	return resp, nil
}

// callGeminiJSON 是 POST application/json + ?key=APIKEY 的统一入口。
// Gemini 用 query parameter 传 API key，不是 Bearer header。
func (p *Provider) callGeminiJSON(ctx context.Context, apiKey, pathSegment string, body any) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/%s", p.modelsURL, pathSegment)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	provider.SetKeyHeader(httpReq.Header, "x-goog-api-key", apiKey)

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
	return respBody, nil
}

// geminiProviderExtras 抽出 provider_options.gemini 子对象；只对 Gemini 透传。
func geminiProviderExtras(opts map[string]any) map[string]any {
	if opts == nil {
		return nil
	}
	v, ok := opts["gemini"]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// base64StdEncode 把字节流编码为标准 base64 字符串。
// 抽成独立函数是为了让测试 mock 时可以拦截（虽然当前没有测试用到）。
func base64StdEncode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func base64EncodeImage(b []byte) string {
	return base64StdEncode(b)
}
