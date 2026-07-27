// Package ollama adapts a local Ollama runtime through its OpenAI-compatible
// endpoint.
//
// Ollama serves unauthenticated by default, so llmkit.New(llmkit.Ollama)
// constructs without a credential. Set one anyway if you front the runtime with
// a proxy that checks bearer tokens.
package ollama

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "http://localhost:11434/v1"

// New constructs an Ollama provider. Pass an empty baseURL to use the default
// local endpoint.
//
// The model field is an Ollama model tag, e.g. "llama3.3" or "qwen3:14b".
func New(baseURL string) *compat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.New(compat.Config{
		ProviderName: "ollama",
		BaseURL:      baseURL,
	})
}
