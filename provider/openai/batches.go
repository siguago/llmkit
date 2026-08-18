package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/siguago/llmkit/protocol/openaibatch"
	"github.com/siguago/llmkit/provider"
)

var (
	_ provider.BatchCreator   = (*Provider)(nil)
	_ provider.BatchRetriever = (*Provider)(nil)
	_ provider.BatchLister    = (*Provider)(nil)
	_ provider.BatchCanceller = (*Provider)(nil)
)

// CreateBatch calls POST /v1/batches. The input file must already exist
// (upload it with purpose "batch" first).
func (p *Provider) CreateBatch(ctx context.Context, apiKey string, req *openaibatch.CreateRequest) (*openaibatch.Batch, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("openai batch: encode create request: %w", err)
	}
	resp, err := p.doBatchRequest(ctx, apiKey, http.MethodPost, p.batchesURL, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var batch openaibatch.Batch
	if err := decodeBatchJSON(resp.Body, &batch, "create response"); err != nil {
		return nil, err
	}
	batch.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &batch, nil
}

// RetrieveBatch calls GET /v1/batches/{id}.
func (p *Provider) RetrieveBatch(ctx context.Context, apiKey, batchID string) (*openaibatch.Batch, error) {
	endpoint, err := p.batchResourceURL(batchID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doBatchRequest(ctx, apiKey, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var batch openaibatch.Batch
	if err := decodeBatchJSON(resp.Body, &batch, "retrieve response"); err != nil {
		return nil, err
	}
	batch.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &batch, nil
}

// ListBatches calls GET /v1/batches. A nil request lists with the upstream's
// own defaults.
func (p *Provider) ListBatches(ctx context.Context, apiKey string, req *openaibatch.ListRequest) (*openaibatch.BatchList, error) {
	endpoint := p.batchesURL
	if req != nil {
		query := make(url.Values)
		if req.After != "" {
			query.Set("after", req.After)
		}
		if req.Limit > 0 {
			query.Set("limit", strconv.Itoa(req.Limit))
		}
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
	}
	resp, err := p.doBatchRequest(ctx, apiKey, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list openaibatch.BatchList
	if err := decodeBatchJSON(resp.Body, &list, "list response"); err != nil {
		return nil, err
	}
	list.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &list, nil
}

// CancelBatch calls POST /v1/batches/{id}/cancel. The batch stays
// "cancelling" for up to ten minutes before settling on "cancelled"; partial
// results still land in the output file.
func (p *Provider) CancelBatch(ctx context.Context, apiKey, batchID string) (*openaibatch.Batch, error) {
	endpoint, err := p.batchResourceURL(batchID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doBatchRequest(ctx, apiKey, http.MethodPost, endpoint+"/cancel", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var batch openaibatch.Batch
	if err := decodeBatchJSON(resp.Body, &batch, "cancel response"); err != nil {
		return nil, err
	}
	batch.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &batch, nil
}

func (p *Provider) batchResourceURL(batchID string) (string, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return "", fmt.Errorf("openai batch: batch ID is required")
	}
	return p.batchesURL + "/" + url.PathEscape(batchID), nil
}

func (p *Provider) doBatchRequest(ctx context.Context, apiKey, method, endpoint string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
	if body != nil {
		httpReq.Header.Set("content-type", "application/json")
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponsesErrorBodyBytes+1))
	if len(payload) > maxResponsesErrorBodyBytes {
		payload = payload[:maxResponsesErrorBodyBytes]
	}
	apiErr := decodeBatchAPIError(resp, payload)
	if readErr != nil {
		return nil, errors.Join(apiErr, fmt.Errorf("openai batch: read error response: %w", readErr))
	}
	return nil, apiErr
}

func decodeBatchAPIError(resp *http.Response, payload []byte) error {
	providerErr := provider.NewProviderErrorFromResponse(resp, "openai batch", payload)
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return provider.WithErrorMetadata(providerErr, "", responsesErrorCategory(resp.StatusCode, ""))
	}
	code := envelope.Error.Code
	if code == "" {
		code = envelope.Error.Type
	}
	return provider.WithErrorMetadata(providerErr, code, responsesErrorCategory(resp.StatusCode, envelope.Error.Type))
}

func decodeBatchJSON(reader io.Reader, target any, what string) error {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("openai batch: read %s: %w", what, err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("openai batch: decode %s: %w", what, err)
	}
	return nil
}
