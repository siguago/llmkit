package provider

import "context"

// RerankRequest is the unified reranking request.
//
// Reranking scores a set of candidate documents against one query, and is the
// second stage of a typical RAG pipeline: embeddings retrieve a coarse
// candidate set cheaply, then a cross-encoder reranker reorders it accurately.
// The two are complementary, not alternatives — a reranker cannot search a
// corpus, and an embedding model cannot see query and document together.
//
// Model, Query and at least one Documents entry are required.
type RerankRequest struct {
	Model string `json:"model"`
	// Query is the search intent the documents are scored against.
	Query string `json:"query"`
	// Documents are the candidates to score. Order is the caller's; the
	// response does not preserve it — see RerankResult.Index.
	Documents []string `json:"documents"`
	// TopN caps how many results come back. Nil means the vendor's default,
	// which is usually "all of them".
	TopN *int `json:"top_n,omitempty"`
	// ReturnDocuments asks the vendor to echo each document's text back in the
	// result. Nil leaves it to the vendor default (usually false). Leaving it
	// off keeps responses small — Index is enough to look the text back up.
	ReturnDocuments *bool `json:"return_documents,omitempty"`
	// ProviderOptions is an opaque per-vendor block forwarded as-is, for knobs
	// this struct does not model. Providers that don't recognize it ignore it.
	ProviderOptions map[string]any `json:"provider_options,omitempty"`
}

// RerankResponse is the unified reranking response.
type RerankResponse struct {
	Object string `json:"object,omitempty"` // "list"
	Model  string `json:"model"`
	// Results are ordered by relevance, most relevant first — NOT by input
	// order. This is the one place the unified surface deliberately breaks the
	// positional contract that Embed keeps: reordering is the entire operation.
	// Use Result.Index to map back to the request's Documents slice.
	Results  []RerankResult `json:"results"`
	Usage    *Usage         `json:"usage,omitempty"`
	Provider string         `json:"provider_name,omitempty"`
}

// RerankResult is one scored document.
type RerankResult struct {
	// Index is the position of this document in the request's Documents slice.
	// It is the only reliable link back to the caller's data, because Results
	// is sorted by score and may be truncated by TopN.
	Index int `json:"index"`
	// RelevanceScore is the vendor's score for this document against the query.
	// The scale is NOT portable across vendors or models — some emit a 0..1
	// probability, others an unbounded logit. Use it to order and to threshold
	// against a value you tuned for that specific model, never to compare
	// across models.
	RelevanceScore float64 `json:"relevance_score"`
	// Document is the document text, present only when ReturnDocuments asked
	// for it and the vendor honoured the request.
	Document string `json:"document,omitempty"`
}

// Reranker is an optional interface for providers exposing a /rerank route.
//
// Kept separate from Provider for the same reason Embedder is: a type assertion
// is what llmkit.Client.SupportsRerank answers with, so an adapter that
// implements this method must actually be able to serve the call. An adapter
// whose vendor has no rerank route simply does not implement it.
type Reranker interface {
	Name() string
	Rerank(ctx context.Context, apiKey, model string, req *RerankRequest) (*RerankResponse, error)
}
