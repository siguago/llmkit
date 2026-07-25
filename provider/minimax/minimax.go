package minimax

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.minimax.io/v1"

// MiniMax /v1/chat/completions is OpenAI-compatible and signals errors via HTTP
// status codes. The legacy /v1/text/chatcompletion_v2 endpoint used a base_resp
// in-body envelope, but we don't target that surface.
//
// Pass an empty baseURL to use the international default; mainland China users
// typically pass https://api.minimaxi.com/v1.
func New(baseURL string) *compat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.New(compat.Config{
		ProviderName: "minimax",
		BaseURL:      baseURL,
	})
}
