package compat

import (
	"context"

	"github.com/siguago/llmkit/provider"
)

// NoEmbeddings is a Provider with exactly one thing withheld: chat, streaming and
// model listing are delegated as usual, and Embeddings is not implemented at all,
// so the value does not satisfy provider.Embedder.
//
// It exists because Go method promotion cannot be opted out of. Embedding
// *Provider promotes Embeddings, which makes llmkit.Client.SupportsEmbeddings
// report true — and for a vendor with no usable /embeddings route that answer is
// a lie. The split capability interfaces exist precisely so a caller can ask
// before spending a call; an adapter that over-reports defeats them.
//
// Use it for a vendor whose OpenAI-compatible surface has no embeddings route, or
// has one whose request shape is not OpenAI's. Each adapter's New documents which
// case it is. Switching back to the full surface once a vendor ships a compatible
// route is a one-word change at its New: NewNoEmbeddings → New.
type NoEmbeddings struct {
	chat *Provider
}

// NewNoEmbeddings creates an OpenAI-compatible provider that does not claim
// embeddings support.
func NewNoEmbeddings(cfg Config) *NoEmbeddings {
	return &NoEmbeddings{chat: New(cfg)}
}

func (p *NoEmbeddings) Name() string { return p.chat.Name() }

func (p *NoEmbeddings) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	return p.chat.ChatCompletion(ctx, apiKey, model, req)
}

func (p *NoEmbeddings) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	return p.chat.ChatCompletionStream(ctx, apiKey, model, req)
}

func (p *NoEmbeddings) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	return p.chat.ListModels(ctx, apiKey)
}

var (
	_ provider.Provider    = (*NoEmbeddings)(nil)
	_ provider.ModelLister = (*NoEmbeddings)(nil)
)
