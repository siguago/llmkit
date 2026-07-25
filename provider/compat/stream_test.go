package compat

import (
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestIsEmptyChunk_KeepsFirstRoleFrame(t *testing.T) {
	// OpenAI 规范首帧：{"role":"assistant","content":""}
	// 客户端 SDK 依赖该帧确定消息角色，必须保留。
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{
				Role:    "assistant",
				Content: "",
			},
		}},
	}
	if isEmptyChunk(chunk) {
		t.Fatalf("role-only first frame must not be filtered")
	}
}

func TestIsEmptyChunk_DropsPurelyEmptyDelta(t *testing.T) {
	// 无 role、无 content、无 tool_calls、无 finish_reason → 空 chunk，过滤。
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{Content: ""},
		}},
	}
	if !isEmptyChunk(chunk) {
		t.Fatalf("truly empty delta should be filtered")
	}
}

func TestIsEmptyChunk_KeepsRefusalFrame(t *testing.T) {
	// OpenAI sends `delta.refusal` (string) on safety refusals, often with no
	// content or tool_calls. Filtering as empty would silently drop the
	// refusal explanation.
	refusal := "I cannot help with that."
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{
				Refusal: &refusal,
			},
		}},
	}
	if isEmptyChunk(chunk) {
		t.Fatalf("refusal-only chunk must not be filtered")
	}
}

func TestIsEmptyChunk_KeepsReasoningSignatureFrame(t *testing.T) {
	// A chunk carrying only reasoning_content_signature is opaque but required
	// for round-tripping Anthropic extended thinking through compat layers.
	sig := "EqQB1234"
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{
				ReasoningContentSignature: &sig,
			},
		}},
	}
	if isEmptyChunk(chunk) {
		t.Fatalf("signature-only chunk must not be filtered")
	}
}

func TestIsEmptyChunk_KeepsContentFrame(t *testing.T) {
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{Content: "hello"},
		}},
	}
	if isEmptyChunk(chunk) {
		t.Fatalf("content frame must not be filtered")
	}
}
