// Command anthropic-batch demonstrates the Anthropic Message Batches
// lifecycle in separate, explicitly chosen steps: create with inline
// requests, poll, stream results, cancel, and delete. Each step is its own
// -mode so an example run never silently queues 24 hours of billable work.
//
//	ANTHROPIC_API_KEY=sk-ant-... go run ./examples/anthropic-batch -mode=submit
//	ANTHROPIC_API_KEY=sk-ant-... go run ./examples/anthropic-batch -mode=status  -batch=msgbatch_...
//	ANTHROPIC_API_KEY=sk-ant-... go run ./examples/anthropic-batch -mode=results -batch=msgbatch_...
//	ANTHROPIC_API_KEY=sk-ant-... go run ./examples/anthropic-batch -mode=cancel  -batch=msgbatch_...
//	ANTHROPIC_API_KEY=sk-ant-... go run ./examples/anthropic-batch -mode=delete  -batch=msgbatch_...
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/siguago/llmkit"
	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
)

func main() {
	log.SetFlags(0)
	mode := flag.String("mode", "submit", "step to run: submit, status, results, cancel, or delete")
	model := flag.String("model", envOr("ANTHROPIC_BATCH_MODEL", "claude-sonnet-4-5-20250929"), "model for the batched requests")
	batchID := flag.String("batch", "", "batch ID for -mode=status/results/cancel/delete")
	flag.Parse()

	client, err := llmkit.New(llmkit.Anthropic)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch *mode {
	case "submit":
		err = runSubmit(ctx, client, *model)
	case "status":
		err = runStatus(ctx, client, *batchID)
	case "results":
		err = runResults(ctx, client, *batchID)
	case "cancel":
		err = runCancel(ctx, client, *batchID)
	case "delete":
		err = runDelete(ctx, client, *batchID)
	default:
		err = fmt.Errorf("unknown -mode %q (want submit, status, results, cancel, or delete)", *mode)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// runSubmit inlines the requests: unlike OpenAI Batch, Anthropic takes the
// Messages parameters directly in the create body, with no input file. Inside
// a batch max_tokens must be at least 1 and stream must stay unset.
func runSubmit(ctx context.Context, client *llmkit.Client, model string) error {
	var requests []anthropicapi.MessageBatchRequestItem
	for i, prompt := range []string{"用一句话解释 CAP 定理。", "用一句话解释拜占庭容错。"} {
		requests = append(requests, anthropicapi.MessageBatchRequestItem{
			CustomID: fmt.Sprintf("req-%d", i+1),
			Params: &anthropicapi.MessageRequest{
				Model:     model,
				MaxTokens: 256,
				Messages: []anthropicapi.MessageParam{{
					Role:    anthropicapi.RoleUser,
					Content: anthropicapi.StringContent(prompt),
				}},
			},
		})
	}
	batch, err := client.CreateAnthropicMessageBatch(ctx, &anthropicapi.MessageBatchCreateRequest{
		Requests: requests,
	})
	if err != nil {
		return err
	}
	fmt.Printf("batch: %s status=%s expires=%s\n", batch.ID, batch.ProcessingStatus, batch.ExpiresAt)
	fmt.Printf("next: go run ./examples/anthropic-batch -mode=status -batch=%s\n", batch.ID)
	return nil
}

// runStatus polls once. The endpoint is idempotent; batches usually finish
// within an hour and always end within 24.
func runStatus(ctx context.Context, client *llmkit.Client, batchID string) error {
	if batchID == "" {
		return fmt.Errorf("-batch is required for -mode=status")
	}
	batch, err := client.RetrieveAnthropicMessageBatch(ctx, batchID)
	if err != nil {
		return err
	}
	counts := batch.RequestCounts
	fmt.Printf("status=%s processing=%d succeeded=%d errored=%d canceled=%d expired=%d\n",
		batch.ProcessingStatus, counts.Processing, counts.Succeeded,
		counts.Errored, counts.Canceled, counts.Expired)
	if batch.HasEnded() {
		fmt.Printf("results ready: go run ./examples/anthropic-batch -mode=results -batch=%s\n", batch.ID)
	}
	return nil
}

// runResults streams the results JSONL. Lines arrive in arbitrary order, so
// join them to your requests by custom_id. Results stay downloadable for 29
// days after creation.
func runResults(ctx context.Context, client *llmkit.Client, batchID string) error {
	if batchID == "" {
		return fmt.Errorf("-batch is required for -mode=results")
	}
	reader, err := client.ReadAnthropicMessageBatchResults(ctx, batchID)
	if err != nil {
		return err
	}
	defer reader.Close()
	for {
		line, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch line.Result.Type {
		case anthropicapi.BatchResultTypeSucceeded:
			message := line.Result.Message
			fmt.Printf("%s: %s (in=%d out=%d)\n", line.CustomID, message.Text(),
				message.Usage.InputTokens, message.Usage.OutputTokens)
		case anthropicapi.BatchResultTypeErrored:
			fmt.Printf("%s: error %s — %s\n", line.CustomID,
				line.Result.Error.Error.Type, line.Result.Error.Error.Message)
		case anthropicapi.BatchResultTypeCanceled:
			fmt.Printf("%s: canceled\n", line.CustomID)
		case anthropicapi.BatchResultTypeExpired:
			fmt.Printf("%s: expired (not billed)\n", line.CustomID)
		default:
			// A future result type stays readable via its raw bytes rather
			// than breaking the whole file.
			fmt.Printf("%s: %s %s\n", line.CustomID, line.Result.Type, line.Result.Raw)
		}
	}
}

func runCancel(ctx context.Context, client *llmkit.Client, batchID string) error {
	if batchID == "" {
		return fmt.Errorf("-batch is required for -mode=cancel")
	}
	batch, err := client.CancelAnthropicMessageBatch(ctx, batchID)
	if err != nil {
		return err
	}
	fmt.Printf("status=%s (non-interruptible requests may still complete and bill)\n", batch.ProcessingStatus)
	return nil
}

// runDelete only works once processing has ended; cancel an in-progress batch
// first.
func runDelete(ctx context.Context, client *llmkit.Client, batchID string) error {
	if batchID == "" {
		return fmt.Errorf("-batch is required for -mode=delete")
	}
	deleted, err := client.DeleteAnthropicMessageBatch(ctx, batchID)
	if err != nil {
		return err
	}
	fmt.Printf("deleted %s (%s)\n", deleted.ID, deleted.Type)
	return nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
