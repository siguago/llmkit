package moonshot

import (
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestIsK2ThinkingModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// K2.6 family
		{"kimi-k2.6", true},
		{"kimi-k2-6", true},
		{"kimi-k2-6-preview", true},
		{"kimi-k2-thinking", true},
		// older K2 / K2.5 — leave alone (legacy enable_thinking still works)
		{"kimi-k2", false},
		{"kimi-k2.5", false},
		{"kimi-k2-0905-preview", false},
		// Other moonshot models
		{"moonshot-v1-8k", false},
		{"moonshot-v1-128k", false},
		// Vendor-prefixed (e.g. via an external router routing through moonshot)
		{"moonshotai/kimi-k2-thinking", true},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := isK2ThinkingModel(tc.model); got != tc.want {
				t.Errorf("isK2ThinkingModel(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestTransform_K2ThinkingConvertsToChatTemplateKwargs(t *testing.T) {
	in := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "enabled", BudgetTokens: 2048},
	}
	out := transform("kimi-k2-thinking", in)
	if out == in {
		t.Fatal("must return a copy when transforming")
	}
	if out.Thinking != nil {
		t.Error("Thinking must be stripped after transform; K2.6 doesn't accept it")
	}
	m, ok := out.ChatTemplateKwargs.(map[string]any)
	if !ok {
		t.Fatalf("ChatTemplateKwargs = %T, want map[string]any", out.ChatTemplateKwargs)
	}
	if m["thinking"] != true {
		t.Errorf("chat_template_kwargs.thinking = %v, want true", m["thinking"])
	}
	if in.Thinking == nil {
		t.Error("original request mutated; transform must copy")
	}
}

func TestTransform_NonK2ModelPassesThrough(t *testing.T) {
	in := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "enabled", BudgetTokens: 2048},
	}
	if got := transform("moonshot-v1-32k", in); got != in {
		t.Error("non-K2.6 model must pass through; legacy enable_thinking handled by other layers")
	}
}

func TestTransform_K2ThinkingForwardsKeepFlag(t *testing.T) {
	// K2.6's thinking.keep parameter controls whether prior-turn reasoning
	// is preserved across turns. Forward it under chat_template_kwargs.
	keep := true
	in := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "enabled", BudgetTokens: 1024, Keep: &keep},
	}
	out := transform("kimi-k2-thinking", in)
	m, _ := out.ChatTemplateKwargs.(map[string]any)
	if m["thinking"] != true {
		t.Errorf("thinking flag missing: %+v", m)
	}
	if m["thinking_keep"] != true {
		t.Errorf("thinking_keep not forwarded: %+v", m)
	}
}

func TestTransform_K2ThinkingKeepFalseForwarded(t *testing.T) {
	keep := false
	in := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "enabled", Keep: &keep},
	}
	out := transform("kimi-k2-thinking", in)
	m, _ := out.ChatTemplateKwargs.(map[string]any)
	if m["thinking_keep"] != false {
		t.Errorf("thinking_keep=false must be forwarded explicitly, got %+v", m)
	}
}

func TestTransform_RespectsExplicitChatTemplateKwargs(t *testing.T) {
	in := &provider.ChatCompletionRequest{
		Messages:           []provider.Message{{Role: "user", Content: "x"}},
		Thinking:           &provider.ThinkingConfig{Type: "enabled"},
		ChatTemplateKwargs: map[string]any{"thinking": false, "tools_in_user_message": true},
	}
	out := transform("kimi-k2-thinking", in)
	if out.Thinking != nil {
		t.Error("Thinking must be stripped to avoid sending both fields")
	}
	m, _ := out.ChatTemplateKwargs.(map[string]any)
	if m["thinking"] != false {
		t.Error("must not overwrite explicit chat_template_kwargs.thinking")
	}
}
