package provider

import "context"

// Provider defines the interface for AI model providers.
type Provider interface {
	Name() string
	ChatCompletion(ctx context.Context, apiKey, model string, req *ChatCompletionRequest) (*ChatCompletionResponse, error)
	ChatCompletionStream(ctx context.Context, apiKey, model string, req *ChatCompletionRequest) (StreamReader, error)
}

// NonChatProvider is implemented by adapters that exist only for non-chat
// endpoints — a vendor whose API this SDK covers for image or video generation
// but whose chat endpoint it does not reach at all.
//
// No bundled adapter implements it today: DashScope and Volcengine did while
// they were video-only, and both now delegate chat to their vendors'
// OpenAI-compatible endpoints. It stays because the situation recurs whenever a
// vendor is worth adapting for one endpoint before the rest.
//
// Chat cannot be made optional the way images and video are: it is on Provider
// itself, so every adapter has the methods and a type assertion can never tell
// the difference. Such adapters return ErrUnsupported from both chat methods and
// implement this interface to say so up front, which is what lets
// llmkit.Client.SupportsChat answer before the call rather than after.
//
// Not implementing it means "this adapter does chat", which is the common case.
type NonChatProvider interface {
	Provider
	// ChatUnsupported is a marker. Its body is irrelevant; implementing the
	// method at all is the declaration.
	ChatUnsupported()
}
