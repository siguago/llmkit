package provider

import "net/http"

// SetBearer attaches the credential as an Authorization bearer token, or
// attaches nothing when there is no credential to send.
//
// The empty case is the whole point. Some endpoints are legitimately
// unauthenticated — a local Ollama or vLLM runtime, an internal gateway that
// authenticates by network position (see llmkit.WithoutAPIKey) — and a literal
// "Bearer " with nothing after it is not a valid credential. Omitting the header
// is both what the endpoint expects and what a proxy in front of it will accept;
// a malformed one invites a 401 whose cause is nowhere near the call.
//
// Every adapter must route its credential through this rather than
// Header.Set("Authorization", "Bearer "+apiKey), so the unauthenticated case
// behaves the same on every route — not just the chat route.
func SetBearer(h http.Header, apiKey string) {
	if apiKey == "" {
		return
	}
	h.Set("Authorization", "Bearer "+apiKey)
}

// SetKeyHeader attaches the credential under a vendor-specific header name —
// Anthropic's "x-api-key", Gemini's "x-goog-api-key" — and attaches nothing when
// there is no credential. Same reasoning as SetBearer: an empty header value is a
// malformed credential, not the absence of one.
func SetKeyHeader(h http.Header, name, apiKey string) {
	if apiKey == "" {
		return
	}
	h.Set(name, apiKey)
}
