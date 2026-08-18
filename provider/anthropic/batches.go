package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/provider"
)

var (
	_ provider.AnthropicMessageBatchCreator       = (*Provider)(nil)
	_ provider.AnthropicMessageBatchRetriever     = (*Provider)(nil)
	_ provider.AnthropicMessageBatchLister        = (*Provider)(nil)
	_ provider.AnthropicMessageBatchCanceller     = (*Provider)(nil)
	_ provider.AnthropicMessageBatchDeleter       = (*Provider)(nil)
	_ provider.AnthropicMessageBatchResultsReader = (*Provider)(nil)
)

// CreateAnthropicMessageBatch calls POST /v1/messages/batches with inline
// requests. Message Batches are GA: the stable anthropic-version suffices, no
// beta header is required.
func (p *Provider) CreateAnthropicMessageBatch(
	ctx context.Context,
	apiKey string,
	req *anthropicapi.MessageBatchCreateRequest,
	opts ...anthropicapi.RequestOption,
) (*anthropicapi.MessageBatch, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode message batch create request: %w", err)
	}
	resp, err := p.doNativeRequest(ctx, apiKey, p.batchesURL, body, false, opts...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeMessageBatch(resp, "create")
}

// RetrieveAnthropicMessageBatch calls GET /v1/messages/batches/{id}; the
// endpoint is idempotent and intended for polling.
func (p *Provider) RetrieveAnthropicMessageBatch(
	ctx context.Context,
	apiKey, batchID string,
	opts ...anthropicapi.RequestOption,
) (*anthropicapi.MessageBatch, error) {
	endpoint, err := p.messageBatchResourceURL(batchID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doMessageBatchRequest(ctx, apiKey, http.MethodGet, endpoint, nil, opts...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeMessageBatch(resp, "retrieve")
}

// ListAnthropicMessageBatches calls GET /v1/messages/batches. A nil request
// lists with the upstream's own defaults.
func (p *Provider) ListAnthropicMessageBatches(
	ctx context.Context,
	apiKey string,
	req *anthropicapi.MessageBatchListRequest,
	opts ...anthropicapi.RequestOption,
) (*anthropicapi.MessageBatchList, error) {
	endpoint := p.batchesURL
	if req != nil {
		query := make(url.Values)
		if req.AfterID != "" {
			query.Set("after_id", req.AfterID)
		}
		if req.BeforeID != "" {
			query.Set("before_id", req.BeforeID)
		}
		if req.Limit > 0 {
			query.Set("limit", strconv.Itoa(req.Limit))
		}
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
	}
	resp, err := p.doMessageBatchRequest(ctx, apiKey, http.MethodGet, endpoint, nil, opts...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list anthropicapi.MessageBatchList
	if err := decodeMessageBatchJSON(resp.Body, &list, "list response"); err != nil {
		return nil, err
	}
	list.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &list, nil
}

// CancelAnthropicMessageBatch calls POST /v1/messages/batches/{id}/cancel.
// Cancellation is asynchronous ("canceling" until in-flight requests settle),
// and non-interruptible requests may still complete and bill.
func (p *Provider) CancelAnthropicMessageBatch(
	ctx context.Context,
	apiKey, batchID string,
	opts ...anthropicapi.RequestOption,
) (*anthropicapi.MessageBatch, error) {
	endpoint, err := p.messageBatchResourceURL(batchID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doMessageBatchRequest(ctx, apiKey, http.MethodPost, endpoint+"/cancel", nil, opts...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeMessageBatch(resp, "cancel")
}

// DeleteAnthropicMessageBatch calls DELETE /v1/messages/batches/{id}. Only
// ended batches can be deleted; cancel an in-progress batch first.
func (p *Provider) DeleteAnthropicMessageBatch(
	ctx context.Context,
	apiKey, batchID string,
	opts ...anthropicapi.RequestOption,
) (*anthropicapi.DeletedMessageBatch, error) {
	endpoint, err := p.messageBatchResourceURL(batchID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doMessageBatchRequest(ctx, apiKey, http.MethodDelete, endpoint, nil, opts...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var deleted anthropicapi.DeletedMessageBatch
	if err := decodeMessageBatchJSON(resp.Body, &deleted, "delete response"); err != nil {
		return nil, err
	}
	deleted.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &deleted, nil
}

// ReadAnthropicMessageBatchResults streams GET /v1/messages/batches/{id}/results
// as decoded JSONL lines. The path is always built from the configured base
// URL — the results_url field in the batch object is treated as an
// availability signal, never as the request target. The returned reader wraps
// a live body on the stream client (no absolute timeout): Close it when done,
// and bound its lifetime with ctx.
func (p *Provider) ReadAnthropicMessageBatchResults(
	ctx context.Context,
	apiKey, batchID string,
	opts ...anthropicapi.RequestOption,
) (*anthropicapi.MessageBatchResultsReader, error) {
	endpoint, err := p.messageBatchResourceURL(batchID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doAnthropicRequest(ctx, apiKey, endpoint+"/results", nil, anthropicRequestBehavior{
		method:            http.MethodGet,
		useStreamClient:   true,
		decodeNativeError: true,
		maxErrorBodyBytes: maxNativeErrorBodyBytes,
	}, opts...)
	if err != nil {
		return nil, err
	}
	maxLine := int64(provider.StreamPolicyFrom(ctx).MaxFrameBytes)
	return anthropicapi.NewMessageBatchResultsReader(resp.Body, maxLine), nil
}

func (p *Provider) messageBatchResourceURL(batchID string) (string, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return "", fmt.Errorf("anthropic: message batch ID is required")
	}
	return p.batchesURL + "/" + url.PathEscape(batchID), nil
}

// doMessageBatchRequest is doNativeRequest for the non-POST resource routes.
func (p *Provider) doMessageBatchRequest(
	ctx context.Context,
	apiKey, method, endpoint string,
	body []byte,
	opts ...anthropicapi.RequestOption,
) (*http.Response, error) {
	return p.doAnthropicRequest(ctx, apiKey, endpoint, body, anthropicRequestBehavior{
		method:            method,
		decodeNativeError: true,
		maxErrorBodyBytes: maxNativeErrorBodyBytes,
	}, opts...)
}

func decodeMessageBatch(resp *http.Response, what string) (*anthropicapi.MessageBatch, error) {
	var batch anthropicapi.MessageBatch
	if err := decodeMessageBatchJSON(resp.Body, &batch, what+" response"); err != nil {
		return nil, err
	}
	batch.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &batch, nil
}

func decodeMessageBatchJSON(reader io.Reader, target any, what string) error {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("anthropic: read message batch %s: %w", what, err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("anthropic: decode message batch %s: %w", what, err)
	}
	return nil
}
