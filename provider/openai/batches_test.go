package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/siguago/llmkit/protocol/openaibatch"
	"github.com/siguago/llmkit/provider"
)

func TestCreateBatch_WireShape(t *testing.T) {
	var gotPath, gotAuth, gotContentType, gotBody string
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("x-request-id", "req_batch_1")
		_, _ = io.WriteString(w, `{"id":"batch_abc","object":"batch","status":"validating","input_file_id":"file-1","endpoint":"/v1/chat/completions","completion_window":"24h","created_at":1711471533}`)
	})
	batch, err := p.CreateBatch(context.Background(), "sk-live", &openaibatch.CreateRequest{
		InputFileID:      "file-1",
		Endpoint:         openaibatch.EndpointChatCompletions,
		CompletionWindow: openaibatch.CompletionWindow24h,
	})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if gotPath != "POST /batches" || gotAuth != "Bearer sk-live" || gotContentType != "application/json" {
		t.Errorf("request = %q auth %q content-type %q", gotPath, gotAuth, gotContentType)
	}
	want := `{"input_file_id":"file-1","endpoint":"/v1/chat/completions","completion_window":"24h"}`
	if gotBody != want {
		t.Errorf("body = %s, want %s", gotBody, want)
	}
	if batch.ID != "batch_abc" || batch.Status != openaibatch.StatusValidating || batch.RequestID != "req_batch_1" {
		t.Errorf("batch = %+v", batch)
	}
}

func TestCreateBatch_ValidatesBeforeNetwork(t *testing.T) {
	p := newFilesTestProvider(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid request must not reach the network")
	})
	if _, err := p.CreateBatch(context.Background(), "sk", nil); err == nil {
		t.Error("nil request must error")
	}
	if _, err := p.CreateBatch(context.Background(), "sk", &openaibatch.CreateRequest{Endpoint: "/v1/embeddings"}); err == nil {
		t.Error("missing input file must error")
	}
}

func TestRetrieveAndCancelBatch_Paths(t *testing.T) {
	var paths []string
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		_, _ = io.WriteString(w, `{"id":"batch_a/b","object":"batch","status":"cancelling"}`)
	})
	if _, err := p.RetrieveBatch(context.Background(), "sk", "batch_a/b"); err != nil {
		t.Fatalf("RetrieveBatch: %v", err)
	}
	batch, err := p.CancelBatch(context.Background(), "sk", "batch_a/b")
	if err != nil {
		t.Fatalf("CancelBatch: %v", err)
	}
	if batch.Status != openaibatch.StatusCancelling {
		t.Errorf("status = %q", batch.Status)
	}
	want := []string{"GET /batches/batch_a%2Fb", "POST /batches/batch_a%2Fb/cancel"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestListBatches_Query(t *testing.T) {
	var gotQuery string
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"object":"list","data":[],"has_more":false}`)
	})
	if _, err := p.ListBatches(context.Background(), "sk", &openaibatch.ListRequest{After: "batch_x", Limit: 5}); err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if !strings.Contains(gotQuery, "after=batch_x") || !strings.Contains(gotQuery, "limit=5") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestBatchOps_RejectEmptyID(t *testing.T) {
	p := newFilesTestProvider(t, func(http.ResponseWriter, *http.Request) {
		t.Error("empty ID must not reach the network")
	})
	if _, err := p.RetrieveBatch(context.Background(), "sk", " "); err == nil {
		t.Error("RetrieveBatch must error")
	}
	if _, err := p.CancelBatch(context.Background(), "sk", ""); err == nil {
		t.Error("CancelBatch must error")
	}
}

func TestBatch_UpstreamErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("retry-after", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limited"}}`)
	}))
	t.Cleanup(srv.Close)
	p := NewWithBaseURL(srv.URL)
	_, err := p.RetrieveBatch(context.Background(), "sk", "batch_1")
	if err == nil {
		t.Fatal("want error")
	}
	var perr *provider.ProviderError
	if !errors.As(err, &perr) || perr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want 429 ProviderError", err)
	}
	if provider.ProviderCode(err) != "rate_limited" {
		t.Errorf("provider code = %q", provider.ProviderCode(err))
	}
}
