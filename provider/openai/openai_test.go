package openai

import (
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestIsReasoningModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// GPT-5 family
		{"gpt-5.5", true},
		{"gpt-5.4", true},
		{"gpt-5", true},
		{"gpt-5-mini", true},
		{"gpt-5-nano", true},
		{"gpt-5.2", true},
		{"gpt-5.2-codex", true},
		// o-series
		{"o1", true},
		{"o1-mini", true},
		{"o1-pro", true},
		{"o3", true},
		{"o3-mini", true},
		{"o4-mini", true},
		// Non-reasoning
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-4-turbo", false},
		{"gpt-3.5-turbo", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := isReasoningModel(tc.model); got != tc.want {
				t.Errorf("isReasoningModel(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestSanitizeForReasoning_StripsUnsupportedParams(t *testing.T) {
	temp := 0.7
	topP := 0.9
	pp := 0.5
	fp := 0.5
	yes := true
	logProbsN := 5
	n := 1

	in := &provider.ChatCompletionRequest{
		Messages:         []provider.Message{{Role: "user", Content: "hi"}},
		Temperature:      &temp,
		TopP:             &topP,
		PresencePenalty:  &pp,
		FrequencyPenalty: &fp,
		LogProbs:         &yes,
		TopLogProbs:      &logProbsN,
		N:                &n,
	}

	out := sanitizeForReasoning("gpt-5-mini", in)
	if out == in {
		t.Fatal("must return a copy when sanitizing, not the original pointer")
	}
	if out.Temperature != nil || out.TopP != nil || out.PresencePenalty != nil ||
		out.FrequencyPenalty != nil || out.LogProbs != nil || out.TopLogProbs != nil {
		t.Errorf("unsupported params not stripped: %+v", out)
	}
	if out.N == nil || *out.N != 1 {
		t.Errorf("N must be preserved (let upstream decide errors), got %v", out.N)
	}
	// 原始请求不能被改动
	if in.Temperature == nil {
		t.Error("original request was mutated; sanitize must copy")
	}
}

func TestSanitizeForReasoning_PassthroughForNonReasoningModel(t *testing.T) {
	temp := 0.7
	in := &provider.ChatCompletionRequest{
		Messages:    []provider.Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
	}
	out := sanitizeForReasoning("gpt-4o", in)
	if out != in {
		t.Error("non-reasoning model must return original pointer (no copy)")
	}
}

func TestSanitizeForReasoning_NoCopyWhenNothingToStrip(t *testing.T) {
	// 即使是 reasoning 模型，如果没有需要剥离的字段，也不要复制（性能 + 内存）
	in := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}
	out := sanitizeForReasoning("o1-mini", in)
	if out != in {
		t.Error("expected pass-through when nothing to strip")
	}
}

func TestSanitizeForReasoning_StripsLogitBias(t *testing.T) {
	// logit_bias is also rejected on reasoning models; sanitize must strip it.
	in := &provider.ChatCompletionRequest{
		Messages:  []provider.Message{{Role: "user", Content: "hi"}},
		LogitBias: map[string]int{"50256": -100},
	}
	out := sanitizeForReasoning("gpt-5", in)
	if out == in {
		t.Fatal("must copy when stripping logit_bias")
	}
	if out.LogitBias != nil {
		t.Errorf("logit_bias not stripped on reasoning model: %+v", out.LogitBias)
	}
}
