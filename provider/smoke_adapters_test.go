package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/cerebras"
	"github.com/siguago/llmkit/provider/fireworks"
	"github.com/siguago/llmkit/provider/groq"
	"github.com/siguago/llmkit/provider/minimax"
	"github.com/siguago/llmkit/provider/mistral"
	"github.com/siguago/llmkit/provider/ollama"
	"github.com/siguago/llmkit/provider/siliconflow"
	"github.com/siguago/llmkit/provider/together"
	"github.com/siguago/llmkit/provider/vercel"
	"github.com/siguago/llmkit/provider/vllm"
	"github.com/siguago/llmkit/provider/xai"
)

// These adapters are thin compat wrappers with no logic of their own to test in
// isolation. The wrapper itself is what can break — a wrong base-URL join, a
// dropped prefill field name, a capability it does not actually have — so drive
// each one end to end against a stub server.
//
// Every new compat wrapper belongs in adapters() below. A default endpoint typo
// sends requests to the wrong vendor or to no host at all, and nothing else in
// the suite would notice.

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
		"xai":         xai.New(baseURL),
		"mistral":     mistral.New(baseURL),
		"groq":        groq.New(baseURL),
		"together":    together.New(baseURL),
		"fireworks":   fireworks.New(baseURL),
		"cerebras":    cerebras.New(baseURL),
		"ollama":      ollama.New(baseURL),
		"vllm":        vllm.New(baseURL),
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
	"xai":         "/chat/completions",
	"mistral":     "/chat/completions",
	"groq":        "/chat/completions",
	"together":    "/chat/completions",
	"fireworks":   "/chat/completions",
	"cerebras":    "/chat/completions",
	"ollama":      "/chat/completions",
	"vllm":        "/chat/completions",
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

// prefillField is the JSON key each adapter uses to carry Message.Prefix, or ""
// for a vendor with no prefill mechanism.
//
// Both directions matter. A vendor that supports prefill and gets "" silently
// drops the caller's intent — the assistant just doesn't continue the text it was
// handed. A vendor that doesn't support it and gets a field name has an unknown
// key in its request body, which strict compat servers reject outright.
var prefillField = map[string]string{
	"mistral": "prefix", // same shape DeepSeek uses
}

func TestUntestedAdapters_Prefill(t *testing.T) {
	yes := true
	for name := range adapters("") {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, smokeChatResponse)
			p := adapters(s.URL)[name]

			_, err := p.ChatCompletion(context.Background(), "sk-test", "m",
				&provider.ChatCompletionRequest{
					Model: "m",
					Messages: []provider.Message{
						{Role: "user", Content: "count: 1, 2,"},
						{Role: "assistant", Content: "3,", Prefix: &yes},
					},
				})
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}

			msgs, ok := s.body["messages"].([]any)
			if !ok || len(msgs) != 2 {
				t.Fatalf("messages not forwarded: %+v", s.body)
			}
			assistant, ok := msgs[1].(map[string]any)
			if !ok {
				t.Fatalf("assistant message = %T", msgs[1])
			}

			field := prefillField[name]
			if field == "" {
				for k := range assistant {
					if k == "prefix" || k == "partial" {
						t.Errorf("%s sent a prefill field %q it has no support for: %+v", name, k, assistant)
					}
				}
				return
			}
			if assistant[field] != true {
				t.Errorf("%s dropped prefill: want %q=true, got %+v", name, field, assistant)
			}
		})
	}
}

type smokeCaps struct{ models, embeddings, imageGen, imageEdit bool }

func TestUntestedAdapters_Capabilities(t *testing.T) {
	// Riding the compat layer gets an adapter models + embeddings. Only vercel
	// adds images — and only generation: its gateway has no editing endpoint, so
	// it must NOT satisfy provider.ImageEditor. Pinning imageEdit false is the
	// regression guard against someone "completing" the interface with an
	// ErrUnsupported stub, which would make the capability check lie.
	//
	// xai / groq / cerebras report embeddings false on purpose: none publishes an
	// /embeddings route, so they build on compat.NoEmbeddings, which withholds the
	// promoted Embeddings method. This is the assertion that catches one of them
	// being switched back to compat.New without the route actually existing.
	//
	// minimax reports true without going through compat: it also builds on
	// NoEmbeddings — its route wants `texts`/`type` and answers with `vectors`, so
	// compat would get every field wrong — and then supplies its own translating
	// Embeddings. Both halves matter here, and this assertion covers the seam: drop
	// the hand-written method and it flips to false; embed compat.Provider instead
	// and it goes true for the wrong implementation.
	want := map[string]smokeCaps{
		"minimax":     {models: true, embeddings: true},
		"siliconflow": {models: true, embeddings: true},
		"vercel":      {models: true, embeddings: true, imageGen: true},
		"mistral":     {models: true, embeddings: true},
		"together":    {models: true, embeddings: true},
		"fireworks":   {models: true, embeddings: true},
		"ollama":      {models: true, embeddings: true},
		"vllm":        {models: true, embeddings: true},
		"xai":         {models: true},
		"groq":        {models: true},
		"cerebras":    {models: true},
	}
	for name, p := range adapters("") {
		var got smokeCaps
		_, got.models = p.(provider.ModelLister)
		_, got.embeddings = p.(provider.Embedder)
		_, got.imageGen = p.(provider.ImageGenerator)
		_, got.imageEdit = p.(provider.ImageEditor)
		if got != want[name] {
			t.Errorf("%s: %+v, want %+v", name, got, want[name])
		}
	}
}

// defaultChatURL is the exact endpoint an empty baseURL must resolve to. A typo
// here is a request sent to the wrong vendor, or to no host at all.
var defaultChatURL = map[string]string{
	"minimax":     "https://api.minimax.io/v1/chat/completions",
	"siliconflow": "https://api.siliconflow.cn/v1/chat/completions",
	"vercel":      "https://ai-gateway.vercel.sh/v1/chat/completions",
	"xai":         "https://api.x.ai/v1/chat/completions",
	"mistral":     "https://api.mistral.ai/v1/chat/completions",
	// Not /v1: Groq and Fireworks each serve their OpenAI-compatible surface under
	// a vendor-specific prefix, which is the single easiest thing to get wrong in a
	// wrapper this thin.
	"groq":      "https://api.groq.com/openai/v1/chat/completions",
	"fireworks": "https://api.fireworks.ai/inference/v1/chat/completions",
	"together":  "https://api.together.xyz/v1/chat/completions",
	"cerebras":  "https://api.cerebras.ai/v1/chat/completions",
	// Local runtimes: plain http, loopback. Nothing in the outbound path may
	// upgrade the scheme or refuse the host.
	"ollama": "http://localhost:11434/v1/chat/completions",
	"vllm":   "http://localhost:8000/v1/chat/completions",
}

func TestUntestedAdapters_DefaultBaseURLs(t *testing.T) {
	// This runs offline. The request is issued under an already-cancelled
	// context, so net/http builds the full URL, then aborts before dialing and
	// reports the target back in *url.Error.URL. That gives us the exact
	// endpoint to assert against without touching the network — the previous
	// version of this test reached the three vendors for real, which made the
	// suite slow behind a firewall and contradicted the "all default tests are
	// offline" promise in the README.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for name, p := range adapters("") {
		t.Run(name, func(t *testing.T) {
			if p == nil {
				t.Fatal("nil adapter")
			}
			_, err := p.ChatCompletion(ctx, "", "m", &provider.ChatCompletionRequest{
				Model:    "m",
				Messages: []provider.Message{{Role: "user", Content: "x"}},
			})
			var urlErr *url.Error
			if !errors.As(err, &urlErr) {
				t.Fatalf("err = %v (%T), want *url.Error carrying the target URL", err, err)
			}
			if !errors.Is(urlErr.Err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled — the request must not have been sent", urlErr.Err)
			}
			if got := urlErr.URL; got != defaultChatURL[name] {
				t.Errorf("default endpoint = %q, want %q", got, defaultChatURL[name])
			}
		})
	}
}
