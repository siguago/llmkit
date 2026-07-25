// Command chat is the smallest possible llmkit program.
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/chat
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/siguago/llmkit"
)

func main() {
	// The key comes from DEEPSEEK_API_KEY when WithAPIKey is omitted.
	client, err := llmkit.New(llmkit.DeepSeek)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// One-liner form.
	answer, err := client.Say(ctx, "deepseek-chat", "用一句话解释 CAP 定理。")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Say:", answer)

	// Full form: multi-turn history, sampling controls, usage accounting.
	temp := 0.3
	maxTokens := 512
	resp, err := client.Chat(ctx, &llmkit.ChatRequest{
		Model: "deepseek-chat",
		Messages: []llmkit.Message{
			llmkit.System("你是一个简洁的技术助手，回答不超过三句话。"),
			llmkit.User("Go 的 channel 和 mutex 该怎么选？"),
		},
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("\nChat:", llmkit.ResponseText(resp))
	if u := resp.Usage; u != nil {
		fmt.Printf("tokens: prompt=%d completion=%d total=%d cached=%d\n",
			u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.CachedTokens)
	}
}
