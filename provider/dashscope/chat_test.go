package dashscope

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// Chat is delegated to the OpenAI-compatible surface, which lives under a
// different prefix than the native video API this adapter also speaks. The path
// is the whole contract: get it wrong and every chat call 404s while video keeps
// working, which is a confusing way to fail.
func TestChatUsesCompatibleModePath(t *testing.T) {
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
		if body["model"] != "qwen-plus" {
			t.Errorf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer ts.Close()

	resp, err := New(ts.URL).ChatCompletion(context.Background(), "sk-test", "qwen-plus", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotPath != "/compatible-mode/v1/chat/completions" {
		t.Errorf("path = %q, want the compatible-mode chat path", gotPath)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Errorf("response not parsed: %+v", resp)
	}
}

// A baseURL already pointing at the compatible endpoint must not have the
// suffix appended a second time — configuring either form is a coin flip users
// lose otherwise, and a doubled path fails as an opaque 404.
func TestCompatBaseURLIsIdempotent(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://dashscope.aliyuncs.com", "https://dashscope.aliyuncs.com/compatible-mode/v1"},
		{"https://dashscope.aliyuncs.com/compatible-mode/v1", "https://dashscope.aliyuncs.com/compatible-mode/v1"},
		{"https://dashscope-intl.aliyuncs.com", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
	} {
		if got := compatBaseURL(tc.in); got != tc.want {
			t.Errorf("compatBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The adapter serves two unrelated upstream APIs; neither may move the other's
// base. This pins that video still targets the host root after chat was added.
func TestVideoPathUnaffectedByCompatBase(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":{"task_id":"t1","task_status":"PENDING"}}`))
	}))
	defer ts.Close()

	if _, err := New(ts.URL).CreateVideoJob(context.Background(), "sk-test", "wan2.7-t2v", &provider.VideoCreateRequest{Prompt: "x"}); err != nil {
		t.Fatalf("CreateVideoJob: %v", err)
	}
	if gotPath != "/api/v1/services/aigc/video-generation/video-synthesis" {
		t.Errorf("video path = %q — chat delegation must not move it", gotPath)
	}
}
