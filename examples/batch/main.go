// Command batch demonstrates the OpenAI Batch lifecycle in separate,
// explicitly chosen steps: build+upload an input file, create the batch, poll
// it, download and decode results, and clean up. Every step is its own -mode
// so an example run never silently queues 24 hours of billable work.
//
//	OPENAI_API_KEY=sk-... go run ./examples/batch -mode=submit
//	OPENAI_API_KEY=sk-... go run ./examples/batch -mode=status  -batch=batch_...
//	OPENAI_API_KEY=sk-... go run ./examples/batch -mode=results -batch=batch_...
//	OPENAI_API_KEY=sk-... go run ./examples/batch -mode=cancel  -batch=batch_...
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/siguago/llmkit"
	"github.com/siguago/llmkit/protocol/openaibatch"
	"github.com/siguago/llmkit/protocol/openaifiles"
)

func main() {
	log.SetFlags(0)
	mode := flag.String("mode", "submit", "step to run: submit, status, results, or cancel")
	model := flag.String("model", envOr("OPENAI_BATCH_MODEL", "gpt-5-mini"), "model every batched request targets (one model per input file)")
	batchID := flag.String("batch", "", "batch ID for -mode=status/results/cancel")
	flag.Parse()

	client, err := llmkit.New(llmkit.OpenAI)
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
	default:
		err = fmt.Errorf("unknown -mode %q (want submit, status, results, or cancel)", *mode)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// runSubmit builds a two-request JSONL input in memory, uploads it with
// purpose "batch", and creates the batch. The input file must stay a single
// model; results arrive out of order and are matched by custom_id.
func runSubmit(ctx context.Context, client *llmkit.Client, model string) error {
	var items []openaibatch.InputItem
	for i, prompt := range []string{"用一句话解释 CAP 定理。", "用一句话解释拜占庭容错。"} {
		item, err := openaibatch.NewInputItem(
			fmt.Sprintf("req-%d", i+1),
			openaibatch.EndpointChatCompletions,
			map[string]any{
				"model":      model,
				"messages":   []map[string]string{{"role": "user", "content": prompt}},
				"max_tokens": 128,
			},
		)
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	var input strings.Builder
	if err := openaibatch.EncodeInput(&input, items...); err != nil {
		return err
	}

	file, err := client.UploadFile(ctx, &openaifiles.UploadRequest{
		Filename:    "batch-input.jsonl",
		Purpose:     openaifiles.PurposeBatch,
		Content:     strings.NewReader(input.String()),
		ContentType: "application/jsonl",
	})
	if err != nil {
		return fmt.Errorf("upload input: %w", err)
	}
	fmt.Printf("input file: %s (%d bytes)\n", file.ID, file.Bytes)

	batch, err := client.CreateBatch(ctx, &openaibatch.CreateRequest{
		InputFileID:      file.ID,
		Endpoint:         openaibatch.EndpointChatCompletions,
		CompletionWindow: openaibatch.CompletionWindow24h,
		Metadata:         map[string]string{"origin": "llmkit-example"},
	})
	if err != nil {
		return fmt.Errorf("create batch: %w", err)
	}
	fmt.Printf("batch: %s status=%s\n", batch.ID, batch.Status)
	fmt.Printf("next: go run ./examples/batch -mode=status -batch=%s\n", batch.ID)
	return nil
}

// runStatus polls once. Batches settle within 24 hours; a scheduler of your
// own (or WaitBatch for short jobs) beats a day-long blocking call.
func runStatus(ctx context.Context, client *llmkit.Client, batchID string) error {
	if batchID == "" {
		return fmt.Errorf("-batch is required for -mode=status")
	}
	batch, err := client.RetrieveBatch(ctx, batchID)
	if err != nil {
		return err
	}
	fmt.Printf("status=%s", batch.Status)
	if batch.RequestCounts != nil {
		fmt.Printf(" total=%d completed=%d failed=%d",
			batch.RequestCounts.Total, batch.RequestCounts.Completed, batch.RequestCounts.Failed)
	}
	if batch.Usage != nil {
		fmt.Printf(" tokens=%d", batch.Usage.TotalTokens)
	}
	fmt.Println()
	if batch.OutputFileID != "" {
		fmt.Printf("output ready: go run ./examples/batch -mode=results -batch=%s\n", batch.ID)
	}
	return nil
}

// runResults downloads the output file and decodes it line by line. The
// upstream deletes output files 30 days after completion.
func runResults(ctx context.Context, client *llmkit.Client, batchID string) error {
	if batchID == "" {
		return fmt.Errorf("-batch is required for -mode=results")
	}
	batch, err := client.RetrieveBatch(ctx, batchID)
	if err != nil {
		return err
	}
	if batch.OutputFileID == "" {
		return fmt.Errorf("batch %s has no output file yet (status=%s)", batch.ID, batch.Status)
	}
	body, err := client.DownloadFileContent(ctx, batch.OutputFileID)
	if err != nil {
		return err
	}
	reader := openaibatch.NewOutputReader(body, 0)
	defer reader.Close()
	for {
		item, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if item.Error != nil {
			fmt.Printf("%s: error %s (%s)\n", item.CustomID, item.Error.Code, item.Error.Message)
			continue
		}
		fmt.Printf("%s: HTTP %d, %d bytes of response body\n",
			item.CustomID, item.Response.StatusCode, len(item.Response.Body))
	}
}

func runCancel(ctx context.Context, client *llmkit.Client, batchID string) error {
	if batchID == "" {
		return fmt.Errorf("-batch is required for -mode=cancel")
	}
	batch, err := client.CancelBatch(ctx, batchID)
	if err != nil {
		return err
	}
	fmt.Printf("status=%s (cancelling settles within ~10 minutes; partial results still bill)\n", batch.Status)
	return nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
