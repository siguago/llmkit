package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestBuildRequest_MapsOpenAIishFieldsToDeepSeek(t *testing.T) {
	prefix := true
	strict := true
	maxCompletionTokens := 256

	body, url, emitUsageChunk, err := buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: []provider.ContentPart{{Type: "text", Text: "hello"}}},
			{Role: "assistant", Content: "prefill", Prefix: &prefix},
		},
		MaxCompletionTokens: &maxCompletionTokens,
		Tools: []provider.Tool{{
			Type: "function",
			Function: provider.ToolFunction{
				Name:   "lookup",
				Strict: &strict,
			},
		}},
	}, true)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	if url != defaultBaseURL+"/beta/chat/completions" {
		t.Fatalf("want beta url, got %s", url)
	}
	if emitUsageChunk {
		t.Fatalf("usage chunk should only be emitted when client opts in")
	}
	if got, ok := body["max_tokens"].(int); !ok || got != maxCompletionTokens {
		t.Fatalf("max_completion_tokens should map to max_tokens, got %#v", body["max_tokens"])
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Fatalf("max_completion_tokens must not be forwarded to DeepSeek")
	}
	if got := body["stream_options"]; got == nil {
		t.Fatalf("stream_options should be added upstream for logging")
	}

	msgs := body["messages"].([]map[string]any)
	if got := msgs[0]["content"]; got != "hello" {
		t.Fatalf("text parts should be flattened to plain string, got %#v", got)
	}
	if got := msgs[1]["prefix"]; got != true {
		t.Fatalf("assistant prefix missing: %#v", msgs[1])
	}
}

func TestListModels_ClassifiesOnlyDocumentedChatIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"deepseek-v4-flash"},
			{"id":"deepseek-v4-pro"},
			{"id":"deepseek-chat"},
			{"id":"deepseek-reasoner"},
			{"id":"deepseek-coder"},
			{"id":"deepseek-ocr"},
			{"id":"future-model"}
		]}`))
	}))
	defer srv.Close()

	models, taskTypes, err := New(srv.URL).ListModelsWithTaskTypes(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 7 {
		t.Fatalf("models = %+v, want every catalog entry preserved", models)
	}
	for _, modelID := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		if got := strings.Join(taskTypes[modelID], ","); got != provider.RemoteModelTaskChat {
			t.Errorf("%s task_types = %q, want %q", modelID, got, provider.RemoteModelTaskChat)
		}
	}
	for _, modelID := range []string{
		"deepseek-chat", "deepseek-reasoner", "deepseek-coder",
		"deepseek-ocr", "future-model",
	} {
		if _, ok := taskTypes[modelID]; ok {
			t.Errorf("retired/unknown/non-chat model %s must remain unclassified", modelID)
		}
	}
}

func TestBuildRequest_RejectsUnsupportedDeepSeekFields(t *testing.T) {
	_, _, _, err := buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &provider.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &provider.JSONSchemaSpec{
				Name:   "out",
				Schema: map[string]any{"type": "object"},
			},
		},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "json_object") {
		t.Fatalf("expected json_schema rejection, got %v", err)
	}
}

func TestBuildRequest_RejectsImageContent(t *testing.T) {
	_, _, _, err := buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{
			Role: "user",
			Content: []provider.ContentPart{{
				Type: "image_url",
				ImageURL: &provider.ImageURL{
					URL: "https://example.com/a.png",
				},
			}},
		}},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "image_url") {
		t.Fatalf("expected image rejection, got %v", err)
	}
}

func TestBuildRequest_RejectsUnsupportedSamplingParams(t *testing.T) {
	n := 2
	_, _, _, err := buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		N:        &n,
	}, false)
	if err == nil || !strings.Contains(err.Error(), "n>1") {
		t.Fatalf("expected n>1 rejection, got %v", err)
	}

	seed := 42
	_, _, _, err = buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Seed:     &seed,
	}, false)
	if err == nil || !strings.Contains(err.Error(), "seed") {
		t.Fatalf("expected seed rejection, got %v", err)
	}

	// n=1 should pass through without error.
	nOne := 1
	if _, _, _, err := buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		N:        &nOne,
	}, false); err != nil {
		t.Fatalf("n=1 should be allowed, got %v", err)
	}
}

func TestBuildRequest_ThinkingStripsAnthropicBudgetTokens(t *testing.T) {
	body, _, _, err := buildRequest(defaultBaseURL, "deepseek-v4-pro", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Thinking: &provider.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 1024, // Anthropic-only — must NOT leak to DeepSeek.
		},
	}, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking should be an object, got %#v", body["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type lost: %#v", thinking)
	}
	if _, has := thinking["budget_tokens"]; has {
		t.Fatalf("budget_tokens must be stripped before hitting DeepSeek: %#v", thinking)
	}

	// Empty Type means nothing meaningful to forward — do not send the key at all.
	bodyEmpty, _, _, err := buildRequest(defaultBaseURL, "deepseek-v4-pro", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Thinking: &provider.ThinkingConfig{BudgetTokens: 1024},
	}, false)
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	if _, has := bodyEmpty["thinking"]; has {
		t.Fatalf("thinking without type should be omitted: %#v", bodyEmpty)
	}
}

func TestBuildRequest_ParamRejections_Use422(t *testing.T) {
	// DeepSeek error code table: 400 = 请求体格式错误；422 = 参数错误。
	// Locally-rejected parameter errors must match the native 422.
	cases := []struct {
		name string
		req  *provider.ChatCompletionRequest
	}{
		{
			name: "n>1",
			req:  &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}, N: intPtr(2)},
		},
		{
			name: "seed",
			req:  &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}, Seed: intPtr(42)},
		},
		{
			name: "json_schema",
			req: &provider.ChatCompletionRequest{
				Messages:       []provider.Message{{Role: "user", Content: "hi"}},
				ResponseFormat: &provider.ResponseFormat{Type: "json_schema"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := buildRequest(defaultBaseURL, "deepseek-chat", tc.req, false)
			var pe *provider.ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("want ProviderError, got %T: %v", err, err)
			}
			if pe.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d (%s)", pe.StatusCode, pe.Message)
			}
		})
	}
}

func TestBuildRequest_RejectsUnknownContentParts(t *testing.T) {
	// image_url is explicit; other unknown types (audio_url, input_audio, file, …) used
	// to be silently dropped by ContentToString. Reject them instead.
	cases := []struct {
		name    string
		content any
	}{
		{
			name: "typed audio part",
			content: []provider.ContentPart{
				{Type: "text", Text: "describe"},
				{Type: "input_audio"},
			},
		},
		{
			name: "raw map audio part",
			content: []any{
				map[string]any{"type": "text", "text": "describe"},
				map[string]any{"type": "audio_url", "audio_url": map[string]any{"url": "x"}},
			},
		},
		{
			name: "image_url still rejected (same path)",
			content: []provider.ContentPart{
				{Type: "image_url", ImageURL: &provider.ImageURL{URL: "x"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
				Messages: []provider.Message{{Role: "user", Content: tc.content}},
			}, false)
			var pe *provider.ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("want ProviderError, got %T: %v", err, err)
			}
			if pe.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d", pe.StatusCode)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

func TestBuildRequest_RejectsMalformedTextParts(t *testing.T) {
	// Clients sometimes emit {"type":"text"} (missing `text`) or {"type":"text","text":123}
	// (wrong type). Without validation these would fall through ContentToString as silent
	// empty strings; fail loudly instead.
	cases := []struct {
		name    string
		content any
	}{
		{
			name: "missing text field",
			content: []any{
				map[string]any{"type": "text"},
			},
		},
		{
			name: "text is not a string (number)",
			content: []any{
				map[string]any{"type": "text", "text": 42},
			},
		},
		{
			name: "text is null",
			content: []any{
				map[string]any{"type": "text", "text": nil},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
				Messages: []provider.Message{{Role: "user", Content: tc.content}},
			}, false)
			var pe *provider.ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("want ProviderError, got %T: %v", err, err)
			}
			if pe.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d (%s)", pe.StatusCode, pe.Message)
			}
		})
	}

	// Empty string is allowed (clients may intentionally send "" as a placeholder).
	if _, _, _, err := buildRequest(defaultBaseURL, "deepseek-chat", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": ""},
		}}},
	}, false); err != nil {
		t.Fatalf("empty text should be allowed, got %v", err)
	}
}

func TestBuildMessages_PreservesAssistantNullContentForToolCalls(t *testing.T) {
	msgs, _, err := buildMessages([]provider.Message{{
		Role: "assistant",
		ToolCalls: []provider.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: provider.ToolCallFunction{
				Name:      "lookup",
				Arguments: "{}",
			},
		}},
	}})
	if err != nil {
		t.Fatalf("buildMessages error: %v", err)
	}

	b, _ := json.Marshal(msgs[0])
	if !strings.Contains(string(b), `"content":null`) {
		t.Fatalf("assistant tool call content should stay null, got %s", string(b))
	}
}
