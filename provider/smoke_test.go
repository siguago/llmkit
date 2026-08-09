package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageToMap_DeepSeekCompat(t *testing.T) {
	idx := 0

	// assistant + tool_calls + nil content → content must be null (DeepSeek/OpenAI spec)
	m := MessageToMap(Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID: "call_1", Type: "function",
			Function: ToolCallFunction{Name: "f", Arguments: "{}"},
			Index:    &idx,
		}},
	})
	b, _ := json.Marshal(m)
	s := string(b)
	if !strings.Contains(s, `"content":null`) {
		t.Fatalf("assistant+tool_calls should emit content:null, got %s", s)
	}
	if strings.Contains(s, `"index"`) {
		t.Fatalf("outbound tool_calls must not carry index, got %s", s)
	}

	// assistant with reasoning_content → passed through
	rc := "think"
	m = MessageToMap(Message{Role: "assistant", Content: "hi", ReasoningContent: &rc})
	b, _ = json.Marshal(m)
	if !strings.Contains(string(b), `"reasoning_content":"think"`) {
		t.Fatalf("assistant reasoning_content missing: %s", b)
	}

	// user role with reasoning_content → must NOT be forwarded
	rc2 := "leaked"
	m = MessageToMap(Message{Role: "user", Content: "q", ReasoningContent: &rc2, Name: "alice"})
	b, _ = json.Marshal(m)
	if strings.Contains(string(b), "reasoning_content") {
		t.Fatalf("reasoning_content leaked on non-assistant role: %s", b)
	}
	if !strings.Contains(string(b), `"name":"alice"`) {
		t.Fatalf("user name dropped: %s", b)
	}

	// tool role with name → name must NOT be forwarded
	m = MessageToMap(Message{Role: "tool", Content: "42", ToolCallID: "c1", Name: "tool_name"})
	b, _ = json.Marshal(m)
	if strings.Contains(string(b), `"name"`) {
		t.Fatalf("name must be stripped for tool role: %s", b)
	}
}

func TestExtractReasoningTokens(t *testing.T) {
	data := []byte(`{"usage":{"completion_tokens":100,"completion_tokens_details":{"reasoning_tokens":42}}}`)
	if got := ExtractReasoningTokens(data); got != 42 {
		t.Fatalf("want 42, got %d", got)
	}
	// Absent → 0
	if got := ExtractReasoningTokens([]byte(`{"usage":{"completion_tokens":10}}`)); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRemoteModelRemainsComparableAndThreeFields(t *testing.T) {
	// RemoteModel is a long-standing public value type. Keep both positional
	// literals and map-key use compiling; task metadata belongs in the separate
	// ModelTaskLister result rather than changing this struct's shape.
	model := RemoteModel{"legacy", "Legacy", 8192}
	set := map[RemoteModel]struct{}{model: {}}
	if _, ok := set[RemoteModel{"legacy", "Legacy", 8192}]; !ok {
		t.Fatal("RemoteModel comparison changed")
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("marshal RemoteModel: %v", err)
	}
	if got, want := string(encoded), `{"model_id":"legacy","display_name":"Legacy","context_window":8192}`; got != want {
		t.Fatalf("RemoteModel JSON = %s, want %s", got, want)
	}
}

func TestMessageToMap_DoesNotLeakPrefillField(t *testing.T) {
	// Prefill field name is provider-specific (DeepSeek "prefix" / Kimi
	// "partial"), so MessageToMap MUST NOT emit either field — that's the
	// compat layer's job (gated on Config.PrefillFieldName). Emitting an
	// unknown prefill field on a request to a non-prefill provider risks 4xx
	// on strict-schema upstreams.
	on := true
	m := MessageToMap(Message{Role: "assistant", Content: "ack:", Prefix: &on})
	if _, has := m["prefix"]; has {
		t.Fatalf("MessageToMap must NOT emit prefix; let compat layer attach per-provider: %+v", m)
	}
	if _, has := m["partial"]; has {
		t.Fatalf("MessageToMap must NOT emit partial; let compat layer attach per-provider: %+v", m)
	}
}

func TestMessageToMap_PreservesRedactedThinking(t *testing.T) {
	// RedactedThinking must round-trip through MessageToMap so the OpenRouter →
	// Anthropic compat path doesn't lose the opaque payload (Anthropic returns
	// 400 "missing thinking block" without it).
	m := MessageToMap(Message{
		Role:             "assistant",
		Content:          "answer",
		RedactedThinking: []string{"BLOB1", "BLOB2"},
	})
	got, ok := m["redacted_thinking"].([]string)
	if !ok {
		t.Fatalf("redacted_thinking must round-trip on assistant role, got %T: %+v", m["redacted_thinking"], m["redacted_thinking"])
	}
	if len(got) != 2 || got[0] != "BLOB1" || got[1] != "BLOB2" {
		t.Fatalf("redacted_thinking content lost: %+v", got)
	}

	// Non-assistant roles must NOT carry redacted_thinking; would confuse upstream.
	mUser := MessageToMap(Message{Role: "user", Content: "x", RedactedThinking: []string{"X"}})
	if _, exists := mUser["redacted_thinking"]; exists {
		t.Fatalf("redacted_thinking must not leak on non-assistant role: %+v", mUser)
	}
}

func TestMessage_PreservesAnnotationsThroughJSONRoundTrip(t *testing.T) {
	// OpenAI's web-search / file-search tools attach citations as
	// message.annotations. The unified type must preserve them via JSON
	// round-trip; without binding the field, decode silently drops them.
	src := `{
		"role": "assistant",
		"content": "answer",
		"annotations": [
			{"type": "url_citation", "url_citation": {"url": "https://example.com", "title": "Example"}}
		]
	}`
	var msg Message
	if err := json.Unmarshal([]byte(src), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Annotations == nil {
		t.Fatalf("annotations were dropped on decode")
	}
	out, _ := json.Marshal(msg)
	if !strings.Contains(string(out), `"url_citation"`) {
		t.Fatalf("annotations not preserved on re-encode: %s", out)
	}
}

func TestMessageToMap_PreservesRefusalOnAssistant(t *testing.T) {
	// OpenAI emits a separate `refusal` field when the model declines for
	// safety reasons. The unified type must round-trip it; without that the
	// client sees an empty assistant message and has no clue what happened.
	refusal := "I cannot help with that"
	m := MessageToMap(Message{Role: "assistant", Refusal: &refusal})
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"refusal":"I cannot help with that"`) {
		t.Fatalf("refusal must round-trip on assistant role: %s", b)
	}

	// Refusal on non-assistant roles is meaningless — must NOT leak.
	mUser := MessageToMap(Message{Role: "user", Content: "x", Refusal: &refusal})
	bUser, _ := json.Marshal(mUser)
	if strings.Contains(string(bUser), "refusal") {
		t.Fatalf("refusal must not leak on non-assistant: %s", bUser)
	}

	// Empty string refusal: omit (no signal).
	empty := ""
	mEmpty := MessageToMap(Message{Role: "assistant", Content: "hi", Refusal: &empty})
	bEmpty, _ := json.Marshal(mEmpty)
	if strings.Contains(string(bEmpty), "refusal") {
		t.Fatalf("empty refusal must not be emitted: %s", bEmpty)
	}
}

func TestNormalizeUsage_DeepSeekAliases(t *testing.T) {
	usage := &Usage{
		PromptCacheHitTokens:  12,
		PromptCacheMissTokens: 99,
		CompletionTokensDetails: &CompletionTokensDetails{
			ReasoningTokens: 34,
		},
	}
	NormalizeUsage(usage)
	if usage.CachedTokens != 12 {
		t.Fatalf("cached_tokens not normalized: %+v", usage)
	}
	if usage.ReasoningTokens != 34 {
		t.Fatalf("reasoning_tokens not normalized: %+v", usage)
	}
	// Original upstream fields must be preserved so clients retain provider-native info.
	if usage.PromptCacheHitTokens != 12 || usage.PromptCacheMissTokens != 99 {
		t.Fatalf("upstream fields must be preserved: %+v", usage)
	}
}

func TestNormalizeUsage_CacheCreationDetails(t *testing.T) {
	usage := &Usage{
		CacheCreationTokensDetails: &CacheCreationTokensDetails{
			Ephemeral5mTokens: 12,
			Ephemeral1hTokens: 8,
		},
	}

	NormalizeUsage(usage)

	if usage.CacheCreationTokens != 20 {
		t.Fatalf("cache creation total not normalized from details: %+v", usage)
	}

	wire, err := json.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal usage: %v", err)
	}
	for _, want := range []string{
		`"cache_creation_input_tokens":20`,
		`"cache_creation":{"ephemeral_5m_input_tokens":12,"ephemeral_1h_input_tokens":8}`,
	} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("usage JSON missing %s: %s", want, wire)
		}
	}
}

func TestNormalizeUsage_DropsInconsistentCacheCreationDetails(t *testing.T) {
	usage := &Usage{
		CacheCreationTokens: 25,
		CacheCreationTokensDetails: &CacheCreationTokensDetails{
			Ephemeral5mTokens: 12,
			Ephemeral1hTokens: 8,
		},
	}

	NormalizeUsage(usage)

	if usage.CacheCreationTokens != 25 {
		t.Fatalf("reported aggregate must remain authoritative: %+v", usage)
	}
	if usage.CacheCreationTokensDetails != nil {
		t.Fatalf("inconsistent details must not be exposed as billing-safe: %+v", usage)
	}
}
