package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/siguago/llmkit/internal/sse"
	responsesapi "github.com/siguago/llmkit/protocol/responses"
	"github.com/siguago/llmkit/provider"
)

type responsesCapturedRequest struct {
	method      string
	escapedPath string
	rawQuery    string
	header      http.Header
	body        []byte
}

func captureResponsesRequest(r *http.Request) (responsesCapturedRequest, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return responsesCapturedRequest{}, err
	}
	return responsesCapturedRequest{
		method:      r.Method,
		escapedPath: r.URL.EscapedPath(),
		rawQuery:    r.URL.RawQuery,
		header:      r.Header.Clone(),
		body:        body,
	}, nil
}

func newResponsesCreateRequest() *responsesapi.CreateRequest {
	return &responsesapi.CreateRequest{
		Model: "gpt-test",
		Input: responsesapi.NewTextInput("hello"),
	}
}

func writeResponsesJSON(w http.ResponseWriter, payload string) {
	w.Header().Set("content-type", "application/json")
	_, _ = io.WriteString(w, payload)
}

const responsesSyncJSON = `{
  "id":"resp_sync",
  "object":"response",
  "created_at":1,
  "status":"completed",
  "error":null,
  "incomplete_details":null,
  "model":"gpt-test",
  "output":[{
    "id":"msg_sync",
    "type":"message",
    "status":"completed",
    "role":"assistant",
    "content":[{"type":"output_text","text":"hello back","annotations":[]}]
  }],
  "parallel_tool_calls":true,
  "store":true,
  "future_response":{"counter":9007199254740993}
}`

func TestNativeResponsesSync_WireHeadersRequestIDAndCallerImmutability(t *testing.T) {
	captured := make(chan responsesCapturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := captureResponsesRequest(r)
		if err != nil {
			t.Errorf("capture request: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		captured <- request
		w.Header().Set("x-request-id", "req_sync_123")
		writeResponsesJSON(w, responsesSyncJSON)
	}))
	defer server.Close()

	callerStream := true
	request := newResponsesCreateRequest()
	request.Stream = &callerStream
	request.ExtraFields = responsesapi.ExtraFields{
		"future_control": json.RawMessage(`{"counter":9007199254740993}`),
	}

	response, err := NewWithBaseURL(server.URL+"/proxy/v1").CreateResponse(
		context.Background(), "secret-key", request,
	)
	if err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	if response.RequestID != "req_sync_123" {
		t.Fatalf("RequestID = %q, want req_sync_123", response.RequestID)
	}
	if got := response.OutputText(); got != "hello back" {
		t.Fatalf("OutputText = %q, want hello back", got)
	}
	if got := string(response.ExtraFields["future_response"]); got != `{"counter":9007199254740993}` {
		t.Fatalf("future response field = %s", got)
	}
	if request.Stream != &callerStream || !*request.Stream {
		t.Fatal("CreateResponse mutated caller-owned Stream")
	}

	wire := <-captured
	if wire.method != http.MethodPost || wire.escapedPath != "/proxy/v1/responses" {
		t.Fatalf("request = %s %s, want POST /proxy/v1/responses", wire.method, wire.escapedPath)
	}
	if got := wire.header.Get("authorization"); got != "Bearer secret-key" {
		t.Errorf("Authorization = %q", got)
	}
	if got := wire.header.Get("content-type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := wire.header.Get("accept"); got != "" {
		t.Errorf("sync Accept = %q, want empty", got)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(wire.body, &body); err != nil {
		t.Fatalf("decode request body: %v\n%s", err, wire.body)
	}
	if got := string(body["model"]); got != `"gpt-test"` {
		t.Errorf("model = %s", got)
	}
	if got := string(body["input"]); got != `"hello"` {
		t.Errorf("input = %s", got)
	}
	if got := string(body["stream"]); got != "false" {
		t.Errorf("stream = %s, want false", got)
	}
	if got := string(body["future_control"]); got != `{"counter":9007199254740993}` {
		t.Errorf("future_control = %s", got)
	}
}

func TestNativeResponsesLifecycle_PathPrefixEscapedIDQueriesAndRequestIDs(t *testing.T) {
	captured := make(chan responsesCapturedRequest, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := captureResponsesRequest(r)
		if err != nil {
			t.Errorf("capture request: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		captured <- request

		switch {
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/resp_empty"):
			w.Header().Set("x-request-id", "req_delete_empty")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			w.Header().Set("x-request-id", "req_delete")
			writeResponsesJSON(w, `{"id":"resp/a b?","object":"response.deleted","deleted":true}`)
		case strings.HasSuffix(r.URL.Path, "/input_items"):
			w.Header().Set("x-request-id", "req_list")
			writeResponsesJSON(w, `{
			  "object":"list",
			  "data":[{
			    "id":"msg_input",
			    "type":"message",
			    "role":"user",
			    "content":[{"type":"input_text","text":"hello"}]
			  }],
			  "first_id":"msg_input",
			  "last_id":"msg_input",
			  "has_more":false
			}`)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			w.Header().Set("x-request-id", "req_cancel")
			writeResponsesJSON(w, `{
			  "id":"resp/a b?",
			  "object":"response",
			  "created_at":1,
			  "status":"cancelled",
			  "error":null,
			  "incomplete_details":null,
			  "model":"gpt-test",
			  "output":[],
			  "parallel_tool_calls":true,
			  "store":true
			}`)
		default:
			w.Header().Set("x-request-id", "req_retrieve")
			writeResponsesJSON(w, responsesSyncJSON)
		}
	}))
	defer server.Close()

	client := NewWithBaseURL(server.URL + "/gateway/v1/")
	responseID := "resp/a b?"
	const escapedResourcePath = "/gateway/v1/responses/resp%2Fa%20b%3F"

	retrieved, err := client.RetrieveResponse(context.Background(), "life-key", responseID, &responsesapi.RetrieveOptions{
		Include: []string{"web_search_call.action.sources", " ", "file_search_call.results"},
	})
	if err != nil {
		t.Fatalf("RetrieveResponse: %v", err)
	}
	if retrieved.RequestID != "req_retrieve" || retrieved.ID != "resp_sync" {
		t.Fatalf("retrieved response = %+v", retrieved)
	}
	assertResponsesLifecycleRequest(t, <-captured, http.MethodGet, escapedResourcePath, []string{
		"web_search_call.action.sources", "file_search_call.results",
	}, "", "", "")

	cancelled, err := client.CancelResponse(context.Background(), "life-key", responseID)
	if err != nil {
		t.Fatalf("CancelResponse: %v", err)
	}
	if cancelled.RequestID != "req_cancel" || cancelled.Status != responsesapi.StatusCancelled {
		t.Fatalf("cancelled response = %+v", cancelled)
	}
	assertResponsesLifecycleRequest(t, <-captured, http.MethodPost, escapedResourcePath+"/cancel", nil, "", "", "")

	deleted, err := client.DeleteResponse(context.Background(), "life-key", responseID)
	if err != nil {
		t.Fatalf("DeleteResponse: %v", err)
	}
	if deleted.RequestID != "req_delete" || !deleted.Deleted || deleted.ID != responseID {
		t.Fatalf("deleted response = %+v", deleted)
	}
	assertResponsesLifecycleRequest(t, <-captured, http.MethodDelete, escapedResourcePath, nil, "", "", "")

	emptyDeleted, err := client.DeleteResponse(context.Background(), "life-key", "resp_empty")
	if err != nil {
		t.Fatalf("DeleteResponse empty body: %v", err)
	}
	if emptyDeleted.RequestID != "req_delete_empty" || !emptyDeleted.Deleted ||
		emptyDeleted.ID != "resp_empty" || emptyDeleted.Object != "response.deleted" {
		t.Fatalf("synthesized deleted response = %+v", emptyDeleted)
	}
	assertResponsesLifecycleRequest(
		t, <-captured, http.MethodDelete, "/gateway/v1/responses/resp_empty", nil, "", "", "",
	)

	limit := 42
	items, err := client.ListResponseInputItems(context.Background(), "life-key", responseID, &responsesapi.ListInputItemsOptions{
		After:   "item_after",
		Include: []string{"reasoning.encrypted_content", " ", "file_search_call.results"},
		Limit:   &limit,
		Order:   "desc",
	})
	if err != nil {
		t.Fatalf("ListResponseInputItems: %v", err)
	}
	if items.RequestID != "req_list" || len(items.Data) != 1 || items.Data[0].Message == nil {
		t.Fatalf("input item list = %+v", items)
	}
	assertResponsesLifecycleRequest(t, <-captured, http.MethodGet, escapedResourcePath+"/input_items", []string{
		"reasoning.encrypted_content", "file_search_call.results",
	}, "item_after", "42", "desc")
}

func assertResponsesLifecycleRequest(
	t *testing.T,
	wire responsesCapturedRequest,
	method, escapedPath string,
	include []string,
	after, limit, order string,
) {
	t.Helper()
	if wire.method != method || wire.escapedPath != escapedPath {
		t.Fatalf("request = %s %s, want %s %s", wire.method, wire.escapedPath, method, escapedPath)
	}
	if got := wire.header.Get("authorization"); got != "Bearer life-key" {
		t.Errorf("Authorization = %q", got)
	}
	query, err := url.ParseQuery(wire.rawQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", wire.rawQuery, err)
	}
	if got := query["include[]"]; !reflect.DeepEqual(got, include) {
		t.Errorf("include[] = %v, want %v", got, include)
	}
	if got := query["include"]; len(got) != 0 {
		t.Errorf("unexpected bare include query = %v", got)
	}
	if got := query.Get("after"); got != after {
		t.Errorf("after = %q, want %q", got, after)
	}
	if got := query.Get("limit"); got != limit {
		t.Errorf("limit = %q, want %q", got, limit)
	}
	if got := query.Get("order"); got != order {
		t.Errorf("order = %q, want %q", got, order)
	}
	if got := query.Get("before"); got != "" {
		t.Errorf("unexpected unsupported before query = %q", got)
	}
}

func TestNativeResponsesTokenCount_PathBodyAndRequestID(t *testing.T) {
	captured := make(chan responsesCapturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := captureResponsesRequest(r)
		if err != nil {
			t.Errorf("capture request: %v", err)
			return
		}
		captured <- request
		w.Header().Set("x-request-id", "req_count_123")
		writeResponsesJSON(w, `{"object":"response.input_tokens","input_tokens":321,"future_count":9007199254740993}`)
	}))
	defer server.Close()

	request := &responsesapi.TokenCountRequest{
		Model: "gpt-test",
		Input: responsesapi.NewTextInput("count me"),
		ExtraFields: responsesapi.ExtraFields{
			"future_option": json.RawMessage(`{"enabled":true}`),
		},
	}
	response, err := NewWithBaseURL(server.URL+"/v1").CountResponseInputTokens(
		context.Background(), "count-key", request,
	)
	if err != nil {
		t.Fatalf("CountResponseInputTokens: %v", err)
	}
	if response.InputTokens != 321 || response.Object != "response.input_tokens" || response.RequestID != "req_count_123" {
		t.Fatalf("token count response = %+v", response)
	}
	if got := string(response.ExtraFields["future_count"]); got != "9007199254740993" {
		t.Fatalf("future token count field = %s", got)
	}

	wire := <-captured
	if wire.method != http.MethodPost || wire.escapedPath != "/v1/responses/input_tokens" {
		t.Fatalf("request = %s %s", wire.method, wire.escapedPath)
	}
	if got := wire.header.Get("authorization"); got != "Bearer count-key" {
		t.Errorf("Authorization = %q", got)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(wire.body, &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body["input"]); got != `"count me"` {
		t.Errorf("input = %s", got)
	}
	if got := string(body["future_option"]); got != `{"enabled":true}` {
		t.Errorf("future_option = %s", got)
	}
	if _, exists := body["stream"]; exists {
		t.Errorf("token count body unexpectedly contains stream: %s", wire.body)
	}
}

func TestNativeResponsesHTTPError_PreservesEnvelopeMetadataAndRetryAfter(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		errorType    string
		code         string
		wantCode     string
		wantCategory provider.ErrorCategory
		retryAfter   string
	}{
		{"authentication", http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid_api_key", provider.ErrorCategoryAuth, ""},
		{"rate_limit_type_fallback", http.StatusTooManyRequests, "rate_limit_error", "", "rate_limit_error", provider.ErrorCategoryRateLimit, "7"},
		{"invalid_request", http.StatusBadRequest, "invalid_request_error", "invalid_value", "invalid_value", provider.ErrorCategoryInvalidRequest, ""},
		{"not_found", http.StatusNotFound, "not_found_error", "resource_not_found", "resource_not_found", provider.ErrorCategoryNotFound, ""},
		{"server", http.StatusInternalServerError, "api_error", "internal_error", "internal_error", provider.ErrorCategoryServer, ""},
		{"408_overrides_rate_body", http.StatusRequestTimeout, "rate_limit_error", "rate_limit_exceeded", "rate_limit_exceeded", "", ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("x-request-id", "req_error_"+test.name)
				if test.retryAfter != "" {
					w.Header().Set("retry-after", test.retryAfter)
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprintf(w, `{"error":{"type":%q,"code":%q,"message":"boom","param":"input"}}`, test.errorType, test.code)
			}))
			defer server.Close()

			_, err := NewWithBaseURL(server.URL).CreateResponse(
				context.Background(), "bad-key", newResponsesCreateRequest(),
			)
			if err == nil {
				t.Fatal("expected API error")
			}
			var providerErr *provider.ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("errors.As(*ProviderError) failed: %T %v", err, err)
			}
			if providerErr.StatusCode != test.status || providerErr.RequestID != "req_error_"+test.name {
				t.Errorf("ProviderError = %+v", providerErr)
			}
			if providerErr.RetryAfter != test.retryAfter {
				t.Errorf("RetryAfter = %q, want %q", providerErr.RetryAfter, test.retryAfter)
			}
			if !strings.Contains(providerErr.Message, "boom") {
				t.Errorf("error message lost envelope: %q", providerErr.Message)
			}
			if got := provider.ProviderCode(err); got != test.wantCode {
				t.Errorf("ProviderCode = %q, want %q", got, test.wantCode)
			}
			if got := provider.ErrorCategoryOf(err); got != test.wantCategory {
				t.Errorf("ErrorCategoryOf = %q, want %q", got, test.wantCategory)
			}
		})
	}
}

func TestNativeResponsesHTTPError_ClassifiesNonJSONBodyFromStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html")
		w.Header().Set("x-request-id", "req_gateway_413")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, "request too large")
	}))
	defer server.Close()

	_, err := NewWithBaseURL(server.URL).CreateResponse(
		context.Background(), "key", newResponsesCreateRequest(),
	)
	if err == nil {
		t.Fatal("expected API error")
	}
	var providerErr *provider.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("errors.As(*ProviderError) failed: %T %v", err, err)
	}
	if providerErr.StatusCode != http.StatusRequestEntityTooLarge ||
		providerErr.RequestID != "req_gateway_413" ||
		!strings.Contains(providerErr.Message, "request too large") {
		t.Fatalf("ProviderError = %+v", providerErr)
	}
	if got := provider.ErrorCategoryOf(err); got != provider.ErrorCategoryInvalidRequest {
		t.Fatalf("ErrorCategoryOf = %q, want %q", got, provider.ErrorCategoryInvalidRequest)
	}
}

func TestNativeResponsesAPIErrorPreservesHTTPMetadataWhenBodyReadFails(t *testing.T) {
	bodyErr := io.ErrUnexpectedEOF
	payload := []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`)
	client := NewWithBaseURL("https://example.invalid/v1")
	client.client = &http.Client{Transport: responsesRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header: http.Header{
				"Retry-After":  []string{"9"},
				"X-Request-Id": []string{"req_partial_error"},
			},
			Body: &responsesErrorAfterDataBody{data: payload, err: bodyErr},
		}, nil
	})}

	_, err := client.CreateResponse(context.Background(), "key", newResponsesCreateRequest())
	if err == nil {
		t.Fatal("expected API/read error")
	}
	if !errors.Is(err, bodyErr) {
		t.Fatalf("body read error was hidden: %v", err)
	}
	var providerErr *provider.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("HTTP error metadata was hidden: %T %v", err, err)
	}
	if providerErr.StatusCode != http.StatusTooManyRequests || providerErr.RetryAfter != "9" || providerErr.RequestID != "req_partial_error" {
		t.Fatalf("ProviderError = %+v", providerErr)
	}
	if provider.ProviderCode(err) != "rate_limit_exceeded" || provider.ErrorCategoryOf(err) != provider.ErrorCategoryRateLimit {
		t.Fatalf("classification = code %q category %q", provider.ProviderCode(err), provider.ErrorCategoryOf(err))
	}
}

type responsesSSEFrame struct {
	name string
	data string
}

func writeResponsesSSE(w http.ResponseWriter, frames []responsesSSEFrame) {
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		if frame.name != "" {
			_, _ = fmt.Fprintf(w, "event: %s\n", frame.name)
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame.data)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func responsesStreamCreatedJSON() string {
	return `{"type":"response.created","sequence_number":0,"response":{"id":"resp_stream","object":"response","created_at":1,"status":"in_progress","error":null,"incomplete_details":null,"model":"gpt-test","output":[],"parallel_tool_calls":true,"store":true}}`
}

func responsesStreamCompletedJSON(sequence int64, id string) string {
	return fmt.Sprintf(`{"type":"response.completed","sequence_number":%d,"response":{"id":%q,"object":"response","created_at":1,"status":"completed","error":null,"incomplete_details":null,"model":"gpt-test","output":[],"parallel_tool_calls":true,"store":true}}`, sequence, id)
}

func TestNativeResponsesStream_EventsUnknownRawAccumulatorAndWire(t *testing.T) {
	captured := make(chan responsesCapturedRequest, 1)
	frames := []responsesSSEFrame{
		{"response.created", responsesStreamCreatedJSON()},
		{"response.in_progress", `{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_stream","object":"response","created_at":1,"status":"in_progress","error":null,"incomplete_details":null,"model":"gpt-test","output":[],"parallel_tool_calls":true,"store":true}}`},
		{"response.output_item.added", `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_stream","type":"message","status":"in_progress","role":"assistant","content":[]}}`},
		{"response.content_part.added", `{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_stream","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`},
		{"response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_stream","output_index":0,"content_index":0,"delta":"Hello "}`},
		{"response.future_notice", `{"type":"response.future_notice","sequence_number":5,"counter":9007199254740993}`},
		{"response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":6,"item_id":"msg_stream","output_index":0,"content_index":0,"delta":"world"}`},
		{"response.output_text.done", `{"type":"response.output_text.done","sequence_number":7,"item_id":"msg_stream","output_index":0,"content_index":0,"text":"Hello world"}`},
		{"response.content_part.done", `{"type":"response.content_part.done","sequence_number":8,"item_id":"msg_stream","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello world","annotations":[]}}`},
		{"response.output_item.done", `{"type":"response.output_item.done","sequence_number":9,"output_index":0,"item":{"id":"msg_stream","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello world","annotations":[]}]}}`},
		{"response.output_item.added", `{"type":"response.output_item.added","sequence_number":10,"output_index":1,"item":{"id":"fc_stream","type":"function_call","call_id":"call_stream","name":"weather","arguments":"","status":"in_progress"}}`},
		{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":11,"item_id":"fc_stream","output_index":1,"delta":"{\"city\":\""}`},
		{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":12,"item_id":"fc_stream","output_index":1,"delta":"Paris\"}"}`},
		{"response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","sequence_number":13,"item_id":"fc_stream","output_index":1,"name":"weather","arguments":"{\"city\":\"Paris\"}"}`},
		{"response.output_item.done", `{"type":"response.output_item.done","sequence_number":14,"output_index":1,"item":{"id":"fc_stream","type":"function_call","call_id":"call_stream","name":"weather","arguments":"{\"city\":\"Paris\"}","status":"completed"}}`},
		{"response.completed", `{"type":"response.completed","sequence_number":15,"response":{"id":"resp_stream","object":"response","created_at":1,"status":"completed","error":null,"incomplete_details":null,"model":"gpt-test","output":[{"id":"msg_stream","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello world","annotations":[]}]},{"id":"fc_stream","type":"function_call","call_id":"call_stream","name":"weather","arguments":"{\"city\":\"Paris\"}","status":"completed"}],"parallel_tool_calls":true,"store":true,"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}}`},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := captureResponsesRequest(r)
		if err != nil {
			t.Errorf("capture stream request: %v", err)
			return
		}
		captured <- request
		w.Header().Set("x-request-id", "req_stream_123")
		writeResponsesSSE(w, frames)
	}))
	defer server.Close()

	callerStream := false
	request := newResponsesCreateRequest()
	request.Stream = &callerStream
	stream, err := NewWithBaseURL(server.URL+"/proxy/v1").CreateResponseStream(
		context.Background(), "stream-key", request,
	)
	if err != nil {
		t.Fatalf("CreateResponseStream: %v", err)
	}
	defer stream.Close()
	if stream.RequestID() != "req_stream_123" {
		t.Fatalf("RequestID = %q", stream.RequestID())
	}

	var gotTypes []responsesapi.EventType
	var sawUnknown bool
	for {
		event, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("Recv after %v: %v", gotTypes, recvErr)
		}
		gotTypes = append(gotTypes, event.Type)
		switch {
		case event.Type == responsesapi.EventTypeResponseOutputTextDelta && event.SequenceNumber == 4:
			if got := stream.FinalResponse().OutputText(); got != "Hello " {
				t.Fatalf("partial text = %q, want %q", got, "Hello ")
			}
		case event.Type == responsesapi.EventType("response.future_notice"):
			sawUnknown = event.RawJSON() != nil && strings.Contains(string(event.RawJSON()), "9007199254740993")
		case event.Type == responsesapi.EventTypeResponseFunctionArgumentsDelta && event.SequenceNumber == 11:
			calls := stream.FinalResponse().FunctionCalls()
			if len(calls) != 1 || calls[0].Arguments != `{"city":"` {
				t.Fatalf("partial function calls = %+v", calls)
			}
		}
		if event.IsTerminal() {
			break
		}
	}
	if !sawUnknown {
		t.Fatal("unknown event was skipped or lost its Raw JSON")
	}
	wantTypes := []responsesapi.EventType{
		responsesapi.EventTypeResponseCreated,
		responsesapi.EventTypeResponseInProgress,
		responsesapi.EventTypeResponseOutputItemAdded,
		responsesapi.EventTypeResponseContentPartAdded,
		responsesapi.EventTypeResponseOutputTextDelta,
		responsesapi.EventType("response.future_notice"),
		responsesapi.EventTypeResponseOutputTextDelta,
		responsesapi.EventTypeResponseOutputTextDone,
		responsesapi.EventTypeResponseContentPartDone,
		responsesapi.EventTypeResponseOutputItemDone,
		responsesapi.EventTypeResponseOutputItemAdded,
		responsesapi.EventTypeResponseFunctionArgumentsDelta,
		responsesapi.EventTypeResponseFunctionArgumentsDelta,
		responsesapi.EventTypeResponseFunctionArgumentsDone,
		responsesapi.EventTypeResponseOutputItemDone,
		responsesapi.EventTypeResponseCompleted,
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event types = %v, want %v", gotTypes, wantTypes)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after terminal event = %v, want EOF", err)
	}

	final := stream.FinalResponse()
	if final == nil || final.ID != "resp_stream" || final.Status != responsesapi.StatusCompleted {
		t.Fatalf("FinalResponse = %+v", final)
	}
	if final.RequestID != "req_stream_123" || final.OutputText() != "Hello world" {
		t.Fatalf("final requestID/text = %q / %q", final.RequestID, final.OutputText())
	}
	calls := final.FunctionCalls()
	if len(calls) != 1 || calls[0].Name != "weather" || calls[0].Arguments != `{"city":"Paris"}` {
		t.Fatalf("final function calls = %+v", calls)
	}
	if final.Usage == nil || final.Usage.TotalTokens != 7 {
		t.Fatalf("final usage = %+v", final.Usage)
	}
	if request.Stream != &callerStream || *request.Stream {
		t.Fatal("CreateResponseStream mutated caller-owned Stream")
	}

	wire := <-captured
	if wire.method != http.MethodPost || wire.escapedPath != "/proxy/v1/responses" {
		t.Fatalf("stream request = %s %s", wire.method, wire.escapedPath)
	}
	if wire.header.Get("authorization") != "Bearer stream-key" {
		t.Errorf("Authorization = %q", wire.header.Get("authorization"))
	}
	if wire.header.Get("accept") != "text/event-stream" {
		t.Errorf("Accept = %q", wire.header.Get("accept"))
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(wire.body, &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body["stream"]); got != "true" {
		t.Fatalf("stream request body flag = %s, want true", got)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

func TestNativeResponsesStream_ReasoningContentPartDoesNotAbort(t *testing.T) {
	frames := []responsesSSEFrame{
		{"response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_reasoning","object":"response","created_at":1,"status":"in_progress","error":null,"incomplete_details":null,"instructions":null,"metadata":{},"model":"gpt-test","output":[],"parallel_tool_calls":true,"temperature":null,"tool_choice":"auto","tools":[],"top_p":null,"store":false}}`},
		{"response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"in_progress","summary":[],"content":[]}}`},
		{"response.content_part.added", `{"type":"response.content_part.added","sequence_number":2,"item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`},
		{"response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","sequence_number":3,"item_id":"rs_1","output_index":0,"content_index":0,"delta":"step "}`},
		{"response.reasoning_text.done", `{"type":"response.reasoning_text.done","sequence_number":4,"item_id":"rs_1","output_index":0,"content_index":0,"text":"step one"}`},
		{"response.content_part.done", `{"type":"response.content_part.done","sequence_number":5,"item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":"step one"}}`},
		{"response.output_item.done", `{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"step one"}]}}`},
		{"response.completed", `{"type":"response.completed","sequence_number":7,"response":{"id":"resp_reasoning","object":"response","created_at":1,"status":"completed","error":null,"incomplete_details":null,"instructions":null,"metadata":{},"model":"gpt-test","output":[{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"step one"}]}],"parallel_tool_calls":true,"temperature":null,"tool_choice":"auto","tools":[],"top_p":null,"store":false}}`},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponsesSSE(w, frames)
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL).CreateResponseStream(
		context.Background(), "stream-key", newResponsesCreateRequest(),
	)
	if err != nil {
		t.Fatalf("CreateResponseStream: %v", err)
	}
	defer stream.Close()
	for {
		event, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
		if event.IsTerminal() {
			break
		}
	}
	final := stream.FinalResponse()
	if final == nil || len(final.Output) != 1 || final.Output[0].Reasoning == nil ||
		len(final.Output[0].Reasoning.Content) != 1 || final.Output[0].Reasoning.Content[0].ReasoningText.Text != "step one" {
		t.Fatalf("FinalResponse = %#v", final)
	}
}

func TestNativeResponsesStream_UnknownContentPartDoesNotHideTerminal(t *testing.T) {
	frames := []responsesSSEFrame{
		{"response.created", responsesStreamCreatedJSON()},
		{"response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_future","role":"assistant","status":"in_progress","content":[]}}`},
		{"response.content_part.added", `{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_future","output_index":0,"content_index":0,"part":{"type":"future_part","value":1}}`},
		{"response.content_part.added", `{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_future","output_index":0,"content_index":1,"part":{"type":"output_text","text":"","annotations":[]}}`},
		{"response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_future","output_index":0,"content_index":1,"delta":"still readable","logprobs":[]}`},
		{"response.completed", `{"type":"response.completed","sequence_number":5,"response":{"id":"resp_future","object":"response","created_at":1,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg_future","role":"assistant","status":"completed","content":[{"type":"future_part","value":1},{"type":"output_text","text":"still readable","annotations":[]}]}],"parallel_tool_calls":true,"store":false}}`},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponsesSSE(w, frames)
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL).CreateResponseStream(
		context.Background(), "key", newResponsesCreateRequest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	for {
		event, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("Recv before terminal: %v", recvErr)
		}
		if event.IsTerminal() {
			break
		}
	}
	final := stream.FinalResponse()
	if final == nil || final.ID != "resp_future" || final.OutputText() != "still readable" {
		t.Fatalf("FinalResponse = %#v", final)
	}
	if parts := final.Output[0].Message.Content.Parts; len(parts) != 2 || parts[0].Raw == nil {
		t.Fatalf("terminal content = %#v", parts)
	}
}

func TestNativeResponsesStream_TerminalEventFirstThenEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "req_terminal_first")
		writeResponsesSSE(w, []responsesSSEFrame{{
			"response.completed", responsesStreamCompletedJSON(0, "resp_terminal"),
		}})
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL).CreateResponseStream(
		context.Background(), "key", newResponsesCreateRequest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	event, err := stream.Recv()
	if err != nil || event.Type != responsesapi.EventTypeResponseCompleted {
		t.Fatalf("first Recv = event=%+v err=%v", event, err)
	}
	if final := stream.FinalResponse(); final == nil || final.ID != "resp_terminal" || final.RequestID != "req_terminal_first" {
		t.Fatalf("FinalResponse = %+v", final)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after terminal = %v, want EOF", err)
	}
}

func TestNativeResponsesStream_ErrorEventThenClassifiedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "req_stream_error")
		writeResponsesSSE(w, []responsesSSEFrame{{
			"error", `{"type":"error","sequence_number":7,"code":"rate_limit_exceeded","message":"slow down","param":"input","future_error":{"scope":"request"}}`,
		}})
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL).CreateResponseStream(
		context.Background(), "key", newResponsesCreateRequest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	event, err := stream.Recv()
	if err != nil || event.Type != responsesapi.EventTypeError || event.Error == nil {
		t.Fatalf("error event Recv = event=%+v err=%v", event, err)
	}
	if event.Error.Code != "rate_limit_exceeded" || event.Error.Message != "slow down" {
		t.Fatalf("error event = %+v", event.Error)
	}
	event.Error.Code = "mutated"
	event.Error.Message = "mutated"
	*event.Error.Param = "mutated"
	event.ExtraFields["future_error"][0] = '['
	finalBeforeError := stream.FinalResponse()
	finalBeforeError.Error.Code = "final mutation"
	finalBeforeError.Error.Message = "final mutation"
	*finalBeforeError.Error.Param = "final mutation"

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected classified error after error event")
	}
	var streamError *responsesapi.ErrorEvent
	if !errors.As(err, &streamError) || streamError.Code != "rate_limit_exceeded" ||
		streamError.Message != "slow down" || streamError.Param == nil || *streamError.Param != "input" {
		t.Fatalf("errors.As(*responses.ErrorEvent) failed: %T %v", err, err)
	}
	if provider.ProviderCode(err) != "rate_limit_exceeded" || provider.ErrorCategoryOf(err) != provider.ErrorCategoryRateLimit {
		t.Fatalf("classified error code/category = %q / %q", provider.ProviderCode(err), provider.ErrorCategoryOf(err))
	}
	if !provider.IsMarkedUnsafeToReplay(err) {
		t.Fatal("an in-stream error must never assert that replay is safe")
	}
	streamError.Code = "pending mutation"
	streamError.Message = "pending mutation"
	*streamError.Param = "pending mutation"
	if finalBeforeError.Error.Code != "final mutation" ||
		finalBeforeError.Error.Message != "final mutation" ||
		*finalBeforeError.Error.Param != "final mutation" {
		t.Fatalf("pending error mutation leaked into FinalResponse: %+v", finalBeforeError.Error)
	}
	finalBeforeError.Error.Code = "rate_limit_exceeded"
	finalBeforeError.Error.Message = "slow down"
	*finalBeforeError.Error.Param = "input"
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after pending error = %v, want EOF", err)
	}
	final := stream.FinalResponse()
	if final == nil || final.Status != responsesapi.StatusFailed || final.Error == nil {
		t.Fatalf("FinalResponse = %+v", final)
	}
	if final.Error.Code != "rate_limit_exceeded" || final.Error.Message != "slow down" ||
		final.Error.Param == nil || *final.Error.Param != "input" ||
		string(final.Error.ExtraFields["future_error"]) != `{"scope":"request"}` ||
		final.RequestID != "req_stream_error" {
		t.Fatalf("final error/request ID = %+v / %q", final.Error, final.RequestID)
	}
}

func TestNativeResponsesStream_ReportsUnexpectedEOFBeforeTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "req_partial")
		writeResponsesSSE(w, []responsesSSEFrame{
			{"response.created", responsesStreamCreatedJSON()},
			{"response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_partial","output_index":0,"content_index":0,"delta":"partial"}`},
		})
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL).CreateResponseStream(
		context.Background(), "key", newResponsesCreateRequest(),
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
	final := stream.FinalResponse()
	if final == nil || final.OutputText() != "partial" || final.RequestID != "req_partial" {
		t.Fatalf("partial FinalResponse = %+v", final)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after unexpected EOF = %v, want EOF", err)
	}
}

func TestNativeResponsesStream_DiscardsUnterminatedTerminalFrame(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n", responsesStreamCompletedJSON(0, "resp_truncated"))
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL).CreateResponseStream(
		context.Background(), "key", newResponsesCreateRequest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	event, err := stream.Recv()
	if event != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Recv = event=%+v err=%v, want nil/io.ErrUnexpectedEOF", event, err)
	}
	if final := stream.FinalResponse(); final != nil {
		t.Fatalf("unterminated terminal frame must not be accumulated: %+v", final)
	}
}

func TestNativeResponsesStream_EnforcesAssembledFrameLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		// Each physical data line fits the ceiling while their assembled event
		// does not, guarding the decoder's aggregate frame limit.
		_, _ = fmt.Fprintf(w, "event: response.future_notice\ndata: %s\ndata: %s\n\n",
			strings.Repeat("x", 70), strings.Repeat("y", 70))
	}))
	defer server.Close()

	ctx := provider.WithStreamPolicy(context.Background(), provider.StreamPolicy{MaxFrameBytes: 128})
	stream, err := NewWithBaseURL(server.URL).CreateResponseStream(ctx, "key", newResponsesCreateRequest())
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

type responsesBlockingBody struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

type responsesRoundTripperFunc func(*http.Request) (*http.Response, error)

func (fn responsesRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type responsesErrorAfterDataBody struct {
	data []byte
	err  error
	done bool
}

func (body *responsesErrorAfterDataBody) Read(target []byte) (int, error) {
	if body.done {
		return 0, io.EOF
	}
	body.done = true
	return copy(target, body.data), body.err
}

func (*responsesErrorAfterDataBody) Close() error { return nil }

func newResponsesBlockingBody() *responsesBlockingBody {
	return &responsesBlockingBody{started: make(chan struct{}), closed: make(chan struct{})}
}

func (body *responsesBlockingBody) Read(_ []byte) (int, error) {
	body.once.Do(func() { close(body.started) })
	<-body.closed
	return 0, errors.New("responses test body closed")
}

func (body *responsesBlockingBody) Close() error {
	select {
	case <-body.closed:
	default:
		close(body.closed)
	}
	return nil
}

func TestNativeResponsesStream_CloseUnblocksRecvAndIsIdempotent(t *testing.T) {
	body := newResponsesBlockingBody()
	stream := newResponsesStream(context.Background(), &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"req_close"}},
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
