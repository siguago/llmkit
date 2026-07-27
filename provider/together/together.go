// Package together adapts Together AI's inference API, an OpenAI-compatible
// service with a broad open-weight model catalog.
package together

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.together.xyz/v1"

// New constructs a Together AI provider. Pass an empty baseURL to use the
// default endpoint.
func New(baseURL string) *compat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.New(compat.Config{
		ProviderName: "together",
		BaseURL:      baseURL,
	})
}
