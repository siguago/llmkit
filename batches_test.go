package llmkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
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

// The list and cancel facades were shipping with no test exercising them at
// all: the provider layer was covered, but the root path that resolves the
// capability, applies the retry policy, and translates errors was not.
func TestListBatches_Facade(t *testing.T) {
	var gotQuery string
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"batch_1","object":"batch","status":"completed"}],"first_id":"batch_1","last_id":"batch_1","has_more":false}`)
	})
	list, err := c.ListBatches(context.Background(), &openaibatch.ListRequest{After: "batch_0", Limit: 10})
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "batch_1" || list.HasMore {
		t.Fatalf("list = %+v", list)
	}
	if !strings.Contains(gotQuery, "after=batch_0") || !strings.Contains(gotQuery, "limit=10") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestListAnthropicMessageBatches_Facade(t *testing.T) {
	var gotQuery string
	c := newTestClientFor(t, Anthropic, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"data":[{"id":"msgbatch_1","type":"message_batch","processing_status":"ended","request_counts":{"processing":0,"succeeded":1,"errored":0,"canceled":0,"expired":0},"created_at":"2026-08-18T00:00:00Z","expires_at":"2026-08-19T00:00:00Z"}],"first_id":"msgbatch_1","last_id":"msgbatch_1","has_more":false}`)
	})
	list, err := c.ListAnthropicMessageBatches(context.Background(), &anthropicapi.MessageBatchListRequest{
		BeforeID: "msgbatch_9", Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListAnthropicMessageBatches: %v", err)
	}
	if len(list.Data) != 1 || !list.Data[0].HasEnded() {
		t.Fatalf("list = %+v", list)
	}
	if !strings.Contains(gotQuery, "before_id=msgbatch_9") || !strings.Contains(gotQuery, "limit=50") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestCancelAnthropicMessageBatch_Facade(t *testing.T) {
	var gotPath string
	c := newTestClientFor(t, Anthropic, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		_, _ = io.WriteString(w, `{"id":"msgbatch_1","type":"message_batch","processing_status":"canceling","request_counts":{"processing":1,"succeeded":0,"errored":0,"canceled":0,"expired":0},"created_at":"2026-08-18T00:00:00Z","expires_at":"2026-08-19T00:00:00Z","cancel_initiated_at":"2026-08-18T00:01:00Z"}`)
	})
	batch, err := c.CancelAnthropicMessageBatch(context.Background(), "msgbatch_1")
	if err != nil {
		t.Fatalf("CancelAnthropicMessageBatch: %v", err)
	}
	if batch.ProcessingStatus != anthropicapi.BatchProcessingStatusCanceling {
		t.Errorf("status = %q", batch.ProcessingStatus)
	}
	if batch.CancelInitiatedAt == "" {
		t.Error("cancel_initiated_at lost")
	}
	if !strings.HasSuffix(gotPath, "/messages/batches/msgbatch_1/cancel") {
		t.Errorf("path = %q", gotPath)
	}
}

// Regression: the batch waiters run on a 26-hour budget, so the "tolerate a
// 404 while the store catches up" rule copied from WaitResponse (30-minute
// budget) turned a wrong or deleted batch ID into a day-long silent hang.
// A persistent 404 must surface instead.
func TestWaitBatch_PersistentNotFoundSurfaces(t *testing.T) {
	var polls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"no such batch","type":"invalid_request_error","code":"not_found"}}`)
	})
	start := &openaibatch.Batch{}
	if err := startFromJSON(start, `{"id":"batch_gone","object":"batch","status":"in_progress"}`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.WaitBatch(context.Background(), start, &WaitBatchOptions{Interval: time.Millisecond})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a batch that is gone must not wait out the full budget")
		}
		if !IsNotFound(err) {
			t.Fatalf("err = %v, want the upstream 404 to reach the caller", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("WaitBatch swallowed a persistent 404 instead of returning it")
	}
	if got := polls.Load(); got > waitNotFoundGracePolls+1 {
		t.Errorf("polled %d times, want at most %d before giving up", got, waitNotFoundGracePolls+1)
	}
}

// The grace window still exists: a batch that 404s briefly, then appears,
// must be waited out rather than failed.
func TestWaitBatch_TransientNotFoundStillTolerated(t *testing.T) {
	var polls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"not yet","type":"invalid_request_error","code":"not_found"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"batch_1","object":"batch","status":"completed"}`)
	})
	start := &openaibatch.Batch{}
	if err := startFromJSON(start, `{"id":"batch_1","object":"batch","status":"validating"}`); err != nil {
		t.Fatal(err)
	}
	final, err := c.WaitBatch(context.Background(), start, &WaitBatchOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("a transient 404 must still be absorbed: %v", err)
	}
	if final.Status != openaibatch.StatusCompleted {
		t.Fatalf("final = %+v", final)
	}
}

func TestWaitAnthropicMessageBatch_PersistentNotFoundSurfaces(t *testing.T) {
	c := newTestClientFor(t, Anthropic, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"deleted"}}`)
	})
	start := &anthropicapi.MessageBatch{}
	if err := startFromJSON(start, `{"id":"msgbatch_gone","type":"message_batch","processing_status":"in_progress","request_counts":{"processing":1,"succeeded":0,"errored":0,"canceled":0,"expired":0},"created_at":"2026-08-18T00:00:00Z","expires_at":"2026-08-19T00:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.WaitAnthropicMessageBatch(context.Background(), start,
			&WaitAnthropicMessageBatchOptions{Interval: time.Millisecond})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !IsNotFound(err) {
			t.Fatalf("err = %v, want the upstream 404", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("WaitAnthropicMessageBatch swallowed a persistent 404: deleting an ended batch is a documented operation")
	}
}
