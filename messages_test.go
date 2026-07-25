package llmkit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// marshalMessage runs a Message through the same serialization the compat layer
// uses on the wire, so these tests assert what upstreams actually receive
// rather than what the Go struct happens to look like.
func marshalMessage(t *testing.T, m Message) map[string]any {
	t.Helper()
	data, err := json.Marshal(provider.MessageToMap(m))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestMessageConstructors_ProduceCorrectRoles(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		role string
		text string
	}{
		{"System", System("be brief"), "system", "be brief"},
		{"User", User("hello"), "user", "hello"},
		{"Assistant", Assistant("hi"), "assistant", "hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := marshalMessage(t, tc.msg)
			if got["role"] != tc.role {
				t.Errorf("role = %v, want %v", got["role"], tc.role)
			}
			if got["content"] != tc.text {
				t.Errorf("content = %v, want %v", got["content"], tc.text)
			}
		})
	}
}

func TestToolResult(t *testing.T) {
	got := marshalMessage(t, ToolResult("call_abc", `{"temp":26}`))
	if got["role"] != "tool" {
		t.Errorf("role = %v, want tool", got["role"])
	}
	if got["tool_call_id"] != "call_abc" {
		t.Errorf("tool_call_id = %v", got["tool_call_id"])
	}
	if got["content"] != `{"temp":26}` {
		t.Errorf("content = %v", got["content"])
	}
}

func TestToolResultJSON(t *testing.T) {
	got := marshalMessage(t, ToolResultJSON("call_abc", map[string]any{
		"city": "杭州",
		"temp": 26,
	}))
	content, ok := got["content"].(string)
	if !ok {
		t.Fatalf("content is %T, want a JSON string", got["content"])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("content is not valid JSON: %v (%q)", err, content)
	}
	if decoded["city"] != "杭州" || decoded["temp"] != float64(26) {
		t.Errorf("payload lost in encoding: %+v", decoded)
	}
}

// A value that can't be marshaled must surface as an error the model can read,
// not as a silently empty tool result.
func TestToolResultJSON_UnmarshalableValueReportsError(t *testing.T) {
	got := marshalMessage(t, ToolResultJSON("call_abc", make(chan int)))
	content, _ := got["content"].(string)
	if content == "" {
		t.Fatal("content is empty; the failure was swallowed")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("error payload is not valid JSON: %q", content)
	}
	if _, ok := decoded["error"]; !ok {
		t.Errorf("expected an {\"error\": ...} payload, got %q", content)
	}
}

func TestUserWith_MultimodalParts(t *testing.T) {
	msg := UserWith(
		Text("what is this?"),
		Image("https://example.com/a.png"),
		ImageDetail("https://example.com/b.png", "high"),
	)
	parts, ok := msg.Content.([]ContentPart)
	if !ok {
		t.Fatalf("Content is %T, want []ContentPart", msg.Content)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "what is this?" {
		t.Errorf("part 0 = %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "https://example.com/a.png" {
		t.Errorf("part 1 = %+v", parts[1])
	}
	if parts[2].ImageURL.Detail != "high" {
		t.Errorf("detail = %q, want high", parts[2].ImageURL.Detail)
	}

	// The serialized form must match OpenAI's content-part schema.
	got := marshalMessage(t, msg)
	arr, ok := got["content"].([]any)
	if !ok {
		t.Fatalf("serialized content is %T, want an array", got["content"])
	}
	first, _ := arr[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "what is this?" {
		t.Errorf("serialized part 0 = %+v", first)
	}
	second, _ := arr[1].(map[string]any)
	imgObj, _ := second["image_url"].(map[string]any)
	if second["type"] != "image_url" || imgObj["url"] != "https://example.com/a.png" {
		t.Errorf("serialized part 1 = %+v", second)
	}
}

func TestDataURI_AndImageBytes(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	uri := DataURI("image/png", png)
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("uri = %q", uri)
	}

	// It must round-trip through the SDK's own data-URI parser.
	mediaType, data, ok := provider.ParseDataURI(uri)
	if !ok {
		t.Fatal("ParseDataURI rejected the URI we generated")
	}
	if mediaType != "image/png" {
		t.Errorf("mediaType = %q", mediaType)
	}
	if data == "" {
		t.Error("empty base64 payload")
	}

	part := ImageBytes("image/png", png)
	if part.Type != "image_url" || part.ImageURL.URL != uri {
		t.Errorf("ImageBytes = %+v", part)
	}
}

func TestAudioAndFileParts(t *testing.T) {
	a := Audio("wav", "AAAA")
	if a.Type != "input_audio" || a.InputAudio.Format != "wav" || a.InputAudio.Data != "AAAA" {
		t.Errorf("Audio = %+v", a)
	}

	f := File("doc.pdf", "application/pdf", "BBBB")
	if f.Type != "file" || f.File.Filename != "doc.pdf" ||
		f.File.MimeType != "application/pdf" || f.File.FileData != "BBBB" {
		t.Errorf("File = %+v", f)
	}
}

func TestNewTool_SerializesToOpenAISchema(t *testing.T) {
	tool := NewTool("get_weather", "Look up weather", map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
		"required":   []string{"city"},
	})

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "function" {
		t.Errorf("type = %v, want function", got["type"])
	}
	fn, ok := got["function"].(map[string]any)
	if !ok {
		t.Fatalf("function is %T", got["function"])
	}
	if fn["name"] != "get_weather" || fn["description"] != "Look up weather" {
		t.Errorf("function = %+v", fn)
	}
	params, ok := fn["parameters"].(map[string]any)
	if !ok || params["type"] != "object" {
		t.Errorf("parameters = %+v", fn["parameters"])
	}
}

func TestResponseFormatHelpers(t *testing.T) {
	if got := JSONFormat(); got.Type != "json_object" || got.JSONSchema != nil {
		t.Errorf("JSONFormat = %+v", got)
	}

	schema := map[string]any{"type": "object"}
	rf := JSONSchemaFormat("recipe", schema)
	if rf.Type != "json_schema" {
		t.Errorf("Type = %q", rf.Type)
	}
	if rf.JSONSchema == nil || rf.JSONSchema.Name != "recipe" {
		t.Fatalf("JSONSchema = %+v", rf.JSONSchema)
	}
	if rf.JSONSchema.Strict == nil || !*rf.JSONSchema.Strict {
		t.Error("Strict should default to true so schema adherence is enforced")
	}
}

func TestThinkingHelpers(t *testing.T) {
	on := EnableThinking(4096)
	if on.Type != "enabled" || on.BudgetTokens != 4096 {
		t.Errorf("EnableThinking = %+v", on)
	}
	// Budget 0 means "vendor decides" and must not emit a zero budget.
	if got := EnableThinking(0); got.BudgetTokens != 0 || got.Type != "enabled" {
		t.Errorf("EnableThinking(0) = %+v", got)
	}
	if off := DisableThinking(); off.Type != "disabled" {
		t.Errorf("DisableThinking = %+v", off)
	}
}

func TestResponseExtractors(t *testing.T) {
	reasoning := "let me think"
	refusal := "no"
	resp := &ChatResponse{
		Choices: []Choice{{
			Message: &Message{
				Role:             "assistant",
				Content:          "the answer",
				ReasoningContent: &reasoning,
				Refusal:          &refusal,
				ToolCalls: []ToolCall{{
					ID:       "call_1",
					Type:     "function",
					Function: ToolCallFunction{Name: "f", Arguments: "{}"},
				}},
			},
		}},
	}
	if got := ResponseText(resp); got != "the answer" {
		t.Errorf("ResponseText = %q", got)
	}
	if got := ResponseReasoning(resp); got != "let me think" {
		t.Errorf("ResponseReasoning = %q", got)
	}
	calls := ResponseToolCalls(resp)
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Errorf("ResponseToolCalls = %+v", calls)
	}
}

// Extractors must tolerate every shape of empty response rather than panicking
// in the caller's request path.
func TestResponseExtractors_HandleEmptyShapes(t *testing.T) {
	shapes := []*ChatResponse{
		nil,
		{},
		{Choices: []Choice{}},
		{Choices: []Choice{{}}}, // nil Message
		{Choices: []Choice{{Message: &Message{}}}}, // nil Content
	}
	for i, resp := range shapes {
		if got := ResponseText(resp); got != "" {
			t.Errorf("shape %d: ResponseText = %q, want empty", i, got)
		}
		if got := ResponseReasoning(resp); got != "" {
			t.Errorf("shape %d: ResponseReasoning = %q, want empty", i, got)
		}
		if got := ResponseToolCalls(resp); got != nil {
			t.Errorf("shape %d: ResponseToolCalls = %+v, want nil", i, got)
		}
	}
}

func TestChunkExtractors(t *testing.T) {
	reasoning := "hmm"
	chunk := &Chunk{Choices: []Choice{{
		Delta: &Message{Content: "tok", ReasoningContent: &reasoning},
	}}}
	if got := ChunkText(chunk); got != "tok" {
		t.Errorf("ChunkText = %q", got)
	}
	if got := ChunkReasoning(chunk); got != "hmm" {
		t.Errorf("ChunkReasoning = %q", got)
	}

	for i, empty := range []*Chunk{nil, {}, {Choices: []Choice{}}, {Choices: []Choice{{}}}} {
		if got := ChunkText(empty); got != "" {
			t.Errorf("empty chunk %d: ChunkText = %q", i, got)
		}
		if got := ChunkReasoning(empty); got != "" {
			t.Errorf("empty chunk %d: ChunkReasoning = %q", i, got)
		}
	}
}

func TestIsTerminalVideoStatus(t *testing.T) {
	terminal := []string{VideoStatusCompleted, VideoStatusFailed, VideoStatusCancelled, VideoStatusExpired}
	ongoing := []string{VideoStatusQueued, VideoStatusInProgress, VideoStatusCancelRequested, ""}

	for _, s := range terminal {
		if !IsTerminalVideoStatus(s) {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range ongoing {
		if IsTerminalVideoStatus(s) {
			t.Errorf("%q should not be terminal", s)
		}
	}
}
