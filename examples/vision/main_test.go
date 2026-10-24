package main

import (
	"testing"

	"github.com/siguago/llmkit"
)

// The default used to be Gemini's model ID no matter what -provider said, so
// `-provider deepseek` asked DeepSeek for a model it does not serve. The default
// must follow the provider, and a provider without a known default must fail
// asking for -model instead of guessing.
func TestDefaultVisionModel(t *testing.T) {
	for _, tt := range []struct {
		provider string
		want     string
		wantErr  bool
	}{
		{llmkit.Gemini, "gemini-2.5-flash", false},
		{llmkit.DeepSeek, "deepseek-flash", false},
		{llmkit.OpenAI, "", true},
		{llmkit.Mistral, "", true},
		{"not-a-provider", "", true},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			got, err := defaultVisionModel(tt.provider)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("defaultVisionModel(%q) = %q, want an error requiring -model", tt.provider, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("defaultVisionModel(%q): %v", tt.provider, err)
			}
			if got != tt.want {
				t.Errorf("defaultVisionModel(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}
