package compat

import (
	"context"

	"github.com/siguago/llmkit/provider"
)

// ChatOnly is an OpenAI-compatible provider with the embeddings route withheld:
// chat, streaming and model listing are delegated, Embeddings is not implemented
// at all.
//
// It exists because Go method promotion cannot be opted out of. Embedding
// *Provider promotes Embeddings, which makes
// llmkit.Client.SupportsEmbeddings report true — and a vendor whose /embeddings
// answers 404 rather than 401 has no such route, so that answer is a lie. The
// split capability interfaces exist precisely so a caller can ask before
// spending a call; an adapter that over-reports defeats them.
//
// Use it for a vendor that serves chat over an OpenAI-compatible surface but has
// no embeddings endpoint. Switching a provider back to the full surface once the
// vendor ships one is a one-word change at its New: NewChatOnly → New.
type ChatOnly struct {
	chat *Provider
}

// NewChatOnly creates an OpenAI-compatible provider that does not claim
// embeddings support.
func NewChatOnly(cfg Config) *ChatOnly {
	return &ChatOnly{chat: New(cfg)}
}

func (p *ChatOnly) Name() string { return p.chat.Name() }

func (p *ChatOnly) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	return p.chat.ChatCompletion(ctx, apiKey, model, req)
}

func (p *ChatOnly) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	return p.chat.ChatCompletionStream(ctx, apiKey, model, req)
}

func (p *ChatOnly) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	return p.chat.ListModels(ctx, apiKey)
}

var (
	_ provider.Provider    = (*ChatOnly)(nil)
	_ provider.ModelLister = (*ChatOnly)(nil)
)
