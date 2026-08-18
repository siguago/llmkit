package llmkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/protocol/openaibatch"
)

func minimalAnthropicBatchCreate() *anthropicapi.MessageBatchCreateRequest {
	return &anthropicapi.MessageBatchCreateRequest{
		Requests: []anthropicapi.MessageBatchRequestItem{{
			CustomID: "r1",
			Params: &anthropicapi.MessageRequest{
				Model: "claude-sonnet-4-5", MaxTokens: 16,
				Messages: []anthropicapi.MessageParam{{
					Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent("hi"),
				}},
			},
		}},
	}
}

func TestBatches_UnsupportedProviderFailsBeforeNetwork(t *testing.T) {
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsupported batch operation reached the network")
	})
	if _, err := c.CreateBatch(context.Background(), &openaibatch.CreateRequest{
		InputFileID: "file-1", Endpoint: openaibatch.EndpointChatCompletions,
	}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("CreateBatch err = %v", err)
	}
	if _, err := c.CreateAnthropicMessageBatch(context.Background(), minimalAnthropicBatchCreate()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("CreateAnthropicMessageBatch err = %v", err)
	}
	if _, err := c.ReadAnthropicMessageBatchResults(context.Background(), "msgbatch_1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ReadAnthropicMessageBatchResults err = %v", err)
	}
}

// Creating a batch queues billable work; a 5xx after the upstream may have
// accepted it must not be replayed.
func TestCreateBatch_NotReplayedOnServerError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
	})
	if _, err := c.CreateBatch(context.Background(), &openaibatch.CreateRequest{
		InputFileID: "file-1", Endpoint: openaibatch.EndpointChatCompletions, CompletionWindow: "24h",
	}); err == nil {
		t.Fatal("want error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("create hit the server %d times, want exactly 1", got)
	}
}

// 429 proves the upstream did not accept work, so create retries it.
func TestCreateBatch_RetriedOnRateLimit(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("retry-after", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"slow","type":"rate_limit_error"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"batch_1","object":"batch","status":"validating"}`)
	})
	batch, err := c.CreateBatch(context.Background(), &openaibatch.CreateRequest{
		InputFileID: "file-1", Endpoint: openaibatch.EndpointChatCompletions, CompletionWindow: "24h",
	})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if calls.Load() != 2 || batch.ID != "batch_1" {
		t.Fatalf("calls = %d batch = %+v", calls.Load(), batch)
	}
}

func TestCancelBatch_NotRetried(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
	})
	if _, err := c.CancelBatch(context.Background(), "batch_1"); err == nil {
		t.Fatal("want error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cancel hit the server %d times, want exactly 1", got)
	}
}

func TestWaitBatch_PollsToTerminal(t *testing.T) {
	var polls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) < 3 {
			_, _ = io.WriteString(w, `{"id":"batch_1","object":"batch","status":"in_progress"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"batch_1","object":"batch","status":"completed","output_file_id":"file-out"}`)
	})
	start := &openaibatch.Batch{}
	if err := startFromJSON(start, `{"id":"batch_1","object":"batch","status":"validating"}`); err != nil {
		t.Fatal(err)
	}
	final, err := c.WaitBatch(context.Background(), start, &WaitBatchOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitBatch: %v", err)
	}
	if final.Status != openaibatch.StatusCompleted || final.OutputFileID != "file-out" {
		t.Fatalf("final = %+v", final)
	}
	if polls.Load() != 3 {
		t.Fatalf("polls = %d, want 3", polls.Load())
	}
}

func TestWaitBatch_TerminalInputReturnsWithoutNetwork(t *testing.T) {
	c := newTestClientFor(t, OpenAI, func(http.ResponseWriter, *http.Request) {
		t.Fatal("terminal batch must not be polled")
	})
	terminal := &openaibatch.Batch{}
	if err := startFromJSON(terminal, `{"id":"batch_1","status":"failed"}`); err != nil {
		t.Fatal(err)
	}
	got, err := c.WaitBatch(context.Background(), terminal, nil)
	if err != nil || got.Status != openaibatch.StatusFailed {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
}

func TestWaitAnthropicMessageBatch_PollsUntilEnded(t *testing.T) {
	var polls atomic.Int32
	c := newTestClientFor(t, Anthropic, func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) < 2 {
			_, _ = io.WriteString(w, `{"id":"msgbatch_1","type":"message_batch","processing_status":"in_progress","request_counts":{"processing":1,"succeeded":0,"errored":0,"canceled":0,"expired":0},"created_at":"2026-08-18T00:00:00Z","expires_at":"2026-08-19T00:00:00Z"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msgbatch_1","type":"message_batch","processing_status":"ended","request_counts":{"processing":0,"succeeded":1,"errored":0,"canceled":0,"expired":0},"created_at":"2026-08-18T00:00:00Z","expires_at":"2026-08-19T00:00:00Z","results_url":"https://api.anthropic.com/v1/messages/batches/msgbatch_1/results"}`)
	})
	start := &anthropicapi.MessageBatch{}
	if err := startFromJSON(start, `{"id":"msgbatch_1","type":"message_batch","processing_status":"in_progress","request_counts":{"processing":1,"succeeded":0,"errored":0,"canceled":0,"expired":0},"created_at":"2026-08-18T00:00:00Z","expires_at":"2026-08-19T00:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	final, err := c.WaitAnthropicMessageBatch(context.Background(), start, &WaitAnthropicMessageBatchOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitAnthropicMessageBatch: %v", err)
	}
	if !final.HasEnded() || final.RequestCounts.Succeeded != 1 {
		t.Fatalf("final = %+v", final)
	}
}

func TestReadAnthropicBatchResults_HandshakeRetriedThenStreams(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, Anthropic, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"flake"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"custom_id":"r1","result":{"type":"canceled"}}`+"\n")
	})
	reader, err := c.ReadAnthropicMessageBatchResults(context.Background(), "msgbatch_1")
	if err != nil {
		t.Fatalf("ReadAnthropicMessageBatchResults: %v", err)
	}
	defer reader.Close()
	line, err := reader.Next()
	if err != nil || line.CustomID != "r1" {
		t.Fatalf("line = %+v, err = %v", line, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("want one handshake retry, got %d calls", calls.Load())
	}
}

func TestDeleteAnthropicMessageBatch_NotRetried(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, Anthropic, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	})
	if _, err := c.DeleteAnthropicMessageBatch(context.Background(), "msgbatch_1"); err == nil {
		t.Fatal("want error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("delete hit the server %d times, want exactly 1", got)
	}
}

// startFromJSON builds protocol objects through their real decoders so tests
// exercise the same UnmarshalJSON paths production traffic does.
func startFromJSON(target interface{ UnmarshalJSON([]byte) error }, wire string) error {
	return target.UnmarshalJSON([]byte(wire))
}
