package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/siguago/llmkit/internal/sse"
	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/provider"
)

const (
	maxLegacyErrorBodyBytes = 10 << 10
	maxNativeErrorBodyBytes = 64 << 10
)

var (
	_ provider.AnthropicMessagesCreator  = (*Provider)(nil)
	_ provider.AnthropicMessagesStreamer = (*Provider)(nil)
	_ provider.AnthropicTokenCounter     = (*Provider)(nil)
)

// CreateAnthropicMessage calls Anthropic's native Messages endpoint without
// translating the request or response through Chat Completions types.
func (p *Provider) CreateAnthropicMessage(
	ctx context.Context,
	apiKey string,
	req *anthropicapi.MessageRequest,
	opts ...anthropicapi.RequestOption,
) (*anthropicapi.MessageResponse, error) {
	if err := validateNativeMessageRequest(req); err != nil {
		return nil, err
	}

	wire := *req
	stream := false
	wire.Stream = &stream
	body, err := json.Marshal(&wire)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode message request: %w", err)
	}

	resp, err := p.doNativeRequest(ctx, apiKey, p.messagesURL, body, false, opts...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: read message response: %w", err)
	}
	var message anthropicapi.MessageResponse
	if err := json.Unmarshal(payload, &message); err != nil {
		return nil, fmt.Errorf("anthropic: decode message response: %w", err)
	}
	message.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &message, nil
}

// CreateAnthropicMessageStream starts a native Messages SSE stream. Recv
// returns Anthropic events, not ChatCompletionChunk values.
func (p *Provider) CreateAnthropicMessageStream(
	ctx context.Context,
	apiKey string,
	req *anthropicapi.MessageRequest,
	opts ...anthropicapi.RequestOption,
) (anthropicapi.Stream, error) {
	if err := validateNativeMessageRequest(req); err != nil {
		return nil, err
	}

	wire := *req
	stream := true
	wire.Stream = &stream
	body, err := json.Marshal(&wire)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode streaming message request: %w", err)
	}

	resp, err := p.doNativeRequest(ctx, apiKey, p.messagesURL, body, true, opts...)
	if err != nil {
		return nil, err
	}
	return newNativeMessageStream(ctx, resp), nil
}

// CountAnthropicMessageTokens asks Anthropic to count the complete native
// request context, including tools and multimodal blocks.
func (p *Provider) CountAnthropicMessageTokens(
	ctx context.Context,
	apiKey string,
	req *anthropicapi.TokenCountRequest,
	opts ...anthropicapi.RequestOption,
) (*anthropicapi.TokenCountResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("anthropic: nil token count request")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("anthropic: token count request model is required")
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("anthropic: token count request messages are required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode token count request: %w", err)
	}

	resp, err := p.doNativeRequest(ctx, apiKey, p.tokenCountURL, body, false, opts...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: read token count response: %w", err)
	}
	var count anthropicapi.TokenCountResponse
	if err := json.Unmarshal(payload, &count); err != nil {
		return nil, fmt.Errorf("anthropic: decode token count response: %w", err)
	}
	count.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &count, nil
}

func validateNativeMessageRequest(req *anthropicapi.MessageRequest) error {
	if req == nil {
		return fmt.Errorf("anthropic: nil message request")
	}
	if strings.TrimSpace(req.Model) == "" {
		return fmt.Errorf("anthropic: message request model is required")
	}
	if req.MaxTokens < 0 {
		return fmt.Errorf("anthropic: max_tokens must be non-negative")
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("anthropic: message request messages are required")
	}
	return nil
}

func (p *Provider) doNativeRequest(
	ctx context.Context,
	apiKey string,
	endpoint string,
	body []byte,
	stream bool,
	opts ...anthropicapi.RequestOption,
) (*http.Response, error) {
	return p.doAnthropicRequest(ctx, apiKey, endpoint, body, anthropicRequestBehavior{
		useStreamClient:   stream,
		acceptEventStream: stream,
		decodeNativeError: true,
		maxErrorBodyBytes: maxNativeErrorBodyBytes,
	}, opts...)
}

// doLegacyChatRequest preserves the Chat adapter's established wire and error
// contract while reusing the native transport setup. In particular, legacy
// streams never sent an explicit Accept header, and legacy HTTP failures were
// returned as a direct *provider.ProviderError.
func (p *Provider) doLegacyChatRequest(
	ctx context.Context,
	apiKey string,
	endpoint string,
	body []byte,
	stream bool,
	opts ...anthropicapi.RequestOption,
) (*http.Response, error) {
	return p.doAnthropicRequest(ctx, apiKey, endpoint, body, anthropicRequestBehavior{
		useStreamClient:   stream,
		maxErrorBodyBytes: maxLegacyErrorBodyBytes,
	}, opts...)
}

type anthropicRequestBehavior struct {
	useStreamClient   bool
	acceptEventStream bool
	decodeNativeError bool
	maxErrorBodyBytes int64
}

func (p *Provider) doAnthropicRequest(
	ctx context.Context,
	apiKey string,
	endpoint string,
	body []byte,
	behavior anthropicRequestBehavior,
	opts ...anthropicapi.RequestOption,
) (*http.Response, error) {
	requestOptions := anthropicapi.ApplyRequestOptions(opts...)
	requestOptions.Version = strings.TrimSpace(requestOptions.Version)
	if requestOptions.Version == "" {
		return nil, fmt.Errorf("anthropic: anthropic-version must not be empty")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setNativeHeaders(httpReq, apiKey, requestOptions, behavior.acceptEventStream)

	client := p.client
	if behavior.useStreamClient {
		client = p.streamClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	maxErrorBodyBytes := behavior.maxErrorBodyBytes
	if maxErrorBodyBytes <= 0 {
		maxErrorBodyBytes = maxNativeErrorBodyBytes
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))

	if !behavior.decodeNativeError {
		// Preserve the legacy Chat contract: callers may use a direct concrete
		// assertion rather than errors.As, and the old implementation ignored a
		// secondary body read failure after receiving the HTTP response.
		return nil, provider.NewProviderErrorFromResponse(resp, "anthropic", payload)
	}

	apiErr := decodeNativeAPIError(resp, payload)
	if readErr != nil {
		// Receiving a status and headers is useful evidence even when reading the
		// body then fails. Join both causes so status, Retry-After, request ID,
		// provider metadata, and the underlying I/O error all remain inspectable.
		return nil, errors.Join(apiErr, fmt.Errorf("anthropic: read error response: %w", readErr))
	}
	return nil, apiErr
}

func setNativeHeaders(req *http.Request, apiKey string, opts anthropicapi.RequestOptions, acceptEventStream bool) {
	provider.SetKeyHeader(req.Header, "x-api-key", apiKey)
	req.Header.Set("anthropic-version", opts.Version)
	req.Header.Set("content-type", "application/json")
	if acceptEventStream {
		req.Header.Set("accept", "text/event-stream")
	}
	if betas := normalizedBetas(opts.Betas); len(betas) > 0 {
		req.Header.Set("anthropic-beta", strings.Join(betas, ","))
	}
}

func normalizedBetas(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		for _, candidate := range strings.Split(value, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			result = append(result, candidate)
		}
	}
	return result
}

func decodeNativeAPIError(resp *http.Response, payload []byte) error {
	providerErr := provider.NewProviderErrorFromResponse(resp, "anthropic", payload)
	var envelope struct {
		Type      string                `json:"type"`
		Error     anthropicapi.APIError `json:"error"`
		RequestID string                `json:"request_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return provider.WithErrorMetadata(providerErr, "", anthropicErrorCategory(resp.StatusCode, ""))
	}
	if providerErr.RequestID == "" {
		providerErr.RequestID = envelope.RequestID
	}
	return provider.WithErrorMetadata(providerErr, envelope.Error.Type, anthropicErrorCategory(resp.StatusCode, envelope.Error.Type))
}

func anthropicErrorCategory(status int, code string) provider.ErrorCategory {
	// An actual HTTP status is the authoritative classification. Never let a
	// contradictory body code turn a delivered 5xx request into a replay-safe
	// rate limit (or hide a real 429). A zero status denotes an in-stream error,
	// where the provider code is the only available signal.
	if status != 0 {
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return provider.ErrorCategoryAuth
		case status == http.StatusTooManyRequests:
			return provider.ErrorCategoryRateLimit
		case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity:
			return provider.ErrorCategoryInvalidRequest
		case status == http.StatusNotFound:
			return provider.ErrorCategoryNotFound
		case status >= http.StatusInternalServerError && status <= 599:
			return provider.ErrorCategoryServer
		default:
			return ""
		}
	}

	switch code {
	case "authentication_error", "permission_error":
		return provider.ErrorCategoryAuth
	case "rate_limit_error":
		return provider.ErrorCategoryRateLimit
	case "invalid_request_error", "request_too_large":
		return provider.ErrorCategoryInvalidRequest
	case "not_found_error":
		return provider.ErrorCategoryNotFound
	case "api_error", "timeout_error", "overloaded_error":
		return provider.ErrorCategoryServer
	default:
		return ""
	}
}

func cloneNativeAPIError(source anthropicapi.APIError) anthropicapi.APIError {
	cloned := source
	if source.ExtraFields == nil {
		return cloned
	}
	cloned.ExtraFields = make(anthropicapi.ExtraFields, len(source.ExtraFields))
	for key, value := range source.ExtraFields {
		cloned.ExtraFields[key] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

type nativeMessageStream struct {
	body        io.ReadCloser
	decoder     *sse.Decoder
	diagnostics provider.StreamDiagnostics
	accumulator *anthropicapi.Accumulator
	requestID   string
	maxBytes    int

	terminal   atomic.Bool
	closed     atomic.Bool
	closeOnce  sync.Once
	closeErr   error
	pendingErr error
}

func newNativeMessageStream(ctx context.Context, resp *http.Response) *nativeMessageStream {
	policy := provider.StreamPolicyFrom(ctx)
	maxBytes := policy.MaxFrameBytes
	if maxBytes <= 0 {
		maxBytes = provider.DefaultMaxFrameBytes
	}
	requestID := provider.RequestIDFromHeader(resp.Header)
	accumulator := anthropicapi.NewAccumulator()
	accumulator.SetRequestID(requestID)
	return &nativeMessageStream{
		body:        resp.Body,
		decoder:     sse.NewDecoder(resp.Body, maxBytes),
		diagnostics: provider.NewStreamDiagnostics(ctx, "anthropic"),
		accumulator: accumulator,
		requestID:   requestID,
		maxBytes:    maxBytes,
	}
}

func (stream *nativeMessageStream) Recv() (*anthropicapi.Event, error) {
	if stream.pendingErr != nil {
		err := stream.pendingErr
		stream.pendingErr = nil
		return nil, err
	}
	if stream.terminal.Load() || stream.closed.Load() {
		return nil, io.EOF
	}

	for {
		frame, err := stream.decoder.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = stream.Close()
				return nil, fmt.Errorf("anthropic stream: %w", io.ErrUnexpectedEOF)
			}
			_ = stream.Close()
			if errors.Is(err, sse.ErrEventTooLarge) {
				return nil, fmt.Errorf("anthropic stream: event exceeds %d bytes; raise it with llmkit.WithMaxStreamFrameBytes: %w", stream.maxBytes, err)
			}
			return nil, fmt.Errorf("anthropic stream: %w", err)
		}

		data := string(frame.Data)
		switch provider.ClassifyFrame(data) {
		case provider.FrameSkip:
			continue
		case provider.FrameDone:
			if malformedErr := stream.diagnostics.Malformed("event", fmt.Errorf("received [DONE] before message_stop"), data); malformedErr != nil {
				_ = stream.Close()
				return nil, malformedErr
			}
			continue
		}

		event, err := anthropicapi.ParseEvent(frame.Data)
		if err != nil {
			if malformedErr := stream.diagnostics.Malformed("event", err, data); malformedErr != nil {
				_ = stream.Close()
				return nil, malformedErr
			}
			continue
		}
		if frame.Name != "" && frame.Name != string(event.Type) {
			mismatch := fmt.Errorf("SSE event name %q does not match JSON type %q", frame.Name, event.Type)
			if malformedErr := stream.diagnostics.Malformed("event name", mismatch, data); malformedErr != nil {
				_ = stream.Close()
				return nil, malformedErr
			}
		}
		if err := stream.accumulator.Add(event); err != nil {
			_ = stream.Close()
			return nil, err
		}

		if event.Terminal() {
			stream.terminal.Store(true)
			_ = stream.Close()
			if event.Error != nil {
				apiErr := cloneNativeAPIError(event.Error.Error)
				stream.pendingErr = provider.MarkUnsafeToReplay(provider.WithErrorMetadata(
					&apiErr,
					apiErr.Type,
					anthropicErrorCategory(0, apiErr.Type),
				))
			}
		}
		return event, nil
	}
}

func (stream *nativeMessageStream) Close() error {
	stream.closed.Store(true)
	stream.closeOnce.Do(func() {
		if stream.body != nil {
			stream.closeErr = stream.body.Close()
		}
	})
	return stream.closeErr
}

func (stream *nativeMessageStream) RequestID() string { return stream.requestID }

func (stream *nativeMessageStream) FinalMessage() *anthropicapi.MessageResponse {
	message := stream.accumulator.Message()
	if message != nil {
		message.RequestID = stream.requestID
	}
	return message
}
