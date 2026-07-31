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
//
// Vendor catalogs churn: models get retired and IDs get renamed. A "model not
// found" here means the entry went stale, not that the SDK broke — re-run with
// LLMKIT_TEST_MODEL_<PROVIDER>. TestLiveModelsCoverAllProviders keeps the table
// from falling behind the provider list.
var liveModels = map[string]string{
	OpenAI:      "gpt-5",
	Anthropic:   "claude-sonnet-4-5-20250929",
	Gemini:      "gemini-2.5-flash",
	XAI:         "grok-4.3",
	Mistral:     "mistral-large-latest", // vendor-maintained alias
	DeepSeek:    "deepseek-chat",
	Moonshot:    "kimi-k2.6", // the k2-*-preview family retired 2026-05-25
	Zhipu:       "glm-4.6",
	MiniMax:     "MiniMax-M2",
	SiliconFlow: "Qwen/Qwen3-8B",
	DashScope:   "qwen-plus",
	Volcengine:  "doubao-seed-1-6-250615",
	Groq:        "openai/gpt-oss-120b",
	Together:    "openai/gpt-oss-120b",
	Fireworks:   "accounts/fireworks/models/gpt-oss-120b",
	Cerebras:    "gpt-oss-120b",
	// Local runtimes serve whatever you pulled or launched, so these are a
	// plausible first guess rather than a catalog entry.
	Ollama:     "llama3.2",
	VLLM:       "Qwen/Qwen3-8B",
	OpenRouter: "openai/gpt-5",
	EasyRouter: "gpt-5",
	Vercel:     "openai/gpt-5",
}

func liveModel(providerName string) string {
	env := "LLMKIT_TEST_MODEL_" + strings.ToUpper(strings.ReplaceAll(providerName, "-", "_"))
	if v := os.Getenv(env); v != "" {
		return v
	}
	return liveModels[providerName]
}

// A provider with no liveModels entry would otherwise be tested with Model: "",
// which every vendor rejects — reported as a chat failure rather than as the
// missing table entry it is. Fail once, up front, naming the gap.
func TestLiveModelsCoverAllProviders(t *testing.T) {
	for _, name := range Providers() {
		if liveModels[name] == "" {
			t.Errorf("liveModels has no entry for %q — add one so TestLive/%s tests something", name, name)
		}
	}
}

// liveEmbedModels and liveRerankModels are the same idea for the non-chat
// routes, and need their own guard for the same reason: TestLiveEmbeddings and
// TestLiveRerank Skip when a provider has no entry, so a capability can be
// declared and never actually exercised against the vendor — the table stays
// quiet about what it is not covering.
//
// That is not hypothetical. Gemini gained embeddings without an entry here and
// silently skipped; writing this guard is what surfaced it, along with six
// other providers whose embeddings claim had never been verified live.
var (
	liveEmbedModels = map[string]string{
		OpenAI:      "text-embedding-3-small",
		Gemini:      "gemini-embedding-001",
		SiliconFlow: "BAAI/bge-m3",
		Zhipu:       "embedding-3",
		Mistral:     "mistral-embed",
		MiniMax:     "embo-01",
		DashScope:   "text-embedding-v4",
		Together:    "BAAI/bge-base-en-v1.5",
		Fireworks:   "nomic-ai/nomic-embed-text-v1.5",
		Ollama:      "nomic-embed-text",
		Vercel:      "openai/text-embedding-3-small",
		EasyRouter:  "text-embedding-3-small",
	}
	liveRerankModels = map[string]string{
		SiliconFlow: "BAAI/bge-reranker-v2-m3",
	}
	// A vLLM process serves exactly one model, so no default is guessable —
	// set LLMKIT_TEST_EMBED_MODEL_VLLM to whatever you started it with.
	liveEmbedUnknown = map[string]bool{VLLM: true}
)

func TestLiveEmbedModelsCoverEmbedders(t *testing.T) {
	for _, name := range Providers() {
		c, err := New(name, WithAPIKey("list-only-placeholder"))
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if !c.SupportsEmbeddings() {
			if liveEmbedModels[name] != "" {
				t.Errorf("liveEmbedModels has an entry for %q, which does not implement Embedder", name)
			}
			continue
		}
		if liveEmbedModels[name] == "" && !liveEmbedUnknown[name] {
			t.Errorf("%q claims embeddings but has no live model — add one to liveEmbedModels, "+
				"or record why not in liveEmbedUnknown", name)
		}
	}
}

func TestLiveRerankModelsCoverRerankers(t *testing.T) {
	for _, name := range Providers() {
		c, err := New(name, WithAPIKey("list-only-placeholder"))
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if !c.SupportsRerank() {
			if liveRerankModels[name] != "" {
				t.Errorf("liveRerankModels has an entry for %q, which does not implement Reranker", name)
			}
			continue
		}
		if liveRerankModels[name] == "" {
			t.Errorf("%q claims rerank but has no live model — add one to liveRerankModels", name)
		}
	}
}

// liveClient skips the test when the provider isn't configured to be reached.
//
// Providers that need no credential (Ollama, vLLM) can't be gated on one, and
// nothing observable says whether a local runtime is up. They opt in through
// LLMKIT_TEST_MODEL_<PROVIDER> instead, which you have to set anyway to name the
// model you actually pulled or launched.
func liveClient(t *testing.T, providerName string, opts ...Option) *Client {
	t.Helper()
	if KeyOptional(providerName) {
		env := "LLMKIT_TEST_MODEL_" + strings.ToUpper(strings.ReplaceAll(providerName, "-", "_"))
		if os.Getenv(env) == "" {
			t.Skipf("%s not set (local runtime; set it to the model you are serving)", env)
		}
	} else if os.Getenv(EnvVar(providerName)) == "" {
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
	defaults := liveEmbedModels
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

// TestLiveRerank checks the rerank endpoint against a real vendor.
//
// The assertion is on the ranking, not just on getting a response: a broken
// integration can echo the input order with flat scores and look healthy. The
// relevant document is sent last, so a passthrough cannot accidentally pass.
func TestLiveRerank(t *testing.T) {
	defaults := liveRerankModels
	for _, name := range Providers() {
		t.Run(name, func(t *testing.T) {
			c := liveClient(t, name)
			if !c.SupportsRerank() {
				t.Skip("no rerank endpoint")
			}
			model := os.Getenv("LLMKIT_TEST_RERANK_MODEL_" + strings.ToUpper(name))
			if model == "" {
				model = defaults[name]
			}
			if model == "" {
				t.Skip("no rerank model configured; set LLMKIT_TEST_RERANK_MODEL_" + strings.ToUpper(name))
			}

			resp, err := c.Rerank(context.Background(), &RerankRequest{
				Model: model,
				Query: "什么是熊猫？",
				Documents: []string{
					"苹果是一种常见的水果。",
					"汽车通常有四个轮子。",
					"熊猫是中国特有的哺乳动物，以竹子为食。",
				},
			})
			if err != nil {
				t.Fatalf("Rerank: %v", err)
			}
			if len(resp.Results) == 0 {
				t.Fatal("no results")
			}
			if got := resp.Results[0].Index; got != 2 {
				t.Errorf("top result index = %d, want 2 — the reranker did not identify the relevant document", got)
			}
			t.Logf("top: index=%d score=%.4f", resp.Results[0].Index, resp.Results[0].RelevanceScore)
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
