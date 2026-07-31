package llmkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// SiliconFlow is the one bundled adapter with a rerank route, so it is the only
// path through Client.Rerank's Reranker assertion.
//
// The request body here mirrors the "rerank" example in README.md — if that
// example stops working, this fails.
func TestRerank_DocumentedExample(t *testing.T) {
	var body map[string]any
	c := newTestClientFor(t, SiliconFlow, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rerank") {
			t.Errorf("path = %s, want a /rerank endpoint", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"results": [
				{"index": 2, "relevance_score": 0.98},
				{"index": 0, "relevance_score": 0.31}
			],
			"tokens": {"input_tokens": 42, "output_tokens": 0}
		}`)
	})

	topN := 2
	resp, err := c.Rerank(context.Background(), &RerankRequest{
		Model:     "BAAI/bge-reranker-v2-m3",
		Query:     "什么是熊猫？",
		Documents: []string{"苹果是一种水果", "汽车有四个轮子", "熊猫是中国特有的哺乳动物"},
		TopN:      &topN,
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}

	if body["query"] != "什么是熊猫？" {
		t.Errorf("query not forwarded: %v", body)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	// The whole point of the call: the third document is the answer, and it
	// comes back first even though it was sent last.
	if resp.Results[0].Index != 2 {
		t.Errorf("top result index = %d, want 2 (the panda sentence)", resp.Results[0].Index)
	}
	if resp.Results[0].RelevanceScore <= resp.Results[1].RelevanceScore {
		t.Errorf("results are not in descending score order: %+v", resp.Results)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 42 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

// A provider without the route must say so before the call, and the call itself
// must fail with the same error the probe predicts. The two have to agree —
// a probe that says "unsupported" and a call that half-works is the failure
// mode the split capability interfaces exist to prevent.
func TestRerank_UnsupportedProvider(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a provider without a rerank route must not send a request")
	})

	if c.SupportsRerank() {
		t.Error("deepseek has no rerank route; SupportsRerank must be false")
	}
	_, err := c.Rerank(context.Background(), &RerankRequest{
		Model: "m", Query: "q", Documents: []string{"a"},
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
	if !IsUnsupportedCapability(err) {
		t.Errorf("IsUnsupportedCapability should agree: %v", err)
	}
}

func TestSupportsRerank(t *testing.T) {
	// Rerank is not part of the OpenAI API, so riding the compat layer must not
	// confer it — only an adapter that opted in via compat.NewWithRerank.
	for name, want := range map[string]bool{
		SiliconFlow: true,
		DeepSeek:    false,
		OpenAI:      false,
		Zhipu:       false,
		Gemini:      false,
		Anthropic:   false,
	} {
		c, err := New(name, WithAPIKey("sk-test"))
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if got := c.SupportsRerank(); got != want {
			t.Errorf("%s SupportsRerank() = %v, want %v", name, got, want)
		}
	}
}

func TestRerank_ValidatesRequest(t *testing.T) {
	c := newTestClientFor(t, SiliconFlow, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an invalid request must not reach the upstream")
	})

	if _, err := c.Rerank(context.Background(), nil); err == nil {
		t.Error("expected an error for a nil request")
	}
	if _, err := c.Rerank(context.Background(), &RerankRequest{
		Query: "q", Documents: []string{"a"},
	}); err == nil {
		t.Error("expected an error for a missing model")
	}
}
