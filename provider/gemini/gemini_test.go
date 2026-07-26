package gemini

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestBuildRequest_ThinkingSetsIncludeThoughts(t *testing.T) {
	// Without IncludeThoughts the model thinks but the thoughts are not returned.
	req := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Thinking: &provider.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 1024,
		},
	}
	r, err := buildRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if r.GenerationConfig == nil || r.GenerationConfig.ThinkingConfig == nil {
		t.Fatalf("thinkingConfig not set: %+v", r.GenerationConfig)
	}
	tc := r.GenerationConfig.ThinkingConfig
	if !tc.IncludeThoughts {
		t.Fatalf("includeThoughts must be true when thinking is enabled")
	}
	if tc.ThinkingBudget != 1024 {
		t.Fatalf("thinking_budget mismatch: got %d", tc.ThinkingBudget)
	}
}

func TestBuildUsage_PopulatesReasoningTokensAndCache(t *testing.T) {
	u := buildUsage(&usageMetadata{
		PromptTokenCount:        100,
		CandidatesTokenCount:    50,
		TotalTokenCount:         180,
		CachedContentTokenCount: 30,
		ThoughtsTokenCount:      30,
	})
	if u.PromptTokens != 100 || u.CompletionTokens != 50 || u.TotalTokens != 180 {
		t.Fatalf("base usage mismatch: %+v", u)
	}
	if u.CachedTokens != 30 || u.ReasoningTokens != 30 {
		t.Fatalf("cached/reasoning mismatch: %+v", u)
	}
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens != 30 {
		t.Fatalf("prompt_tokens_details.cached_tokens missing: %+v", u.PromptTokensDetails)
	}
	if u.CompletionTokensDetails == nil || u.CompletionTokensDetails.ReasoningTokens != 30 {
		t.Fatalf("completion_tokens_details.reasoning_tokens missing: %+v", u.CompletionTokensDetails)
	}
}

func TestMapFinishReason_SafetyCategoriesMapToContentFilter(t *testing.T) {
	contentFilter := []string{"SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "LANGUAGE"}
	for _, r := range contentFilter {
		if got := mapFinishReason(r); got != "content_filter" {
			t.Fatalf("%s should map to content_filter, got %s", r, got)
		}
	}
	if got := mapFinishReason("MAX_TOKENS"); got != "length" {
		t.Fatalf("MAX_TOKENS should map to length, got %s", got)
	}
	if got := mapFinishReason("TOO_MANY_TOOL_CALLS"); got != "length" {
		t.Fatalf("TOO_MANY_TOOL_CALLS should map to length, got %s", got)
	}
	if got := mapFinishReason("MALFORMED_FUNCTION_CALL"); got != "stop" {
		t.Fatalf("malformed function call should map to stop, got %s", got)
	}
	if got := mapFinishReason("STOP"); got != "stop" {
		t.Fatalf("STOP should map to stop, got %s", got)
	}
	if got := mapFinishReason(""); got != "stop" {
		t.Fatalf("empty should map to stop, got %s", got)
	}
}

func TestBuildRequest_ToolMessageNameLookupFromToolCallID(t *testing.T) {
	// OpenAI tool messages carry tool_call_id but often omit name; Gemini's
	// functionResponse.Name is required. Resolve it from earlier assistant
	// tool_calls.
	req := &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "use lookup"},
			{
				Role: "assistant",
				ToolCalls: []provider.ToolCall{{
					ID:   "call_42",
					Type: "function",
					Function: provider.ToolCallFunction{
						Name:      "lookup",
						Arguments: "{}",
					},
				}},
			},
			{Role: "tool", ToolCallID: "call_42", Content: `{"result":"ok"}`},
		},
	}
	r, err := buildRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	// Expect 3 contents: user, model(tool_calls), user(functionResponse)
	if len(r.Contents) < 3 {
		t.Fatalf("want 3 contents, got %d: %+v", len(r.Contents), r.Contents)
	}
	last := r.Contents[len(r.Contents)-1]
	if len(last.Parts) == 0 || last.Parts[0].FunctionResponse == nil {
		t.Fatalf("last content should be a functionResponse, got %+v", last)
	}
	if last.Parts[0].FunctionResponse.Name != "lookup" {
		t.Fatalf("functionResponse.Name not resolved from tool_call_id: %+v", last.Parts[0].FunctionResponse)
	}
}

func TestBuildRequest_StructuredOutputJSONSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	req := &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "extract"}},
		ResponseFormat: &provider.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &provider.JSONSchemaSpec{
				Name:   "out",
				Schema: schema,
			},
		},
	}
	r, err := buildRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if r.GenerationConfig == nil || r.GenerationConfig.ResponseMimeType != "application/json" {
		t.Fatalf("responseMimeType must be application/json: %+v", r.GenerationConfig)
	}
	// json_schema goes through responseJsonSchema (full JSON Schema spec)
	// rather than responseSchema (OpenAPI subset) so client schemas using
	// $ref / $defs / additionalProperties survive intact.
	if r.GenerationConfig.ResponseJSONSchema == nil {
		t.Fatalf("responseJsonSchema must be forwarded")
	}
	if r.GenerationConfig.ResponseSchema != nil {
		t.Fatalf("responseSchema must NOT also be set (mutually exclusive with responseJsonSchema)")
	}
	// Schema should be passed through as-is (not double-encoded)
	b, _ := json.Marshal(r.GenerationConfig.ResponseJSONSchema)
	if !contains(string(b), "properties") {
		t.Fatalf("schema not preserved: %s", b)
	}
}

func TestConvertImageURLToInlineData_DataURI(t *testing.T) {
	// Tiny 1x1 PNG
	data := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	uri := "data:image/png;base64," + data
	p, err := convertImageURLToInlineData(uri)
	if err != nil {
		t.Fatalf("data URI must be accepted: %v", err)
	}
	if p.InlineData == nil || p.InlineData.MimeType != "image/png" {
		t.Fatalf("unexpected inline data: %+v", p.InlineData)
	}
	if p.InlineData.Data != data {
		t.Fatalf("base64 round-trip mismatch")
	}
}

func TestConvertImageURLToInlineData_RejectsRelativeURL(t *testing.T) {
	_, err := convertImageURLToInlineData("./local.png")
	if err == nil {
		t.Fatalf("relative URL must be rejected")
	}
	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("want ProviderError, got %T", err)
	}
	if pe.StatusCode != 422 {
		t.Fatalf("relative URL should be 422, got %d", pe.StatusCode)
	}
}

func TestBuildRequest_ForwardsSafetySettings(t *testing.T) {
	settings := []map[string]string{
		{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_NONE"},
		{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_LOW_AND_ABOVE"},
	}
	r, err := buildRequest(context.Background(), &provider.ChatCompletionRequest{
		Messages:       []provider.Message{{Role: "user", Content: "hi"}},
		SafetySettings: settings,
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if r.SafetySettings == nil {
		t.Fatalf("safety_settings not forwarded")
	}
	// Survive the type round-trip: should still be the original slice.
	if got, ok := r.SafetySettings.([]map[string]string); !ok || len(got) != 2 {
		t.Fatalf("safety_settings shape lost: %#v", r.SafetySettings)
	}
}

func TestConvertResponse_MultipleCandidatesYieldMultipleChoices(t *testing.T) {
	// CandidateCount > 1 must surface as multiple OpenAI Choices, each with
	// a distinct Index. Previous code dropped all but the first candidate.
	resp := &response{
		Candidates: []candidate{
			{
				Content:      &content{Parts: []part{{Text: "first"}}},
				FinishReason: "STOP",
				Index:        0,
			},
			{
				Content:      &content{Parts: []part{{Text: "second"}}},
				FinishReason: "STOP",
				Index:        1,
			},
		},
	}
	out := convertResponse(resp, "gemini-2.5-flash")
	if len(out.Choices) != 2 {
		t.Fatalf("expected 2 choices for 2 candidates, got %d", len(out.Choices))
	}
	if out.Choices[0].Index != 0 || out.Choices[1].Index != 1 {
		t.Fatalf("choice indices must reflect candidate indices, got [%d, %d]",
			out.Choices[0].Index, out.Choices[1].Index)
	}
	if out.Choices[0].Message.Content != "first" || out.Choices[1].Message.Content != "second" {
		t.Fatalf("content scrambled across candidates: %+v", out.Choices)
	}
}

func TestStreamReader_MultiCandidateChunkEmitsAllChoices(t *testing.T) {
	// When Gemini sends multiple candidates in a single streaming chunk,
	// every candidate must surface as its own Choice with a distinct Index.
	// Mirror of the non-streaming convertResponse fix.
	body := io.NopCloser(strings.NewReader(
		`data: {"candidates":[` +
			`{"content":{"parts":[{"text":"first"}]},"finishReason":"STOP","index":0},` +
			`{"content":{"parts":[{"text":"second"}]},"finishReason":"STOP","index":1}` +
			`]}` + "\n\n",
	))
	sr := NewStreamReader(context.Background(), body, "gemini-test")
	chunk, err := sr.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if len(chunk.Choices) != 2 {
		t.Fatalf("expected 2 choices in chunk, got %d", len(chunk.Choices))
	}
	if chunk.Choices[0].Index != 0 || chunk.Choices[1].Index != 1 {
		t.Fatalf("indices wrong: [%d, %d]", chunk.Choices[0].Index, chunk.Choices[1].Index)
	}
	if chunk.Choices[0].Delta.Content != "first" || chunk.Choices[1].Delta.Content != "second" {
		t.Fatalf("content scrambled: %+v / %+v", chunk.Choices[0].Delta, chunk.Choices[1].Delta)
	}
}

func TestConvertResponse_BlockedPromptYieldsContentFilterChoice(t *testing.T) {
	// When Gemini blocks the prompt outright, candidates is empty but
	// promptFeedback.blockReason is set. The gateway must synthesize a
	// content_filter choice so OpenAI-compat clients can react.
	resp := &response{
		Candidates: nil,
		PromptFeedback: &promptFeedback{
			BlockReason: "SAFETY",
		},
	}
	out := convertResponse(resp, "gemini-2.5-flash")
	if len(out.Choices) != 1 {
		t.Fatalf("expected 1 synthesized choice, got %d", len(out.Choices))
	}
	if fr := out.Choices[0].FinishReason; fr == nil || *fr != "content_filter" {
		t.Fatalf("blocked prompt must map to content_filter, got %v", fr)
	}
}

func TestConvertResponse_EmptyCandidatesAlwaysReturnsArray(t *testing.T) {
	// Even without promptFeedback (defensive), choices must serialize as []
	// not null so OpenAI clients don't crash on null.
	resp := &response{Candidates: nil}
	out := convertResponse(resp, "x")
	if out.Choices == nil {
		t.Fatalf("choices must be initialized as empty slice, got nil")
	}
}

func TestBuildRequest_PropagatesImageError(t *testing.T) {
	_, err := buildRequest(context.Background(), &provider.ChatCompletionRequest{
		Messages: []provider.Message{{
			Role: "user",
			Content: []provider.ContentPart{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &provider.ImageURL{URL: "not-a-valid-url"}},
			},
		}},
	})
	if err == nil {
		t.Fatalf("expected error from invalid image URL")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
