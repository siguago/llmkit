// Package vllm adapts a self-hosted vLLM server through its OpenAI-compatible
// endpoint.
//
// A vLLM server started without --api-key serves unauthenticated, so
// llmkit.New(llmkit.VLLM) constructs without a credential; pass one when the
// server was started with --api-key.
package vllm

import "github.com/siguago/llmkit/provider/compat"

// vLLM's OpenAI-compatible server listens on :8000 by default. Self-hosted
// deployments almost always override this — pass your own baseURL.
const defaultBaseURL = "http://localhost:8000/v1"

// New constructs a vLLM provider. Pass an empty baseURL to use the default
// local endpoint.
//
// vLLM accepts chat_template_kwargs for template-level switches such as
// enable_thinking on Qwen3; the compat layer already forwards that field.
//
// SupportsEmbeddings is true because vLLM's OpenAI server does serve
// /v1/embeddings — but a server is launched with one model, so the route answers
// only when that model is an embedding model. Unlike a vendor that has no such
// route at all, this is a deployment question we cannot answer from here.
func New(baseURL string) *compat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.New(compat.Config{
		ProviderName: "vllm",
		BaseURL:      baseURL,
	})
}
