// Package cerebras adapts Cerebras Inference, an OpenAI-compatible service
// running open-weight models on wafer-scale hardware.
package cerebras

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.cerebras.ai/v1"

// New constructs a Cerebras provider. Pass an empty baseURL to use the default
// endpoint.
//
// Chat-only: Cerebras has no /embeddings endpoint — it answers 404, not 401 — so
// SupportsEmbeddings reports false instead of promising a route that does not
// exist. See compat.ChatOnly.
//
// Known upstream constraints, forwarded rather than pre-empted so the caller
// sees Cerebras's own error: frequency_penalty / presence_penalty / logit_bias
// are rejected on most models.
func New(baseURL string) *compat.ChatOnly {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.NewChatOnly(compat.Config{
		ProviderName: "cerebras",
		BaseURL:      baseURL,
	})
}
