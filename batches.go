package llmkit

import (
	"context"
	"fmt"
	"time"

	"github.com/siguago/llmkit/protocol/openaibatch"
	"github.com/siguago/llmkit/provider"
)

// CreateBatch creates an OpenAI batch over a previously uploaded JSONL input
// file. Creation queues billable work with no SDK-level idempotency key, so
// it uses the replay-safe subset of the configured retry policy — creating
// twice from one ambiguous failure would run and bill the batch twice.
func (c *Client) CreateBatch(ctx context.Context, req *openaibatch.CreateRequest) (*openaibatch.Batch, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil batch create request")
	}
	creator, ok := c.provider.(provider.BatchCreator)
	if !ok {
		return nil, unsupportedf(c.name, "batch creation")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	batch, err := doValue(ctx, c.cfg.creationRetryPolicy(), func() (*openaibatch.Batch, error) {
		return creator.CreateBatch(ctx, c.cfg.apiKey, req)
	})
	return batch, translateUnsupported(err)
}

// RetrieveBatch retrieves one batch by ID; poll it (or use WaitBatch) to
// follow progress.
func (c *Client) RetrieveBatch(ctx context.Context, batchID string) (*openaibatch.Batch, error) {
	retriever, ok := c.provider.(provider.BatchRetriever)
	if !ok {
		return nil, unsupportedf(c.name, "batch retrieval")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	batch, err := doValue(ctx, c.cfg.retry, func() (*openaibatch.Batch, error) {
		return retriever.RetrieveBatch(ctx, c.cfg.apiKey, batchID)
	})
	return batch, translateUnsupported(err)
}

// ListBatches lists batches. A nil request uses the upstream's own defaults
// (limit 20, newest first).
func (c *Client) ListBatches(ctx context.Context, req *openaibatch.ListRequest) (*openaibatch.BatchList, error) {
	lister, ok := c.provider.(provider.BatchLister)
	if !ok {
		return nil, unsupportedf(c.name, "batch listing")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	list, err := doValue(ctx, c.cfg.retry, func() (*openaibatch.BatchList, error) {
		return lister.ListBatches(ctx, c.cfg.apiKey, req)
	})
	return list, translateUnsupported(err)
}

// CancelBatch asks the upstream to cancel an in-progress batch. It is not
// automatically retried because cancellation changes resource state. The
// batch stays "cancelling" for up to ten minutes; partial results still land
// in the output file and bill normally.
func (c *Client) CancelBatch(ctx context.Context, batchID string) (*openaibatch.Batch, error) {
	canceller, ok := c.provider.(provider.BatchCanceller)
	if !ok {
		return nil, unsupportedf(c.name, "batch cancellation")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	batch, err := canceller.CancelBatch(ctx, c.cfg.apiKey, batchID)
	return batch, translateUnsupported(err)
}

// SupportsBatchCreation reports whether OpenAI batches can be created. The
// other batch operations are separately probeable.
func (c *Client) SupportsBatchCreation() bool {
	_, ok := c.provider.(provider.BatchCreator)
	return ok
}

// SupportsBatchRetrieval reports whether batches can be retrieved.
func (c *Client) SupportsBatchRetrieval() bool {
	_, ok := c.provider.(provider.BatchRetriever)
	return ok
}

// SupportsBatchListing reports whether batches can be listed.
func (c *Client) SupportsBatchListing() bool {
	_, ok := c.provider.(provider.BatchLister)
	return ok
}

// SupportsBatchCancellation reports whether batches can be cancelled.
func (c *Client) SupportsBatchCancellation() bool {
	_, ok := c.provider.(provider.BatchCanceller)
	return ok
}

// WaitBatchOptions tunes WaitBatch polling.
type WaitBatchOptions struct {
	// Interval between polls. Default 60s — batches are measured in minutes
	// to hours, and tighter polling only burns rate limit.
	Interval time.Duration
	// Timeout bounds the whole wait. Default 26h, which covers the 24-hour
	// completion window plus cancellation/finalization slack; the batch is
	// guaranteed terminal by then. Set a smaller value (or a context
	// deadline) when you would rather give up than wait a day.
	Timeout time.Duration
	// OnUpdate is called after each successful poll.
	OnUpdate func(*openaibatch.Batch)
}

// WaitBatch polls a batch until it reaches a terminal status (completed,
// failed, expired, or cancelled). Terminal-but-unsuccessful batches are
// returned with a nil polling error; inspect Batch.Status, Errors, and
// ErrorFileID for the outcome. Long-running production jobs are usually
// better served by their own scheduler (or upstream webhooks, which this SDK
// does not cover) than by a day-long blocking call.
func (c *Client) WaitBatch(ctx context.Context, batch *openaibatch.Batch, opts *WaitBatchOptions) (*openaibatch.Batch, error) {
	if batch == nil {
		return nil, fmt.Errorf("llmkit: nil Batch")
	}
	if batch.ID == "" {
		return nil, fmt.Errorf("llmkit: Batch.ID is required")
	}
	if batch.IsTerminal() {
		return batch, nil
	}
	if !c.SupportsBatchRetrieval() {
		return nil, unsupportedf(c.name, "batch retrieval")
	}

	interval := time.Minute
	timeout := 26 * time.Hour
	var onUpdate func(*openaibatch.Batch)
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
	for {
		select {
		case <-ctx.Done():
			return current, fmt.Errorf("llmkit: waiting for batch %s: %w", current.ID, ctx.Err())
		case <-ticker.C:
		}
		updated, err := c.RetrieveBatch(ctx, current.ID)
		if err != nil {
			if IsRetryable(err) || IsNotFound(err) {
				continue
			}
			return current, err
		}
		current = updated
		if onUpdate != nil {
			onUpdate(current)
		}
		if current.IsTerminal() {
			return current, nil
		}
	}
}
