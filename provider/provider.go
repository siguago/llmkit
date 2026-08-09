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

// CacheWriteUsageReporter is implemented by adapters whose current
// configuration reliably populates Usage.CacheCreationTokens — the aggregate
// prompt portion written into the vendor's cache — on every successful unified
// chat path, both buffered and streaming.
//
// It exists for billing-aware callers that must not guess. An adapter whose
// vendor has no cache-write concept leaves CacheCreationTokens at zero, and so
// does an adapter that has one but never reported it; the field alone cannot
// tell those apart. When this capability reports true, zero means that no cache
// write happened for that successful request:
//
//	if r, ok := p.(provider.CacheWriteUsageReporter); ok && r.ReportsCacheWriteUsage() {
//		// zero means no cache write happened
//	}
//
// The aggregate count is not sufficient for exact billing when a vendor prices
// cache writes differently by TTL. Billing code must use
// [Usage.CacheCreationTokensDetails] for that calculation.
//
// Only the Anthropic adapter exposes this capability today. Not implementing
// it, or returning false, means "assume nothing about cache writes from this
// adapter's usage".
type CacheWriteUsageReporter interface {
	Provider
	// ReportsCacheWriteUsage reports a capability of this provider instance's
	// current configuration, including its configured endpoint. It must return
	// true only when every successful ChatCompletion and every fully consumed,
	// successfully completed ChatCompletionStream path reliably reports the
	// aggregate cache-write count. It is not a
	// per-request status, must not describe only some paths, and must not depend
	// on the most recent request.
	ReportsCacheWriteUsage() bool
}
