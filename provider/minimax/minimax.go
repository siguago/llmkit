package minimax

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.minimax.io/v1"

// MiniMax /v1/chat/completions is OpenAI-compatible and signals errors via HTTP
// status codes. The legacy /v1/text/chatcompletion_v2 endpoint used a base_resp
// in-body envelope, but we don't target that surface.
//
// Chat-only, and for a different reason than the vendors that simply lack the
// route: MiniMax has /v1/embeddings, but it is not OpenAI-shaped. It wants a
// GroupId query parameter, takes `texts` rather than `input`, requires a `type`
// of "db" or "query", and answers with a top-level `vectors` array instead of
// `data[].embedding`. The compat layer's Embeddings would send and parse the
// wrong shapes on every field, so claiming the capability would be worse than
// not having it — the failure would look like a broken SDK rather than an
// unimplemented adapter. Supporting it means a hand-written method, not a
// promoted one.
//
// Pass an empty baseURL to use the international default; mainland China users
// typically pass https://api.minimaxi.com/v1.
func New(baseURL string) *compat.ChatOnly {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.NewChatOnly(compat.Config{
		ProviderName: "minimax",
		BaseURL:      baseURL,
	})
}
