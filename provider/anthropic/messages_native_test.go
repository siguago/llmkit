package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/siguago/llmkit/internal/sse"
	protocol "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/provider"
)

type nativeCapturedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

func captureNativeRequest(r *http.Request) (nativeCapturedRequest, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nativeCapturedRequest{}, err
	}
	return nativeCapturedRequest{
		method: r.Method,
		path:   r.URL.EscapedPath(),
		header: r.Header.Clone(),
		body:   body,
	}, nil
}

func nativeMessageRequest(maxTokens int64) *protocol.MessageRequest {
	return &protocol.MessageRequest{
		Model:     "claude-test",
		MaxTokens: maxTokens,
		Messages: []protocol.MessageParam{{
			Role:    protocol.RoleUser,
			Content: protocol.StringContent("hello"),
		}},
	}
}

func writeNativeJSONResponse(w http.ResponseWriter) {
	w.Header().Set("content-type", "application/json")
	_, _ = io.WriteString(w, `{
  "id":"msg_sync",
  "type":"message",
  "role":"assistant",
  "model":"claude-test",
  "content":[{"type":"text","text":"hello back"}],
  "stop_reason":"end_turn",
  "stop_sequence":null,
  "stop_details":null,
  "usage":{"input_tokens":4,"output_tokens":2}
}`)
}

func TestNativeMessagesSync_WireHeadersOptionsAndRequestID(t *testing.T) {
	captured := make(chan nativeCapturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := captureNativeRequest(r)
		if err != nil {
			t.Errorf("read request: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		captured <- request
		w.Header().Set("request-id", "req_sync_123")
		writeNativeJSONResponse(w)
	}))
	defer server.Close()

	originalStream := true
	request := nativeMessageRequest(128)
	request.Stream = &originalStream
	request.ExtraFields = protocol.ExtraFields{
		"future_control": json.RawMessage(`{"counter":9007199254740993}`),
	}

	message, err := NewWithBaseURL(server.URL+"/v1").CreateAnthropicMessage(
		context.Background(),
		"secret-key",
		request,
		protocol.WithVersion("2026-01-01"),
		protocol.WithBetas("tools-2025-01-01, fine-grained-2025-05-14", "tools-2025-01-01", " future-beta "),
	)
	if err != nil {
		t.Fatalf("CreateAnthropicMessage: %v", err)
	}
	if message.RequestID != "req_sync_123" {
		t.Fatalf("RequestID = %q, want req_sync_123", message.RequestID)
	}
	if got := message.Text(); got != "hello back" {
		t.Fatalf("message text = %q, want hello back", got)
	}
	if request.Stream == nil || !*request.Stream {
		t.Fatal("CreateAnthropicMessage mutated caller-owned Stream")
	}

	wire := <-captured
	if wire.method != http.MethodPost || wire.path != "/v1/messages" {
		t.Fatalf("request = %s %s, want POST /v1/messages", wire.method, wire.path)
	}
	if got := wire.header.Get("x-api-key"); got != "secret-key" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := wire.header.Get("anthropic-version"); got != "2026-01-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := wire.header.Get("anthropic-beta"); got != "tools-2025-01-01,fine-grained-2025-05-14,future-beta" {
		t.Errorf("anthropic-beta = %q", got)
	}
	if got := wire.header.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	if got := wire.header.Get("accept"); got != "" {
		t.Errorf("sync Accept = %q, want empty", got)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(wire.body, &body); err != nil {
		t.Fatalf("decode request body: %v\n%s", err, wire.body)
	}
	if got := string(body["model"]); got != `"claude-test"` {
		t.Errorf("model = %s", got)
	}
	if got := string(body["max_tokens"]); got != "128" {
		t.Errorf("max_tokens = %s", got)
	}
	if got := string(body["stream"]); got != "false" {
		t.Errorf("stream = %s, want false", got)
	}
	if got := string(body["future_control"]); got != `{"counter":9007199254740993}` {
		t.Errorf("future_control = %s", got)
	}
	var messages []protocol.MessageParam
	if err := json.Unmarshal(body["messages"], &messages); err != nil || len(messages) != 1 {
		t.Fatalf("messages = %s, err=%v", body["messages"], err)
	}
}

func TestNativeMessagesSync_AllowsExplicitZeroMaxTokens(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- body
		writeNativeJSONResponse(w)
	}))
	defer server.Close()

	if _, err := NewWithBaseURL(server.URL).CreateAnthropicMessage(
		context.Background(), "key", nativeMessageRequest(0),
	); err != nil {
		t.Fatalf("max_tokens=0 must reach Anthropic unchanged: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(<-requestBody, &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body["max_tokens"]); got != "0" {
		t.Fatalf("max_tokens = %s, want explicit 0", got)
	}
}

func TestNativeMessagesTokenCount_PathBodyHeadersAndRequestID(t *testing.T) {
	captured := make(chan nativeCapturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := captureNativeRequest(r)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		captured <- request
		w.Header().Set("x-request-id", "req_count_123")
		_, _ = io.WriteString(w, `{"input_tokens":321,"future_count":9007199254740993}`)
	}))
	defer server.Close()

	request := &protocol.TokenCountRequest{
		Model: "claude-test",
		Messages: []protocol.MessageParam{{
			Role:    protocol.RoleUser,
			Content: protocol.BlockContent(protocol.NewTextBlock("count me")),
		}},
		ExtraFields: protocol.ExtraFields{"future_option": json.RawMessage(`{"enabled":true}`)},
	}
	response, err := NewWithBaseURL(server.URL+"/v1").CountAnthropicMessageTokens(
		context.Background(), "count-key", request, protocol.WithBetas("count-beta"),
	)
	if err != nil {
		t.Fatalf("CountAnthropicMessageTokens: %v", err)
	}
	if response.InputTokens != 321 || response.RequestID != "req_count_123" {
		t.Fatalf("response = %+v", response)
	}
	if got := string(response.ExtraFields["future_count"]); got != "9007199254740993" {
		t.Fatalf("future_count = %s", got)
	}

	wire := <-captured
	if wire.method != http.MethodPost || wire.path != "/v1/messages/count_tokens" {
		t.Fatalf("request = %s %s", wire.method, wire.path)
	}
	if wire.header.Get("anthropic-version") != protocol.DefaultVersion {
		t.Errorf("anthropic-version = %q", wire.header.Get("anthropic-version"))
	}
	if wire.header.Get("anthropic-beta") != "count-beta" {
		t.Errorf("anthropic-beta = %q", wire.header.Get("anthropic-beta"))
	}
	if wire.header.Get("x-api-key") != "count-key" {
		t.Errorf("x-api-key = %q", wire.header.Get("x-api-key"))
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(wire.body, &body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["max_tokens"]; exists {
		t.Errorf("token count body unexpectedly contains max_tokens: %s", wire.body)
	}
	if _, exists := body["stream"]; exists {
		t.Errorf("token count body unexpectedly contains stream: %s", wire.body)
	}
	if got := string(body["future_option"]); got != `{"enabled":true}` {
		t.Errorf("future_option = %s", got)
	}
}

func TestNativeMessagesHTTPError_PreservesCodeCategoryAndRequestID(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		code         string
		wantCategory provider.ErrorCategory
		headerID     string
		bodyID       string
		wantID       string
	}{
		{"authentication", http.StatusUnauthorized, "authentication_error", provider.ErrorCategoryAuth, "req_header", "req_body", "req_header"},
		{"rate_limit", http.StatusTooManyRequests, "rate_limit_error", provider.ErrorCategoryRateLimit, "", "req_rate", "req_rate"},
		{"invalid", http.StatusBadRequest, "invalid_request_error", provider.ErrorCategoryInvalidRequest, "", "req_invalid", "req_invalid"},
		{"not_found", http.StatusNotFound, "not_found_error", provider.ErrorCategoryNotFound, "", "req_missing", "req_missing"},
		{"overloaded", 529, "overloaded_error", provider.ErrorCategoryServer, "req_overload", "", "req_overload"},
		{"future_code", http.StatusInternalServerError, "future_error", "", "req_future", "", "req_future"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.headerID != "" {
					w.Header().Set("request-id", test.headerID)
				}
				w.Header().Set("retry-after", "7")
				w.WriteHeader(test.status)
				_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":"boom"},"request_id":%q}`, test.code, test.bodyID)
			}))
			defer server.Close()

			_, err := NewWithBaseURL(server.URL).CreateAnthropicMessage(
				context.Background(), "bad-key", nativeMessageRequest(1),
			)
			if err == nil {
				t.Fatal("expected API error")
			}
			var providerErr *provider.ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("errors.As(*ProviderError) failed: %T %v", err, err)
			}
			if providerErr.StatusCode != test.status || providerErr.RequestID != test.wantID {
				t.Errorf("ProviderError = %+v, want status=%d requestID=%q", providerErr, test.status, test.wantID)
			}
			if providerErr.RetryAfter != "7" {
				t.Errorf("RetryAfter = %q", providerErr.RetryAfter)
			}
			if got := provider.ProviderCode(err); got != test.code {
				t.Errorf("ProviderCode = %q, want %q", got, test.code)
			}
			if got := provider.ErrorCategoryOf(err); got != test.wantCategory {
				t.Errorf("ErrorCategoryOf = %q, want %q", got, test.wantCategory)
			}
		})
	}
}

type nativeSSEFrame struct {
	typeName string
	json     string
}

func writeNativeSSE(w http.ResponseWriter, frames []nativeSSEFrame) {
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.typeName, frame.json)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func nativeMessageStartJSON() string {
	return `{"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":5,"output_tokens":0}}}`
}

func TestNativeMessagesStream_AccumulatesTextToolThinkingPingUnknownAndTerminal(t *testing.T) {
	captured := make(chan nativeCapturedRequest, 1)
	frames := []nativeSSEFrame{
		{"message_start", nativeMessageStartJSON()},
		{"ping", `{"type":"ping"}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"deep thought"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-opaque"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello "}}`},
		{"future_notice", `{"type":"future_notice","counter":9007199254740993}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"world"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"weather","input":{}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\""}}`},
		{"ping", `{"type":"ping","future_ping":true}`},
		{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"Paris\"}"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":2}`},
		{"content_block_start", `{"type":"content_block_start","index":3,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"\"Claude streaming\"}"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":3}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null,"stop_details":null},"usage":{"output_tokens":7}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := captureNativeRequest(r)
		if err != nil {
			t.Errorf("capture stream request: %v", err)
			return
		}
		captured <- request
		w.Header().Set("request-id", "req_stream_123")
		writeNativeSSE(w, frames)
	}))
	defer server.Close()

	request := nativeMessageRequest(64)
	stream, err := NewWithBaseURL(server.URL+"/v1").CreateAnthropicMessageStream(
		context.Background(), "stream-key", request,
	)
	if err != nil {
		t.Fatalf("CreateAnthropicMessageStream: %v", err)
	}
	defer stream.Close()
	if stream.RequestID() != "req_stream_123" {
		t.Fatalf("stream RequestID = %q", stream.RequestID())
	}

	var gotTypes []protocol.EventType
	var sawPing, sawUnknown bool
	for {
		event, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv after %v: %v", gotTypes, recvErr)
		}
		gotTypes = append(gotTypes, event.Type)
		if event.Type == protocol.EventTypePing {
			sawPing = true
		}
		if event.Type == protocol.EventType("future_notice") {
			sawUnknown = event.Unknown != nil && strings.Contains(string(event.Raw), "9007199254740993")
		}
	}
	if !sawPing || !sawUnknown {
		t.Fatalf("sawPing=%t sawUnknown=%t types=%v", sawPing, sawUnknown, gotTypes)
	}
	if len(gotTypes) == 0 || gotTypes[len(gotTypes)-1] != protocol.EventTypeMessageStop {
		t.Fatalf("last event = %v, want message_stop", gotTypes)
	}

	message := stream.FinalMessage()
	if message == nil {
		t.Fatal("FinalMessage is nil after message_stop")
	}
	if message.RequestID != "req_stream_123" || message.Text() != "Hello world" {
		t.Fatalf("final message metadata/text = requestID=%q text=%q", message.RequestID, message.Text())
	}
	if len(message.Content) != 4 {
		t.Fatalf("content blocks = %d, want 4: %+v", len(message.Content), message.Content)
	}
	if thinking := message.Content[0].Thinking; thinking == nil || thinking.Thinking != "deep thought" || thinking.Signature != "sig-opaque" {
		t.Fatalf("thinking block = %+v", thinking)
	}
	tool := message.Content[2].ToolUse
	if tool == nil || tool.ID != "toolu_1" || tool.Name != "weather" {
		t.Fatalf("tool block = %+v", tool)
	}
	var toolInput map[string]string
	if err := json.Unmarshal(tool.Input, &toolInput); err != nil || toolInput["city"] != "Paris" {
		t.Fatalf("tool input = %s, err=%v", tool.Input, err)
	}
	serverTool := message.Content[3]
	if serverTool.Type != protocol.ContentBlockType("server_tool_use") || serverTool.Raw == nil {
		t.Fatalf("server tool block = %+v", serverTool)
	}
	var serverToolWire struct {
		Input map[string]string `json:"input"`
	}
	if err := json.Unmarshal(serverTool.Raw, &serverToolWire); err != nil || serverToolWire.Input["query"] != "Claude streaming" {
		t.Fatalf("server tool input = %s, err=%v", serverTool.Raw, err)
	}
	if serverTool.PartialJSON != `{"query":"Claude streaming"}` {
		t.Fatalf("server tool PartialJSON = %q", serverTool.PartialJSON)
	}
	if message.StopReason == nil || *message.StopReason != protocol.StopReasonToolUse {
		t.Fatalf("stop reason = %v", message.StopReason)
	}
	if message.Usage.InputTokens != 5 || message.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", message.Usage)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}

	wire := <-captured
	if wire.path != "/v1/messages" || wire.header.Get("accept") != "text/event-stream" {
		t.Fatalf("stream wire path=%q accept=%q", wire.path, wire.header.Get("accept"))
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(wire.body, &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body["stream"]); got != "true" {
		t.Fatalf("stream request body flag = %s", got)
	}
	if request.Stream != nil {
		t.Fatal("stream call mutated caller-owned request Stream")
	}
}

func TestNativeMessagesStream_ErrorEventThenClassifiedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("request-id", "req_stream_error")
		writeNativeSSE(w, []nativeSSEFrame{
			{"message_start", nativeMessageStartJSON()},
			{"error", `{"type":"error","error":{"type":"overloaded_error","message":"busy","future":1}}`},
		})
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL).CreateAnthropicMessageStream(
		context.Background(), "key", nativeMessageRequest(8),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if event, err := stream.Recv(); err != nil || event.Type != protocol.EventTypeMessageStart {
		t.Fatalf("first Recv = event=%+v err=%v", event, err)
	}
	errorEvent, err := stream.Recv()
	if err != nil || errorEvent.Type != protocol.EventTypeError || errorEvent.Error == nil {
		t.Fatalf("error event Recv = event=%+v err=%v", errorEvent, err)
	}
	if errorEvent.Error.Error.Type != "overloaded_error" {
		t.Fatalf("stream error payload = %+v", errorEvent.Error.Error)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected classified error after error event")
	}
	var apiError *protocol.APIError
	if !errors.As(err, &apiError) || apiError.Type != "overloaded_error" {
		t.Fatalf("errors.As(*APIError) failed: %T %v", err, err)
	}
	if provider.ProviderCode(err) != "overloaded_error" || provider.ErrorCategoryOf(err) != provider.ErrorCategoryServer {
		t.Fatalf("classified stream error: code=%q category=%q", provider.ProviderCode(err), provider.ErrorCategoryOf(err))
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after pending error = %v, want EOF", err)
	}
	partial := stream.FinalMessage()
	if partial == nil || partial.ID != "msg_stream" || partial.RequestID != "req_stream_error" {
		t.Fatalf("partial FinalMessage = %+v", partial)
	}
}

func TestNativeMessagesStream_ReportsUnexpectedEOFBeforeTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeNativeSSE(w, []nativeSSEFrame{
			{"message_start", nativeMessageStartJSON()},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`},
		})
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL).CreateAnthropicMessageStream(
		context.Background(), "key", nativeMessageRequest(8),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	for {
		_, err = stream.Recv()
		if err != nil {
			break
		}
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("terminal-before-EOF error = %v, want io.ErrUnexpectedEOF", err)
	}
	partial := stream.FinalMessage()
	if partial == nil || partial.Text() != "partial" {
		t.Fatalf("partial FinalMessage = %+v", partial)
	}
}

func TestNativeMessagesStream_EnforcesAssembledFrameLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Each physical line fits under the configured ceiling (plus SSE syntax
		// allowance), while the assembled multi-data event does not.
		w.Header().Set("content-type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: future_event\ndata: %s\ndata: %s\n\n",
			strings.Repeat("x", 70), strings.Repeat("y", 70))
	}))
	defer server.Close()

	ctx := provider.WithStreamPolicy(context.Background(), provider.StreamPolicy{MaxFrameBytes: 128})
	stream, err := NewWithBaseURL(server.URL).CreateAnthropicMessageStream(ctx, "key", nativeMessageRequest(8))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	_, err = stream.Recv()
	if !errors.Is(err, sse.ErrEventTooLarge) {
		t.Fatalf("frame-limit error = %v, want ErrEventTooLarge", err)
	}
	if !strings.Contains(err.Error(), "128") {
		t.Fatalf("frame-limit error omits configured ceiling: %v", err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after frame error = %v, want EOF", err)
	}
}

type nativeBlockingBody struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newNativeBlockingBody() *nativeBlockingBody {
	return &nativeBlockingBody{started: make(chan struct{}), closed: make(chan struct{})}
}

func (body *nativeBlockingBody) Read(_ []byte) (int, error) {
	body.once.Do(func() { close(body.started) })
	<-body.closed
	return 0, errors.New("native test body closed")
}

func (body *nativeBlockingBody) Close() error {
	select {
	case <-body.closed:
	default:
		close(body.closed)
	}
	return nil
}

func TestNativeMessagesStream_CloseUnblocksRecvAndIsIdempotent(t *testing.T) {
	body := newNativeBlockingBody()
	stream := newNativeMessageStream(context.Background(), &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Request-Id": []string{"req_close"}},
		Body:       body,
	})
	if stream.RequestID() != "req_close" {
		t.Fatalf("RequestID = %q", stream.RequestID())
	}

	recvDone := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		recvDone <- err
	}()

	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("Recv did not start reading")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	select {
	case err := <-recvDone:
		if err == nil {
			t.Fatal("blocked Recv returned nil error after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Recv")
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after Close = %v, want EOF", err)
	}
}
