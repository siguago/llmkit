package llmkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const chatOK = `{
	"id": "cmpl-1",
	"object": "chat.completion",
	"model": "test-model",
	"choices": [{"index":0,"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],
	"usage": {"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
}`

// newTestClient wires a Client to an httptest server through the DeepSeek
// adapter, which is a thin OpenAI-compatible passthrough — the closest thing to
// testing the façade without a vendor's quirks in the way.
func newTestClient(t *testing.T, h http.HandlerFunc, opts ...Option) *Client {
	return newTestClientFor(t, DeepSeek, h, opts...)
}

// newTestClientFor is newTestClient against a specific provider, for exercising
// capabilities DeepSeek's adapter doesn't implement.
func newTestClientFor(t *testing.T, providerName string, h http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	all := append([]Option{WithAPIKey("sk-test"), WithBaseURL(srv.URL)}, opts...)
	c, err := New(providerName, all...)
	if err != nil {
		t.Fatalf("New(%s): %v", providerName, err)
	}
	return c
}

func TestNew_UnknownProvider(t *testing.T) {
	_, err := New("not-a-vendor", WithAPIKey("k"))
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("err = %v, want ErrUnknownProvider", err)
	}
}

func TestNew_MissingAPIKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	_, err := New(DeepSeek)
	if !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestNew_APIKeyFromEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-from-env")
	c, err := New(DeepSeek)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.apiKey != "sk-from-env" {
		t.Errorf("apiKey = %q, want sk-from-env", c.cfg.apiKey)
	}
}

func TestProviders_CoversEveryFactory(t *testing.T) {
	names := Providers()
	if len(names) != len(factories) {
		t.Fatalf("Providers() returned %d names, factories has %d", len(names), len(factories))
	}
	for _, name := range names {
		if EnvVar(name) == "" {
			t.Errorf("provider %q has no env var registered", name)
		}
		// Every factory must produce a usable adapter whose Name matches the key
		// callers use — otherwise Wrap's env lookup silently misses.
		p := factories[name]("")
		if p == nil {
			t.Errorf("factory %q returned nil", name)
			continue
		}
		if p.Name() != name {
			t.Errorf("factory %q built adapter named %q", name, p.Name())
		}
	}
}

func TestChat_HappyPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	})

	resp, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hello")},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := ResponseText(resp); got != "hi there" {
		t.Errorf("text = %q, want %q", got, "hi there")
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage not decoded: %+v", resp.Usage)
	}
}

func TestChat_RejectsEmptyModel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called")
	})
	if _, err := c.Chat(context.Background(), &ChatRequest{Messages: []Message{User("x")}}); err == nil {
		t.Error("expected an error for a missing model")
	}
	if _, err := c.Chat(context.Background(), nil); err == nil {
		t.Error("expected an error for a nil request")
	}
}

func TestChat_RetriesServerErrorThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"overloaded"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithRetry(RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}))

	resp, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hello")},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if ResponseText(resp) != "hi there" {
		t.Errorf("unexpected text %q", ResponseText(resp))
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("upstream calls = %d, want 3", got)
	}
}

func TestChat_DoesNotRetryBadRequest(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad model"}`)
	}, WithRetry(RetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond}))

	_, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hello")},
	})
	if !IsInvalidRequest(err) {
		t.Fatalf("err = %v, want a 400", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (400 is not retryable)", got)
	}
}

func TestChat_SurfacesRetryAfter(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"slow down"}`)
	}, WithRetry(NoRetry()))

	_, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hello")},
	})
	if !IsRateLimited(err) {
		t.Fatalf("err = %v, want a 429", err)
	}
	if got := RetryAfter(err); got != 42*time.Second {
		t.Errorf("RetryAfter = %v, want 42s", got)
	}
}

func TestWithHeader_ReachesUpstream(t *testing.T) {
	var sawReferer, sawTitle, sawAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sawReferer = r.Header.Get("HTTP-Referer")
		sawTitle = r.Header.Get("X-Title")
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	},
		WithHeader("HTTP-Referer", "https://example.com"),
		WithHeaders(map[string]string{"X-Title": "my app"}),
	)

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hello")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if sawReferer != "https://example.com" {
		t.Errorf("HTTP-Referer = %q", sawReferer)
	}
	if sawTitle != "my app" {
		t.Errorf("X-Title = %q", sawTitle)
	}
	if sawAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want the adapter's own credential", sawAuth)
	}
}

// A caller-supplied header must never be able to replace the credential the
// adapter negotiated.
func TestWithHeader_CannotOverrideCredential(t *testing.T) {
	var sawAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithHeader("Authorization", "Bearer sk-attacker"))

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hello")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if sawAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want the client's own key", sawAuth)
	}
}

func TestWithTimeout_BoundsTheCall(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}, WithTimeout(100*time.Millisecond), WithRetry(NoRetry()))

	start := time.Now()
	_, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hello")},
	})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("call took %v; WithTimeout did not bound it", elapsed)
	}
}

func TestChatStream_DeltasAndUsage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		frames := []string{
			`data: {"id":"1","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
			`data: {"id":"1","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"content":"lo!"}}]}`,
			`data: {"id":"1","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
			`data: [DONE]`,
		}
		for _, f := range frames {
			_, _ = io.WriteString(w, f+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	})

	text, usage, err := c.StreamText(context.Background(), "test-model", "hi", nil)
	if err != nil {
		t.Fatalf("StreamText: %v", err)
	}
	if text != "Hello!" {
		t.Errorf("text = %q, want %q", text, "Hello!")
	}
	if usage == nil || usage.TotalTokens != 3 {
		t.Errorf("usage = %+v, want total 3", usage)
	}
}

func TestChatStream_RetriesHandshakeFailure(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}, WithRetry(RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}))

	text, _, err := c.StreamText(context.Background(), "test-model", "hi", nil)
	if err != nil {
		t.Fatalf("StreamText: %v", err)
	}
	if text != "ok" {
		t.Errorf("text = %q", text)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
}

func TestStreamChat_InvokesCallbackPerDelta(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\n\n"+
				"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"b\"}}]}\n\n"+
				"data: [DONE]\n\n")
	})

	var got []string
	text, _, err := c.StreamChat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}, func(s string) { got = append(got, s) })
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if strings.Join(got, "") != "ab" || text != "ab" {
		t.Errorf("deltas = %v, text = %q", got, text)
	}
}

type caps struct {
	chat                  bool
	models, embeddings    bool
	imageGen, imageEdit   bool
	videoGen, videoCancel bool
}

// TestCapabilityMatrix pins the optional interfaces each adapter implements.
// It is the executable copy of the capability table in the README: when an
// adapter gains or loses a capability, this test says so.
//
// Image and video are tracked per endpoint, not per feature, because vendor
// support is per endpoint: the aggregators generate images without being able
// to edit them, and nobody can cancel a video job. A single images/video column
// would have to round one way or the other, and either way it lies to callers.
func TestCapabilityMatrix(t *testing.T) {
	want := map[string]caps{
		Anthropic: {chat: true, models: true},
		// No embeddings route this SDK can reach, so these adapters withhold the
		// capability (compat.NoEmbeddings) rather than answer 404 to a caller who
		// asked first. Each is a different flavor of "not there", all verified
		// against vendor docs — see each adapter's New for the specifics:
		//   xai / groq / cerebras — no /embeddings endpoint published at all
		//   moonshot             — Kimi publishes no embeddings API
		//   volcengine           — text API retired; the live one embeds a whole
		//                          multimodal input into ONE vector, so it is a
		//                          different operation, not a different spelling
		//
		// MiniMax is the counter-example below: its route is not OpenAI-shaped
		// either, but the batch semantics do line up, so the adapter translates.
		Cerebras:    {chat: true, models: true},
		Groq:        {chat: true, models: true},
		XAI:         {chat: true, models: true},
		Moonshot:    {chat: true, models: true},
		Volcengine:  {chat: true, models: true, videoGen: true, videoCancel: true},
		DashScope:   {chat: true, models: true, embeddings: true, videoGen: true},
		DeepSeek:    {chat: true, models: true},
		Fireworks:   {chat: true, models: true, embeddings: true},
		EasyRouter:  {chat: true, models: true, embeddings: true, imageGen: true, imageEdit: true, videoGen: true},
		Gemini:      {chat: true, models: true, imageGen: true, imageEdit: true, videoGen: true},
		MiniMax:     {chat: true, models: true, embeddings: true}, // hand-written, not compat's
		Mistral:     {chat: true, models: true, embeddings: true},
		Ollama:      {chat: true, models: true, embeddings: true},
		OpenAI:      {chat: true, models: true, embeddings: true, imageGen: true, imageEdit: true},
		OpenRouter:  {chat: true, models: true, imageGen: true, videoGen: true},
		SiliconFlow: {chat: true, models: true, embeddings: true},
		Together:    {chat: true, models: true, embeddings: true},
		VLLM:        {chat: true, models: true, embeddings: true},
		Vercel:      {chat: true, models: true, embeddings: true, imageGen: true},
		Zhipu:       {chat: true, models: true, embeddings: true},
	}
	if len(want) != len(factories) {
		t.Fatalf("matrix covers %d providers, factories has %d", len(want), len(factories))
	}
	for name, expect := range want {
		c, err := Wrap(factories[name](""), WithAPIKey("sk-test"))
		if err != nil {
			t.Fatalf("Wrap(%s): %v", name, err)
		}
		got := caps{
			chat:        c.SupportsChat(),
			models:      c.SupportsModels(),
			embeddings:  c.SupportsEmbeddings(),
			imageGen:    c.SupportsImageGeneration(),
			imageEdit:   c.SupportsImageEditing(),
			videoGen:    c.SupportsVideoGeneration(),
			videoCancel: c.SupportsVideoCancellation(),
		}
		if got != expect {
			t.Errorf("%s capabilities = %+v, want %+v", name, got, expect)
		}
	}
}

// Volcengine is the only provider with a real cancel endpoint. Every other
// video provider must report false AND return ErrUnsupported — the two have to
// agree, because a probe that says "supported" and a call that says otherwise is
// exactly the mismatch this split was meant to remove.
func TestVideoCancellation_ProbeMatchesBehavior(t *testing.T) {
	for name := range factories {
		c, err := Wrap(factories[name](""), WithAPIKey("sk-test"))
		if err != nil {
			t.Fatalf("Wrap(%s): %v", name, err)
		}
		wantCancel := name == Volcengine
		if got := c.SupportsVideoCancellation(); got != wantCancel {
			t.Errorf("%s: SupportsVideoCancellation = %v, want %v — update the docs on CancelVideo", name, got, wantCancel)
		}
		if wantCancel {
			continue
		}
		_, err = c.CancelVideo(context.Background(), &VideoJob{ID: "j1"})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: CancelVideo err = %v, want ErrUnsupported", name, err)
		}
	}
}

// The deprecated aliases must keep reporting what they always did for the
// generation case, so existing callers don't silently change behavior.
func TestDeprecatedCapabilityAliases(t *testing.T) {
	for name := range factories {
		c, err := Wrap(factories[name](""), WithAPIKey("sk-test"))
		if err != nil {
			t.Fatalf("Wrap(%s): %v", name, err)
		}
		if c.SupportsImages() != c.SupportsImageGeneration() {
			t.Errorf("%s: SupportsImages diverged from SupportsImageGeneration", name)
		}
		if c.SupportsVideo() != c.SupportsVideoGeneration() {
			t.Errorf("%s: SupportsVideo diverged from SupportsVideoGeneration", name)
		}
	}
}

func TestUnsupportedCapability_ReturnsTypedError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called")
	})

	_, err := c.GenerateImage(context.Background(), &ImageRequest{Model: "x", Prompt: "y"})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("GenerateImage err = %v, want ErrUnsupported", err)
	}
	if !IsUnsupportedCapability(err) {
		t.Error("IsUnsupportedCapability should report true")
	}
	if !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("error should name the provider: %v", err)
	}

	if _, err := c.CreateVideo(context.Background(), &VideoRequest{Model: "x", Prompt: "y"}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("CreateVideo err = %v, want ErrUnsupported", err)
	}
}

func TestEmbed(t *testing.T) {
	// Zhipu rides the OpenAI-compat layer, which carries the embeddings
	// endpoint; DeepSeek's native adapter deliberately does not (the vendor
	// has no embeddings API).
	c := newTestClientFor(t, Zhipu, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			t.Errorf("path = %s, want an /embeddings endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","model":"emb","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`)
	})

	resp, err := c.Embed(context.Background(), &EmbeddingRequest{Model: "emb", Input: "hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data = %+v", resp.Data)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 4 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

// MiniMax is the one provider whose Embeddings is hand-written rather than
// inherited from the compat layer, so it is the one whose facade path — the
// Embedder type assertion in Client.Embed, and ProviderOptions surviving the trip
// — nothing else exercises. TestEmbed above covers the compat path via Zhipu.
//
// The request body here is copied verbatim from the "minimax embeddings" example
// in README.md. That example documents two things a caller cannot guess (the
// provider-keyed nesting, and that `type` lives there at all), and until this test
// existed nothing would have caught it going stale.
func TestEmbed_MiniMaxDocumentedExample(t *testing.T) {
	var body map[string]any
	var query string
	c := newTestClientFor(t, MiniMax, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"vectors":[[0.1,0.2],[0.3,0.4]],"total_tokens":6,"base_resp":{"status_code":0}}`)
	})

	resp, err := c.Embed(context.Background(), &EmbeddingRequest{
		Model: "embo-01",
		Input: []string{"天很蓝", "海很深"},
		ProviderOptions: map[string]any{"minimax": map[string]any{
			"type":     "query",
			"group_id": "182...",
		}},
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if body["type"] != "query" {
		t.Errorf("type = %v, want the ProviderOptions value to reach the wire", body["type"])
	}
	if query != "GroupId=182..." {
		t.Errorf("query = %q, want the group id in the query string", query)
	}
	if _, present := body["input"]; present {
		t.Errorf("sent OpenAI's `input`; MiniMax wants `texts`: %+v", body)
	}
	if len(resp.Data) != 2 || resp.Data[1].Index != 1 {
		t.Fatalf("data = %+v, want two positional items", resp.Data)
	}
	// Same assertion llmkit-probe makes, on the one provider that could break it.
	if _, ok := resp.Data[0].Embedding.([]any); !ok {
		t.Errorf("Embedding is %T, want []any like every compat provider", resp.Data[0].Embedding)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 6 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestSay(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	})
	got, err := c.Say(context.Background(), "test-model", "hello")
	if err != nil {
		t.Fatalf("Say: %v", err)
	}
	if got != "hi there" {
		t.Errorf("Say = %q", got)
	}
}

func TestWrap_UsesAdapterDirectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}))
	defer srv.Close()

	c, err := Wrap(factories[DeepSeek](srv.URL), WithAPIKey("sk-test"))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if c.Provider() != DeepSeek {
		t.Errorf("Provider() = %q", c.Provider())
	}
	if _, err := c.Say(context.Background(), "test-model", "hi"); err != nil {
		t.Fatalf("Say: %v", err)
	}
}

func TestModels(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("path = %s, want a /models endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"m-1"},{"id":"m-2"}]}`)
	})

	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0].ModelID != "m-1" {
		t.Errorf("models = %+v", models)
	}
}

// EditImage must NOT be retried: the upload readers are single-use, so a second
// attempt would send an empty body.
func TestEditImage_IsNeverRetried(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"overloaded"}`)
	}, WithRetry(RetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond}))

	_, err := c.EditImage(context.Background(), &ImageEditRequest{
		Model:  "gpt-image-1",
		Prompt: "edit",
		Images: []UploadPart{{
			Filename:    "in.png",
			ContentType: "image/png",
			Reader:      strings.NewReader("png-bytes"),
		}},
	})
	if !IsServerError(err) {
		t.Fatalf("err = %v, want the 503 surfaced", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 — retrying would resend a consumed reader", got)
	}
}

// fastRetry is DefaultRetry's shape with the waits collapsed, so retry-counting
// tests don't pay 1.5s of real backoff.
func fastRetry() RetryConfig {
	return RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
}

// A 5xx does not prove the vendor never took the job: an overloaded backend can
// start generating and then fail to answer. Replaying would bill twice, so the
// default policy stops after the first attempt.
func TestGenerateImage_NotRetriedOnServerError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}, WithRetry(fastRetry()))

	if _, err := c.GenerateImage(context.Background(), &ImageRequest{Model: "gpt-image-1", Prompt: "cat"}); err == nil {
		t.Fatal("GenerateImage: want error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 — a 5xx may mean the image was already generated and billed", got)
	}
}

// A 429 is a quota decision made before any generation starts, so replaying it
// cannot double-charge.
func TestGenerateImage_RetriedWhenReplaySafe(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"AAAA"}]}`)
	}, WithRetry(fastRetry()))

	resp, err := c.GenerateImage(context.Background(), &ImageRequest{Model: "gpt-image-1", Prompt: "cat"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "AAAA" {
		t.Errorf("data = %+v", resp.Data)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2 (a rate-limit refusal is safe to replay)", got)
	}
}

// WithMediaRetry hands the decision back to the caller, including the unsafe one.
func TestGenerateImage_MediaRetryOverride(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"AAAA"}]}`)
	}, WithMediaRetry(fastRetry()))

	if _, err := c.GenerateImage(context.Background(), &ImageRequest{Model: "gpt-image-1", Prompt: "cat"}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2 — WithMediaRetry opted back into replaying a 5xx", got)
	}
}

// Narrowing must never widen: a caller who disabled retries entirely still gets
// exactly one attempt, replay-safe error or not.
func TestGenerateImage_NoRetryStillWins(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}, WithRetry(NoRetry()))

	if _, err := c.GenerateImage(context.Background(), &ImageRequest{Model: "gpt-image-1", Prompt: "cat"}); err == nil {
		t.Fatal("GenerateImage: want error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

// WithRetry and WithMediaRetry must compose the same way in either order.
func TestMediaRetryPolicy_OptionOrderIndependent(t *testing.T) {
	custom := RetryConfig{MaxAttempts: 7, InitialBackoff: time.Millisecond}
	forward := clientConfig{}
	WithRetry(NoRetry())(&forward)
	WithMediaRetry(custom)(&forward)

	reverse := clientConfig{}
	WithMediaRetry(custom)(&reverse)
	WithRetry(NoRetry())(&reverse)

	if a, b := forward.mediaRetryPolicy().MaxAttempts, reverse.mediaRetryPolicy().MaxAttempts; a != b || a != 7 {
		t.Errorf("MaxAttempts forward=%d reverse=%d, want 7 both", a, b)
	}
}

func TestAdapter_ExposesUnderlyingProvider(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	if a := c.Adapter(); a == nil || a.Name() != DeepSeek {
		t.Errorf("Adapter() = %v", a)
	}
}

func TestSayWithSystem(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	})

	got, err := c.SayWithSystem(context.Background(), "test-model", "be terse", "hello")
	if err != nil {
		t.Fatalf("SayWithSystem: %v", err)
	}
	if got != "hi there" {
		t.Errorf("SayWithSystem = %q", got)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be terse" {
		t.Errorf("system message = %+v", first)
	}
}

// SupportsChat must agree with what the adapter actually does. A provider that
// reports chat support and then returns ErrUnsupported is the exact mismatch
// these capability probes exist to eliminate.
func TestSupportsChat_MatchesBehavior(t *testing.T) {
	for name := range factories {
		c, err := Wrap(factories[name](""), WithAPIKey("sk-test"))
		if err != nil {
			t.Fatalf("Wrap(%s): %v", name, err)
		}
		if c.SupportsChat() {
			continue
		}
		req := &ChatRequest{Model: "m", Messages: []Message{User("hi")}}
		if _, err := c.Chat(context.Background(), req); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: SupportsChat is false but Chat returned %v", name, err)
		}
		if _, err := c.ChatStream(context.Background(), req); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: SupportsChat is false but ChatStream returned %v", name, err)
		}
	}
}
