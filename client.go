package llmkit

import (
	"context"
	"fmt"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
)

// Client talks to one provider with one credential. It is safe for concurrent
// use; the underlying HTTP connections are pooled and shared.
type Client struct {
	name     string
	provider provider.Provider
	cfg      clientConfig
}

// Provider returns the provider name this client targets.
func (c *Client) Provider() string { return c.name }

// Adapter exposes the underlying provider implementation for the rare case
// where you need a vendor-specific method the façade does not surface.
func (c *Client) Adapter() provider.Provider { return c.provider }

// prepare applies per-call context decoration: the configured timeout and any
// extra headers. Callers must invoke the returned cancel func.
func (c *Client) prepare(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = httpx.WithExtraHeaders(ctx, c.cfg.headers)
	if c.cfg.timeout > 0 {
		return context.WithTimeout(ctx, c.cfg.timeout)
	}
	return ctx, func() {}
}

// ---------------------------------------------------------------- chat

// Chat performs a non-streaming chat completion.
//
// req.Model selects the model. The call is retried per the client's retry
// policy; a non-streaming completion is safe to replay because nothing has been
// delivered to the caller until it succeeds.
func (c *Client) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil request")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("llmkit: request.Model is required")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	return doValue(ctx, c.cfg.retry, func() (*ChatResponse, error) {
		return c.provider.ChatCompletion(ctx, c.cfg.apiKey, req.Model, req)
	})
}

// ChatStream starts a streaming chat completion. Read it with Recv until
// io.EOF and always Close it:
//
//	stream, err := client.ChatStream(ctx, req)
//	if err != nil {
//		return err
//	}
//	defer stream.Close()
//	for {
//		chunk, err := stream.Recv()
//		if errors.Is(err, io.EOF) {
//			break
//		}
//		if err != nil {
//			return err
//		}
//		fmt.Print(llmkit.ChunkText(chunk))
//	}
//
// Retries cover only the handshake — establishing the stream and receiving
// response headers. Once the stream is open, a mid-stream failure surfaces from
// Recv and is not retried: the model has already emitted tokens the caller
// consumed, and replaying the request would duplicate them.
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (Stream, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil request")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("llmkit: request.Model is required")
	}
	// The stream outlives this call, so the context must not be cancelled on
	// return. WithTimeout is therefore not applied to streams — their ceiling is
	// the provider's own stream timeout. Callers wanting a bound should pass a
	// context with a deadline.
	ctx = httpx.WithExtraHeaders(ctx, c.cfg.headers)

	return doValue(ctx, c.cfg.retry, func() (Stream, error) {
		return c.provider.ChatCompletionStream(ctx, c.cfg.apiKey, req.Model, req)
	})
}

// ---------------------------------------------------------------- models

// Models lists the models this provider's API reports as available.
//
// Returns ErrUnsupported for providers with no catalog endpoint.
func (c *Client) Models(ctx context.Context) ([]RemoteModel, error) {
	lister, ok := c.provider.(provider.ModelLister)
	if !ok {
		return nil, unsupportedf(c.name, "listing models")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	return doValue(ctx, c.cfg.retry, func() ([]RemoteModel, error) {
		return lister.ListModels(ctx, c.cfg.apiKey)
	})
}

// SupportsModels reports whether Models is available on this provider.
func (c *Client) SupportsModels() bool {
	_, ok := c.provider.(provider.ModelLister)
	return ok
}

// ---------------------------------------------------------------- embeddings

// Embed computes embedding vectors.
//
// Returns ErrUnsupported for providers with no embeddings endpoint. Note that a
// provider supporting the endpoint says nothing about whether a given model is
// an embedding model — that error comes from upstream.
func (c *Client) Embed(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil request")
	}
	embedder, ok := c.provider.(provider.Embedder)
	if !ok {
		return nil, unsupportedf(c.name, "embeddings")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("llmkit: request.Model is required")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	return doValue(ctx, c.cfg.retry, func() (*EmbeddingResponse, error) {
		return embedder.Embeddings(ctx, c.cfg.apiKey, req.Model, req)
	})
}

// SupportsEmbeddings reports whether Embed is available on this provider.
func (c *Client) SupportsEmbeddings() bool {
	_, ok := c.provider.(provider.Embedder)
	return ok
}

// ---------------------------------------------------------------- images

// GenerateImage creates images from a text prompt.
//
// Returns ErrUnsupported for providers with no image generation endpoint.
func (c *Client) GenerateImage(ctx context.Context, req *ImageRequest) (*ImageResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil request")
	}
	imager, ok := c.provider.(provider.ImageProvider)
	if !ok {
		return nil, unsupportedf(c.name, "image generation")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("llmkit: request.Model is required")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	resp, err := doValue(ctx, c.cfg.retry, func() (*ImageResponse, error) {
		return imager.GenerateImage(ctx, c.cfg.apiKey, req.Model, req)
	})
	return resp, translateUnsupported(err)
}

// EditImage edits uploaded images against a prompt.
//
// This call is never retried: req.Images and req.Mask carry one-shot io.Readers
// that a second attempt could not re-read. Handle failures yourself, re-opening
// the sources, if you need retries here.
//
// Returns ErrUnsupported for providers with no image editing endpoint.
func (c *Client) EditImage(ctx context.Context, req *ImageEditRequest) (*ImageResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil request")
	}
	imager, ok := c.provider.(provider.ImageProvider)
	if !ok {
		return nil, unsupportedf(c.name, "image editing")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("llmkit: request.Model is required")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	resp, err := imager.EditImage(ctx, c.cfg.apiKey, req.Model, req)
	return resp, translateUnsupported(err)
}

// SupportsImages reports whether GenerateImage / EditImage are available.
func (c *Client) SupportsImages() bool {
	_, ok := c.provider.(provider.ImageProvider)
	return ok
}

// ---------------------------------------------------------------- video

// CreateVideo submits an asynchronous video generation job. Video generation is
// always asynchronous: poll with GetVideo, or use WaitVideo to block until the
// job reaches a terminal state.
//
// Returns ErrUnsupported for providers with no video endpoint.
func (c *Client) CreateVideo(ctx context.Context, req *VideoRequest) (*VideoJob, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil request")
	}
	videoer, ok := c.provider.(provider.VideoProvider)
	if !ok {
		return nil, unsupportedf(c.name, "video generation")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("llmkit: request.Model is required")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	job, err := doValue(ctx, c.cfg.retry, func() (*VideoJob, error) {
		return videoer.CreateVideoJob(ctx, c.cfg.apiKey, req.Model, req)
	})
	return job, translateUnsupported(err)
}

// GetVideo refreshes a job's status from upstream.
func (c *Client) GetVideo(ctx context.Context, job *VideoJob) (*VideoJob, error) {
	if job == nil {
		return nil, fmt.Errorf("llmkit: nil job")
	}
	videoer, ok := c.provider.(provider.VideoProvider)
	if !ok {
		return nil, unsupportedf(c.name, "video generation")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	out, err := doValue(ctx, c.cfg.retry, func() (*VideoJob, error) {
		return videoer.GetVideoJob(ctx, c.cfg.apiKey, job)
	})
	return out, translateUnsupported(err)
}

// CancelVideo asks upstream to cancel a running job. Some vendors expose no
// cancel endpoint and return ErrUnsupported even though they support video.
func (c *Client) CancelVideo(ctx context.Context, job *VideoJob) (*VideoJob, error) {
	if job == nil {
		return nil, fmt.Errorf("llmkit: nil job")
	}
	videoer, ok := c.provider.(provider.VideoProvider)
	if !ok {
		return nil, unsupportedf(c.name, "video generation")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()

	out, err := videoer.CancelVideoJob(ctx, c.cfg.apiKey, job)
	return out, translateUnsupported(err)
}

// SupportsVideo reports whether the video methods are available.
func (c *Client) SupportsVideo() bool {
	_, ok := c.provider.(provider.VideoProvider)
	return ok
}

// WaitOptions tunes WaitVideo's polling.
type WaitOptions struct {
	// Interval between polls. Default 5s.
	Interval time.Duration
	// Timeout bounds the whole wait. Default 30m. Zero uses the default;
	// use a context deadline for an unbounded-but-cancellable wait.
	Timeout time.Duration
	// OnUpdate, when set, is called after each poll — useful for progress
	// reporting. It must not block for long.
	OnUpdate func(*VideoJob)
}

// WaitVideo polls until the job reaches a terminal state (completed, failed,
// cancelled, expired) and returns it.
//
// A job that finishes as failed is returned with a nil error — the call
// succeeded, the generation did not. Check job.Status and job.Error:
//
//	job, err := client.WaitVideo(ctx, job, nil)
//	if err != nil {
//		return err // polling itself broke
//	}
//	if job.Status != llmkit.VideoStatusCompleted {
//		return fmt.Errorf("generation failed: %v", job.Error)
//	}
func (c *Client) WaitVideo(ctx context.Context, job *VideoJob, opts *WaitOptions) (*VideoJob, error) {
	if job == nil {
		return nil, fmt.Errorf("llmkit: nil job")
	}
	interval := 5 * time.Second
	timeout := 30 * time.Minute
	var onUpdate func(*VideoJob)
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

	current := job
	if IsTerminalVideoStatus(current.Status) {
		return current, nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return current, fmt.Errorf("llmkit: waiting for video job %s: %w", current.ID, ctx.Err())
		case <-ticker.C:
		}

		updated, err := c.GetVideo(ctx, current)
		if err != nil {
			// A transient poll failure shouldn't abandon a job that may still
			// be running; keep polling until the deadline. Terminal errors
			// (auth, not found) will keep failing and surface at timeout.
			if IsRetryable(err) || IsNotFound(err) {
				continue
			}
			return current, err
		}
		current = updated
		if onUpdate != nil {
			onUpdate(current)
		}
		if IsTerminalVideoStatus(current.Status) {
			return current, nil
		}
	}
}
