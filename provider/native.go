package provider

import (
	"context"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/protocol/responses"
)

// ResponsesCreator is implemented by providers that expose the OpenAI
// Responses create endpoint. It is intentionally separate from Provider: a
// third-party Chat Completions adapter must not grow a new required method just
// because llmkit learned another protocol.
type ResponsesCreator interface {
	CreateResponse(ctx context.Context, apiKey string, req *responses.CreateRequest) (*responses.Response, error)
}

// ResponsesStreamer is the streaming half of Responses creation. It is a
// separate capability because some relays implement only the JSON endpoint.
type ResponsesStreamer interface {
	CreateResponseStream(ctx context.Context, apiKey string, req *responses.CreateRequest) (responses.Stream, error)
}

// ResponsesRetriever retrieves a stored or background Response.
type ResponsesRetriever interface {
	RetrieveResponse(ctx context.Context, apiKey, responseID string, opts *responses.RetrieveOptions) (*responses.Response, error)
}

// ResponsesCanceller cancels a background Response.
type ResponsesCanceller interface {
	CancelResponse(ctx context.Context, apiKey, responseID string) (*responses.Response, error)
}

// ResponsesDeleter deletes a stored Response.
type ResponsesDeleter interface {
	DeleteResponse(ctx context.Context, apiKey, responseID string) (*responses.DeletedResponse, error)
}

// ResponsesInputItemLister lists the items that formed a Response's input.
type ResponsesInputItemLister interface {
	ListResponseInputItems(ctx context.Context, apiKey, responseID string, opts *responses.ListInputItemsOptions) (*responses.InputItemList, error)
}

// ResponsesTokenCounter calls the Responses input-token counting endpoint.
type ResponsesTokenCounter interface {
	CountResponseInputTokens(ctx context.Context, apiKey string, req *responses.TokenCountRequest) (*responses.TokenCountResponse, error)
}

// AnthropicMessagesCreator exposes Anthropic's native Messages JSON response
// without converting it to Chat Completions first.
type AnthropicMessagesCreator interface {
	CreateAnthropicMessage(ctx context.Context, apiKey string, req *anthropicapi.MessageRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageResponse, error)
}

// AnthropicMessagesStreamer exposes Anthropic's native Messages event stream.
type AnthropicMessagesStreamer interface {
	CreateAnthropicMessageStream(ctx context.Context, apiKey string, req *anthropicapi.MessageRequest, opts ...anthropicapi.RequestOption) (anthropicapi.Stream, error)
}

// AnthropicTokenCounter calls Anthropic's server-side token counting endpoint.
type AnthropicTokenCounter interface {
	CountAnthropicMessageTokens(ctx context.Context, apiKey string, req *anthropicapi.TokenCountRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.TokenCountResponse, error)
}
