// Image generation for Vercel AI Gateway.
//
// Vercel exposes only the OpenAI-compatible `/v1/images/generations` endpoint
// under its /v1 base. There is **no** `/v1/images/edits` — the official docs
// route image editing through multimodal chat completions (Nano Banana 等),
// not the OpenAI images-edit wire format. So `EditImage` here is a hard
// "unsupported" stub; we don't ship a multipart implementation that would
// 404 on every request.
//
// Vercel docs:
//
//	https://vercel.com/docs/ai-gateway/capabilities/image-generation/openai
//	"Image-only models use the OpenAI-compatible /v1/images/generations
//	 endpoint, not /v1/chat/completions. Call them with openai.images.generate
//	 from the OpenAI SDK."
package vercel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"time"

	"github.com/siguago/llmkit/provider"
)

// GenerateImage POSTs to Vercel's /images/generations with an OpenAI-compatible
// JSON body. Streaming on this endpoint isn't supported by the gateway shape
// (image bytes need to be aggregated for billing) so we reject stream=true
// inline rather than letting the upstream succeed and us drop the chunks.
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
	if req.ResponseFormat == "url" || req.ResponseFormat == "b64_json" {
		body["response_format"] = req.ResponseFormat
	}
	if req.Stream != nil {
		body["stream"] = *req.Stream
	}
	if extras := vercelExtras(req.ProviderOptions); len(extras) > 0 {
		maps.Copy(body, extras)
	}
	if isTrue(body["stream"]) {
		return nil, provider.NewProviderError(http.StatusBadRequest, name,
			[]byte(`{"error":{"message":"image streaming is not supported by this gateway endpoint; set stream=false","type":"invalid_request_error","code":"unsupported_image_stream"}}`))
	}

	raw, err := p.postJSON(ctx, apiKey, p.baseURL+"/images/generations", body)
	if err != nil {
		return nil, err
	}
	resp, err := parseImagesResponse(model, raw)
	if err != nil {
		return nil, err
	}
	resp.Provider = name
	return resp, nil
}

// EditImage is unsupported on Vercel AI Gateway. Vercel staff (Pauline P. Narvas)
// confirmed on the community forum that the gateway "supports image generation
// through multimodal models... but doesn't yet have endpoints for uploading and
// editing existing images" — last verified 2026-05.
//
//	https://community.vercel.com/t/support-for-image-upload-and-edit-endpoints-for-image-gen-of-gpt-models-like-gpt-image-1-5/34607
//
// OpenAI's own /v1/images/edits supports gpt-image-2; the gap is at Vercel's
// gateway layer, which doesn't transparently forward edits requests. If/when
// Vercel ships the endpoint, restore the multipart implementation from git
// history (commit prior to this stub) and flip the seed task_types back. The
// alternative editing paths Vercel does support today live elsewhere:
//   - multimodal chat completions (Nano Banana, Gemini image-preview, ...)
//   - AI SDK's experimental_generateImage with image inputs
//
// Neither matches the OpenAI /v1/images/edits wire format this method expects,
// so there's no useful translation layer to apply here — return ErrUnsupported
// and let the handler surface a clean 4xx.
func (p *Provider) EditImage(ctx context.Context, apiKey, model string, req *provider.ImageEditRequest) (*provider.ImageGenerationResponse, error) {
	return nil, &provider.ErrUnsupported{Provider: name, Op: "image_edit"}
}

// postJSON sends a JSON request and returns the raw response body. Treats
// both 200 and 201 as success — image endpoints occasionally use 201 Created.
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
		return nil, provider.NewProviderErrorFromResponse(resp, name, respBody)
	}
	return respBody, nil
}

// parseImagesResponse maps OpenAI's `images.generate` / `images.edit` JSON
// shape to the gateway's unified ImageGenerationResponse. Token-billed image
// models (gpt-image-1/2 family) report `usage.input_tokens` / `output_tokens`;
// per-image-billed models leave usage empty — billing then falls back to the
// per-image strategy via the binding's billing_unit.
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

// vercelExtras pulls out the `vercel` namespace from request.provider_options
// so callers can pass Vercel-specific knobs (e.g. providerOptions ladders)
// without polluting the unified ImageGenerationRequest fields. Returns nil
// when no relevant block exists.
func vercelExtras(opts map[string]any) map[string]any {
	if opts == nil {
		return nil
	}
	v, ok := opts["vercel"]
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

func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// Compile-time check that *Provider satisfies the ImageProvider contract.
// Without this, a future signature drift would surface only at runtime when
// the images handler does the type assertion.
var _ provider.ImageProvider = (*Provider)(nil)
