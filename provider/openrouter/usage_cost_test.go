package openrouter

import (
	"encoding/json"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// OpenRouter returns the actual cross-vendor cost in usage.cost. The unified
// Usage struct now carries a Cost field that omitempty-hides on providers
// that don't emit it, so OpenRouter responses pass through transparently.
func TestUsage_OpenRouterCostFieldRoundtrips(t *testing.T) {
	raw := []byte(`{
        "id": "gen-abc",
        "object": "chat.completion",
        "model": "anthropic/claude-sonnet-4-5",
        "choices": [{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
        "usage": {
            "prompt_tokens": 10,
            "completion_tokens": 5,
            "total_tokens": 15,
            "cost": 0.000234
        }
    }`)
	var resp provider.ChatCompletionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("usage missing")
	}
	if resp.Usage.Cost != 0.000234 {
		t.Errorf("usage.cost = %v, want 0.000234 — OpenRouter cost passthrough broken", resp.Usage.Cost)
	}
}

func TestUsage_NoCostOmitsOnSerialize(t *testing.T) {
	// Providers that don't emit cost (OpenAI, Anthropic native, etc.) must
	// not surface a "cost":0 in the response — that would mislead clients
	// into thinking the request was free.
	u := &provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	bytes, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(bytes); got != `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}` {
		t.Errorf("unexpected serialization: %s", got)
	}
}
