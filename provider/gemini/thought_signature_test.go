package gemini

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestConvertResponse_CapturesThoughtSignature(t *testing.T) {
	// Gemini 2.5+ / 3.x emit thought_signature on assistant parts when
	// thinking is active. The gateway must preserve it so the client can
	// echo it back on the next turn — Gemini 3 returns 400 otherwise during
	// multi-turn function calling.
	thought := true
	resp := &response{
		Candidates: []candidate{{
			Content: &content{
				Parts: []part{
					{Thought: &thought, Text: "thinking..."},
					{Text: "Final answer.", ThoughtSignature: "OPAQUE_SIG_BLOB"},
				},
			},
			FinishReason: "STOP",
		}},
	}
	out := convertResponse(resp, "gemini-3-pro")
	if len(out.Choices) == 0 || out.Choices[0].Message == nil {
		t.Fatal("response has no message")
	}
	msg := out.Choices[0].Message
	if msg.ThoughtSignature == nil || *msg.ThoughtSignature != "OPAQUE_SIG_BLOB" {
		t.Errorf("thought_signature lost: %+v", msg.ThoughtSignature)
	}
}

func TestConvertResponse_PrefersFirstNonEmptySignature(t *testing.T) {
	// When multiple parts carry signatures, OpenAI message envelope only has
	// one slot; the leading non-empty one wins. (Empty leading ones are
	// skipped — Gemini sometimes emits empty strings on text-only parts.)
	resp := &response{
		Candidates: []candidate{{
			Content: &content{
				Parts: []part{
					{Text: "intro"},
					{Text: "main", ThoughtSignature: "FIRST"},
					{Text: "tail", ThoughtSignature: "SECOND"},
				},
			},
			FinishReason: "STOP",
		}},
	}
	out := convertResponse(resp, "gemini-3-pro")
	if *out.Choices[0].Message.ThoughtSignature != "FIRST" {
		t.Errorf("must capture leading non-empty signature, got %q",
			*out.Choices[0].Message.ThoughtSignature)
	}
}

func TestBuildRequest_ThoughtSignatureRoundTripsToFirstAssistantPart(t *testing.T) {
	// Multi-turn round-trip: client passes back the assistant message with
	// ThoughtSignature; we land it on the first model-role part.
	sig := "OPAQUE_SIG_BLOB"
	r, err := buildRequest(context.Background(), &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "first turn"},
			{Role: "assistant", Content: "I thought about it.", ThoughtSignature: &sig},
			{Role: "user", Content: "follow-up"},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	// Find the model-role content in the conversation.
	var modelContent *content
	for i := range r.Contents {
		if r.Contents[i].Role == "model" {
			modelContent = &r.Contents[i]
			break
		}
	}
	if modelContent == nil {
		t.Fatal("no model role content emitted")
	}
	if len(modelContent.Parts) == 0 {
		t.Fatal("model content has no parts")
	}
	if modelContent.Parts[0].ThoughtSignature != "OPAQUE_SIG_BLOB" {
		t.Errorf("thought_signature not attached to first model part: %+v", modelContent.Parts[0])
	}

	// Wire-level: the field must serialize as `thoughtSignature` (Gemini's
	// expected key), not `thought_signature` (OpenAI-style).
	bytes, _ := json.Marshal(modelContent.Parts[0])
	if !strings.Contains(string(bytes), `"thoughtSignature":"OPAQUE_SIG_BLOB"`) {
		t.Errorf("wrong wire field name; serialized: %s", string(bytes))
	}
}

func TestBuildRequest_ThoughtSignatureWithToolCalls(t *testing.T) {
	// When the assistant turn was a function-call (Gemini 3 strict case),
	// the signature should still attach to the leading part of the model
	// content (the functionCall part).
	sig := "TOOL_SIG"
	r, err := buildRequest(context.Background(), &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "use tool"},
			{
				Role:             "assistant",
				ThoughtSignature: &sig,
				ToolCalls: []provider.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: provider.ToolCallFunction{
						Name:      "get_weather",
						Arguments: `{"city":"Tokyo"}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "call_1", Name: "get_weather", Content: `{"temp":20}`},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	var modelContent *content
	for i := range r.Contents {
		if r.Contents[i].Role == "model" {
			modelContent = &r.Contents[i]
			break
		}
	}
	if modelContent == nil || len(modelContent.Parts) == 0 {
		t.Fatal("model content not built")
	}
	if modelContent.Parts[0].ThoughtSignature != "TOOL_SIG" {
		t.Errorf("signature missing on tool-call assistant turn: %+v", modelContent.Parts[0])
	}
}

func TestBuildRequest_NoSignatureLeavesPartsClean(t *testing.T) {
	r, err := buildRequest(context.Background(), &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	var modelContent *content
	for i := range r.Contents {
		if r.Contents[i].Role == "model" {
			modelContent = &r.Contents[i]
			break
		}
	}
	if modelContent != nil && len(modelContent.Parts) > 0 {
		bytes, _ := json.Marshal(modelContent.Parts[0])
		if strings.Contains(string(bytes), "thoughtSignature") {
			t.Errorf("empty signature must not pollute wire bytes: %s", string(bytes))
		}
	}
}
