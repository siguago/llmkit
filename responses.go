package llmkit

import (
	"context"
	"fmt"
	"time"

	responsesapi "github.com/siguago/llmkit/protocol/responses"
	"github.com/siguago/llmkit/provider"
)

// CreateResponse creates an OpenAI Responses API response without translating
// it through Chat Completions. The operation uses the replay-safe subset of the
// configured retry policy because repeating an accepted create can generate
// and bill twice.
func (c *Client) CreateResponse(ctx context.Context, req *responsesapi.CreateRequest) (*responsesapi.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil Responses request")
	}
	creator, ok := c.provider.(provider.ResponsesCreator)
	if !ok {
		return nil, unsupportedf(c.name, "OpenAI Responses create")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	response, err := doValue(ctx, c.cfg.creationRetryPolicy(), func() (*responsesapi.Response, error) {
		return creator.CreateResponse(ctx, c.cfg.apiKey, req)
	})
	return response, translateUnsupported(err)
}

// CreateResponseStream starts a native Responses SSE stream. Always Close the
// returned stream. Retries cover only creation of the stream; Recv failures are
// never replayed after events may have been delivered.
func (c *Client) CreateResponseStream(ctx context.Context, req *responsesapi.CreateRequest) (responsesapi.Stream, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil Responses request")
	}
	streamer, ok := c.provider.(provider.ResponsesStreamer)
	if !ok {
		return nil, unsupportedf(c.name, "OpenAI Responses streaming")
	}
	ctx = c.decorate(ctx)
	stream, err := doValue(ctx, c.cfg.creationRetryPolicy(), func() (responsesapi.Stream, error) {
		return streamer.CreateResponseStream(ctx, c.cfg.apiKey, req)
	})
	return stream, translateUnsupported(err)
}

// RetrieveResponse retrieves a stored or background Response by ID.
func (c *Client) RetrieveResponse(ctx context.Context, responseID string, opts *responsesapi.RetrieveOptions) (*responsesapi.Response, error) {
	retriever, ok := c.provider.(provider.ResponsesRetriever)
	if !ok {
		return nil, unsupportedf(c.name, "OpenAI Responses retrieval")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	response, err := doValue(ctx, c.cfg.retry, func() (*responsesapi.Response, error) {
		return retriever.RetrieveResponse(ctx, c.cfg.apiKey, responseID, opts)
	})
	return response, translateUnsupported(err)
}

// CancelResponse asks OpenAI to cancel a background Response. It is not
// automatically retried because cancellation changes resource state.
func (c *Client) CancelResponse(ctx context.Context, responseID string) (*responsesapi.Response, error) {
	canceller, ok := c.provider.(provider.ResponsesCanceller)
	if !ok {
		return nil, unsupportedf(c.name, "OpenAI Responses cancellation")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	response, err := canceller.CancelResponse(ctx, c.cfg.apiKey, responseID)
	return response, translateUnsupported(err)
}

// DeleteResponse deletes a stored Response. It is not automatically retried
// because deletion changes resource state.
func (c *Client) DeleteResponse(ctx context.Context, responseID string) (*responsesapi.DeletedResponse, error) {
	deleter, ok := c.provider.(provider.ResponsesDeleter)
	if !ok {
		return nil, unsupportedf(c.name, "OpenAI Responses deletion")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	response, err := deleter.DeleteResponse(ctx, c.cfg.apiKey, responseID)
	return response, translateUnsupported(err)
}

// ListResponseInputItems lists the input items associated with a Response.
func (c *Client) ListResponseInputItems(ctx context.Context, responseID string, opts *responsesapi.ListInputItemsOptions) (*responsesapi.InputItemList, error) {
	lister, ok := c.provider.(provider.ResponsesInputItemLister)
	if !ok {
		return nil, unsupportedf(c.name, "OpenAI Responses input items")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	items, err := doValue(ctx, c.cfg.retry, func() (*responsesapi.InputItemList, error) {
		return lister.ListResponseInputItems(ctx, c.cfg.apiKey, responseID, opts)
	})
	return items, translateUnsupported(err)
}

// CountResponseInputTokens counts the input tokens that a Responses request
// would consume without creating a Response.
func (c *Client) CountResponseInputTokens(ctx context.Context, req *responsesapi.TokenCountRequest) (*responsesapi.TokenCountResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil Responses token-count request")
	}
	counter, ok := c.provider.(provider.ResponsesTokenCounter)
	if !ok {
		return nil, unsupportedf(c.name, "OpenAI Responses input token count")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	count, err := doValue(ctx, c.cfg.retry, func() (*responsesapi.TokenCountResponse, error) {
		return counter.CountResponseInputTokens(ctx, c.cfg.apiKey, req)
	})
	return count, translateUnsupported(err)
}

// SupportsResponses reports whether native synchronous Responses creation is
// available. Other operations are separately probeable because relays often
// implement only part of the Responses resource.
func (c *Client) SupportsResponses() bool {
	_, ok := c.provider.(provider.ResponsesCreator)
	return ok
}

// SupportsResponseStreaming reports whether native Responses SSE creation is available.
func (c *Client) SupportsResponseStreaming() bool {
	_, ok := c.provider.(provider.ResponsesStreamer)
	return ok
}

// SupportsResponseRetrieval reports whether stored Responses can be retrieved.
func (c *Client) SupportsResponseRetrieval() bool {
	_, ok := c.provider.(provider.ResponsesRetriever)
	return ok
}

// SupportsResponseCancellation reports whether background Responses can be cancelled.
func (c *Client) SupportsResponseCancellation() bool {
	_, ok := c.provider.(provider.ResponsesCanceller)
	return ok
}

// SupportsResponseDeletion reports whether stored Responses can be deleted.
func (c *Client) SupportsResponseDeletion() bool {
	_, ok := c.provider.(provider.ResponsesDeleter)
	return ok
}

// SupportsResponseInputItems reports whether a Response's input items can be listed.
func (c *Client) SupportsResponseInputItems() bool {
	_, ok := c.provider.(provider.ResponsesInputItemLister)
	return ok
}

// SupportsResponseTokenCount reports whether Responses input tokens can be counted.
func (c *Client) SupportsResponseTokenCount() bool {
	_, ok := c.provider.(provider.ResponsesTokenCounter)
	return ok
}

// WaitResponseOptions tunes WaitResponse polling.
type WaitResponseOptions struct {
	// Interval between polls. Default 1s.
	Interval time.Duration
	// Timeout bounds the whole wait. Default 30m. Use a context deadline for a
	// different cancellation policy.
	Timeout time.Duration
	// OnUpdate is called after each successful retrieval.
	OnUpdate func(*responsesapi.Response)
}

// WaitResponse polls a background Response until it reaches a terminal status.
// Failed and incomplete Responses are returned with a nil polling error; inspect
// Response.Status, Error, and IncompleteDetails for the generation outcome.
func (c *Client) WaitResponse(ctx context.Context, response *responsesapi.Response, opts *WaitResponseOptions) (*responsesapi.Response, error) {
	if response == nil {
		return nil, fmt.Errorf("llmkit: nil Response")
	}
	if response.ID == "" {
		return nil, fmt.Errorf("llmkit: Response.ID is required")
	}
	if isTerminalResponseStatus(response.Status) {
		return response, nil
	}
	if !c.SupportsResponseRetrieval() {
		return nil, unsupportedf(c.name, "OpenAI Responses retrieval")
	}

	interval := time.Second
	timeout := 30 * time.Minute
	var onUpdate func(*responsesapi.Response)
	if opts != nil {
		if opts.Interval > 0 {
			interval = opts.Interval
		}
		if opts.Timeout > 0 {
			timeout = opts.Timeout
		}
		onUpdate = opts.OnUpdate
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	current := response
	for {
		select {
		case <-ctx.Done():
			return current, fmt.Errorf("llmkit: waiting for Response %s: %w", current.ID, ctx.Err())
		case <-ticker.C:
		}
		updated, err := c.RetrieveResponse(ctx, current.ID, nil)
		if err != nil {
			// A newly-created background resource can briefly lag behind the
			// create response in the retrieval store. Treat that eventual 404
			// like a transient poll failure; a persistent one still ends at the
			// caller's wait deadline instead of spinning forever.
			if IsRetryable(err) || IsNotFound(err) {
				continue
			}
			return current, err
		}
		current = updated
		if onUpdate != nil {
			onUpdate(current)
		}
		if isTerminalResponseStatus(current.Status) {
			return current, nil
		}
	}
}

func isTerminalResponseStatus(status string) bool {
	switch status {
	case responsesapi.StatusCompleted, responsesapi.StatusFailed,
		responsesapi.StatusIncomplete, responsesapi.StatusCancelled:
		return true
	default:
		return false
	}
}
