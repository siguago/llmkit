package openai

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
	"strings"
	"time"

	"github.com/siguago/llmkit/provider"
)

// OpenAI Images API
//   POST /v1/images/generations
//   POST /v1/images/edits
//
// gpt-image-* 默认返回 b64_json；DALL-E 系列才支持 response_format=url。
// adapter 不无条件透传 response_format。

// supportsResponseFormatURL 判断 model 是否允许 response_format=url。
// gpt-image-* 不允许；其余（含 dall-e-*）允许。判断逻辑保持保守——保持白名单不如黑名单稳定，
// 但 gpt-image 是单一已知不支持 url 的家族，黑名单更准。
func supportsResponseFormatURL(model string) bool {
	return !strings.HasPrefix(strings.ToLower(model), "gpt-image")
}

// GenerateImage 调用 /v1/images/generations。返回的资产已经标准化成 MediaAsset。
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
	if req.Quality != "" {
		body["quality"] = req.Quality
	}
	if req.Background != "" {
		body["background"] = req.Background
	}
	if req.OutputFormat != "" {
		body["output_format"] = req.OutputFormat
	}
	if req.OutputCompression != nil {
		body["output_compression"] = *req.OutputCompression
	}
	if req.Moderation != "" {
		body["moderation"] = req.Moderation
	}
	if req.ResponseFormat != "" && supportsResponseFormatURL(model) {
		// gpt-image-* 不支持 response_format；只对 DALL-E 系列允许透传 url/b64_json。
		// 网关 asset 模式由 handler 决定如何呈现，不发给上游。
		if req.ResponseFormat == "url" || req.ResponseFormat == "b64_json" {
			body["response_format"] = req.ResponseFormat
		}
	}
	if req.User != "" {
		body["user"] = req.User
	}
	// provider_options.openai 是 OpenAI 专用扩展位，做白名单合并。
	if extras := openaiProviderExtras(req.ProviderOptions); len(extras) > 0 {
		maps.Copy(body, extras)
	}

	raw, err := callOpenAIJSON(ctx, p.client, apiKey, p.imagesGenURL, body)
	if err != nil {
		return nil, err
	}
	return parseOpenAIImagesResponse(model, raw)
}

// EditImage 调用 /v1/images/edits（multipart/form-data）。
// 注意：multipart 必须按 OpenAI 文档把 image 字段名重复使用（多图为 image[] 或多个同名字段）。
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
			if err := writeField("n", fmt.Sprintf("%d", *req.N)); err != nil {
				errCh <- err
				return
			}
		}
		if err := writeField("size", req.Size); err != nil {
			errCh <- err
			return
		}
		if err := writeField("quality", req.Quality); err != nil {
			errCh <- err
			return
		}
		if err := writeField("background", req.Background); err != nil {
			errCh <- err
			return
		}
		if err := writeField("output_format", req.OutputFormat); err != nil {
			errCh <- err
			return
		}
		if req.OutputCompression != nil {
			if err := writeField("output_compression", fmt.Sprintf("%d", *req.OutputCompression)); err != nil {
				errCh <- err
				return
			}
		}
		if err := writeField("user", req.User); err != nil {
			errCh <- err
			return
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.imagesEditURL, pr)
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
		return nil, provider.NewProviderErrorFromResponse(resp, "openai", body)
	}
	return parseOpenAIImagesResponse(model, body)
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

// openaiProviderExtras 抽出 provider_options.openai 子对象——只对 OpenAI 透传。
// 没有该子对象时返回 nil；非 map 类型也返回 nil，避免把字符串等错误结构当 body merge。
func openaiProviderExtras(opts map[string]any) map[string]any {
	if opts == nil {
		return nil
	}
	v, ok := opts["openai"]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// callOpenAIJSON 是 POST application/json 的统一入口，封装错误码处理。
func callOpenAIJSON(ctx context.Context, client *http.Client, apiKey, url string, body any) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
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
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewProviderErrorFromResponse(resp, "openai", respBody)
	}
	return respBody, nil
}

// parseOpenAIImagesResponse 解析 /v1/images/generations | /edits 的统一响应。
// data[].url 或 data[].b64_json 至少有一个；usage 字段（如有）使用 input_tokens / output_tokens 命名。
func parseOpenAIImagesResponse(model string, raw []byte) (*provider.ImageGenerationResponse, error) {
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
			InputTokensDetails *struct {
				TextTokens  int `json:"text_tokens"`
				ImageTokens int `json:"image_tokens"`
			} `json:"input_tokens_details"`
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil, err
	}
	resp := &provider.ImageGenerationResponse{
		Created:  aux.Created,
		Model:    model,
		Provider: "openai",
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
			if asset.MimeType == "" {
				asset.MimeType = "image/png"
			}
		}
		if d.RevisedPrompt != "" {
			if asset.Metadata == nil {
				asset.Metadata = map[string]any{}
			}
			asset.Metadata["revised_prompt"] = d.RevisedPrompt
		}
		if d.Size != "" {
			if asset.Metadata == nil {
				asset.Metadata = map[string]any{}
			}
			asset.Metadata["size"] = d.Size
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
