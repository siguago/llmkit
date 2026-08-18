//go:build integration

package llmkit

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/protocol/openaibatch"
	"github.com/siguago/llmkit/protocol/openaifiles"
)

// Release evidence for the Files and Batch resource surfaces. These make real,
// billable calls and clean up after themselves.
//
// Files is cheap (storage-priced, deleted immediately). Batch is not free: a
// cancel races the upstream, and any request already dispatched runs and bills.
// Both therefore need an explicit opt-in beyond the integration build tag:
//
//	OPENAI_API_KEY=sk-... LLMKIT_RUN_BATCH=1 go test -tags=integration -v -run TestLiveFiles .
//	OPENAI_API_KEY=sk-... LLMKIT_RUN_BATCH=1 go test -tags=integration -v -run TestLiveBatch .
//	ANTHROPIC_API_KEY=... LLMKIT_RUN_BATCH=1 go test -tags=integration -v -run TestLiveAnthropicMessageBatch .
//
// Retention is the caller's problem and these tests act like it: every file
// they upload is deleted, and every batch they create is cancelled. A
// cancelled OpenAI batch object cannot be deleted at all — it stays in the
// account listing until the upstream archives it.

func requireBatchOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("LLMKIT_RUN_BATCH") != "1" {
		t.Skip("set LLMKIT_RUN_BATCH=1 to run the billable Files/Batch release checks")
	}
}

func TestLiveFiles(t *testing.T) {
	requireBatchOptIn(t)
	client := liveClient(t, OpenAI, WithTimeout(2*time.Minute))
	ctx := context.Background()

	const content = "llmkit integration probe: safe to delete\n"
	uploaded, err := client.UploadFile(ctx, &openaifiles.UploadRequest{
		Filename:    "llmkit-integration.txt",
		Purpose:     openaifiles.PurposeUserData,
		Content:     strings.NewReader(content),
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	// Delete even if an assertion below fails: leaked files are the caller's
	// storage bill and this test created them.
	t.Cleanup(func() {
		if _, err := client.DeleteFile(context.Background(), uploaded.ID); err != nil {
			t.Errorf("cleanup DeleteFile(%s): %v", uploaded.ID, err)
		}
	})

	if uploaded.ID == "" || uploaded.Bytes != int64(len(content)) {
		t.Fatalf("upload identity/size: id=%q bytes=%d want %d", uploaded.ID, uploaded.Bytes, len(content))
	}

	retrieved, err := client.RetrieveFile(ctx, uploaded.ID)
	if err != nil {
		t.Fatalf("RetrieveFile: %v", err)
	}
	if retrieved.ID != uploaded.ID || retrieved.Filename != "llmkit-integration.txt" {
		t.Errorf("retrieve mismatch: %+v", retrieved)
	}
	if retrieved.Purpose != openaifiles.PurposeUserData {
		t.Errorf("purpose = %q, want %q", retrieved.Purpose, openaifiles.PurposeUserData)
	}

	list, err := client.ListFiles(ctx, &openaifiles.ListRequest{Limit: 100, Purpose: openaifiles.PurposeUserData})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	var found bool
	for _, file := range list.Data {
		if file.ID == uploaded.ID {
			found = true
			break
		}
	}
	// A busy account can push the new file past the first page, so absence is
	// only a failure when the page is provably complete.
	if !found && !list.HasMore {
		t.Errorf("uploaded file missing from a complete listing of %d files", len(list.Data))
	}

	body, err := client.DownloadFileContent(ctx, uploaded.ID)
	if err != nil {
		t.Fatalf("DownloadFileContent: %v", err)
	}
	downloaded, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatalf("read downloaded content: %v", err)
	}
	if string(downloaded) != content {
		t.Errorf("round-trip mismatch: got %q want %q", downloaded, content)
	}
}

func TestLiveBatch(t *testing.T) {
	requireBatchOptIn(t)
	client := liveClient(t, OpenAI, WithTimeout(2*time.Minute))
	model := liveModel(OpenAI)
	ctx := context.Background()

	item, err := openaibatch.NewInputItem("live-1", openaibatch.EndpointChatCompletions, map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: pong"}},
		"max_tokens": 16,
	})
	if err != nil {
		t.Fatalf("NewInputItem: %v", err)
	}
	var input strings.Builder
	if err := openaibatch.EncodeInput(&input, item); err != nil {
		t.Fatalf("EncodeInput: %v", err)
	}

	file, err := client.UploadFile(ctx, &openaifiles.UploadRequest{
		Filename:    "llmkit-integration-batch.jsonl",
		Purpose:     openaifiles.PurposeBatch,
		Content:     strings.NewReader(input.String()),
		ContentType: "application/jsonl",
	})
	if err != nil {
		t.Fatalf("UploadFile(batch input): %v", err)
	}
	t.Cleanup(func() {
		if _, err := client.DeleteFile(context.Background(), file.ID); err != nil {
			t.Errorf("cleanup DeleteFile(%s): %v", file.ID, err)
		}
	})

	batch, err := client.CreateBatch(ctx, &openaibatch.CreateRequest{
		InputFileID:      file.ID,
		Endpoint:         openaibatch.EndpointChatCompletions,
		CompletionWindow: openaibatch.CompletionWindow24h,
		Metadata:         map[string]string{"origin": "llmkit-integration"},
	})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if batch.ID == "" || batch.InputFileID != file.ID {
		t.Fatalf("batch identity: %+v", batch)
	}
	// Cancel immediately: this test proves the endpoints work, not that a
	// 24-hour job completes. A cancelled batch cannot be deleted, so it stays
	// in the account until the upstream archives it.
	t.Cleanup(func() {
		if _, err := client.CancelBatch(context.Background(), batch.ID); err != nil {
			t.Logf("cleanup CancelBatch(%s): %v (already terminal?)", batch.ID, err)
		}
	})

	fetched, err := client.RetrieveBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("RetrieveBatch: %v", err)
	}
	if fetched.ID != batch.ID {
		t.Errorf("retrieve mismatch: %q vs %q", fetched.ID, batch.ID)
	}
	if fetched.Endpoint != openaibatch.EndpointChatCompletions {
		t.Errorf("endpoint = %q", fetched.Endpoint)
	}

	list, err := client.ListBatches(ctx, &openaibatch.ListRequest{Limit: 20})
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(list.Data) == 0 {
		t.Error("listing returned no batches right after creating one")
	}

	cancelled, err := client.CancelBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("CancelBatch: %v", err)
	}
	switch cancelled.Status {
	case openaibatch.StatusCancelling, openaibatch.StatusCancelled:
	default:
		// Racing a fast batch to a terminal state is legitimate; anything else
		// means cancel did not take.
		if !cancelled.IsTerminal() {
			t.Errorf("status after cancel = %q, want cancelling/cancelled or terminal", cancelled.Status)
		}
	}
}

func TestLiveAnthropicMessageBatch(t *testing.T) {
	requireBatchOptIn(t)
	client := liveClient(t, Anthropic, WithTimeout(2*time.Minute))
	model := liveModel(Anthropic)
	ctx := context.Background()

	created, err := client.CreateAnthropicMessageBatch(ctx, &anthropicapi.MessageBatchCreateRequest{
		Requests: []anthropicapi.MessageBatchRequestItem{{
			CustomID: "live-1",
			Params: &anthropicapi.MessageRequest{
				Model:     model,
				MaxTokens: 16,
				Messages: []anthropicapi.MessageParam{{
					Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent("Reply with exactly: pong"),
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("CreateAnthropicMessageBatch: %v", err)
	}
	if created.ID == "" || created.CreatedAt == "" || created.ExpiresAt == "" {
		t.Fatalf("batch identity/timestamps: %+v", created)
	}
	if created.RequestCounts.Processing != 1 {
		t.Errorf("processing count = %d, want 1", created.RequestCounts.Processing)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := client.CancelAnthropicMessageBatch(cleanupCtx, created.ID); err != nil {
			t.Logf("cleanup cancel(%s): %v (already ended?)", created.ID, err)
		}
		// Only ended batches are deletable; a still-cancelling one is left to
		// the upstream's own expiry.
		if batch, err := client.RetrieveAnthropicMessageBatch(cleanupCtx, created.ID); err == nil && batch.HasEnded() {
			if _, err := client.DeleteAnthropicMessageBatch(cleanupCtx, created.ID); err != nil {
				t.Logf("cleanup delete(%s): %v", created.ID, err)
			}
		}
	})

	fetched, err := client.RetrieveAnthropicMessageBatch(ctx, created.ID)
	if err != nil {
		t.Fatalf("RetrieveAnthropicMessageBatch: %v", err)
	}
	if fetched.ID != created.ID || fetched.Type != "message_batch" {
		t.Errorf("retrieve mismatch: %+v", fetched)
	}

	list, err := client.ListAnthropicMessageBatches(ctx, &anthropicapi.MessageBatchListRequest{Limit: 20})
	if err != nil {
		t.Fatalf("ListAnthropicMessageBatches: %v", err)
	}
	if len(list.Data) == 0 {
		t.Error("listing returned no batches right after creating one")
	}

	cancelled, err := client.CancelAnthropicMessageBatch(ctx, created.ID)
	if err != nil {
		t.Fatalf("CancelAnthropicMessageBatch: %v", err)
	}
	if cancelled.ProcessingStatus != anthropicapi.BatchProcessingStatusCanceling && !cancelled.HasEnded() {
		t.Errorf("status after cancel = %q", cancelled.ProcessingStatus)
	}

	// Results only exist once processing ends. Poll briefly rather than
	// blocking for the full 24-hour window; a still-cancelling batch is a
	// legitimate outcome for a test that cancels immediately.
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ended, err := client.WaitAnthropicMessageBatch(waitCtx, cancelled, &WaitAnthropicMessageBatchOptions{
		Interval: 10 * time.Second,
		Timeout:  2 * time.Minute,
	})
	if err != nil {
		t.Logf("batch did not end within the short wait (expected for an immediate cancel): %v", err)
		return
	}

	reader, err := client.ReadAnthropicMessageBatchResults(waitCtx, ended.ID)
	if err != nil {
		t.Fatalf("ReadAnthropicMessageBatchResults: %v", err)
	}
	defer reader.Close()
	var lines int
	for {
		line, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("results line %d: %v", lines+1, err)
		}
		lines++
		if line.CustomID == "" || line.Result.Type == "" {
			t.Errorf("results line %d lost identity: %+v", lines, line)
		}
		// Assert protocol structure, never the natural-language content.
		switch line.Result.Type {
		case anthropicapi.BatchResultTypeSucceeded:
			if line.Result.Message == nil || line.Result.Message.ID == "" {
				t.Errorf("succeeded line %d has no message", lines)
			}
		case anthropicapi.BatchResultTypeErrored:
			if line.Result.Error == nil {
				t.Errorf("errored line %d has no error envelope", lines)
			}
		}
	}
	if lines != int(ended.RequestCounts.Succeeded+ended.RequestCounts.Errored+
		ended.RequestCounts.Canceled+ended.RequestCounts.Expired) {
		t.Errorf("results lines = %d, request_counts total = %s", lines, fmt.Sprint(ended.RequestCounts))
	}
}
