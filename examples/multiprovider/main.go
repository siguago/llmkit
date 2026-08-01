// Command multiprovider runs the same prompt across every provider whose API
// key is present in the environment — the point of a unified SDK.
//
//	DEEPSEEK_API_KEY=... ZHIPU_API_KEY=... go run ./examples/multiprovider
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/siguago/llmkit"
)

// One representative model per provider. Swap these for whatever you actually
// have access to.
var models = map[string]string{
	llmkit.OpenAI:      "gpt-5",
	llmkit.Anthropic:   "claude-sonnet-4-5-20250929",
	llmkit.Gemini:      "gemini-2.5-flash",
	llmkit.DeepSeek:    "deepseek-v4-flash",
	llmkit.Moonshot:    "kimi-k2-turbo-preview",
	llmkit.Zhipu:       "glm-4.6",
	llmkit.MiniMax:     "MiniMax-M2",
	llmkit.SiliconFlow: "Qwen/Qwen3-8B",
	llmkit.DashScope:   "qwen-plus",
	llmkit.Volcengine:  "doubao-seed-1-6-250615",
	llmkit.OpenRouter:  "openai/gpt-5",
	llmkit.EasyRouter:  "gpt-5",
	llmkit.Vercel:      "openai/gpt-5",
}

const prompt = "用一句话说明什么是幂等性。"

type result struct {
	provider string
	text     string
	elapsed  time.Duration
	err      error
}

func main() {
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make(chan result, len(models))

	for _, name := range llmkit.Providers() {
		// Skip providers with no credential configured.
		if os.Getenv(llmkit.EnvVar(name)) == "" {
			continue
		}
		model, ok := models[name]
		if !ok {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			// Same code, every vendor. Only the client differs.
			client, err := llmkit.New(name, llmkit.WithTimeout(60*time.Second))
			if err != nil {
				results <- result{provider: name, err: err}
				return
			}
			start := time.Now()
			text, err := client.Say(ctx, model, prompt)
			results <- result{provider: name, text: text, elapsed: time.Since(start), err: err}
		}()
	}

	wg.Wait()
	close(results)

	any := false
	for r := range results {
		any = true
		if r.err != nil {
			fmt.Printf("✗ %-12s %s\n", r.provider, describe(r.err))
			continue
		}
		fmt.Printf("✓ %-12s (%s) %s\n", r.provider, r.elapsed.Round(time.Millisecond), r.text)
	}
	if !any {
		fmt.Println("no provider API keys found in the environment; set one of:")
		for _, name := range llmkit.Providers() {
			fmt.Printf("  %s=%s\n", llmkit.EnvVar(name), "...")
		}
	}
}

// describe turns an error into an actionable one-liner using the SDK's
// classification helpers.
func describe(err error) string {
	switch {
	case llmkit.IsAuthError(err):
		return "credential rejected — check the API key"
	case llmkit.IsRateLimited(err):
		if d := llmkit.RetryAfter(err); d > 0 {
			return fmt.Sprintf("rate limited, retry after %s", d)
		}
		return "rate limited"
	case llmkit.IsNotFound(err):
		return "model not found — is it enabled on this account?"
	case llmkit.IsInvalidRequest(err):
		return "request rejected: " + err.Error()
	case errors.Is(err, llmkit.ErrUnsupported):
		return "capability unsupported by this provider"
	default:
		return err.Error()
	}
}
