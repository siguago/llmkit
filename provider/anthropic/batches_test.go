package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/provider"
)

func newBatchTestProvider(t *testing.T, h http.HandlerFunc) *Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewWithBaseURL(srv.URL + "/v1")
}

func minimalBatchCreate() *anthropicapi.MessageBatchCreateRequest {
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

const wireBatch = `{"id":"msgbatch_1","type":"message_batch","processing_status":"in_progress",
  "request_counts":{"processing":1,"succeeded":0,"errored":0,"canceled":0,"expired":0},
  "created_at":"2026-08-18T00:00:00Z","expires_at":"2026-08-19T00:00:00Z"}`

func TestCreateAnthropicMessageBatch_WireShape(t *testing.T) {
	var gotPath, gotVersion, gotBeta, gotKey, gotBody string
	p := newBatchTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		gotVersion = r.Header.Get("anthropic-version")
		gotBeta = r.Header.Get("anthropic-beta")
		gotKey = r.Header.Get("x-api-key")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("request-id", "req_ab_1")
		_, _ = io.WriteString(w, wireBatch)
	})
	batch, err := p.CreateAnthropicMessageBatch(context.Background(), "sk-ant", minimalBatchCreate())
	if err != nil {
		t.Fatalf("CreateAnthropicMessageBatch: %v", err)
	}
	if gotPath != "POST /v1/messages/batches" {
		t.Errorf("path = %q", gotPath)
	}
	if gotVersion != "2023-06-01" || gotKey != "sk-ant" {
		t.Errorf("headers: version %q key %q", gotVersion, gotKey)
	}
	if gotBeta != "" {
		t.Errorf("Message Batches is GA — no beta header may be sent, got %q", gotBeta)
	}
	if !strings.Contains(gotBody, `"custom_id":"r1"`) || !strings.Contains(gotBody, `"max_tokens":16`) {
		t.Errorf("body = %s", gotBody)
	}
	if batch.ID != "msgbatch_1" || batch.RequestID != "req_ab_1" {
		t.Errorf("batch = %+v", batch)
	}
}

func TestBatchResourceOps_MethodsAndPaths(t *testing.T) {
	var seen []string
	p := newBatchTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		switch {
		case r.Method == http.MethodDelete:
			_, _ = io.WriteString(w, `{"id":"msgbatch_1","type":"message_batch_deleted"}`)
		case strings.HasSuffix(r.URL.Path, "/batches"):
			_, _ = io.WriteString(w, `{"data":[],"has_more":false}`)
		default:
			_, _ = io.WriteString(w, wireBatch)
		}
	})
	ctx := context.Background()
	if _, err := p.RetrieveAnthropicMessageBatch(ctx, "sk", "msgbatch_1"); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if _, err := p.ListAnthropicMessageBatches(ctx, "sk", &anthropicapi.MessageBatchListRequest{
		AfterID: "msgbatch_0", Limit: 30,
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := p.CancelAnthropicMessageBatch(ctx, "sk", "msgbatch_1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	deleted, err := p.DeleteAnthropicMessageBatch(ctx, "sk", "msgbatch_1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted.Type != "message_batch_deleted" {
		t.Errorf("deleted = %+v", deleted)
	}
	want := []string{
		"GET /v1/messages/batches/msgbatch_1?",
		"GET /v1/messages/batches?after_id=msgbatch_0&limit=30",
		"POST /v1/messages/batches/msgbatch_1/cancel?",
		"DELETE /v1/messages/batches/msgbatch_1?",
	}
	if len(seen) != len(want) {
		t.Fatalf("requests = %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, seen[i], want[i])
		}
	}
}

// The SSRF stance: results are fetched from the documented path under the
// configured base URL. A results_url pointing anywhere else — as a tampering
// relay could arrange — must not decide where the request goes.
func TestReadResults_IgnoresResultsURLFromBody(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request followed results_url instead of the configured base URL")
	}))
	t.Cleanup(elsewhere.Close)

	var gotPath string
	p := newBatchTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		_, _ = io.WriteString(w, `{"custom_id":"r1","result":{"type":"canceled"}}`+"\n")
	})
	// The batch object advertising a foreign results_url is irrelevant to the
	// call below — it takes only the ID.
	reader, err := p.ReadAnthropicMessageBatchResults(context.Background(), "sk", "msgbatch_1")
	if err != nil {
		t.Fatalf("ReadAnthropicMessageBatchResults: %v", err)
	}
	defer reader.Close()
	line, err := reader.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if line.CustomID != "r1" {
		t.Errorf("line = %+v", line)
	}
	if gotPath != "GET /v1/messages/batches/msgbatch_1/results" {
		t.Errorf("path = %q", gotPath)
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Errorf("end = %v, want io.EOF", err)
	}
}

// WithMaxStreamFrameBytes reaches the results reader through the context
// stream policy, exactly like SSE frame limits.
func TestReadResults_HonorsStreamPolicyLineCap(t *testing.T) {
	long := `{"custom_id":"r1","result":{"type":"canceled","pad":"` + strings.Repeat("x", 4096) + `"}}`
	p := newBatchTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, long+"\n")
	})
	ctx := provider.WithStreamPolicy(context.Background(), provider.StreamPolicy{MaxFrameBytes: 256})
	reader, err := p.ReadAnthropicMessageBatchResults(ctx, "sk", "msgbatch_1")
	if err != nil {
		t.Fatalf("ReadAnthropicMessageBatchResults: %v", err)
	}
	defer reader.Close()
	if _, err := reader.Next(); err == nil || !strings.Contains(err.Error(), "line limit") {
		t.Fatalf("err = %v, want line-limit failure", err)
	}
}

func TestBatchOps_RejectEmptyID(t *testing.T) {
	p := newBatchTestProvider(t, func(http.ResponseWriter, *http.Request) {
		t.Error("empty ID must not reach the network")
	})
	ctx := context.Background()
	if _, err := p.RetrieveAnthropicMessageBatch(ctx, "sk", ""); err == nil {
		t.Error("retrieve must error")
	}
	if _, err := p.DeleteAnthropicMessageBatch(ctx, "sk", "  "); err == nil {
		t.Error("delete must error")
	}
	if _, err := p.ReadAnthropicMessageBatchResults(ctx, "sk", ""); err == nil {
		t.Error("results must error")
	}
}

func TestBatch_UpstreamErrorClassified(t *testing.T) {
	p := newBatchTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"no such batch"},"request_id":"req_nf"}`)
	})
	_, err := p.RetrieveAnthropicMessageBatch(context.Background(), "sk", "msgbatch_missing")
	if err == nil {
		t.Fatal("want error")
	}
	var perr *provider.ProviderError
	if !errors.As(err, &perr) || perr.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %v, want 404 ProviderError", err)
	}
	if provider.ProviderCode(err) != "not_found_error" {
		t.Errorf("provider code = %q", provider.ProviderCode(err))
	}
}
