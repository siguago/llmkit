package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestChatImageInput_PreservesContentAndSharedRequestBehavior(t *testing.T) {
	const remoteURL = "https://image.example.test/a.png?x=1&y=%2F"
	const dataURL = "data:image/png;base64,iVBORw0KGgo="
	contents := map[string]any{
		"typed": []provider.ContentPart{
			{Type: "text", Text: "first"},
			{Type: "image_url", ImageURL: &provider.ImageURL{URL: remoteURL, Detail: "high"}},
			{Type: "text", Text: "between"},
			{Type: "image_url", ImageURL: &provider.ImageURL{URL: dataURL, Detail: "low"}},
			{Type: "text", Text: "last"},
		},
		"raw": []any{
			map[string]any{"type": "text", "text": "first", "future_text": "kept"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": remoteURL, "detail": "high", "future_image": "kept"}, "future_part": "kept"},
			map[string]any{"type": "text", "text": "between"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL, "detail": "low"}},
			map[string]any{"type": "text", "text": "last"},
		},
	}
	for shape, content := range contents {
		for _, mode := range []string{"chat", "stream", "stream_usage"} {
			t.Run(shape+"/"+mode, func(t *testing.T) {
				captured := make(chan map[string]any, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/beta/chat/completions" || r.Method != http.MethodPost {
						t.Errorf("request = %s %s, want POST beta chat endpoint", r.Method, r.URL.Path)
					}
					if got := r.Header.Get("Authorization"); got != "Bearer key" {
						t.Errorf("authorization = %q", got)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode request: %v", err)
					}
					captured <- body
					if mode == "chat" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"red"},"finish_reason":"stop"}],"usage":{"prompt_tokens":46,"completion_tokens":5,"total_tokens":51,"prompt_cache_hit_tokens":12,"prompt_cache_miss_tokens":34,"completion_tokens_details":{"reasoning_tokens":2}}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"red\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":46,\"completion_tokens\":5,\"total_tokens\":51,\"prompt_cache_hit_tokens\":12,\"prompt_cache_miss_tokens\":34,\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\ndata: [DONE]\n\n")
				}))
				defer srv.Close()
				prefix, strict := true, true
				reasoning := "previous reasoning"
				req := &provider.ChatCompletionRequest{
					Messages: []provider.Message{
						{Role: "user", Content: content},
						{Role: "assistant", Prefix: &prefix, ReasoningContent: &reasoning, ToolCalls: []provider.ToolCall{{ID: "call_1", Type: "function", Function: provider.ToolCallFunction{Name: "lookup", Arguments: "{}"}}}},
					},
					Tools:    []provider.Tool{{Type: "function", Function: provider.ToolFunction{Name: "lookup", Strict: &strict}}},
					Thinking: &provider.ThinkingConfig{Type: "enabled", BudgetTokens: 1024},
				}
				if mode == "stream_usage" {
					req.StreamOptions = &provider.StreamOptions{IncludeUsage: true}
				}
				p := New(srv.URL)
				var usage *provider.Usage
				if mode == "chat" {
					resp, err := p.ChatCompletion(context.Background(), "key", "deepseek-flash", req)
					if err != nil {
						t.Fatal(err)
					}
					usage = resp.Usage
				} else {
					stream, err := p.ChatCompletionStream(context.Background(), "key", "deepseek-flash", req)
					if err != nil {
						t.Fatal(err)
					}
					defer stream.Close()
					usageChunks := 0
					for {
						chunk, err := stream.Recv()
						if errors.Is(err, io.EOF) {
							break
						}
						if err != nil {
							t.Fatal(err)
						}
						if chunk.Usage != nil && len(chunk.Choices) == 0 {
							usageChunks++
						}
					}
					wantChunks := 0
					if mode == "stream_usage" {
						wantChunks = 1
					}
					if usageChunks != wantChunks {
						t.Errorf("usage chunks = %d, want %d", usageChunks, wantChunks)
					}
					usage = stream.GetUsage()
				}
				if usage == nil || usage.CachedTokens != 12 || usage.PromptCacheMissTokens != 34 || usage.CacheCreationTokens != 0 || usage.ReasoningTokens != 2 {
					t.Errorf("normalized usage = %+v, want separate cache hit/miss and reasoning", usage)
				}
				body := <-captured
				if body["model"] != "deepseek-flash" || body["stream"] != (mode != "chat") {
					t.Errorf("model/stream altered: %#v", body)
				}
				messages := body["messages"].([]any)
				gotContent := messages[0].(map[string]any)["content"]
				encoded, err := json.Marshal(content)
				if err != nil {
					t.Fatal(err)
				}
				var wantContent any
				if err := json.Unmarshal(encoded, &wantContent); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotContent, wantContent) {
					t.Errorf("content changed:\n got %#v\nwant %#v", gotContent, wantContent)
				}
				assistant := messages[1].(map[string]any)
				if value, exists := assistant["content"]; !exists || value != nil || assistant["reasoning_content"] != reasoning || assistant["prefix"] != true || len(assistant["tool_calls"].([]any)) != 1 {
					t.Errorf("assistant tool/prefix/reasoning fields changed: %#v", assistant)
				}
				if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "enabled"}) {
					t.Errorf("thinking = %#v, want only type", body["thinking"])
				}
				if mode != "chat" && !reflect.DeepEqual(body["stream_options"], map[string]any{"include_usage": true}) {
					t.Errorf("upstream stream_options = %#v", body["stream_options"])
				}
			})
		}
	}
}

func TestChatImageInput_PreservesEmptyTextParts(t *testing.T) {
	const imageURL = "https://example.test/a.png"
	cacheControl := map[string]any{"type": "ephemeral"}
	for shape, content := range map[string]any{
		"typed": []provider.ContentPart{
			{Type: "text", Text: "", CacheControl: cacheControl},
			{Type: "image_url", ImageURL: &provider.ImageURL{URL: imageURL}},
		},
		"raw": []any{
			map[string]any{"type": "text", "text": "", "cache_control": cacheControl},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
		},
	} {
		t.Run(shape, func(t *testing.T) {
			captured := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				captured <- body
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"red"}}]}`)
			}))
			defer srv.Close()
			_, err := New(srv.URL).ChatCompletion(context.Background(), "key", "deepseek-flash", &provider.ChatCompletionRequest{
				Messages: []provider.Message{{Role: "user", Content: content}},
			})
			if err != nil {
				t.Fatal(err)
			}
			body := <-captured
			got := body["messages"].([]any)[0].(map[string]any)["content"]
			want := []any{
				map[string]any{"type": "text", "text": "", "cache_control": cacheControl},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("content = %#v, want explicit empty text and preserved fields %#v", got, want)
			}
		})
	}
}

func TestChatImageInput_LocalRejectionsDoNotCallUpstream(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	typedImage := []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: "https://example.test/a.png"}}}
	rawImage := []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png"}}}
	type rejection struct {
		name, model, role string
		content           any
	}
	var cases []rejection
	// Only models that genuinely lack image support are rejected locally: the
	// legacy Flash names are allowed (covered by the positive test below).
	for _, model := range []string{"deepseek-v4-pro", "future-model", "deepseek-chat", "deepseek-reasoner"} {
		cases = append(cases, rejection{model + "/typed", model, "user", typedImage}, rejection{model + "/raw", model, "user", rawImage})
	}
	for _, role := range []string{"system", "assistant", "tool", "developer"} {
		cases = append(cases, rejection{role + "/typed", "deepseek-flash", role, typedImage}, rejection{role + "/raw", "deepseek-flash", role, rawImage})
	}
	malformed := map[string]any{
		"typed_nil":        []provider.ContentPart{{Type: "image_url"}},
		"typed_empty":      []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{}}},
		"typed_whitespace": []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: " \t"}}},
		"raw_missing":      []any{map[string]any{"type": "image_url"}},
		"raw_null":         []any{map[string]any{"type": "image_url", "image_url": nil}},
		"raw_string":       []any{map[string]any{"type": "image_url", "image_url": "https://example.test/a.png"}},
		"raw_no_url":       []any{map[string]any{"type": "image_url", "image_url": map[string]any{}}},
		"raw_number_url":   []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": 42}}},
		"raw_empty_url":    []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": ""}}},
		"raw_bad_detail":   []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png", "detail": 1}}},
		"raw_bad_text":     []any{rawImage[0], map[string]any{"type": "text", "text": 42}},
		"raw_missing_text": []any{rawImage[0], map[string]any{"type": "text"}},
		"raw_unknown_part": []any{rawImage[0], map[string]any{"type": "input_audio"}},
		"raw_non_object":   []any{rawImage[0], "text"},
	}
	for name, content := range malformed {
		cases = append(cases, rejection{name, "deepseek-flash", "user", content})
	}
	p := New(srv.URL)
	for _, tc := range cases {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, streaming), func(t *testing.T) {
				req := &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: tc.role, Content: tc.content}}}
				var err error
				if streaming {
					var stream provider.StreamReader
					stream, err = p.ChatCompletionStream(context.Background(), "key", tc.model, req)
					if stream != nil {
						_ = stream.Close()
					}
				} else {
					_, err = p.ChatCompletion(context.Background(), "key", tc.model, req)
				}
				var pe *provider.ProviderError
				if !errors.As(err, &pe) || pe.StatusCode != http.StatusUnprocessableEntity || provider.ErrorOriginOf(err) != provider.ErrorOriginLocal {
					t.Fatalf("error = %v, want local ProviderError 422", err)
				}
				if got := calls.Load(); got != 0 {
					t.Fatalf("upstream calls = %d, want zero", got)
				}
			})
		}
	}
}

// The legacy Flash names are still callable upstream and route to a
// vision-capable Flash model, so local rejection would block real calls. All
// three documented names must reach the upstream with the image part intact.
func TestChatImageInput_LegacyVisionModelNamesAreForwarded(t *testing.T) {
	contents := map[string]any{
		"typed": []provider.ContentPart{
			{Type: "text", Text: "describe"},
			{Type: "image_url", ImageURL: &provider.ImageURL{URL: "https://example.test/a.png"}},
		},
		"raw": []any{
			map[string]any{"type": "text", "text": "describe"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png"}},
		},
	}
	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		for shape, content := range contents {
			t.Run(model+"/"+shape, func(t *testing.T) {
				captured := make(chan map[string]any, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode request: %v", err)
					}
					captured <- body
					_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"red"},"finish_reason":"stop"}]}`)
				}))
				defer srv.Close()

				_, err := New(srv.URL).ChatCompletion(context.Background(), "key", model, &provider.ChatCompletionRequest{
					Messages: []provider.Message{{Role: "user", Content: content}},
				})
				if err != nil {
					t.Fatalf("ChatCompletion(%s): %v", model, err)
				}
				body := <-captured
				if body["model"] != model {
					t.Errorf("model = %#v, want %q", body["model"], model)
				}
				parts, ok := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
				if !ok || len(parts) != 2 {
					t.Fatalf("content parts lost for %s: %#v", model, body["messages"])
				}
				image, ok := parts[1].(map[string]any)["image_url"].(map[string]any)
				if !ok || image["url"] != "https://example.test/a.png" {
					t.Errorf("image part lost for %s: %#v", model, parts[1])
				}
			})
		}
	}
}

// The file block (file_id / file_data) is a DeepSeek vision input the adapter
// does not implement. It stays a local 422 — but the message must name the
// block and the two supported part types instead of the generic "got type".
func TestChatImageInput_FileBlockRejectedClearly(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	contents := map[string]any{
		"typed": []provider.ContentPart{
			{Type: "text", Text: "describe"},
			{Type: "file", File: &provider.FileContent{FileID: "file-1"}},
		},
		"raw": []any{
			map[string]any{"type": "text", "text": "describe"},
			map[string]any{"type": "file", "file": map[string]any{"file_id": "file-1"}},
		},
	}
	p := New(srv.URL)
	for shape, content := range contents {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", shape, streaming), func(t *testing.T) {
				req := &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: content}}}
				var err error
				if streaming {
					var stream provider.StreamReader
					stream, err = p.ChatCompletionStream(context.Background(), "key", "deepseek-flash", req)
					if stream != nil {
						_ = stream.Close()
					}
				} else {
					_, err = p.ChatCompletion(context.Background(), "key", "deepseek-flash", req)
				}
				var pe *provider.ProviderError
				if !errors.As(err, &pe) || pe.StatusCode != http.StatusUnprocessableEntity || provider.ErrorOriginOf(err) != provider.ErrorOriginLocal {
					t.Fatalf("error = %v, want local ProviderError 422", err)
				}
				if !strings.Contains(pe.Message, "file") || !strings.Contains(pe.Message, "only text and image_url") {
					t.Errorf("message = %q, want the unsupported file block and supported types named", pe.Message)
				}
				if got := calls.Load(); got != 0 {
					t.Fatalf("upstream calls = %d, want zero", got)
				}
			})
		}
	}
}

func TestBuildRequest_TextOnlyContentKeepsStringNormalization(t *testing.T) {
	for _, content := range []any{
		[]provider.ContentPart{{Type: "text", Text: "first"}, {Type: "text", Text: "second"}},
		[]any{map[string]any{"type": "text", "text": "first"}, map[string]any{"type": "text", "text": "second"}},
	} {
		body, _, _, err := buildRequest(defaultBaseURL, "deepseek-flash", &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: content}}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := body["messages"].([]map[string]any)[0]["content"]; got != "firstsecond" {
			t.Errorf("text content = %#v, want joined string", got)
		}
	}
}

func TestPublicErrors_ReportLocalConstructionAndUpstreamRejections(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, stage := range []string{"validation", "marshal", "request"} {
			t.Run(fmt.Sprintf("%s/stream=%t", stage, streaming), func(t *testing.T) {
				p := New("://invalid-host")
				req := &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hello"}}}
				switch stage {
				case "validation":
					req.Seed = intPtr(42)
				case "marshal":
					req.Stop = func() {}
				}
				var err error
				if streaming {
					_, err = p.ChatCompletionStream(context.Background(), "key", "deepseek-flash", req)
				} else {
					_, err = p.ChatCompletion(context.Background(), "key", "deepseek-flash", req)
				}
				if err == nil || provider.ErrorOriginOf(err) != provider.ErrorOriginLocal {
					t.Fatalf("error = %v, want local origin", err)
				}
			})
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.Header().Set("X-Request-Id", "upstream-request")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream validation failed"}}`)
	}))
	defer srv.Close()
	p := New(srv.URL)
	req := &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hello"}}}
	for _, operation := range []string{"chat", "stream", "models"} {
		t.Run(operation, func(t *testing.T) {
			var err error
			switch operation {
			case "chat":
				_, err = p.ChatCompletion(context.Background(), "key", "deepseek-flash", req)
			case "stream":
				_, err = p.ChatCompletionStream(context.Background(), "key", "deepseek-flash", req)
			case "models":
				_, _, err = p.ListModelsWithTaskTypes(context.Background(), "key")
			}
			var pe *provider.ProviderError
			if !errors.As(err, &pe) || pe.StatusCode != http.StatusUnprocessableEntity || pe.RetryAfter != "5" || pe.RequestID != "upstream-request" || provider.ErrorOriginOf(err) != provider.ErrorOriginUpstream {
				t.Fatalf("error = %v (%+v), want upstream 422 with original metadata", err, pe)
			}
		})
	}
}
