package anthropic

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestMessageRequestRoundTripPreservesBlocksAndUnknownJSON(t *testing.T) {
	input := []byte(`{
  "model":"claude-opus-4-6",
  "max_tokens":4096,
  "messages":[
    {"role":"user","content":[
      {"type":"text","text":"inspect","future_integer":900719925474099312345},
      {"type":"image","source":{"type":"url","url":"https://example.test/a.png","future_source":{"x":1}}},
      {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"Zg=="},"title":"doc"},
      {"type":"tool_result","tool_use_id":"toolu_1","content":"ok","is_error":false},
      {"type":"thinking","thinking":"private","signature":"sig-A"},
      {"type":"redacted_thinking","data":"opaque-A"},
      {"type":"future_block","payload":{"large":999999999999999999999}}
    ],"future_turn":true}
  ],
  "system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral","ttl":"1h"}}],
  "tools":[{"name":"lookup","description":"Lookup","input_schema":{"type":"object","properties":{"n":{"type":"integer"}}},"future_tool":7}],
  "tool_choice":{"type":"auto","disable_parallel_tool_use":false},
  "thinking":{"type":"adaptive","display":"summarized"},
  "output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object"}}},
  "cache_control":{"type":"ephemeral"},
  "future_request":{"nested":[1,2,3]}
}`)

	var request MessageRequest
	if err := json.Unmarshal(input, &request); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(request.Messages) != 1 || len(request.Messages[0].Content.Blocks) != 7 {
		t.Fatalf("unexpected blocks: %#v", request.Messages)
	}
	blocks := request.Messages[0].Content.Blocks
	if blocks[4].Thinking == nil || blocks[4].Thinking.Signature != "sig-A" {
		t.Fatalf("thinking signature lost: %#v", blocks[4])
	}
	if blocks[5].RedactedThinking == nil || blocks[5].RedactedThinking.Data != "opaque-A" {
		t.Fatalf("redacted data lost: %#v", blocks[5])
	}
	if blocks[6].Type != "future_block" || blocks[6].Raw == nil {
		t.Fatalf("unknown block not retained: %#v", blocks[6])
	}
	if got := string(blocks[0].Text.ExtraFields["future_integer"]); got != "900719925474099312345" {
		t.Fatalf("large integer changed: %s", got)
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	assertJSONEqual(t, input, encoded)

	var second MessageRequest
	if err := json.Unmarshal(encoded, &second); err != nil {
		t.Fatalf("second Unmarshal: %v", err)
	}
	if got := second.Messages[0].Content.Blocks[4].Thinking.Signature; got != "sig-A" {
		t.Fatalf("second signature = %q", got)
	}
}

func TestMessageResponseRoundTripAndHelpers(t *testing.T) {
	input := []byte(`{
  "id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6",
  "content":[
    {"type":"thinking","thinking":"summary","signature":"sig-1"},
    {"type":"redacted_thinking","data":"encrypted-1"},
    {"type":"text","text":"hello ","citations":null},
    {"type":"tool_use","id":"toolu_1","name":"lookup","input":{"n":900719925474099312345}},
    {"type":"text","text":"world","citations":[]}
  ],
  "container":null,"stop_reason":"tool_use","stop_sequence":null,"stop_details":null,
  "usage":{"input_tokens":20,"output_tokens":8,"cache_creation_input_tokens":null,"cache_read_input_tokens":3,"cache_creation":null,"inference_geo":null,"output_tokens_details":{"thinking_tokens":2},"server_tool_use":null,"service_tier":"standard","future_usage":12345678901234567890},
  "future_response":{"ok":true}
}`)
	message := MessageResponse{RequestID: "req_before_decode"}
	if err := json.Unmarshal(input, &message); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if message.RequestID != "req_before_decode" {
		t.Fatalf("RequestID was overwritten: %q", message.RequestID)
	}
	if got := MessageText(&message); got != "hello world" {
		t.Fatalf("MessageText = %q", got)
	}
	uses := ToolUses(&message)
	if len(uses) != 1 || uses[0].ID != "toolu_1" {
		t.Fatalf("ToolUses = %#v", uses)
	}
	uses[0].Input[0] = '['
	if message.Content[3].ToolUse.Input[0] == '[' {
		t.Fatal("ToolUses returned aliased input")
	}
	if message.Content[0].Thinking.Signature != "sig-1" || message.Content[1].RedactedThinking.Data != "encrypted-1" {
		t.Fatal("thinking continuity fields were lost")
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	assertJSONEqual(t, input, encoded)
}

func TestContentStringAndEmptyBlockArrayRemainDistinct(t *testing.T) {
	tests := []struct {
		name string
		json string
		text bool
	}{
		{name: "empty string", json: `""`, text: true},
		{name: "empty array", json: `[]`, text: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var content Content
			if err := json.Unmarshal([]byte(test.json), &content); err != nil {
				t.Fatal(err)
			}
			if (content.Text != nil) != test.text {
				t.Fatalf("wrong variant: %#v", content)
			}
			encoded, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.json {
				t.Fatalf("encoded %s, want %s", encoded, test.json)
			}
		})
	}
}

func TestMessageRequestExtraFieldsCannotOverrideKnownFields(t *testing.T) {
	request := MessageRequest{
		Model:     "claude-opus-4-6",
		MaxTokens: 1,
		Messages:  []MessageParam{{Role: RoleUser, Content: StringContent("hello")}},
		ExtraFields: ExtraFields{
			"model": json.RawMessage(`"attacker-model"`),
		},
	}
	_, err := json.Marshal(request)
	if !errors.Is(err, ErrExtraFieldConflict) {
		t.Fatalf("Marshal error = %v, want ErrExtraFieldConflict", err)
	}
	var conflict *ExtraFieldConflictError
	if !errors.As(err, &conflict) || conflict.Field != "model" {
		t.Fatalf("conflict = %#v", conflict)
	}
}

func TestTokenCountRequestAndResponseRoundTrip(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}],"future_count_option":1}`)
	var request TokenCountRequest
	if err := json.Unmarshal(requestJSON, &request); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, requestJSON, encoded)

	response := TokenCountResponse{RequestID: "req_count"}
	responseJSON := []byte(`{"input_tokens":2095,"future_breakdown":{"image":300}}`)
	if err := json.Unmarshal(responseJSON, &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "req_count" || response.InputTokens != 2095 {
		t.Fatalf("response = %#v", response)
	}
	encoded, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, responseJSON, encoded)
}

func TestRequestOptionsAreCopiedAndNotJSON(t *testing.T) {
	betas := []string{"feature-a", "feature-b"}
	options := ApplyRequestOptions(WithVersion("2026-01-01"), WithBetas(betas...))
	betas[0] = "changed"
	if options.Version != "2026-01-01" || !reflect.DeepEqual(options.Betas, []string{"feature-a", "feature-b"}) {
		t.Fatalf("options = %#v", options)
	}
	defaults := ApplyRequestOptions()
	if defaults.Version != DefaultVersion || len(defaults.Betas) != 0 {
		t.Fatalf("defaults = %#v", defaults)
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{}` {
		t.Fatalf("RequestOptions leaked into JSON: %s", encoded)
	}
}

func assertJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()
	var wantValue any
	var gotValue any
	if err := decodeUseNumber(want, &wantValue); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if err := decodeUseNumber(got, &gotValue); err != nil {
		t.Fatalf("decode got: %v\ngot: %s", err, got)
	}
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("JSON differs\nwant: %s\n got: %s", want, got)
	}
}
