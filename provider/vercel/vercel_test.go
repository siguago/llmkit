package vercel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Users configure either a host root or a full /v1 URL; both must end up at the
// same place, and a trailing slash must not produce a doubled path segment.
func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back to default", "", defaultBaseURL},
		{"whitespace only", "   ", defaultBaseURL},
		{"host root gains /v1", "https://ai-gateway.vercel.sh", "https://ai-gateway.vercel.sh/v1"},
		{"trailing slash", "https://ai-gateway.vercel.sh/", "https://ai-gateway.vercel.sh/v1"},
		{"already /v1", "https://ai-gateway.vercel.sh/v1", "https://ai-gateway.vercel.sh/v1"},
		{"/v1 with trailing slash", "https://ai-gateway.vercel.sh/v1/", "https://ai-gateway.vercel.sh/v1"},
		{"proxy with path prefix", "https://proxy.example/llm", "https://proxy.example/llm/v1"},
		{"query and fragment stripped", "https://proxy.example/v1?a=b#frag", "https://proxy.example/v1"},
		{"http preserved for local relays", "http://localhost:8080", "http://localhost:8080/v1"},
		// Not a parseable absolute URL: the string fallback still has to
		// guarantee the /v1 suffix rather than passing the input through.
		{"schemeless host", "ai-gateway.vercel.sh", "ai-gateway.vercel.sh/v1"},
		{"schemeless host already /v1", "ai-gateway.vercel.sh/v1", "ai-gateway.vercel.sh/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeBaseURL(tc.in); got != tc.want {
				t.Errorf("normalizeBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNew_UsesNormalizedBaseURL(t *testing.T) {
	if got := New("https://proxy.example").baseURL; got != "https://proxy.example/v1" {
		t.Errorf("baseURL = %q, want https://proxy.example/v1", got)
	}
	if got := New("").baseURL; got != defaultBaseURL {
		t.Errorf("baseURL = %q, want %q", got, defaultBaseURL)
	}
}

func TestName(t *testing.T) {
	if got := New("").Name(); got != name {
		t.Errorf("Name() = %q, want %q", got, name)
	}
}

// ListModels exists to surface Vercel's richer metadata and to hide models the
// gateway cannot actually dispatch to.
func TestListModels_MapsAndFilters(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[
			{"id":"openai/gpt-5","name":"GPT-5","type":"language","context_window":400000},
			{"id":"openai/text-embedding-3","name":"Embed 3","type":"embedding"},
			{"id":"legacy/no-type","name":"Legacy"},
			{"id":"openai/gpt-image-2","type":"image","pricing":{"input":"0.000001","output":"0.000002"}},
			{"id":"bfl/flux","type":"image","pricing":{"image":"0.04"}},
			{"id":"some/video","type":"video"},
			{"id":"some/rerank","type":"reranking"},
			{"id":"unnamed/model","type":"language"}
		]}`))
	}))
	defer srv.Close()

	models, err := New(srv.URL).ListModels(context.Background(), "vk-test")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer vk-test" {
		t.Errorf("Authorization = %q, want Bearer vk-test", gotAuth)
	}

	got := make(map[string]string, len(models))
	for _, m := range models {
		got[m.ModelID] = m.DisplayName
	}

	for _, id := range []string{"openai/gpt-5", "openai/text-embedding-3", "legacy/no-type", "openai/gpt-image-2", "unnamed/model"} {
		if _, ok := got[id]; !ok {
			t.Errorf("dispatchable model %q was filtered out", id)
		}
	}
	// Per-image-billed images, video and reranking have no dispatch path; if
	// they were imported they would become chat bindings with no token price.
	for _, id := range []string{"bfl/flux", "some/video", "some/rerank"} {
		if _, ok := got[id]; ok {
			t.Errorf("model %q has no dispatch path and should be filtered out", id)
		}
	}

	if got["openai/gpt-5"] != "GPT-5" {
		t.Errorf("display name lost: %q", got["openai/gpt-5"])
	}
	// A model with no name must still be usable, so the ID stands in.
	if got["unnamed/model"] != "unnamed/model" {
		t.Errorf("missing name should fall back to the ID, got %q", got["unnamed/model"])
	}

	for _, m := range models {
		if m.ModelID == "openai/gpt-5" && m.ContextWindow != 400000 {
			t.Errorf("context_window lost: %d", m.ContextWindow)
		}
	}
}

func TestListModels_EmptyCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	models, err := New(srv.URL).ListModels(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if models == nil {
		t.Error("an empty catalog should yield an empty slice, not nil")
	}
	if len(models) != 0 {
		t.Errorf("got %d models, want 0", len(models))
	}
}

func TestListModels_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL).ListModels(context.Background(), "k")
	if err == nil {
		t.Fatal("expected an error on 401")
	}
}

func TestListModels_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data": [`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL).ListModels(context.Background(), "k"); err == nil {
		t.Fatal("expected a decode error on a truncated body")
	}
}

func TestListModels_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := New(srv.URL).ListModels(ctx, "k"); err == nil {
		t.Fatal("expected the cancelled context to abort the request")
	}
}

// Vercel's /v1/models is public, but the Bearer header is sent anyway to keep
// every provider's ListModels shape uniform. An empty key must not produce a
// malformed "Bearer " header.
func TestListModels_NoKey(t *testing.T) {
	var sawAuth string
	var authPresent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth, authPresent = r.Header.Get("Authorization"), r.Header.Values("Authorization") != nil
		w.Write([]byte(`{"data":[{"id":"a","type":"language"}]}`))
	}))
	defer srv.Close()

	models, err := New(srv.URL).ListModels(context.Background(), "")
	if err != nil {
		t.Fatalf("an empty key should still work against a public catalog: %v", err)
	}
	if len(models) != 1 {
		t.Errorf("got %d models, want 1", len(models))
	}
	if authPresent && sawAuth == "Bearer " {
		t.Errorf("an empty key produced a malformed header %q", sawAuth)
	}
}

// A base URL that survives normalisation but cannot be parsed into a request
// must fail at construction rather than being dialled.
func TestListModels_UnparseableBaseURL(t *testing.T) {
	if _, err := New("https://exa mple.com").ListModels(context.Background(), "k"); err == nil {
		t.Fatal("expected a request-construction error")
	}
}

func TestVercelModelImportable(t *testing.T) {
	cases := []struct {
		name string
		in   vercelModelEntry
		want bool
	}{
		{"language", vercelModelEntry{Type: "language"}, true},
		{"embedding", vercelModelEntry{Type: "embedding"}, true},
		{"missing type treated as language", vercelModelEntry{Type: ""}, true},
		{"video", vercelModelEntry{Type: "video"}, false},
		{"reranking", vercelModelEntry{Type: "reranking"}, false},
		{"unknown type", vercelModelEntry{Type: "speech"}, false},

		// Image models split on how they are billed, because only the
		// token-billed ones have a working chat/completions dispatch.
		{"image with no pricing", vercelModelEntry{Type: "image"}, false},
		{"image billed per image", vercelModelEntry{
			Type: "image", Pricing: &vercelPricing{Image: "0.04"},
		}, false},
		{"image billed per token", vercelModelEntry{
			Type: "image", Pricing: &vercelPricing{Input: "0.01", Output: "0.02"},
		}, true},
		{"image with output price only", vercelModelEntry{
			Type: "image", Pricing: &vercelPricing{Output: "0.02"},
		}, true},
		// Both set: per-image billing wins, since importing it as a chat
		// binding would price the image at zero.
		{"image billed both ways", vercelModelEntry{
			Type: "image", Pricing: &vercelPricing{Input: "0.01", Image: "0.04"},
		}, false},
		{"image with empty pricing block", vercelModelEntry{
			Type: "image", Pricing: &vercelPricing{},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vercelModelImportable(tc.in); got != tc.want {
				t.Errorf("vercelModelImportable(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
