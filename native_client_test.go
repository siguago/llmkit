package llmkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	responsesapi "github.com/siguago/llmkit/protocol/responses"
)

func TestNativeProtocolCapabilityMatrix(t *testing.T) {
	for name := range factories {
		client, err := Wrap(factories[name](""), WithAPIKey("sk-test"))
		if err != nil {
			t.Fatalf("Wrap(%s): %v", name, err)
		}

		wantResponses := name == OpenAI
		responseCapabilities := map[string]bool{
			"create":      client.SupportsResponses(),
			"stream":      client.SupportsResponseStreaming(),
			"retrieve":    client.SupportsResponseRetrieval(),
			"cancel":      client.SupportsResponseCancellation(),
			"delete":      client.SupportsResponseDeletion(),
			"input_items": client.SupportsResponseInputItems(),
			"token_count": client.SupportsResponseTokenCount(),
		}
		for capability, got := range responseCapabilities {
			if got != wantResponses {
				t.Errorf("%s Responses %s = %v, want %v", name, capability, got, wantResponses)
			}
		}

		wantFiles := name == OpenAI
		fileCapabilities := map[string]bool{
			"upload":   client.SupportsFileUpload(),
			"list":     client.SupportsFileListing(),
			"retrieve": client.SupportsFileRetrieval(),
			"delete":   client.SupportsFileDeletion(),
			"download": client.SupportsFileContentDownload(),
		}
		for capability, got := range fileCapabilities {
			if got != wantFiles {
				t.Errorf("%s Files %s = %v, want %v", name, capability, got, wantFiles)
			}
		}

		wantBatches := name == OpenAI
		batchCapabilities := map[string]bool{
			"create":   client.SupportsBatchCreation(),
			"retrieve": client.SupportsBatchRetrieval(),
			"list":     client.SupportsBatchListing(),
			"cancel":   client.SupportsBatchCancellation(),
		}
		for capability, got := range batchCapabilities {
			if got != wantBatches {
				t.Errorf("%s Batch %s = %v, want %v", name, capability, got, wantBatches)
			}
		}

		wantAnthropic := name == Anthropic
		anthropicCapabilities := map[string]bool{
			"create":      client.SupportsAnthropicMessages(),
			"stream":      client.SupportsAnthropicMessageStreaming(),
			"token_count": client.SupportsAnthropicTokenCount(),
		}
		for capability, got := range anthropicCapabilities {
			if got != wantAnthropic {
				t.Errorf("%s Anthropic %s = %v, want %v", name, capability, got, wantAnthropic)
			}
		}

		anthropicBatchCapabilities := map[string]bool{
			"create":   client.SupportsAnthropicMessageBatchCreation(),
			"retrieve": client.SupportsAnthropicMessageBatchRetrieval(),
			"list":     client.SupportsAnthropicMessageBatchListing(),
			"cancel":   client.SupportsAnthropicMessageBatchCancellation(),
			"delete":   client.SupportsAnthropicMessageBatchDeletion(),
			"results":  client.SupportsAnthropicMessageBatchResults(),
		}
		for capability, got := range anthropicBatchCapabilities {
			if got != wantAnthropic {
				t.Errorf("%s Anthropic Message Batches %s = %v, want %v", name, capability, got, wantAnthropic)
			}
		}
	}
}

func TestNativeProtocolUnsupportedReturnsBeforeNetwork(t *testing.T) {
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsupported native operation reached the network")
	})

	_, err := client.CreateResponse(context.Background(), &responsesapi.CreateRequest{
		Model: "gpt-test",
		Input: responsesapi.NewTextInput("hello"),
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("CreateResponse error = %v, want ErrUnsupported", err)
	}
	_, err = client.CreateAnthropicMessage(context.Background(), &anthropicapi.MessageRequest{
		Model:     "claude-test",
		MaxTokens: 16,
		Messages: []anthropicapi.MessageParam{{
			Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent("hello"),
		}},
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("CreateAnthropicMessage error = %v, want ErrUnsupported", err)
	}
}

func TestCreateResponseUsesReplaySafeRetryPolicy(t *testing.T) {
	t.Run("server failure is not replayed", func(t *testing.T) {
		var calls atomic.Int32
		client := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"type":"server_error","code":"overloaded","message":"busy"}}`)
		}, WithRetry(RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}))

		_, err := client.CreateResponse(context.Background(), testResponseCreateRequest())
		if err == nil {
			t.Fatal("expected create error")
		}
		if calls.Load() != 1 {
			t.Fatalf("server failure create calls = %d, want 1", calls.Load())
		}
	})

	t.Run("conflicting 408 body is retryable but not replayed", func(t *testing.T) {
		var calls atomic.Int32
		client := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusRequestTimeout)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"timed out"}}`)
		}, WithRetry(RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}))

		_, err := client.CreateResponse(context.Background(), testResponseCreateRequest())
		if err == nil {
			t.Fatal("expected create error")
		}
		if calls.Load() != 1 {
			t.Fatalf("408 create calls = %d, want 1", calls.Load())
		}
		if !IsRetryable(err) || IsRateLimited(err) || IsSafeToReplay(err) {
			t.Fatalf("classification: retryable=%t rateLimit=%t safe=%t err=%v", IsRetryable(err), IsRateLimited(err), IsSafeToReplay(err), err)
		}
	})

	t.Run("explicit rate-limit refusal is replayed", func(t *testing.T) {
		var calls atomic.Int32
		client := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`)
				return
			}
			writeTestResponse(w, "resp_retry", responsesapi.StatusCompleted)
		}, WithRetry(RetryConfig{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}))

		response, err := client.CreateResponse(context.Background(), testResponseCreateRequest())
		if err != nil {
			t.Fatalf("CreateResponse: %v", err)
		}
		if response.ID != "resp_retry" || calls.Load() != 2 {
			t.Fatalf("response=%+v calls=%d", response, calls.Load())
		}
	})
}

func TestCreateAnthropicMessageUsesReplaySafeRetryPolicy(t *testing.T) {
	retry := WithRetry(RetryConfig{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})

	t.Run("conflicting 500 body is not replayed", func(t *testing.T) {
		var calls atomic.Int32
		client := newTestClientFor(t, Anthropic, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"conflicting body"}}`)
		}, retry)

		_, err := client.CreateAnthropicMessage(context.Background(), testAnthropicMessageRequest())
		if err == nil {
			t.Fatal("expected create error")
		}
		if calls.Load() != 1 {
			t.Fatalf("500 create calls = %d, want 1", calls.Load())
		}
		if !IsServerError(err) || IsRateLimited(err) || IsSafeToReplay(err) {
			t.Fatalf("classification: server=%t rateLimit=%t safe=%t err=%v", IsServerError(err), IsRateLimited(err), IsSafeToReplay(err), err)
		}
	})

	t.Run("explicit 429 refusal is replayed", func(t *testing.T) {
		var calls atomic.Int32
		client := newTestClientFor(t, Anthropic, func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"conflicting body"}}`)
				return
			}
			writeTestAnthropicMessage(w, "msg_retry")
		}, retry)

		message, err := client.CreateAnthropicMessage(context.Background(), testAnthropicMessageRequest())
		if err != nil {
			t.Fatalf("CreateAnthropicMessage: %v", err)
		}
		if message.ID != "msg_retry" || calls.Load() != 2 {
			t.Fatalf("message=%+v calls=%d", message, calls.Load())
		}
	})
}

func TestWaitResponsePollsToTerminalAndPreservesOutcome(t *testing.T) {
	var calls atomic.Int32
	client := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/responses/resp_wait" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		status := responsesapi.StatusInProgress
		if calls.Add(1) >= 2 {
			status = responsesapi.StatusIncomplete
		}
		writeTestResponse(w, "resp_wait", status)
	})

	var updates atomic.Int32
	response, err := client.WaitResponse(context.Background(), &responsesapi.Response{
		ID: "resp_wait", Status: responsesapi.StatusQueued,
	}, &WaitResponseOptions{
		Interval: time.Millisecond,
		Timeout:  time.Second,
		OnUpdate: func(*responsesapi.Response) { updates.Add(1) },
	})
	if err != nil {
		t.Fatalf("WaitResponse: %v", err)
	}
	if response.Status != responsesapi.StatusIncomplete {
		t.Fatalf("status = %q, want incomplete", response.Status)
	}
	if calls.Load() != 2 || updates.Load() != 2 {
		t.Fatalf("calls=%d updates=%d, want 2/2", calls.Load(), updates.Load())
	}
}

func TestWaitResponseToleratesInitialNotFoundPropagation(t *testing.T) {
	var calls atomic.Int32
	client := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"type":"not_found_error","code":"response_not_found","message":"not propagated yet"}}`)
			return
		}
		writeTestResponse(w, "resp_eventual", responsesapi.StatusCompleted)
	})

	response, err := client.WaitResponse(context.Background(), &responsesapi.Response{
		ID: "resp_eventual", Status: responsesapi.StatusQueued,
	}, &WaitResponseOptions{Interval: time.Millisecond, Timeout: time.Second})
	if err != nil {
		t.Fatalf("WaitResponse: %v", err)
	}
	if response.Status != responsesapi.StatusCompleted || calls.Load() != 2 {
		t.Fatalf("response=%+v calls=%d, want completed after two polls", response, calls.Load())
	}
}

func TestWaitResponseValidationAndImmediateTerminal(t *testing.T) {
	client := newTestClientFor(t, OpenAI, func(http.ResponseWriter, *http.Request) {
		t.Fatal("terminal or invalid wait reached the network")
	})
	if _, err := client.WaitResponse(context.Background(), nil, nil); err == nil {
		t.Fatal("WaitResponse(nil) succeeded")
	}
	if _, err := client.WaitResponse(context.Background(), &responsesapi.Response{}, nil); err == nil {
		t.Fatal("WaitResponse(empty ID) succeeded")
	}
	terminal := &responsesapi.Response{ID: "resp_done", Status: responsesapi.StatusCompleted}
	got, err := client.WaitResponse(context.Background(), terminal, nil)
	if err != nil || got != terminal {
		t.Fatalf("terminal wait = (%p, %v), want original %p", got, err, terminal)
	}

	unsupportedClient := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("terminal wait reached the network")
	})
	if unsupportedClient.SupportsResponseRetrieval() {
		t.Fatal("test client unexpectedly supports Responses retrieval")
	}
	got, err = unsupportedClient.WaitResponse(context.Background(), terminal, nil)
	if err != nil || got != terminal {
		t.Fatalf("terminal wait without retrieval = (%p, %v), want original %p", got, err, terminal)
	}
}

func testResponseCreateRequest() *responsesapi.CreateRequest {
	store := false
	return &responsesapi.CreateRequest{
		Model: "gpt-test",
		Input: responsesapi.NewTextInput("hello"),
		Store: &store,
	}
}

func testAnthropicMessageRequest() *anthropicapi.MessageRequest {
	return &anthropicapi.MessageRequest{
		Model:     "claude-test",
		MaxTokens: 16,
		Messages: []anthropicapi.MessageParam{{
			Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent("hello"),
		}},
	}
}

func writeTestAnthropicMessage(w http.ResponseWriter, id string) {
	w.Header().Set("content-type", "application/json")
	_, _ = fmt.Fprintf(w, `{
  "id":%q,"type":"message","role":"assistant","model":"claude-test",
  "content":[],"stop_reason":"end_turn","stop_sequence":null,"stop_details":null,
  "usage":{"input_tokens":1,"output_tokens":1}
}`, id)
}

func writeTestResponse(w http.ResponseWriter, id, status string) {
	w.Header().Set("content-type", "application/json")
	_, _ = fmt.Fprintf(w, `{
  "id":%q,"object":"response","created_at":1,"status":%q,
  "model":"gpt-test","output":[],"parallel_tool_calls":true,"store":false
}`, id, status)
}

func TestNativeRequestValidationPrecedesWire(t *testing.T) {
	client := newTestClientFor(t, OpenAI, func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid request reached the network")
	})
	text := "conflicting input"
	_, err := client.CreateResponse(context.Background(), &responsesapi.CreateRequest{
		Model: "gpt-test",
		Input: responsesapi.Input{Text: &text, Items: []responsesapi.Item{}},
	})
	if err == nil || !strings.Contains(err.Error(), "variants") {
		t.Fatalf("invalid-union error = %v", err)
	}
}
