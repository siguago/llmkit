// Command responses demonstrates the three independent OpenAI Responses calls
// supported by llmkit: synchronous create, SSE create, and input-token count.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/siguago/llmkit"
	responsesapi "github.com/siguago/llmkit/protocol/responses"
)

func main() {
	log.SetFlags(0)
	mode := flag.String("mode", "sync", "operation to run: sync, stream, or tokens")
	model := flag.String("model", envOr("OPENAI_RESPONSES_MODEL", "gpt-5-mini"), "OpenAI model")
	prompt := flag.String("prompt", "Explain CAP theorem in one concise paragraph.", "input text")
	flag.Parse()

	client, err := llmkit.New(llmkit.OpenAI)
	if err != nil {
		log.Fatal(err)
	}

	// Run exactly one operation per invocation. A create has no SDK-level
	// idempotency key: llmkit retries only failures proven safe to replay, and a
	// caller should not wrap it in a blanket retry loop.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch *mode {
	case "sync":
		err = runSync(ctx, client, *model, *prompt)
	case "stream":
		err = runStream(ctx, client, *model, *prompt)
	case "tokens":
		err = runTokenCount(ctx, client, *model, *prompt)
	default:
		err = fmt.Errorf("unknown -mode %q (want sync, stream, or tokens)", *mode)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runSync(ctx context.Context, client *llmkit.Client, model, prompt string) error {
	response, err := client.CreateResponse(ctx, newCreateRequest(model, prompt))
	if err != nil {
		return err
	}
	fmt.Println(response.OutputText())
	printResponseState(response, response.RequestID)
	return nil
}

func runStream(ctx context.Context, client *llmkit.Client, model, prompt string) error {
	stream, err := client.CreateResponseStream(ctx, newCreateRequest(model, prompt))
	if err != nil {
		return err
	}
	defer stream.Close()

	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if event.OutputTextDelta != nil {
			fmt.Print(event.OutputTextDelta.Delta)
		}
		if event.Error != nil {
			return event.Error
		}
	}

	response := stream.FinalResponse()
	if response == nil {
		return errors.New("Responses stream ended without a final response")
	}
	fmt.Println()
	printResponseState(response, stream.RequestID())
	return nil
}

func runTokenCount(ctx context.Context, client *llmkit.Client, model, prompt string) error {
	count, err := client.CountResponseInputTokens(ctx, &responsesapi.TokenCountRequest{
		Model: model,
		Input: responsesapi.NewTextInput(prompt),
	})
	if err != nil {
		return err
	}
	fmt.Printf("input_tokens=%d request_id=%s\n", count.InputTokens, count.RequestID)
	return nil
}

func newCreateRequest(model, prompt string) *responsesapi.CreateRequest {
	// llmkit deliberately preserves the upstream default when Store is nil.
	// This example does not need server-side retention, so make the choice
	// explicit. Set it according to your own retention policy.
	store := false
	return &responsesapi.CreateRequest{
		Model: model,
		Input: responsesapi.NewTextInput(prompt),
		Store: &store,
	}
}

func printResponseState(response *responsesapi.Response, requestID string) {
	fmt.Printf("status=%s request_id=%s\n", response.Status, requestID)
	if response.Error != nil {
		fmt.Printf("response_error=%v\n", response.Error)
	}
	if response.IncompleteDetails != nil {
		fmt.Printf("incomplete_reason=%s\n", response.IncompleteDetails.Reason)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
