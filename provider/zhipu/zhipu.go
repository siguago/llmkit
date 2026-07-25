package zhipu

import (
	"context"

	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const defaultBaseURL = "https://open.bigmodel.cn/api/paas/v4"

// Provider wraps the OpenAI-compat layer with Zhipu-specific request cleanup.
// Current GLM models accept the unified `thinking: {type:"enabled|disabled"}`
// shape directly. Older clients may still send `enable_thinking`; when both
// are present, keep the current official field and drop the legacy boolean so
// the upstream does not receive conflicting switches.
type Provider struct {
	*compat.Provider
}

// New constructs a Zhipu/GLM provider. Pass an empty baseURL to use the
// default mainland China endpoint.
func New(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		Provider: compat.New(compat.Config{
			ProviderName: "zhipu",
			BaseURL:      baseURL,
		}),
	}
}

// transform avoids sending both `thinking` and `enable_thinking`. Returns the
// original pointer when no cleanup is needed (common path).
func transform(req *provider.ChatCompletionRequest) *provider.ChatCompletionRequest {
	if req.Thinking == nil || req.Thinking.Type == "" || req.EnableThinking == nil {
		return req
	}
	out := *req
	out.EnableThinking = nil
	return &out
}

func (p *Provider) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	return p.Provider.ChatCompletion(ctx, apiKey, model, transform(req))
}

func (p *Provider) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	return p.Provider.ChatCompletionStream(ctx, apiKey, model, transform(req))
}
