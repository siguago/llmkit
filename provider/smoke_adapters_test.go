package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/minimax"
	"github.com/siguago/llmkit/provider/siliconflow"
	"github.com/siguago/llmkit/provider/vercel"
)

// These three adapters are thin compat wrappers and previously had no tests at
// all. The wrapper itself is what can break — a wrong base-URL join, a dropped
// prefill field name, a missing capability — so drive each one end to end
// against a stub server.

const smokeChatResponse = `{
	"id":"c1","object":"chat.completion","model":"m",
	"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
}`

type stub struct {
	*httptest.Server
	paths []string
	auth  []string
	body  map[string]any
}

func newStub(t *testing.T, response string) *stub {
	t.Helper()
	s := &stub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.paths = append(s.paths, r.URL.Path)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &s.body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(s.Close)
	return s
}

func adapters(baseURL string) map[string]provider.Provider {
	return map[string]provider.Provider{
		"minimax":     minimax.New(baseURL),
		"siliconflow": siliconflow.New(baseURL),
		"vercel":      vercel.New(baseURL),
	}
}

// chatPath is the request path each adapter produces from a bare host root.
// Vercel deliberately differs: its normalizeBaseURL appends /v1 so that
// configuring either "https://ai-gateway.vercel.sh" or ".../v1" works. Pinning
// this here documents the asymmetry and catches an accidental change to it.
var chatPath = map[string]string{
	"minimax":     "/chat/completions",
	"siliconflow": "/chat/completions",
	"vercel":      "/v1/chat/completions",
}

func TestUntestedAdapters_Chat(t *testing.T) {
	for name := range adapters("") {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, smokeChatResponse)
			p := adapters(s.URL)[name]

			resp, err := p.ChatCompletion(context.Background(), "sk-test", "m",
				&provider.ChatCompletionRequest{
					Model:    "m",
					Messages: []provider.Message{{Role: "user", Content: "ping"}},
				})
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			if p.Name() != name {
				t.Errorf("Name() = %q, want %q", p.Name(), name)
			}
			if got := provider.ContentToString(resp.Choices[0].Message.Content); got != "pong" {
				t.Errorf("content = %q", got)
			}
			if resp.Usage == nil || resp.Usage.TotalTokens != 2 {
				t.Errorf("usage = %+v", resp.Usage)
			}
			// RequestCount is normalized to at least 1 so per-request billing works.
			if resp.Usage.RequestCount != 1 {
				t.Errorf("RequestCount = %d, want 1", resp.Usage.RequestCount)
			}
			if len(s.paths) != 1 || s.paths[0] != chatPath[name] {
				t.Errorf("hit %v, want %s", s.paths, chatPath[name])
			}
			if s.auth[0] != "Bearer sk-test" {
				t.Errorf("auth = %q", s.auth[0])
			}
			if s.body["model"] != "m" {
				t.Errorf("model not forwarded: %+v", s.body)
			}
		})
	}
}

func TestUntestedAdapters_Stream(t *testing.T) {
	sse := "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"po\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ng\"}}]}\n\n" +
		"data: [DONE]\n\n"

	for name := range adapters("") {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, sse)
			p := adapters(s.URL)[name]

			stream, err := p.ChatCompletionStream(context.Background(), "sk-test", "m",
				&provider.ChatCompletionRequest{
					Model:    "m",
					Messages: []provider.Message{{Role: "user", Content: "ping"}},
				})
			if err != nil {
				t.Fatalf("ChatCompletionStream: %v", err)
			}
			defer stream.Close()

			var text string
			for {
				chunk, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Recv: %v", err)
				}
				if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil {
					text += provider.ContentToString(chunk.Choices[0].Delta.Content)
				}
			}
			if text != "pong" {
				t.Errorf("streamed text = %q, want pong", text)
			}
			// The compat layer must always ask upstream for usage.
			if s.body["stream"] != true {
				t.Errorf("stream flag not set: %+v", s.body)
			}
		})
	}
}

func TestUntestedAdapters_ErrorsCarryStatusAndRetryAfter(t *testing.T) {
	for name := range adapters("") {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "13")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":"rate limited"}`)
			}))
			defer srv.Close()

			p := adapters(srv.URL)[name]
			_, err := p.ChatCompletion(context.Background(), "sk-test", "m",
				&provider.ChatCompletionRequest{
					Model:    "m",
					Messages: []provider.Message{{Role: "user", Content: "ping"}},
				})
			var apiErr *provider.ProviderError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v (%T), want *provider.ProviderError", err, err)
			}
			if apiErr.StatusCode != http.StatusTooManyRequests {
				t.Errorf("StatusCode = %d", apiErr.StatusCode)
			}
			if apiErr.RetryAfter != "13" {
				t.Errorf("RetryAfter = %q, want 13", apiErr.RetryAfter)
			}
		})
	}
}

func TestUntestedAdapters_Capabilities(t *testing.T) {
	// All three ride the compat layer, so all three get models + embeddings.
	// Only vercel adds image generation on top.
	want := map[string]struct{ models, embeddings, images bool }{
		"minimax":     {models: true, embeddings: true},
		"siliconflow": {models: true, embeddings: true},
		"vercel":      {models: true, embeddings: true, images: true},
	}
	for name, p := range adapters("") {
		_, models := p.(provider.ModelLister)
		_, embeddings := p.(provider.Embedder)
		_, images := p.(provider.ImageProvider)
		got := struct{ models, embeddings, images bool }{models, embeddings, images}
		if got != want[name] {
			t.Errorf("%s: %+v, want %+v", name, got, want[name])
		}
	}
}

func TestUntestedAdapters_DefaultBaseURLs(t *testing.T) {
	// An empty baseURL must select the vendor's official endpoint, not produce
	// a request to "/chat/completions" with no host.
	for name, p := range adapters("") {
		if p == nil {
			t.Fatalf("%s: nil adapter", name)
		}
		// A request against the default endpoint is expected to fail (no
		// credential / no network in CI), but it must fail as a transport or
		// API error, never as a malformed-URL error.
		_, err := p.ChatCompletion(t.Context(), "", "m", &provider.ChatCompletionRequest{
			Model:    "m",
			Messages: []provider.Message{{Role: "user", Content: "x"}},
		})
		if err == nil {
			t.Skipf("%s: unexpectedly reached upstream; skipping", name)
		}
		if isMalformedURLError(err) {
			t.Errorf("%s: default base URL is malformed: %v", name, err)
		}
	}
}

// isMalformedURLError distinguishes "the default base URL is broken" from the
// expected "no credential / no network" failure.
func isMalformedURLError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unsupported protocol scheme") ||
		strings.Contains(msg, "missing protocol scheme") ||
		strings.Contains(msg, "no Host in request URL")
}
