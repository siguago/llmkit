package siliconflow

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.siliconflow.cn/v1"

// New constructs a SiliconFlow provider. Pass an empty baseURL to use the
// default mainland China endpoint.
//
// SiliconFlow publishes /rerank alongside the OpenAI-compatible surface, in the
// Cohere-derived shape compat.WithRerank implements, so this adapter claims the
// capability. Its rerank models are the BAAI/bge-reranker-* and Qwen reranker
// families; the vendor-specific max_chunks_per_doc knob rides in
// ProviderOptions["siliconflow"].
func New(baseURL string) *compat.WithRerank {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.NewWithRerank(compat.Config{
		ProviderName: "siliconflow",
		BaseURL:      baseURL,
	})
}
