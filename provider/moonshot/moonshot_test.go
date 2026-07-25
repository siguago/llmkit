package moonshot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// TestNew_PrefillFieldNamePartial guards against accidental removal of the
// PrefillFieldName="partial" wiring in moonshot.New. The compat layer is what
// actually emits the field, but the wiring lives here — without this test, a
// future refactor could silently strip the assistant prefill semantic from
// every Kimi request through the gateway.
//
// We exercise it end-to-end via httptest so the assertion is on the actual
// outbound wire body, not internal state.
func TestNew_PrefillFieldNamePartial(t *testing.T) {
	var capturedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	p := New(upstream.URL)
	if p == nil {
		t.Fatal("New returned nil")
	}

	on := true
	_, err := p.ChatCompletion(context.Background(), "test-key", "kimi-k2", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "ack:"},
			{Role: "assistant", Content: "ack:", Prefix: &on},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}

	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("upstream body parse: %v\nraw: %s", err, capturedBody)
	}
	if len(body.Messages) < 2 {
		t.Fatalf("expected 2 messages, got %d", len(body.Messages))
	}
	assistant := body.Messages[1]
	if assistant["partial"] != true {
		t.Fatalf("Moonshot must emit partial=true (Kimi prefill key) on assistant message, got %+v", assistant)
	}
	// Moonshot must NOT emit "prefix" — that's DeepSeek-only and would be an
	// unknown field on the Kimi request.
	if _, has := assistant["prefix"]; has {
		t.Fatalf("Moonshot must not emit DeepSeek-style prefix field: %+v", assistant)
	}
}

func TestNew_DefaultBaseURLWhenEmpty(t *testing.T) {
	// Semantic invariants on the default endpoint (no need to repeat the
	// literal — that would just be a tautology). What we actually care about:
	//   - HTTPS (no plain HTTP fallback)
	//   - Moonshot domain (not accidentally pointing elsewhere)
	//   - International (.ai) host — mainland China users must opt in via
	//     config.providers.moonshot_base_url, not have it silently flipped
	//     by an upstream change. If you ARE switching the gateway default to
	//     .cn, also update config.example.yaml + deploy docs and adjust this
	//     guard.
	if !strings.HasPrefix(defaultBaseURL, "https://") {
		t.Fatalf("defaultBaseURL must use HTTPS: %s", defaultBaseURL)
	}
	if !strings.Contains(defaultBaseURL, ".moonshot.") {
		t.Fatalf("defaultBaseURL must point at a Moonshot domain: %s", defaultBaseURL)
	}
	if !strings.Contains(defaultBaseURL, ".moonshot.ai") {
		t.Fatalf("defaultBaseURL is no longer the international (.ai) endpoint — "+
			"if intentional, update config.example.yaml and this guard together: %s", defaultBaseURL)
	}

	p := New("")
	if p == nil {
		t.Fatal("New(\"\") returned nil")
	}
	if p.Name() != "moonshot" {
		t.Fatalf("provider name mismatch: %s", p.Name())
	}
}

func TestNew_AcceptsCustomBaseURL(t *testing.T) {
	// Mainland China users pass api.moonshot.cn; the override must be honored.
	// Indirectly verified by capturing the outbound URL via httptest.
	var hitURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitURL = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	p := New(upstream.URL + "/custom-prefix")
	_, err := p.ChatCompletion(context.Background(), "k", "kimi-k2", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}
	if !strings.HasPrefix(hitURL, "/custom-prefix/") {
		t.Fatalf("custom baseURL prefix not honored, hit %s", hitURL)
	}
}
