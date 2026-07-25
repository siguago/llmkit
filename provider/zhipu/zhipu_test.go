package zhipu

import (
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestTransform_KeepsThinkingEnabled(t *testing.T) {
	in := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "enabled", BudgetTokens: 1024},
	}
	out := transform(in)
	if out != in {
		t.Fatal("thinking-only requests should pass through unchanged")
	}
	if out.Thinking == nil || out.Thinking.Type != "enabled" {
		t.Fatalf("thinking should be preserved for current Zhipu GLM models: %#v", out.Thinking)
	}
}

func TestTransform_KeepsThinkingDisabled(t *testing.T) {
	in := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "disabled"},
	}
	out := transform(in)
	if out != in {
		t.Fatal("thinking-only requests should pass through unchanged")
	}
	if out.Thinking == nil || out.Thinking.Type != "disabled" {
		t.Fatalf("thinking should be preserved for current Zhipu GLM models: %#v", out.Thinking)
	}
}

func TestTransform_NoThinkingPassesThrough(t *testing.T) {
	in := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
	}
	if got := transform(in); got != in {
		t.Error("expected pass-through when no thinking field present")
	}
}

func TestTransform_ThinkingWinsOverLegacyEnableThinking(t *testing.T) {
	// 当前官方接口使用 thinking.type；客户端同时传旧 enable_thinking 时只剥掉旧字段，
	// 避免上游收到两个互相冲突的开关。
	yes := true
	in := &provider.ChatCompletionRequest{
		Messages:       []provider.Message{{Role: "user", Content: "x"}},
		Thinking:       &provider.ThinkingConfig{Type: "disabled"},
		EnableThinking: &yes,
	}
	out := transform(in)
	if out == in {
		t.Fatal("must return a copy when dropping enable_thinking")
	}
	if out.Thinking == nil || out.Thinking.Type != "disabled" {
		t.Fatalf("Thinking should be preserved: %#v", out.Thinking)
	}
	if out.EnableThinking != nil {
		t.Fatalf("EnableThinking must be stripped to avoid sending both fields: %#v", out.EnableThinking)
	}
	if in.EnableThinking == nil {
		t.Error("original request was mutated; transform must copy")
	}
}
