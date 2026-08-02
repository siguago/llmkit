// Command stream shows both streaming styles: the callback shortcut and the
// raw chunk loop.
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/stream
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/siguago/llmkit"
)

func main() {
	client, err := llmkit.New(llmkit.DeepSeek)
	if err != nil {
		log.Fatal(err)
	}
	// A stream can remain active indefinitely on adapters without a fixed client
	// ceiling. Give the whole example a finite budget and propagate it to both
	// streaming styles.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Style 1: callback. The stream is drained and closed for you.
	fmt.Println("--- StreamText ---")
	_, usage, err := client.StreamText(ctx, "deepseek-v4-flash", "数到五，每个数字一行。",
		func(delta string) { fmt.Print(delta) })
	if err != nil {
		log.Fatal(err)
	}
	if usage != nil {
		fmt.Printf("\n[%d tokens]\n", usage.TotalTokens)
	}

	// Style 2: raw chunks — needed when you care about reasoning traces, tool
	// call deltas, or finish reasons.
	fmt.Println("\n--- raw chunks ---")
	stream, err := client.ChatStream(ctx, &llmkit.ChatRequest{
		Model:    "deepseek-v4-pro",
		Messages: []llmkit.Message{llmkit.User("13 和 17 哪个更接近 15？只回答数字。")},
		// Ask upstream to report usage on the final chunk.
		StreamOptions: &llmkit.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	inReasoning := false
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			log.Fatalf("\nstream: %v", err)
		}

		// Reasoning models emit their thinking on a separate channel.
		if r := llmkit.ChunkReasoning(chunk); r != "" {
			if !inReasoning {
				fmt.Fprint(os.Stderr, "\n[thinking] ")
				inReasoning = true
			}
			fmt.Fprint(os.Stderr, r)
			continue
		}
		if text := llmkit.ChunkText(chunk); text != "" {
			if inReasoning {
				fmt.Fprintln(os.Stderr)
				inReasoning = false
			}
			fmt.Print(text)
		}
	}
	fmt.Println()

	if u := stream.GetUsage(); u != nil {
		fmt.Printf("[reasoning tokens: %d, total: %d]\n", u.ReasoningTokens, u.TotalTokens)
	}
}
