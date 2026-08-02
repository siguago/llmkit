package openai

import (
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
	responsesapi "github.com/siguago/llmkit/protocol/responses"
	"github.com/siguago/llmkit/provider"
)

// DefaultResponsesMaxFrameBytes is the zero-config ceiling on one OpenAI
// Responses SSE frame. Image-generation events carry base64 image data in a
// single frame, so Responses needs a larger default than ordinary token streams.
const DefaultResponsesMaxFrameBytes = 32 << 20

// CreateResponseStream calls POST /v1/responses with stream=true. The caller's
// request is copied so selecting the streaming wire form never mutates it.
func (p *Provider) CreateResponseStream(ctx context.Context, apiKey string, req *responsesapi.CreateRequest) (responsesapi.Stream, error) {
	if err := validateCreateResponseRequest(req); err != nil {
		return nil, err
	}
	wire := *req
	stream := true
	wire.Stream = &stream
	body, err := json.Marshal(&wire)
	if err != nil {
		return nil, fmt.Errorf("openai responses: encode streaming create request: %w", err)
	}

	resp, err := p.doResponsesRequest(ctx, apiKey, http.MethodPost, p.responsesURL, body, true)
	if err != nil {
		return nil, err
	}
	return newResponsesStream(ctx, resp), nil
}

type responsesStream struct {
	body        io.ReadCloser
	decoder     *sse.Decoder
	diagnostics provider.StreamDiagnostics
	accumulator *responsesapi.Accumulator
	requestID   string
	maxBytes    int

	terminal  atomic.Bool
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error

	// pendingErr belongs to the single Recv consumer. Stream implementations,
	// like io.Reader, do not support concurrent Recv calls.
	pendingErr error
}

func newResponsesStream(ctx context.Context, resp *http.Response) *responsesStream {
	policy := provider.StreamPolicyFrom(ctx)
	maxBytes := policy.MaxFrameBytes
	if maxBytes <= 0 {
		maxBytes = DefaultResponsesMaxFrameBytes
	}
	requestID := provider.RequestIDFromHeader(resp.Header)
	accumulator := new(responsesapi.Accumulator)
	accumulator.SetRequestID(requestID)
	return &responsesStream{
		body:        resp.Body,
		decoder:     sse.NewDecoder(resp.Body, maxBytes),
		diagnostics: provider.NewStreamDiagnostics(ctx, "openai responses"),
		accumulator: accumulator,
		requestID:   requestID,
		maxBytes:    maxBytes,
	}
}

func (stream *responsesStream) Recv() (*responsesapi.Event, error) {
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
			_ = stream.Close()
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("openai responses stream: %w", io.ErrUnexpectedEOF)
			}
			if errors.Is(err, sse.ErrEventTooLarge) {
				return nil, fmt.Errorf("openai responses stream: event exceeds %d bytes; raise it with llmkit.WithMaxStreamFrameBytes: %w", stream.maxBytes, err)
			}
			return nil, fmt.Errorf("openai responses stream: %w", err)
		}

		data := string(frame.Data)
		switch provider.ClassifyFrame(data) {
		case provider.FrameSkip:
			continue
		case provider.FrameDone:
			if malformedErr := stream.diagnostics.Malformed("event", errors.New("received [DONE] before a Responses terminal event"), data); malformedErr != nil {
				_ = stream.Close()
				return nil, malformedErr
			}
			continue
		}

		event, err := responsesapi.ParseEvent(frame.Data)
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

		if event.IsTerminal() {
			stream.terminal.Store(true)
			_ = stream.Close()
			if event.Type == responsesapi.EventTypeError && event.Error != nil {
				streamError := *event.Error
				if event.Error.Param != nil {
					param := *event.Error.Param
					streamError.Param = &param
				}
				stream.pendingErr = provider.MarkUnsafeToReplay(provider.WithErrorMetadata(
					&streamError,
					streamError.Code,
					responsesStreamErrorCategory(streamError.Code),
				))
			}
		}
		return event, nil
	}
}

func (stream *responsesStream) Close() error {
	stream.closed.Store(true)
	stream.closeOnce.Do(func() {
		if stream.body != nil {
			stream.closeErr = stream.body.Close()
		}
	})
	return stream.closeErr
}

func (stream *responsesStream) RequestID() string { return stream.requestID }

func (stream *responsesStream) FinalResponse() *responsesapi.Response {
	return stream.accumulator.FinalResponse()
}

func responsesStreamErrorCategory(code string) provider.ErrorCategory {
	code = strings.ToLower(strings.TrimSpace(code))
	switch {
	case strings.Contains(code, "auth"), strings.Contains(code, "permission"), strings.Contains(code, "api_key"):
		return provider.ErrorCategoryAuth
	case strings.Contains(code, "rate_limit"), strings.Contains(code, "quota"):
		return provider.ErrorCategoryRateLimit
	case strings.Contains(code, "not_found"):
		return provider.ErrorCategoryNotFound
	case strings.Contains(code, "invalid"), strings.Contains(code, "bad_request"):
		return provider.ErrorCategoryInvalidRequest
	case strings.Contains(code, "server"), strings.Contains(code, "overload"), strings.Contains(code, "timeout"):
		return provider.ErrorCategoryServer
	default:
		return ""
	}
}
