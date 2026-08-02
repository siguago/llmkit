package llmkit

import (
	"context"
	"fmt"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/provider"
)

// CreateAnthropicMessage calls Anthropic's native Messages endpoint without
// translating through the OpenAI-shaped Chat API. Header options are scoped to
// this call.
func (c *Client) CreateAnthropicMessage(ctx context.Context, req *anthropicapi.MessageRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil Anthropic Messages request")
	}
	creator, ok := c.provider.(provider.AnthropicMessagesCreator)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Messages create")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	message, err := doValue(ctx, c.cfg.creationRetryPolicy(), func() (*anthropicapi.MessageResponse, error) {
		return creator.CreateAnthropicMessage(ctx, c.cfg.apiKey, req, opts...)
	})
	return message, translateUnsupported(err)
}

// CreateAnthropicMessageStream starts a native Messages SSE stream. Always
// Close the returned stream. Recv returns Anthropic events, including unknown
// future events with their raw JSON intact. WithTimeout does not bound streams;
// put the desired lifetime on ctx.
func (c *Client) CreateAnthropicMessageStream(ctx context.Context, req *anthropicapi.MessageRequest, opts ...anthropicapi.RequestOption) (anthropicapi.Stream, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil Anthropic Messages request")
	}
	streamer, ok := c.provider.(provider.AnthropicMessagesStreamer)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Messages streaming")
	}
	ctx = c.decorate(ctx)
	stream, err := doValue(ctx, c.cfg.creationRetryPolicy(), func() (anthropicapi.Stream, error) {
		return streamer.CreateAnthropicMessageStream(ctx, c.cfg.apiKey, req, opts...)
	})
	return stream, translateUnsupported(err)
}

// CountAnthropicMessageTokens counts the tokens in a native Messages request
// without generating a Message.
func (c *Client) CountAnthropicMessageTokens(ctx context.Context, req *anthropicapi.TokenCountRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.TokenCountResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil Anthropic token-count request")
	}
	counter, ok := c.provider.(provider.AnthropicTokenCounter)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Messages token count")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	count, err := doValue(ctx, c.cfg.retry, func() (*anthropicapi.TokenCountResponse, error) {
		return counter.CountAnthropicMessageTokens(ctx, c.cfg.apiKey, req, opts...)
	})
	return count, translateUnsupported(err)
}

// SupportsAnthropicMessages reports whether native synchronous Messages
// creation is available. Streaming and token count are independent capabilities.
func (c *Client) SupportsAnthropicMessages() bool {
	_, ok := c.provider.(provider.AnthropicMessagesCreator)
	return ok
}

// SupportsAnthropicMessageStreaming reports whether native Messages SSE creation is available.
func (c *Client) SupportsAnthropicMessageStreaming() bool {
	_, ok := c.provider.(provider.AnthropicMessagesStreamer)
	return ok
}

// SupportsAnthropicTokenCount reports whether native Messages input tokens can be counted.
func (c *Client) SupportsAnthropicTokenCount() bool {
	_, ok := c.provider.(provider.AnthropicTokenCounter)
	return ok
}
