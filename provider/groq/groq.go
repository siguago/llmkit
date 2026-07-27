// Package groq adapts Groq's LPU inference API, an OpenAI-compatible service
// hosting open-weight models.
package groq

import "github.com/siguago/llmkit/provider/compat"

// Groq serves its OpenAI-compatible surface under /openai/v1, not /v1.
const defaultBaseURL = "https://api.groq.com/openai/v1"

// New constructs a Groq provider. Pass an empty baseURL to use the default
// endpoint.
//
// No embeddings: Groq's OpenAI-compatible surface covers chat, models and audio,
// but publishes no /embeddings route and serves no embedding models, so
// SupportsEmbeddings reports false rather than claiming a route the caller would
// only discover is missing by spending a call. See compat.NoEmbeddings — if Groq
// ships embeddings, this becomes compat.New again.
//
// Known upstream constraints, forwarded rather than pre-empted so the caller
// sees Groq's own error instead of a locally invented one: n must be 1, and
// logit_bias / logprobs are accepted but ignored on most models.
func New(baseURL string) *compat.NoEmbeddings {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.NewNoEmbeddings(compat.Config{
		ProviderName: "groq",
		BaseURL:      baseURL,
	})
}
