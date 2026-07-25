package compat

import (
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestLiftReasoningOnResponse_ShorterReasoningField(t *testing.T) {
	// Kimi K2.6 returns message.reasoning instead of message.reasoning_content.
	// The compat layer should lift it so all downstream consumers see the
	// unified reasoning_content field.
	raw := []byte(`{
        "choices": [{
            "index": 0,
            "message": {
                "role": "assistant",
                "content": "Final answer.",
                "reasoning": "Let me think..."
            },
            "finish_reason": "stop"
        }]
    }`)
	resp := &provider.ChatCompletionResponse{
		Choices: []provider.Choice{{
			Index: 0,
			Message: &provider.Message{
				Role:    "assistant",
				Content: "Final answer.",
			},
		}},
	}
	liftReasoningOnResponse(raw, resp)
	if resp.Choices[0].Message.ReasoningContent == nil {
		t.Fatal("reasoning_content not lifted from reasoning field")
	}
	if *resp.Choices[0].Message.ReasoningContent != "Let me think..." {
		t.Errorf("reasoning_content = %q, want %q",
			*resp.Choices[0].Message.ReasoningContent, "Let me think...")
	}
}

func TestLiftReasoningOnResponse_DoesNotOverrideExistingField(t *testing.T) {
	// When reasoning_content is already populated (DeepSeek / o-series), we
	// must not overwrite it with the shorter alias.
	raw := []byte(`{
        "choices": [{
            "message": {
                "reasoning_content": "primary thinking",
                "reasoning": "duplicate alias"
            }
        }]
    }`)
	primary := "primary thinking"
	resp := &provider.ChatCompletionResponse{
		Choices: []provider.Choice{{
			Message: &provider.Message{
				ReasoningContent: &primary,
			},
		}},
	}
	liftReasoningOnResponse(raw, resp)
	if *resp.Choices[0].Message.ReasoningContent != "primary thinking" {
		t.Errorf("must not overwrite existing reasoning_content; got %q",
			*resp.Choices[0].Message.ReasoningContent)
	}
}

func TestLiftReasoningOnChunk_Delta(t *testing.T) {
	raw := []byte(`{"choices":[{"index":0,"delta":{"reasoning":"thinking..."}}]}`)
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{},
		}},
	}
	liftReasoningOnChunk(raw, chunk)
	if chunk.Choices[0].Delta.ReasoningContent == nil ||
		*chunk.Choices[0].Delta.ReasoningContent != "thinking..." {
		t.Errorf("delta reasoning_content not lifted: %+v", chunk.Choices[0].Delta)
	}
}

func TestLiftReasoningOnResponse_SkipsEmptyString(t *testing.T) {
	// Some upstreams emit "reasoning":"" alongside content; we must NOT
	// surface an empty reasoning_content (which clients render as a blank
	// "thinking" panel).
	raw := []byte(`{"choices":[{"message":{"reasoning":""}}]}`)
	resp := &provider.ChatCompletionResponse{
		Choices: []provider.Choice{{Message: &provider.Message{}}},
	}
	liftReasoningOnResponse(raw, resp)
	if resp.Choices[0].Message.ReasoningContent != nil {
		t.Errorf("empty reasoning string must be skipped, got %q",
			*resp.Choices[0].Message.ReasoningContent)
	}
}

func TestLiftReasoningOnChunk_SkipsEmptyString(t *testing.T) {
	raw := []byte(`{"choices":[{"index":0,"delta":{"reasoning":""}}]}`)
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{Index: 0, Delta: &provider.Message{}}},
	}
	liftReasoningOnChunk(raw, chunk)
	if chunk.Choices[0].Delta.ReasoningContent != nil {
		t.Errorf("empty reasoning chunk must be skipped, got %q",
			*chunk.Choices[0].Delta.ReasoningContent)
	}
}
