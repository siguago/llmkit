package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const defaultBaseURL = "https://api.openai.com/v1"

type Provider struct {
	*compat.Provider
	baseURL       string
	modelsURL     string
	imagesGenURL  string
	imagesEditURL string
	client        *http.Client
}

// isReasoningModel reports whether the given model ID is an OpenAI reasoning
// model (o-series or GPT-5 family). These models reject the legacy sampling
// knobs (temperature/top_p/penalties/logprobs) with HTTP 400 when present, so
// the gateway strips them before forwarding rather than leaking the upstream
// rejection to clients that wired in default values.
//
// Reference: https://platform.openai.com/docs/guides/reasoning
func isReasoningModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "gpt-5") ||
		strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4")
}

// sanitizeForReasoning returns a copy of req with reasoning-incompatible
// sampling parameters cleared. The original request is not mutated so retries
// or alternate routing can still see the client's intent. Returns the original
// pointer when no sanitization is needed (common case).
//
// What we strip and why:
//   - temperature, top_p: reasoning models always sample at temperature=1
//   - presence_penalty, frequency_penalty: not supported
//   - logprobs, top_logprobs: not exposed for reasoning models
//
// What we deliberately keep:
//   - n: a value > 1 is the client's explicit ask; we let upstream return 400
//     so the user sees a clear error rather than silently receiving 1 result
//   - max_tokens vs max_completion_tokens: compat layer already prefers
//     max_completion_tokens when both are set, which is the documented choice
//   - logit_bias: also unsupported on reasoning models; stripped here so the
//     upstream doesn't reject the otherwise-valid request
func sanitizeForReasoning(model string, req *provider.ChatCompletionRequest) *provider.ChatCompletionRequest {
	if !isReasoningModel(model) {
		return req
	}
	if req.Temperature == nil && req.TopP == nil && req.PresencePenalty == nil &&
		req.FrequencyPenalty == nil && req.LogProbs == nil && req.TopLogProbs == nil &&
		req.LogitBias == nil {
		return req
	}
	out := *req
	out.Temperature = nil
	out.TopP = nil
	out.PresencePenalty = nil
	out.FrequencyPenalty = nil
	out.LogProbs = nil
	out.TopLogProbs = nil
	out.LogitBias = nil
	return &out
}

func (p *Provider) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	return p.Provider.ChatCompletion(ctx, apiKey, model, sanitizeForReasoning(model, req))
}

func (p *Provider) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	return p.Provider.ChatCompletionStream(ctx, apiKey, model, sanitizeForReasoning(model, req))
}

// New constructs an OpenAI provider pointed at the official API.
func New() *Provider { return NewWithBaseURL("") }

// NewWithBaseURL constructs an OpenAI provider against a custom API root
// (Azure-style gateways, relay/proxy deployments). Pass an empty string for the
// official endpoint. The root must be the API base without a trailing slash,
// e.g. "https://api.openai.com/v1".
func NewWithBaseURL(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Provider{
		Provider: compat.New(compat.Config{
			ProviderName: "openai",
			BaseURL:      baseURL,
		}),
		baseURL:       baseURL,
		modelsURL:     baseURL + "/models",
		imagesGenURL:  baseURL + "/images/generations",
		imagesEditURL: baseURL + "/images/edits",
		// 300s 与其它长任务 provider 对齐：共享给 ListModels（短任务，自带 30s
		// context 内层兜底）和 images.go 的图像路径。dall-e-3 / gpt-image-2
		// 单次请求经常 30-90s，30s 上限会让上游正常长任务被网关
		// context.DeadlineExceeded 染成 502，掩盖真实状态。
		client: &http.Client{
			Timeout:   300 * time.Second,
			Transport: httpx.NewOutbound(),
		},
	}
}

// ListModels overrides compat.Provider.ListModels to filter by owned_by.
func (p *Provider) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.modelsURL, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, "openai", respBody)
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []provider.RemoteModel
	for _, m := range result.Data {
		if m.OwnedBy != "openai" && m.OwnedBy != "system" {
			continue
		}
		models = append(models, provider.RemoteModel{
			ModelID:     m.ID,
			DisplayName: m.ID,
		})
	}
	return models, nil
}
