package volcengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// Ark serves chat and the native video API off the same /api/v3 base, so chat
// delegation reuses baseURL verbatim. This pins that it does — a stray suffix
// here would 404 every chat call.
func TestChatUsesArkBase(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth header = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		// Doubao takes either a model ID or an endpoint ID here; both pass through.
		if body["model"] != "ep-20250101-abcde" {
			t.Errorf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer ts.Close()

	resp, err := New(ts.URL).ChatCompletion(context.Background(), "sk-test", "ep-20250101-abcde", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions relative to the Ark base", gotPath)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Errorf("response not parsed: %+v", resp)
	}
}

// Video and chat share a base but not a path; adding chat must not disturb the
// video task endpoint.
func TestVideoPathUnaffectedByChatDelegation(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"t1","status":"queued"}`))
	}))
	defer ts.Close()

	if _, err := New(ts.URL).CreateVideoJob(context.Background(), "sk-test", "doubao-seedance", &provider.VideoCreateRequest{Prompt: "x"}); err != nil {
		t.Fatalf("CreateVideoJob: %v", err)
	}
	if gotPath != "/contents/generations/tasks" {
		t.Errorf("video path = %q — chat delegation must not move it", gotPath)
	}
}
