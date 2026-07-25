package provider

import "context"

// Provider defines the interface for AI model providers.
type Provider interface {
	Name() string
	ChatCompletion(ctx context.Context, apiKey, model string, req *ChatCompletionRequest) (*ChatCompletionResponse, error)
	ChatCompletionStream(ctx context.Context, apiKey, model string, req *ChatCompletionRequest) (StreamReader, error)
}
