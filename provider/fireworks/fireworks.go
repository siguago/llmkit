// Package fireworks adapts Fireworks AI's inference API, an OpenAI-compatible
// service with a broad open-weight model catalog.
package fireworks

import "github.com/siguago/llmkit/provider/compat"

// Fireworks serves its OpenAI-compatible surface under /inference/v1.
const defaultBaseURL = "https://api.fireworks.ai/inference/v1"

// New constructs a Fireworks AI provider. Pass an empty baseURL to use the
// default endpoint.
//
// Model IDs are fully-qualified paths, e.g.
// "accounts/fireworks/models/llama-v3p3-70b-instruct".
func New(baseURL string) *compat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.New(compat.Config{
		ProviderName: "fireworks",
		BaseURL:      baseURL,
	})
}
