package moonshot

import (
	"context"
	"strings"

	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const defaultBaseURL = "https://api.moonshot.ai/v1"

// Provider wraps the OpenAI-compat layer with Moonshot-specific transforms.
//
// Kimi K2.6+ (released April 2026) replaced the legacy `enable_thinking`
// toggle with `chat_template_kwargs.thinking`, and renamed the response
// reasoning field from `reasoning_content` to `reasoning`. The compat layer
// already lifts `reasoning` → `reasoning_content` on the response side
// (benefits all OpenAI-compatible providers), so this wrapper only handles
// the request-side rewrite.
//
// It builds on compat.NoEmbeddings: the Kimi platform publishes chat completions,
// models, tokenizers, balance and files, and no embeddings route at all, so
// SupportsEmbeddings must not report true for a path that answers 404.
type Provider struct {
	*compat.NoEmbeddings
}

// New constructs a Moonshot/Kimi provider. Pass an empty baseURL to use the
// international default; mainland China users typically pass
// https://api.moonshot.cn/v1.
func New(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		NoEmbeddings: compat.NewNoEmbeddings(compat.Config{
			ProviderName:     "moonshot",
			BaseURL:          baseURL,
			PrefillFieldName: "partial", // Kimi/Moonshot's assistant prefill key
		}),
	}
}

// isK2ThinkingModel reports whether the given model ID belongs to Kimi K2.6+
// (or the dedicated kimi-k2-thinking SKU). These models reject the unified
// `thinking` field at the top level and require the new chat_template_kwargs
// shape instead.
//
// Kimi K2 → K2.5 use the legacy enable_thinking; we leave them alone (compat
// layer will pass `enable_thinking` through if a client sets it explicitly).
func isK2ThinkingModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:] // strip vendor prefix in case of openrouter routing through
	}
	if strings.Contains(m, "thinking") && strings.HasPrefix(m, "kimi-k") {
		return true
	}
	if strings.HasPrefix(m, "kimi-k2.6") || strings.HasPrefix(m, "kimi-k2-6") {
		return true
	}
	return false
}

// transform translates the unified `thinking` field into Kimi K2.6's
// chat_template_kwargs.thinking shape when the model needs it. Returns the
// original pointer when no transform is needed (common case).
func transform(model string, req *provider.ChatCompletionRequest) *provider.ChatCompletionRequest {
	if !isK2ThinkingModel(model) {
		return req
	}
	if req.Thinking == nil || req.Thinking.Type == "" {
		return req
	}
	// Client already set chat_template_kwargs explicitly — respect it,
	// just strip the unified thinking field so we don't double-send.
	if req.ChatTemplateKwargs != nil {
		out := *req
		out.Thinking = nil
		return &out
	}
	out := *req
	var kwargs map[string]any
	switch req.Thinking.Type {
	case "enabled":
		kwargs = map[string]any{"thinking": true}
	case "disabled":
		kwargs = map[string]any{"thinking": false}
	default:
		return req
	}
	// K2.6's thinking_keep controls whether prior-turn reasoning blocks remain
	// in the prompt context across multi-turn conversations. Forward it
	// alongside the toggle when the client set it.
	if req.Thinking.Keep != nil {
		kwargs["thinking_keep"] = *req.Thinking.Keep
	}
	out.ChatTemplateKwargs = kwargs
	out.Thinking = nil
	return &out
}

func (p *Provider) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	return p.NoEmbeddings.ChatCompletion(ctx, apiKey, model, transform(model, req))
}

func (p *Provider) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	return p.NoEmbeddings.ChatCompletionStream(ctx, apiKey, model, transform(model, req))
}
