package llmkit

import (
	"context"
	"fmt"
	"time"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/provider"
)

// CreateAnthropicMessageBatch creates a native Message Batch with inline
// requests. Creation queues billable work with no SDK-level idempotency key,
// so it uses the replay-safe subset of the configured retry policy.
func (c *Client) CreateAnthropicMessageBatch(ctx context.Context, req *anthropicapi.MessageBatchCreateRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatch, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil Anthropic message batch create request")
	}
	creator, ok := c.provider.(provider.AnthropicMessageBatchCreator)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Message Batches create")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	batch, err := doValue(ctx, c.cfg.creationRetryPolicy(), func() (*anthropicapi.MessageBatch, error) {
		return creator.CreateAnthropicMessageBatch(ctx, c.cfg.apiKey, req, opts...)
	})
	return batch, translateUnsupported(err)
}

// RetrieveAnthropicMessageBatch retrieves one Message Batch by ID; the
// idempotent polling primitive (or use WaitAnthropicMessageBatch).
func (c *Client) RetrieveAnthropicMessageBatch(ctx context.Context, batchID string, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatch, error) {
	retriever, ok := c.provider.(provider.AnthropicMessageBatchRetriever)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Message Batches retrieval")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	batch, err := doValue(ctx, c.cfg.retry, func() (*anthropicapi.MessageBatch, error) {
		return retriever.RetrieveAnthropicMessageBatch(ctx, c.cfg.apiKey, batchID, opts...)
	})
	return batch, translateUnsupported(err)
}

// ListAnthropicMessageBatches lists Message Batches in the workspace. A nil
// request uses the upstream's own defaults (limit 20, newest first).
func (c *Client) ListAnthropicMessageBatches(ctx context.Context, req *anthropicapi.MessageBatchListRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatchList, error) {
	lister, ok := c.provider.(provider.AnthropicMessageBatchLister)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Message Batches listing")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	list, err := doValue(ctx, c.cfg.retry, func() (*anthropicapi.MessageBatchList, error) {
		return lister.ListAnthropicMessageBatches(ctx, c.cfg.apiKey, req, opts...)
	})
	return list, translateUnsupported(err)
}

// CancelAnthropicMessageBatch asks the upstream to cancel a Message Batch.
// It is not automatically retried because cancellation changes resource
// state. Non-interruptible in-flight requests may still complete and bill;
// check request_counts and the individual results for what was actually
// canceled.
func (c *Client) CancelAnthropicMessageBatch(ctx context.Context, batchID string, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatch, error) {
	canceller, ok := c.provider.(provider.AnthropicMessageBatchCanceller)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Message Batches cancellation")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	batch, err := canceller.CancelAnthropicMessageBatch(ctx, c.cfg.apiKey, batchID, opts...)
	return batch, translateUnsupported(err)
}

// DeleteAnthropicMessageBatch deletes an ended Message Batch. It is not
// automatically retried because deletion changes resource state. In-progress
// batches cannot be deleted — cancel first.
func (c *Client) DeleteAnthropicMessageBatch(ctx context.Context, batchID string, opts ...anthropicapi.RequestOption) (*anthropicapi.DeletedMessageBatch, error) {
	deleter, ok := c.provider.(provider.AnthropicMessageBatchDeleter)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Message Batches deletion")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	deleted, err := deleter.DeleteAnthropicMessageBatch(ctx, c.cfg.apiKey, batchID, opts...)
	return deleted, translateUnsupported(err)
}

// ReadAnthropicMessageBatchResults streams the results JSONL of an ended
// batch, decoded line by line. Always Close the returned reader. Retries
// cover only the request handshake; a read error mid-stream surfaces as-is.
// WithTimeout does not bound the stream — put the desired lifetime on ctx.
// Results stay downloadable for 29 days after batch creation.
func (c *Client) ReadAnthropicMessageBatchResults(ctx context.Context, batchID string, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatchResultsReader, error) {
	reader, ok := c.provider.(provider.AnthropicMessageBatchResultsReader)
	if !ok {
		return nil, unsupportedf(c.name, "Anthropic Message Batches results")
	}
	ctx = c.decorate(ctx)
	results, err := doValue(ctx, c.cfg.retry, func() (*anthropicapi.MessageBatchResultsReader, error) {
		return reader.ReadAnthropicMessageBatchResults(ctx, c.cfg.apiKey, batchID, opts...)
	})
	return results, translateUnsupported(err)
}

// SupportsAnthropicMessageBatchCreation reports whether Message Batches can
// be created. The other operations are separately probeable.
func (c *Client) SupportsAnthropicMessageBatchCreation() bool {
	_, ok := c.provider.(provider.AnthropicMessageBatchCreator)
	return ok
}

// SupportsAnthropicMessageBatchRetrieval reports whether Message Batches can
// be retrieved.
func (c *Client) SupportsAnthropicMessageBatchRetrieval() bool {
	_, ok := c.provider.(provider.AnthropicMessageBatchRetriever)
	return ok
}

// SupportsAnthropicMessageBatchListing reports whether Message Batches can be
// listed.
func (c *Client) SupportsAnthropicMessageBatchListing() bool {
	_, ok := c.provider.(provider.AnthropicMessageBatchLister)
	return ok
}

// SupportsAnthropicMessageBatchCancellation reports whether Message Batches
// can be cancelled.
func (c *Client) SupportsAnthropicMessageBatchCancellation() bool {
	_, ok := c.provider.(provider.AnthropicMessageBatchCanceller)
	return ok
}

// SupportsAnthropicMessageBatchDeletion reports whether Message Batches can
// be deleted.
func (c *Client) SupportsAnthropicMessageBatchDeletion() bool {
	_, ok := c.provider.(provider.AnthropicMessageBatchDeleter)
	return ok
}

// SupportsAnthropicMessageBatchResults reports whether batch results can be
// streamed.
func (c *Client) SupportsAnthropicMessageBatchResults() bool {
	_, ok := c.provider.(provider.AnthropicMessageBatchResultsReader)
	return ok
}

// WaitAnthropicMessageBatchOptions tunes WaitAnthropicMessageBatch polling.
type WaitAnthropicMessageBatchOptions struct {
	// Interval between polls. Default 60s.
	Interval time.Duration
	// Timeout bounds the whole wait. Default 26h — the 24-hour expiry plus
	// cancellation/finalization slack guarantees the batch has ended by then.
	Timeout time.Duration
	// OnUpdate is called after each successful poll.
	OnUpdate func(*anthropicapi.MessageBatch)
}

// WaitAnthropicMessageBatch polls a Message Batch until processing ends.
// An ended batch full of errored or expired requests is still a nil-error
// return: inspect RequestCounts and the results stream for per-request
// outcomes. Long-running production jobs are usually better served by their
// own scheduler than by a day-long blocking call.
func (c *Client) WaitAnthropicMessageBatch(ctx context.Context, batch *anthropicapi.MessageBatch, opts *WaitAnthropicMessageBatchOptions) (*anthropicapi.MessageBatch, error) {
	if batch == nil {
		return nil, fmt.Errorf("llmkit: nil MessageBatch")
	}
	if batch.ID == "" {
		return nil, fmt.Errorf("llmkit: MessageBatch.ID is required")
	}
	if batch.HasEnded() {
		return batch, nil
	}
	if !c.SupportsAnthropicMessageBatchRetrieval() {
		return nil, unsupportedf(c.name, "Anthropic Message Batches retrieval")
	}

	interval := time.Minute
	timeout := 26 * time.Hour
	var onUpdate func(*anthropicapi.MessageBatch)
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

	current := batch
	notFoundPolls := 0
	for {
		select {
		case <-ctx.Done():
			return current, fmt.Errorf("llmkit: waiting for message batch %s: %w", current.ID, ctx.Err())
		case <-ticker.C:
		}
		updated, err := c.RetrieveAnthropicMessageBatch(ctx, current.ID)
		if err != nil {
			if IsRetryable(err) {
				continue
			}
			// Same bounded grace as WaitBatch, and it matters more here:
			// deleting an ended batch is a documented operation, so polling a
			// deleted one is a realistic mistake rather than a freak event.
			if IsNotFound(err) {
				notFoundPolls++
				if notFoundPolls <= waitNotFoundGracePolls {
					continue
				}
			}
			return current, err
		}
		notFoundPolls = 0
		current = updated
		if onUpdate != nil {
			onUpdate(current)
		}
		if current.HasEnded() {
			return current, nil
		}
	}
}
