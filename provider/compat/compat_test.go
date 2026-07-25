package compat

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestNewProviderErrorFromResponse_CapturesRetryAfter(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{},
	}
	resp.Header.Set("Retry-After", "30")
	pe := provider.NewProviderErrorFromResponse(resp, "openai", []byte(`{"error":{"message":"rate limited"}}`))
	if pe.StatusCode != 429 {
		t.Fatalf("status not preserved: %d", pe.StatusCode)
	}
	if pe.RetryAfter != "30" {
		t.Fatalf("retry-after not captured: %q", pe.RetryAfter)
	}
}

func TestNewProviderErrorFromResponse_NoRetryAfterHeader(t *testing.T) {
	resp := &http.Response{StatusCode: 500, Header: http.Header{}}
	pe := provider.NewProviderErrorFromResponse(resp, "openai", []byte("oops"))
	if pe.RetryAfter != "" {
		t.Fatalf("retry-after should be empty when absent, got %q", pe.RetryAfter)
	}
}

func TestStreamReader_FiltersUsageChunkWhenNotRequested(t *testing.T) {
	// Default: client did not opt in to stream_options.include_usage. The
	// terminal {choices:[], usage:{...}} chunk must NOT be forwarded.
	body := strings.NewReader(`data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}` + "\n\n" +
		`data: {"id":"x","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}` + "\n\n" +
		`data: [DONE]` + "\n\n")

	r := NewStreamReader(io.NopCloser(body), "test", false)
	chunks := 0
	for {
		c, err := r.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if len(c.Choices) == 0 && c.Usage != nil {
			t.Fatalf("usage chunk leaked to client when emitUsageChunk=false: %+v", c)
		}
		chunks++
	}
	if r.GetUsage() == nil || r.GetUsage().TotalTokens != 3 {
		t.Fatalf("usage must still be captured internally: %+v", r.GetUsage())
	}
	if chunks != 1 {
		t.Fatalf("expected 1 client-visible chunk (the content delta), got %d", chunks)
	}
}

func TestStreamReader_ForwardsUsageChunkWhenRequested(t *testing.T) {
	body := strings.NewReader(`data: {"id":"x","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n" +
		`data: {"id":"x","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}` + "\n\n" +
		`data: [DONE]` + "\n\n")

	r := NewStreamReader(io.NopCloser(body), "test", true)
	gotUsageChunk := false
	for {
		c, err := r.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if len(c.Choices) == 0 && c.Usage != nil {
			gotUsageChunk = true
		}
	}
	if !gotUsageChunk {
		t.Fatalf("client opted in to include_usage but did not receive the usage chunk")
	}
}

func TestDetectStreamError_OpenAIShape(t *testing.T) {
	data := []byte(`{"error":{"message":"server overloaded","type":"server_error","code":503}}`)
	pe, ok := detectStreamError(data, "openai")
	if !ok {
		t.Fatalf("must detect error envelope")
	}
	if !strings.Contains(pe.Message, "server overloaded") {
		t.Fatalf("error message lost: %s", pe.Message)
	}
	if pe.StatusCode != 503 {
		t.Fatalf("status code from error.code mismatch: %d", pe.StatusCode)
	}
	// Wire format must include the actual provider name (not generic "upstream")
	// so handler errorBody surfaces the right upstream_provider in the body.
	if !strings.Contains(pe.Message, "openai api error (status 503): ") {
		t.Fatalf("error message must follow '<provider> api error (status N): ...' for unwrap + provider attribution: %s", pe.Message)
	}
}

func TestDetectStreamError_DefaultsToUpstreamWhenProviderEmpty(t *testing.T) {
	data := []byte(`{"error":{"message":"x","code":500}}`)
	pe, ok := detectStreamError(data, "")
	if !ok {
		t.Fatalf("must detect error envelope")
	}
	if !strings.HasPrefix(pe.Message, "upstream api error (status 500): ") {
		t.Fatalf("empty provider should fall back to 'upstream' label: %s", pe.Message)
	}
}

func TestDetectStreamError_ExtractsRetryAfter(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{"error":{"message":"slow down","retry_after":30}}`, "30"},
		{`{"error":{"message":"slow down","retry_after":"45"}}`, "45"},
		{`{"error":{"message":"slow down","retryAfter":7}}`, "7"},
		{`{"error":{"message":"no hint"}}`, ""},
	}
	for _, c := range cases {
		pe, ok := detectStreamError([]byte(c.body), "openai")
		if !ok {
			t.Fatalf("must detect error envelope for %s", c.body)
		}
		if pe.RetryAfter != c.want {
			t.Fatalf("body %s: want retry_after=%q, got %q", c.body, c.want, pe.RetryAfter)
		}
	}
}

func TestDetectStreamError_NormalChunkIgnored(t *testing.T) {
	data := []byte(`{"id":"x","choices":[{"delta":{"content":"hi"}}]}`)
	if _, ok := detectStreamError(data, "openai"); ok {
		t.Fatalf("normal chunk must not be flagged as error")
	}
}

func TestDetectStreamError_ErrorAlongsideChoicesIgnored(t *testing.T) {
	// Some providers attach a partial error to a final chunk; don't let it
	// override the chunk's content delivery.
	data := []byte(`{"id":"x","choices":[{"finish_reason":"stop"}],"error":{"message":"x"}}`)
	if _, ok := detectStreamError(data, "openai"); ok {
		t.Fatalf("error alongside choices should be deferred to chunk handling")
	}
}

func TestBuildRequest_PrefillFieldNameAttachedPerProvider(t *testing.T) {
	on := true
	req := &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "ack:"},
			{Role: "assistant", Content: "ack:", Prefix: &on},
		},
	}
	cases := []struct {
		name      string
		fieldName string
		wantKey   string
		hasNoise  []string // keys that must NOT appear on the assistant turn
	}{
		{"deepseek-style", "prefix", "prefix", []string{"partial"}},
		{"kimi-style", "partial", "partial", []string{"prefix"}},
		{"no-prefill-provider", "", "", []string{"prefix", "partial"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := buildRequest("m", req, c.fieldName)
			msgs := body["messages"].([]map[string]any)
			assistant := msgs[1]
			if c.wantKey != "" && assistant[c.wantKey] != true {
				t.Fatalf("expected %s=true on assistant, got %+v", c.wantKey, assistant)
			}
			for _, noisy := range c.hasNoise {
				if _, has := assistant[noisy]; has {
					t.Fatalf("%s should not appear on assistant for provider style %q: %+v", noisy, c.name, assistant)
				}
			}
		})
	}
}

func TestBuildRequest_PrefillNotEmittedWithoutPrefixField(t *testing.T) {
	// Even when configured for prefix-style, an assistant message without
	// Prefix set must not emit the field (otherwise we falsify the user's
	// intent and silently change the model's behavior).
	body := buildRequest("m", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}, "prefix")
	msgs := body["messages"].([]map[string]any)
	if _, has := msgs[1]["prefix"]; has {
		t.Fatalf("prefix must not be emitted when Message.Prefix is nil: %+v", msgs[1])
	}
}

func TestBuildRequest_ForwardsProviderSpecificExtensions(t *testing.T) {
	// 各厂商专属字段 (cache_id/Kimi、do_sample+request_id/Zhipu、bot_setting/MiniMax)
	// 都通过 compat 透传：非目标厂商收到后忽略，目标厂商正常消费。
	greedy := false
	toolStream := true
	body := buildRequest("any", &provider.ChatCompletionRequest{
		Messages:   []provider.Message{{Role: "user", Content: "hi"}},
		CacheID:    "kimi-cache-key-abc",
		DoSample:   &greedy,
		ToolStream: &toolStream,
		RequestID:  "req-12345",
		BotSetting: []map[string]any{{"bot_name": "MM Pro", "content": "..."}},
	}, "")
	if body["cache_id"] != "kimi-cache-key-abc" {
		t.Fatalf("cache_id (Kimi) lost: %#v", body["cache_id"])
	}
	if got, ok := body["do_sample"].(bool); !ok || got != false {
		t.Fatalf("do_sample (Zhipu) lost: %#v", body["do_sample"])
	}
	if got, ok := body["tool_stream"].(bool); !ok || got != true {
		t.Fatalf("tool_stream (Zhipu) lost: %#v", body["tool_stream"])
	}
	if body["request_id"] != "req-12345" {
		t.Fatalf("request_id (Zhipu) lost: %#v", body["request_id"])
	}
	if body["bot_setting"] == nil {
		t.Fatalf("bot_setting (MiniMax) lost: %+v", body)
	}
}

func TestBuildRequest_ThinkingStripsAnthropicBudgetTokens(t *testing.T) {
	// 智谱 GLM-4.6+ / Kimi 等只接受 thinking={"type":"enabled|disabled"}。
	// Anthropic 专属的 budget_tokens 字段必须剥离，否则严格 schema 校验
	// 会直接 400。
	body := buildRequest("glm-4.6", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Thinking: &provider.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 2048,
		},
	}, "")
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking should be a map, got %T: %#v", body["thinking"], body["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type lost: %#v", thinking)
	}
	if _, has := thinking["budget_tokens"]; has {
		t.Fatalf("budget_tokens must be stripped before forwarding to compat upstreams: %#v", thinking)
	}
}

func TestBuildRequest_ThinkingEmptyTypeOmitted(t *testing.T) {
	// 没有 type 时不发 thinking 字段（避免触发上游 thinking 默认开启）。
	body := buildRequest("glm-4.6", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Thinking: &provider.ThinkingConfig{BudgetTokens: 1024},
	}, "")
	if _, has := body["thinking"]; has {
		t.Fatalf("thinking without type should be omitted, got %+v", body["thinking"])
	}
}

func TestBuildRequest_ForwardsTopK(t *testing.T) {
	// top_k is non-standard for OpenAI but accepted by Zhipu/SiliconFlow/some
	// Kimi models. Silent drop hides bugs in tuning code; forward it and let
	// the upstream reject if it's not supported.
	topK := 40
	body := buildRequest("some-model", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		TopK:     &topK,
	}, "")
	if got, ok := body["top_k"].(int); !ok || got != 40 {
		t.Fatalf("top_k should be forwarded, got %#v", body["top_k"])
	}
}

func TestBuildRequest_PrefersMaxCompletionTokens(t *testing.T) {
	mc := 256
	mt := 128
	body := buildRequest("m", &provider.ChatCompletionRequest{
		Messages:            []provider.Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: &mc,
		MaxTokens:           &mt,
	}, "")
	// max_completion_tokens wins when both are present (modern OpenAI surface).
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("max_tokens should not be sent when max_completion_tokens is present, got %+v", body)
	}
	if got, ok := body["max_completion_tokens"].(int); !ok || got != mc {
		t.Fatalf("max_completion_tokens missing or wrong: %+v", body)
	}
}

func TestBuildRequest_ForwardsParallelToolCalls(t *testing.T) {
	off := false
	on := true
	cases := []struct {
		name string
		val  *bool
	}{
		{"explicit false", &off},
		{"explicit true", &on},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := buildRequest("m", &provider.ChatCompletionRequest{
				Messages:          []provider.Message{{Role: "user", Content: "hi"}},
				ParallelToolCalls: c.val,
			}, "")
			got, ok := body["parallel_tool_calls"].(bool)
			if !ok || got != *c.val {
				t.Fatalf("parallel_tool_calls not forwarded: %#v", body["parallel_tool_calls"])
			}
		})
	}
}

func TestBuildRequest_ForwardsServiceTierAndUser(t *testing.T) {
	tier := "flex"
	body := buildRequest("m", &provider.ChatCompletionRequest{
		Messages:    []provider.Message{{Role: "user", Content: "hi"}},
		ServiceTier: &tier,
		User:        "alice@example.com",
	}, "")
	if body["service_tier"] != "flex" {
		t.Fatalf("service_tier missing: %+v", body)
	}
	if body["user"] != "alice@example.com" {
		t.Fatalf("user missing: %+v", body)
	}
}

func TestBuildRequest_ForwardsStoreAndMetadata(t *testing.T) {
	storeOn := true
	body := buildRequest("m", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Store:    &storeOn,
		Metadata: map[string]string{"team": "platform", "env": "prod"},
	}, "")
	if got, ok := body["store"].(bool); !ok || got != true {
		t.Fatalf("store not forwarded: %#v", body["store"])
	}
	meta, ok := body["metadata"].(map[string]string)
	if !ok || meta["team"] != "platform" {
		t.Fatalf("metadata not forwarded: %#v", body["metadata"])
	}
}

func TestBuildRequest_OmitsUnsetParams(t *testing.T) {
	body := buildRequest("m", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, "")
	for _, key := range []string{"temperature", "top_p", "top_k", "max_tokens", "max_completion_tokens", "frequency_penalty", "presence_penalty", "seed", "n", "logprobs", "top_logprobs", "tools", "tool_choice", "tool_stream", "response_format", "reasoning_effort", "thinking", "stop", "parallel_tool_calls", "service_tier", "user", "store", "metadata", "verbosity", "prompt_cache_key", "safety_identifier"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must not appear in body when unset, got %+v", key, body)
		}
	}
}
