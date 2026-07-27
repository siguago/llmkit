// Package xai adapts xAI's Grok API, which is OpenAI-compatible at the wire
// level: same bearer auth, same /chat/completions shape, same SSE framing.
package xai

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.x.ai/v1"

// New constructs an xAI provider. Pass an empty baseURL to use the default
// endpoint.
//
// Chat-only: xAI's API reference covers chat, models, image generation and
// tokenization, but no generally-available /embeddings route, so
// SupportsEmbeddings reports false rather than claiming one. See compat.ChatOnly
// — if xAI ships embeddings, this becomes compat.New again.
//
// Grok's reasoning models read the standard `reasoning_effort` field, which the
// compat layer already forwards. Live Search is a vendor extension carried on
// the request as `search_parameters`; it is not a first-class field here, so
// route it through ProviderOptions if you need it.
func New(baseURL string) *compat.ChatOnly {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.NewChatOnly(compat.Config{
		ProviderName: "xai",
		BaseURL:      baseURL,
	})
}
