// Command anthropic-native demonstrates the three independent native
// Anthropic Messages calls supported by llmkit: synchronous create, SSE create,
// and server-side input-token count.
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
	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
)

func main() {
	log.SetFlags(0)
	mode := flag.String("mode", "sync", "operation to run: sync, stream, or tokens")
	model := flag.String("model", envOr("ANTHROPIC_MODEL", "claude-sonnet-4-5-20250929"), "Anthropic model")
	prompt := flag.String("prompt", "Explain CAP theorem in one concise paragraph.", "user message")
	maxTokens := flag.Int64("max-tokens", 256, "maximum generated tokens")
	flag.Parse()

	client, err := llmkit.New(llmkit.Anthropic)
	if err != nil {
		log.Fatal(err)
	}

	// Run exactly one operation per invocation. Message creation has no
	// SDK-level idempotency key: llmkit retries only failures proven safe to
	// replay, and a caller should not wrap it in a blanket retry loop.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch *mode {
	case "sync":
		err = runSync(ctx, client, *model, *prompt, *maxTokens)
	case "stream":
		err = runStream(ctx, client, *model, *prompt, *maxTokens)
	case "tokens":
		err = runTokenCount(ctx, client, *model, *prompt)
	default:
		err = fmt.Errorf("unknown -mode %q (want sync, stream, or tokens)", *mode)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runSync(ctx context.Context, client *llmkit.Client, model, prompt string, maxTokens int64) error {
	message, err := client.CreateAnthropicMessage(ctx, newMessageRequest(model, prompt, maxTokens))
	if err != nil {
		return err
	}
	fmt.Println(message.Text())
	fmt.Printf("stop_reason=%s input_tokens=%d output_tokens=%d request_id=%s\n",
		stopReason(message.StopReason), message.Usage.InputTokens, message.Usage.OutputTokens, message.RequestID)
	return nil
}

func runStream(ctx context.Context, client *llmkit.Client, model, prompt string, maxTokens int64) error {
	stream, err := client.CreateAnthropicMessageStream(ctx, newMessageRequest(model, prompt, maxTokens))
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
		if event.ContentBlockDelta != nil && event.ContentBlockDelta.Delta.Text != nil {
			fmt.Print(event.ContentBlockDelta.Delta.Text.Text)
		}
		if event.Error != nil {
			return event.Error.Error
		}
	}

	message := stream.FinalMessage()
	if message == nil {
		return errors.New("Anthropic stream ended without a final message")
	}
	fmt.Printf("\nstop_reason=%s request_id=%s\n", stopReason(message.StopReason), stream.RequestID())
	return nil
}

func runTokenCount(ctx context.Context, client *llmkit.Client, model, prompt string) error {
	count, err := client.CountAnthropicMessageTokens(ctx, &anthropicapi.TokenCountRequest{
		Model:    model,
		Messages: userTurns(prompt),
	})
	if err != nil {
		return err
	}
	fmt.Printf("input_tokens=%d request_id=%s\n", count.InputTokens, count.RequestID)
	return nil
}

func newMessageRequest(model, prompt string, maxTokens int64) *anthropicapi.MessageRequest {
	return &anthropicapi.MessageRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  userTurns(prompt),
	}
}

func userTurns(prompt string) []anthropicapi.MessageParam {
	return []anthropicapi.MessageParam{{
		Role:    anthropicapi.RoleUser,
		Content: anthropicapi.StringContent(prompt),
	}}
}

func stopReason(reason *anthropicapi.StopReason) string {
	if reason == nil {
		return ""
	}
	return string(*reason)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
