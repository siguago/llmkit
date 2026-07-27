// Package mistral adapts Mistral AI's La Plateforme API, which serves an
// OpenAI-compatible surface at api.mistral.ai/v1.
package mistral

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.mistral.ai/v1"

// New constructs a Mistral provider. Pass an empty baseURL to use the default
// endpoint.
//
// Mistral takes assistant prefill as `prefix: true` on the trailing assistant
// message — the same shape DeepSeek uses — so Message.Prefix is forwarded under
// that name rather than dropped.
func New(baseURL string) *compat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.New(compat.Config{
		ProviderName:     "mistral",
		BaseURL:          baseURL,
		PrefillFieldName: "prefix",
	})
}
