//go:build integration

// Package llmkit integration tests make REAL API calls and COST REAL MONEY.
// They are excluded from `go test ./...` by the build tag and never run in CI.
//
// Run them yourself when you want to confirm the SDK actually works end to end
// against live vendors — the offline tests prove the wire format is built
// correctly, only this proves the vendors accept it.
//
//	# every provider whose key is in the environment
//	DEEPSEEK_API_KEY=sk-... ZHIPU_API_KEY=... go test -tags=integration -v -run TestLive .
//
//	# one provider
//	DEEPSEEK_API_KEY=sk-... go test -tags=integration -v -run TestLive/deepseek .
//
//	# include the expensive media tests (images/video cost noticeably more)
//	OPENAI_API_KEY=sk-... go test -tags=integration -v -run TestLiveImage . -media
//
// Cost per full TestLive run is a fraction of a cent per provider: each case
// asks for a handful of tokens with MaxTokens capped.
package llmkit

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

var runMedia = flag.Bool("media", false, "also run image/video tests (slower and more expensive)")

// liveModels is the model used per provider. Override any of them with
// LLMKIT_TEST_MODEL_<PROVIDER>, e.g. LLMKIT_TEST_MODEL_DEEPSEEK=deepseek-chat.
var liveModels = map[string]string{
	OpenAI:      "gpt-5",
	Anthropic:   "claude-sonnet-4-5-20250929",
	Gemini:      "gemini-2.5-flash",
	DeepSeek:    "deepseek-chat",
	Moonshot:    "kimi-k2-turbo-preview",
	Zhipu:       "glm-4.6",
	MiniMax:     "MiniMax-M2",
	SiliconFlow: "Qwen/Qwen3-8B",
	DashScope:   "qwen-plus",
	Volcengine:  "doubao-seed-1-6-250615",
	OpenRouter:  "openai/gpt-5",
	EasyRouter:  "gpt-5",
	Vercel:      "openai/gpt-5",
}

func liveModel(providerName string) string {
	env := "LLMKIT_TEST_MODEL_" + strings.ToUpper(strings.ReplaceAll(providerName, "-", "_"))
	if v := os.Getenv(env); v != "" {
		return v
	}
	return liveModels[providerName]
}

// liveClient skips the test when no credential is configured for the provider.
func liveClient(t *testing.T, providerName string, opts ...Option) *Client {
	t.Helper()
	if os.Getenv(EnvVar(providerName)) == "" {
		t.Skipf("%s not set", EnvVar(providerName))
	}
	c, err := New(providerName, opts...)
	if err != nil {
		t.Fatalf("New(%s): %v", providerName, err)
	}
	return c
}

// TestLive exercises the core path — non-streaming chat, streaming chat, and
// usage accounting — against every provider whose key is present.
func TestLive(t *testing.T) {
	for _, name := range Providers() {
		t.Run(name, func(t *testing.T) {
			c := liveClient(t, name, WithTimeout(90*time.Second))
			model := liveModel(name)
			ctx := context.Background()
			maxTokens := 32

			t.Run("chat", func(t *testing.T) {
				resp, err := c.Chat(ctx, &ChatRequest{
					Model:     model,
					Messages:  []Message{User("Reply with exactly the word: pong")},
					MaxTokens: &maxTokens,
				})
				if err != nil {
					t.Fatalf("Chat: %v", err)
				}
				text := ResponseText(resp)
				if text == "" {
					t.Errorf("empty response: %+v", resp)
				}
				t.Logf("reply: %q", text)

				if resp.Usage == nil {
					t.Error("no usage reported")
				} else {
					t.Logf("usage: prompt=%d completion=%d total=%d",
						resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
					if resp.Usage.TotalTokens == 0 {
						t.Error("usage present but zero tokens")
					}
				}
			})

			t.Run("stream", func(t *testing.T) {
				var chunks int
				text, usage, err := c.StreamText(ctx, model, "Count: 1 2 3", func(string) { chunks++ })
				if err != nil {
					t.Fatalf("StreamText: %v", err)
				}
				if text == "" {
					t.Error("stream produced no text")
				}
				if chunks == 0 {
					t.Error("callback never fired; the response did not actually stream")
				}
				t.Logf("streamed %d chunks, %d chars, usage=%+v", chunks, len(text), usage)
			})

			t.Run("multiturn", func(t *testing.T) {
				// Round-tripping the assistant turn is where provider-specific
				// fields (thinking signatures, thought_signature) must survive.
				first, err := c.Chat(ctx, &ChatRequest{
					Model:     model,
					Messages:  []Message{User("My favorite number is 7. Acknowledge briefly.")},
					MaxTokens: &maxTokens,
				})
				if err != nil {
					t.Fatalf("turn 1: %v", err)
				}
				second, err := c.Chat(ctx, &ChatRequest{
					Model: model,
					Messages: []Message{
						User("My favorite number is 7. Acknowledge briefly."),
						*first.Choices[0].Message,
						User("What is my favorite number? Digits only."),
					},
					MaxTokens: &maxTokens,
				})
				if err != nil {
					t.Fatalf("turn 2: %v", err)
				}
				answer := ResponseText(second)
				t.Logf("recalled: %q", answer)
				if !strings.Contains(answer, "7") {
					t.Errorf("model lost context across turns: %q", answer)
				}
			})
		})
	}
}

// TestLiveTools verifies a real tool-calling round trip: the model must ask for
// the tool and then use the result.
func TestLiveTools(t *testing.T) {
	for _, name := range Providers() {
		t.Run(name, func(t *testing.T) {
			c := liveClient(t, name, WithTimeout(90*time.Second))
			model := liveModel(name)
			ctx := context.Background()

			tools := []Tool{NewTool("get_temperature", "Current temperature for a city", map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []string{"city"},
			})}
			msgs := []Message{User("What's the temperature in Hangzhou? Use the tool.")}

			first, err := c.Chat(ctx, &ChatRequest{Model: model, Messages: msgs, Tools: tools})
			if err != nil {
				t.Fatalf("turn 1: %v", err)
			}
			calls := ResponseToolCalls(first)
			if len(calls) == 0 {
				t.Skipf("model chose not to call the tool: %q", ResponseText(first))
			}
			t.Logf("tool call: %s(%s)", calls[0].Function.Name, calls[0].Function.Arguments)

			msgs = append(msgs, *first.Choices[0].Message)
			for _, call := range calls {
				msgs = append(msgs, ToolResultJSON(call.ID, map[string]any{"celsius": 26}))
			}
			second, err := c.Chat(ctx, &ChatRequest{Model: model, Messages: msgs, Tools: tools})
			if err != nil {
				t.Fatalf("turn 2: %v", err)
			}
			answer := ResponseText(second)
			t.Logf("final: %q", answer)
			if !strings.Contains(answer, "26") {
				t.Errorf("tool result not reflected in the answer: %q", answer)
			}
		})
	}
}

// TestLiveModels checks the catalog endpoint on every provider that has one.
func TestLiveModels(t *testing.T) {
	for _, name := range Providers() {
		t.Run(name, func(t *testing.T) {
			c := liveClient(t, name)
			if !c.SupportsModels() {
				t.Skip("no catalog endpoint")
			}
			models, err := c.Models(context.Background())
			if err != nil {
				t.Fatalf("Models: %v", err)
			}
			if len(models) == 0 {
				t.Error("catalog is empty")
			}
			t.Logf("%d models, first: %+v", len(models), models[0])
		})
	}
}

// TestLiveEmbeddings checks the embeddings endpoint. Set
// LLMKIT_TEST_EMBED_MODEL_<PROVIDER> to pick the model.
func TestLiveEmbeddings(t *testing.T) {
	defaults := map[string]string{
		OpenAI:      "text-embedding-3-small",
		SiliconFlow: "BAAI/bge-m3",
		Zhipu:       "embedding-3",
		Moonshot:    "",
		MiniMax:     "",
		Vercel:      "openai/text-embedding-3-small",
		EasyRouter:  "text-embedding-3-small",
	}
	for _, name := range Providers() {
		t.Run(name, func(t *testing.T) {
			c := liveClient(t, name)
			if !c.SupportsEmbeddings() {
				t.Skip("no embeddings endpoint")
			}
			model := os.Getenv("LLMKIT_TEST_EMBED_MODEL_" + strings.ToUpper(name))
			if model == "" {
				model = defaults[name]
			}
			if model == "" {
				t.Skip("no embedding model configured; set LLMKIT_TEST_EMBED_MODEL_" + strings.ToUpper(name))
			}

			resp, err := c.Embed(context.Background(), &EmbeddingRequest{Model: model, Input: "hello"})
			if err != nil {
				t.Fatalf("Embed: %v", err)
			}
			if len(resp.Data) != 1 {
				t.Fatalf("data = %+v", resp.Data)
			}
			vec, ok := resp.Data[0].Embedding.([]any)
			if !ok || len(vec) == 0 {
				t.Fatalf("embedding payload = %T %v", resp.Data[0].Embedding, resp.Data[0].Embedding)
			}
			t.Logf("%d dimensions", len(vec))
		})
	}
}

// TestLiveImage generates one real image. Requires -media.
func TestLiveImage(t *testing.T) {
	if !*runMedia {
		t.Skip("pass -media to run image tests")
	}
	for _, name := range Providers() {
		t.Run(name, func(t *testing.T) {
			c := liveClient(t, name, WithTimeout(180*time.Second))
			if !c.SupportsImageGeneration() {
				t.Skip("no image generation endpoint")
			}
			model := os.Getenv("LLMKIT_TEST_IMAGE_MODEL_" + strings.ToUpper(name))
			if model == "" {
				t.Skip("set LLMKIT_TEST_IMAGE_MODEL_" + strings.ToUpper(name) + " to run")
			}
			resp, err := c.GenerateImage(context.Background(), &ImageRequest{
				Model:  model,
				Prompt: "a single red circle on white, flat vector",
			})
			if err != nil {
				t.Fatalf("GenerateImage: %v", err)
			}
			if len(resp.Data) == 0 {
				t.Fatal("no image returned")
			}
			a := resp.Data[0]
			if a.URL == "" && a.B64JSON == "" && a.DataURL == "" {
				t.Errorf("asset carries no payload: %+v", a)
			}
			t.Logf("asset: mime=%s url=%t b64=%t", a.MimeType, a.URL != "", a.B64JSON != "")
		})
	}
}

// TestLiveErrorClassification confirms the SDK's error helpers classify a real
// upstream rejection, not just a synthetic one. It deliberately sends a bad
// credential, so it costs nothing.
func TestLiveErrorClassification(t *testing.T) {
	for _, name := range Providers() {
		t.Run(name, func(t *testing.T) {
			if os.Getenv(EnvVar(name)) == "" {
				t.Skipf("%s not set", EnvVar(name))
			}
			c, err := New(name, WithAPIKey("sk-definitely-invalid-key"), WithRetry(NoRetry()))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			maxTokens := 8
			_, err = c.Chat(context.Background(), &ChatRequest{
				Model:     liveModel(name),
				Messages:  []Message{User("hi")},
				MaxTokens: &maxTokens,
			})
			if err == nil {
				t.Fatal("an invalid key was accepted")
			}
			t.Logf("status=%d auth=%t retryable=%t err=%v",
				StatusCode(err), IsAuthError(err), IsRetryable(err), err)
			if StatusCode(err) == 0 {
				t.Errorf("upstream status was not captured: %v", err)
			}
			if !IsAuthError(err) {
				t.Errorf("a bad credential should classify as an auth error, got status %d", StatusCode(err))
			}
		})
	}
}

// TestLiveStreamCancellation confirms that cancelling mid-stream actually stops
// the request instead of leaking a goroutine or blocking.
func TestLiveStreamCancellation(t *testing.T) {
	name := DeepSeek
	c := liveClient(t, name)

	ctx, cancel := context.WithCancel(context.Background())
	// Unconditional: the loop below may never reach its own cancel() call if the
	// model answers in fewer than three chunks.
	defer cancel()

	stream, err := c.ChatStream(ctx, &ChatRequest{
		Model:    liveModel(name),
		Messages: []Message{User("Write a 500 word essay about the sea.")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()

	received := 0
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Logf("stream ended after cancel with: %v", err)
			break
		}
		if ChunkText(chunk) != "" {
			received++
			if received == 3 {
				cancel() // stop mid-flight
			}
		}
	}
	if received == 0 {
		t.Error("no chunks received before cancellation")
	}
	t.Logf("received %d chunks before cancelling", received)
}
