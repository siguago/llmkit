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

	responsesapi "github.com/siguago/llmkit/protocol/responses"
	"github.com/siguago/llmkit/provider"
)

const maxResponsesErrorBodyBytes = 64 << 10

var (
	_ provider.ResponsesCreator         = (*Provider)(nil)
	_ provider.ResponsesStreamer        = (*Provider)(nil)
	_ provider.ResponsesRetriever       = (*Provider)(nil)
	_ provider.ResponsesCanceller       = (*Provider)(nil)
	_ provider.ResponsesDeleter         = (*Provider)(nil)
	_ provider.ResponsesInputItemLister = (*Provider)(nil)
	_ provider.ResponsesTokenCounter    = (*Provider)(nil)
)

// CreateResponse calls POST /v1/responses and returns the native Response
// object, including its heterogeneous output items and lifecycle status.
func (p *Provider) CreateResponse(ctx context.Context, apiKey string, req *responsesapi.CreateRequest) (*responsesapi.Response, error) {
	if err := validateCreateResponseRequest(req); err != nil {
		return nil, err
	}
	wire := *req
	stream := false
	wire.Stream = &stream
	body, err := json.Marshal(&wire)
	if err != nil {
		return nil, fmt.Errorf("openai responses: encode create request: %w", err)
	}

	resp, err := p.doResponsesRequest(ctx, apiKey, http.MethodPost, p.responsesURL, body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result responsesapi.Response
	if err := decodeResponsesJSON(resp.Body, &result, "create response"); err != nil {
		return nil, err
	}
	result.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &result, nil
}

// RetrieveResponse retrieves a stored or background Response by ID.
func (p *Provider) RetrieveResponse(
	ctx context.Context,
	apiKey, responseID string,
	opts *responsesapi.RetrieveOptions,
) (*responsesapi.Response, error) {
	endpoint, err := p.responseResourceURL(responseID)
	if err != nil {
		return nil, err
	}
	if opts != nil && len(opts.Include) > 0 {
		query := make(url.Values)
		addQueryValues(query, "include[]", opts.Include)
		endpoint += "?" + query.Encode()
	}
	resp, err := p.doResponsesRequest(ctx, apiKey, http.MethodGet, endpoint, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result responsesapi.Response
	if err := decodeResponsesJSON(resp.Body, &result, "retrieve response"); err != nil {
		return nil, err
	}
	result.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &result, nil
}

// CancelResponse cancels a Response created with background=true.
func (p *Provider) CancelResponse(ctx context.Context, apiKey, responseID string) (*responsesapi.Response, error) {
	endpoint, err := p.responseResourceURL(responseID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doResponsesRequest(ctx, apiKey, http.MethodPost, endpoint+"/cancel", nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result responsesapi.Response
	if err := decodeResponsesJSON(resp.Body, &result, "cancel response"); err != nil {
		return nil, err
	}
	result.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &result, nil
}

// DeleteResponse deletes a stored Response.
func (p *Provider) DeleteResponse(ctx context.Context, apiKey, responseID string) (*responsesapi.DeletedResponse, error) {
	endpoint, err := p.responseResourceURL(responseID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doResponsesRequest(ctx, apiKey, http.MethodDelete, endpoint, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai responses: read delete response: %w", err)
	}
	requestID := provider.RequestIDFromHeader(resp.Header)
	// The current OpenAI endpoint may answer 204 with no representation. Some
	// older deployments and relays still return a deletion object, so support
	// both wire forms and synthesize the same useful result for an empty body.
	if len(bytes.TrimSpace(payload)) == 0 {
		return &responsesapi.DeletedResponse{
			ID: responseID, Object: "response.deleted", Deleted: true, RequestID: requestID,
		}, nil
	}
	var result responsesapi.DeletedResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("openai responses: decode delete response: %w", err)
	}
	result.RequestID = requestID
	return &result, nil
}

// ListResponseInputItems lists the input items associated with a Response.
func (p *Provider) ListResponseInputItems(
	ctx context.Context,
	apiKey, responseID string,
	opts *responsesapi.ListInputItemsOptions,
) (*responsesapi.InputItemList, error) {
	endpoint, err := p.responseResourceURL(responseID)
	if err != nil {
		return nil, err
	}
	endpoint += "/input_items"
	query := make(url.Values)
	if opts != nil {
		if opts.Limit != nil {
			if *opts.Limit < 1 || *opts.Limit > 100 {
				return nil, fmt.Errorf("openai responses: input-items limit must be between 1 and 100")
			}
			query.Set("limit", strconv.Itoa(*opts.Limit))
		}
		if opts.Order != "" {
			if opts.Order != "asc" && opts.Order != "desc" {
				return nil, fmt.Errorf("openai responses: input-items order must be asc or desc")
			}
			query.Set("order", opts.Order)
		}
		if opts.After != "" {
			query.Set("after", opts.After)
		}
		addQueryValues(query, "include[]", opts.Include)
	}
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	resp, err := p.doResponsesRequest(ctx, apiKey, http.MethodGet, endpoint, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result responsesapi.InputItemList
	if err := decodeResponsesJSON(resp.Body, &result, "list response input items"); err != nil {
		return nil, err
	}
	result.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &result, nil
}

// CountResponseInputTokens calls POST /v1/responses/input_tokens.
func (p *Provider) CountResponseInputTokens(
	ctx context.Context,
	apiKey string,
	req *responsesapi.TokenCountRequest,
) (*responsesapi.TokenCountResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("openai responses: nil token count request")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("openai responses: encode token count request: %w", err)
	}
	resp, err := p.doResponsesRequest(ctx, apiKey, http.MethodPost, p.responseInputTokensURL, body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result responsesapi.TokenCountResponse
	if err := decodeResponsesJSON(resp.Body, &result, "count response input tokens"); err != nil {
		return nil, err
	}
	result.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &result, nil
}

func validateCreateResponseRequest(req *responsesapi.CreateRequest) error {
	if req == nil {
		return fmt.Errorf("openai responses: nil create request")
	}
	if err := req.Validate(); err != nil {
		return err
	}
	return nil
}

func (p *Provider) responseResourceURL(responseID string) (string, error) {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return "", fmt.Errorf("openai responses: response ID is required")
	}
	return p.responsesURL + "/" + url.PathEscape(responseID), nil
}

func addQueryValues(query url.Values, key string, values []string) {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			query.Add(key, value)
		}
	}
}

func (p *Provider) doResponsesRequest(
	ctx context.Context,
	apiKey, method, endpoint string,
	body []byte,
	stream bool,
) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
	httpReq.Header.Set("content-type", "application/json")
	if stream {
		httpReq.Header.Set("accept", "text/event-stream")
	}
	client := p.client
	if stream {
		client = p.streamClient
	}
	resp, err := client.Do(httpReq)
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
	apiErr := decodeResponsesAPIError(resp, payload)
	if readErr != nil {
		return nil, errors.Join(apiErr, fmt.Errorf("openai responses: read error response: %w", readErr))
	}
	return nil, apiErr
}

func decodeResponsesAPIError(resp *http.Response, payload []byte) error {
	providerErr := provider.NewProviderErrorFromResponse(resp, "openai responses", payload)
	var envelope struct {
		Error responsesapi.Error `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return provider.WithErrorMetadata(
			providerErr, "", responsesErrorCategory(resp.StatusCode, ""),
		)
	}
	code := envelope.Error.Code
	if code == "" {
		code = envelope.Error.Type
	}
	return provider.WithErrorMetadata(providerErr, code, responsesErrorCategory(resp.StatusCode, envelope.Error.Type))
}

func responsesErrorCategory(status int, errorType string) provider.ErrorCategory {
	if status != 0 {
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return provider.ErrorCategoryAuth
		case status == http.StatusTooManyRequests:
			return provider.ErrorCategoryRateLimit
		case status == http.StatusNotFound:
			return provider.ErrorCategoryNotFound
		case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity:
			return provider.ErrorCategoryInvalidRequest
		case status >= 500 && status <= 599:
			return provider.ErrorCategoryServer
		default:
			// A delivered HTTP status is stronger evidence than a conflicting
			// body type. In particular, never let a 408/409/402 body claim the
			// replay-safe rate-limit category reserved for an explicit 429.
			return ""
		}
	}
	switch errorType {
	case "authentication_error", "permission_error":
		return provider.ErrorCategoryAuth
	case "rate_limit_error":
		return provider.ErrorCategoryRateLimit
	case "invalid_request_error":
		return provider.ErrorCategoryInvalidRequest
	case "not_found_error":
		return provider.ErrorCategoryNotFound
	case "server_error", "api_error":
		return provider.ErrorCategoryServer
	default:
		return ""
	}
}

func decodeResponsesJSON(reader io.Reader, target any, what string) error {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("openai responses: read %s: %w", what, err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("openai responses: decode %s: %w", what, err)
	}
	return nil
}
