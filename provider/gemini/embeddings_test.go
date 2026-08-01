package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// embedServer stands up a Gemini-shaped endpoint and returns a provider wired
// to it, plus a pointer to the decoded request body the handler saw.
func embedServer(t *testing.T, status int, body string) (*Provider, *batchEmbedRequest, *string, *string) {
	t.Helper()
	var got batchEmbedRequest
	var path, key string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		key = r.Header.Get("x-goog-api-key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return NewWithBaseURL(srv.URL), &got, &path, &key
}

const twoVectors = `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}]}`

func TestEmbeddings_RequestShape(t *testing.T) {
	p, got, path, key := embedServer(t, http.StatusOK, twoVectors)

	resp, err := p.Embeddings(context.Background(), "k-test", "gemini-embedding-001", &provider.EmbeddingRequest{
		Input: []string{"天很蓝", "海很深"},
	})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}

	// The batch endpoint, not the single-shot one — N inputs must cost one
	// round trip, not N.
	if !strings.HasSuffix(*path, "/models/gemini-embedding-001:batchEmbedContents") {
		t.Errorf("path = %q, want the batchEmbedContents route", *path)
	}
	if *key != "k-test" {
		t.Errorf("x-goog-api-key = %q, want k-test", *key)
	}
	if len(got.Requests) != 2 {
		t.Fatalf("sent %d sub-requests, want 2", len(got.Requests))
	}
	// Gemini names the model in the URL and again in every sub-request, and the
	// inner one wants the fully qualified resource name.
	if got.Requests[0].Model != "models/gemini-embedding-001" {
		t.Errorf("sub-request model = %q, want models/gemini-embedding-001", got.Requests[0].Model)
	}
	if got.Requests[0].Content.Parts[0].Text != "天很蓝" || got.Requests[1].Content.Parts[0].Text != "海很深" {
		t.Errorf("input order not preserved: %+v", got.Requests)
	}
	if len(resp.Data) != 2 || resp.Data[1].Index != 1 {
		t.Errorf("response not positional: %+v", resp.Data)
	}
}

// A single string must still go through the batch envelope rather than taking a
// different code path.
func TestEmbeddings_SingleStringInput(t *testing.T) {
	p, got, _, _ := embedServer(t, http.StatusOK, `{"embeddings":[{"values":[0.5]}]}`)

	resp, err := p.Embeddings(context.Background(), "k", "gemini-embedding-001", &provider.EmbeddingRequest{
		Input: "只有一句",
	})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if len(got.Requests) != 1 || got.Requests[0].Content.Parts[0].Text != "只有一句" {
		t.Errorf("single input not wrapped correctly: %+v", got.Requests)
	}
	if len(resp.Data) != 1 {
		t.Errorf("want 1 vector, got %d", len(resp.Data))
	}
}

// Callers may pass either the bare name or the catalog's "models/" form.
func TestEmbeddings_AcceptsQualifiedModelName(t *testing.T) {
	p, got, path, _ := embedServer(t, http.StatusOK, `{"embeddings":[{"values":[1]}]}`)

	if _, err := p.Embeddings(context.Background(), "k", "models/gemini-embedding-001", &provider.EmbeddingRequest{
		Input: "x",
	}); err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	// The path must not end up with a doubled prefix.
	if strings.Contains(*path, "models/models/") {
		t.Errorf("path has a doubled prefix: %q", *path)
	}
	if got.Requests[0].Model != "models/gemini-embedding-001" {
		t.Errorf("sub-request model = %q", got.Requests[0].Model)
	}
}

func TestEmbeddings_DimensionsForwarded(t *testing.T) {
	p, got, _, _ := embedServer(t, http.StatusOK, `{"embeddings":[{"values":[1]}]}`)
	dims := 256

	if _, err := p.Embeddings(context.Background(), "k", "gemini-embedding-001", &provider.EmbeddingRequest{
		Input: "x", Dimensions: &dims,
	}); err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if got.Requests[0].OutputDimensionality == nil || *got.Requests[0].OutputDimensionality != 256 {
		t.Errorf("outputDimensionality not forwarded: %+v", got.Requests[0])
	}
}

// Unset Dimensions must be omitted, not sent as 0 — which Gemini would read as
// an explicit request for a zero-width vector.
func TestEmbeddings_DimensionsOmittedWhenUnset(t *testing.T) {
	p, _, _, _ := embedServer(t, http.StatusOK, `{"embeddings":[{"values":[1]}]}`)

	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var envelope struct {
			Requests []map[string]any `json:"requests"`
		}
		_ = json.Unmarshal(raw, &envelope)
		if len(envelope.Requests) > 0 {
			seen = envelope.Requests[0]
		}
		_, _ = io.WriteString(w, `{"embeddings":[{"values":[1]}]}`)
	}))
	defer srv.Close()
	p = NewWithBaseURL(srv.URL)

	if _, err := p.Embeddings(context.Background(), "k", "m", &provider.EmbeddingRequest{Input: "x"}); err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	for _, k := range []string{"outputDimensionality", "taskType", "title"} {
		if _, present := seen[k]; present {
			t.Errorf("unset %q should be omitted, got %v", k, seen[k])
		}
	}
}

func TestEmbeddings_TaskTypeAndTitleForwarded(t *testing.T) {
	p, got, _, _ := embedServer(t, http.StatusOK, `{"embeddings":[{"values":[1]}]}`)

	if _, err := p.Embeddings(context.Background(), "k", "gemini-embedding-001", &provider.EmbeddingRequest{
		Input: "x",
		ProviderOptions: map[string]any{"gemini": map[string]any{
			"task_type": "RETRIEVAL_DOCUMENT",
			"title":     "一篇文档",
		}},
	}); err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if got.Requests[0].TaskType != "RETRIEVAL_DOCUMENT" || got.Requests[0].Title != "一篇文档" {
		t.Errorf("task_type/title not forwarded: %+v", got.Requests[0])
	}
}

// An unrecognised task_type is forwarded so the caller sees Gemini's own error.
// This is the opposite of MiniMax's two-valued `type`, which is validated
// locally — Gemini's set keeps growing, so a local whitelist would reject valid
// values.
func TestEmbeddings_UnknownTaskTypeIsForwardedNotRejected(t *testing.T) {
	p, got, _, _ := embedServer(t, http.StatusOK, `{"embeddings":[{"values":[1]}]}`)

	if _, err := p.Embeddings(context.Background(), "k", "m", &provider.EmbeddingRequest{
		Input:           "x",
		ProviderOptions: map[string]any{"gemini": map[string]any{"task_type": "SOME_FUTURE_TYPE"}},
	}); err != nil {
		t.Fatalf("an unknown task_type should reach the vendor, not fail locally: %v", err)
	}
	if got.Requests[0].TaskType != "SOME_FUTURE_TYPE" {
		t.Errorf("task_type = %q, want it forwarded verbatim", got.Requests[0].TaskType)
	}
}

func TestEmbeddings_RejectsNonFloatEncoding(t *testing.T) {
	p, _, _, _ := embedServer(t, http.StatusOK, twoVectors)
	b64 := "base64"

	_, err := p.Embeddings(context.Background(), "k", "m", &provider.EmbeddingRequest{
		Input: "x", EncodingFormat: &b64,
	})
	var unsup *provider.ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("expected ErrUnsupported for base64, got %v", err)
	}
}

func TestEmbeddings_AcceptsExplicitFloatEncoding(t *testing.T) {
	p, _, _, _ := embedServer(t, http.StatusOK, `{"embeddings":[{"values":[1]}]}`)
	f := "float"

	if _, err := p.Embeddings(context.Background(), "k", "m", &provider.EmbeddingRequest{
		Input: "x", EncodingFormat: &f,
	}); err != nil {
		t.Fatalf("float is what Gemini returns; it must be accepted: %v", err)
	}
}

// The positional contract is the whole point: Data[i] describes Input[i], so a
// short vector list has to fail rather than misattribute.
func TestEmbeddings_VectorCountMismatch(t *testing.T) {
	p, _, _, _ := embedServer(t, http.StatusOK, `{"embeddings":[{"values":[1]}]}`)

	_, err := p.Embeddings(context.Background(), "k", "m", &provider.EmbeddingRequest{
		Input: []string{"a", "b", "c"},
	})
	if err == nil || !strings.Contains(err.Error(), "1 vectors for 3 inputs") {
		t.Fatalf("expected a count-mismatch error, got %v", err)
	}
}

func TestEmbeddings_UpstreamError(t *testing.T) {
	p, _, _, _ := embedServer(t, http.StatusTooManyRequests, `{"error":{"message":"quota"}}`)

	_, err := p.Embeddings(context.Background(), "k", "m", &provider.EmbeddingRequest{Input: "x"})
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *provider.ProviderError, got %T (%v)", err, err)
	}
	if pe.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", pe.StatusCode)
	}
}

func TestEmbeddings_MalformedResponse(t *testing.T) {
	p, _, _, _ := embedServer(t, http.StatusOK, `{"embeddings": [`)

	if _, err := p.Embeddings(context.Background(), "k", "m", &provider.EmbeddingRequest{Input: "x"}); err == nil {
		t.Fatal("a truncated body should error")
	}
}

func TestEmbeddings_NilRequest(t *testing.T) {
	p, _, _, _ := embedServer(t, http.StatusOK, twoVectors)

	if _, err := p.Embeddings(context.Background(), "k", "m", nil); err == nil {
		t.Fatal("expected an error for a nil request")
	}
}

// Gemini sends no token counts here. Usage must still exist with RequestCount
// floored at 1, or per-call pricing has nothing to multiply.
func TestEmbeddings_UsageHasRequestCountButNoTokens(t *testing.T) {
	resp, err := parseBatchEmbeddings([]byte(twoVectors), "m", 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("Usage must not be nil")
	}
	if resp.Usage.RequestCount != 1 {
		t.Errorf("RequestCount = %d, want 1", resp.Usage.RequestCount)
	}
	if resp.Usage.TotalTokens != 0 || resp.Usage.PromptTokens != 0 {
		t.Errorf("Gemini reports no tokens here; they must stay zero, not be invented: %+v", resp.Usage)
	}
}

// Vectors decode as []any, matching every compat-based provider — callers
// (llmkit-probe included) type-assert exactly that.
func TestParseBatchEmbeddings_VectorElementType(t *testing.T) {
	resp, err := parseBatchEmbeddings([]byte(twoVectors), "m", 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vec, ok := resp.Data[0].Embedding.([]any)
	if !ok {
		t.Fatalf("Embedding is %T, want []any for consistency with compat providers", resp.Data[0].Embedding)
	}
	if len(vec) != 2 {
		t.Fatalf("vector length = %d, want 2", len(vec))
	}
	if _, ok := vec[0].(float64); !ok {
		t.Errorf("vector element is %T, want float64", vec[0])
	}
	if resp.Object != "list" || resp.Data[0].Object != "embedding" {
		t.Errorf("object fields wrong: %+v", resp)
	}
}

func TestEmbedInputTexts(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		want    []string
		wantErr bool
	}{
		{"string", "a", []string{"a"}, false},
		{"string slice", []string{"a", "b"}, []string{"a", "b"}, false},
		{"any slice of strings", []any{"a", "b"}, []string{"a", "b"}, false},
		{"empty string", "", nil, true},
		{"empty slice", []string{}, nil, true},
		{"empty any slice", []any{}, nil, true},
		{"slice with empty member", []string{"a", ""}, nil, true},
		{"any slice with empty member", []any{"a", ""}, nil, true},
		{"nil", nil, nil, true},
		// Pre-tokenized input is an OpenAI feature with no Gemini equivalent;
		// refused rather than mangled into text.
		{"token array", []int{1, 2}, nil, true},
		{"any slice with non-string", []any{"a", 1}, nil, true},
		{"wrong type", 42, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := embedInputTexts(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestEmbedProviderOptions(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		got, err := embedProviderOptions(nil)
		if err != nil || got != nil {
			t.Errorf("got %v, %v", got, err)
		}
	})
	t.Run("not a map is ignored", func(t *testing.T) {
		// The field is a passthrough for compat providers, so generic code that
		// always sets it must not break here.
		got, err := embedProviderOptions("whatever")
		if err != nil || got != nil {
			t.Errorf("got %v, %v", got, err)
		}
	})
	t.Run("another vendor's block is ignored", func(t *testing.T) {
		got, err := embedProviderOptions(map[string]any{"minimax": map[string]any{"type": "db"}})
		if err != nil || got != nil {
			t.Errorf("got %v, %v", got, err)
		}
	})
	t.Run("our block is returned", func(t *testing.T) {
		got, err := embedProviderOptions(map[string]any{"gemini": map[string]any{"task_type": "CLUSTERING"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["task_type"] != "CLUSTERING" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("our block shaped wrong errors", func(t *testing.T) {
		if _, err := embedProviderOptions(map[string]any{"gemini": "oops"}); err == nil {
			t.Error("a block clearly meant for us but shaped wrong should error, not be dropped")
		}
	})
	// Forgetting the nesting level would otherwise silently drop task_type and
	// degrade retrieval quality invisibly.
	for _, k := range []string{"task_type", "title"} {
		t.Run("top-level "+k+" errors", func(t *testing.T) {
			_, err := embedProviderOptions(map[string]any{k: "x"})
			if err == nil || !strings.Contains(err.Error(), "top level") {
				t.Errorf("expected a nesting hint, got %v", err)
			}
		})
	}
}

func TestModelNameHelpers(t *testing.T) {
	for _, tc := range []struct{ in, bare, qualified string }{
		{"gemini-embedding-001", "gemini-embedding-001", "models/gemini-embedding-001"},
		{"models/gemini-embedding-001", "gemini-embedding-001", "models/gemini-embedding-001"},
	} {
		if got := bareModel(tc.in); got != tc.bare {
			t.Errorf("bareModel(%q) = %q, want %q", tc.in, got, tc.bare)
		}
		if got := qualifyModel(tc.in); got != tc.qualified {
			t.Errorf("qualifyModel(%q) = %q, want %q", tc.in, got, tc.qualified)
		}
	}
}

func TestRemoteModelTaskTypes(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		methods []string
		want    string
	}{
		{"chat", "gemini-2.5-flash", []string{"generateContent", "countTokens"}, provider.RemoteModelTaskChat},
		{"current Gemini 3 chat", "gemini-3.1-pro-preview", []string{"generateContent"}, provider.RemoteModelTaskChat},
		{"latest Gemini chat", "gemini-3.6-flash", []string{"generateContent"}, provider.RemoteModelTaskChat},
		{"rolling Gemini alias", "gemini-flash-latest", []string{"generateContent"}, provider.RemoteModelTaskChat},
		{"gemma chat", "gemma-3-27b-it", []string{"generateContent"}, provider.RemoteModelTaskChat},
		{"current Gemma 4 chat", "gemma-4-26b-a4b-it", []string{"generateContent"}, provider.RemoteModelTaskChat},
		{"embedding methods deduplicated", "gemini-embedding-001", []string{"embedContent", "batchEmbedContents"}, provider.RemoteModelTaskEmbedding},
		{"unknown family is not guessed chat", "mixed", []string{"embedContent", "generateContent"}, provider.RemoteModelTaskEmbedding},
		{"image is media not chat", "gemini-2.5-flash-image", []string{"generateContent"}, provider.RemoteModelTaskImageGenerate + "," + provider.RemoteModelTaskImageEdit},
		{"Gemini 3 image family", "gemini-3-pro-image", []string{"generateContent"}, provider.RemoteModelTaskImageGenerate + "," + provider.RemoteModelTaskImageEdit},
		{"Gemini 3.1 image family", "gemini-3.1-flash-image", []string{"generateContent"}, provider.RemoteModelTaskImageGenerate + "," + provider.RemoteModelTaskImageEdit},
		{"marketing alias is not an API contract", "nano-banana-pro-preview", []string{"generateContent"}, ""},
		{"unknown image-named family is not guessed", "future-image-analyzer", []string{"generateContent"}, ""},
		{"future Gemini generation is not guessed chat", "gemini-4-pro-preview", []string{"generateContent"}, ""},
		{"future Gemini image generation is not guessed", "gemini-4-pro-image-preview", []string{"generateContent"}, ""},
		{"future Gemma generation is not guessed chat", "gemma-5-27b-it", []string{"generateContent"}, ""},
		{"LearnLM family is not guessed chat", "learnlm-future", []string{"generateContent"}, ""},
		{"shut down Gemini 2.0 is not classified", "gemini-2.0-flash", []string{"generateContent"}, ""},
		{"shut down image preview is not classified", "gemini-3-pro-image-preview", []string{"generateContent"}, ""},
		{"image name still method gated", "gemini-image-preview", []string{"countTokens"}, ""},
		{"Veo long-running", "veo-3.1-generate-preview", []string{"predictLongRunning"}, provider.RemoteModelTaskVideoGenerate},
		{"unknown long-running is not guessed video", "future-renderer", []string{"predictLongRunning"}, ""},
		{"future Veo generation is not guessed video", "veo-4-generate-preview", []string{"predictLongRunning"}, ""},
		{"TTS is not chat", "gemini-2.5-flash-preview-tts", []string{"generateContent"}, ""},
		{"native audio is not chat", "gemini-2.5-flash-native-audio-preview", []string{"generateContent"}, ""},
		{"Live is not chat", "gemini-live-2.5-flash-preview", []string{"generateContent"}, ""},
		{"Lyria is not chat", "lyria-realtime-exp", []string{"generateContent"}, ""},
		{"generic realtime is not chat", "gemini-realtime-preview", []string{"generateContent"}, ""},
		{"Omni is not chat", "gemini-2.5-flash-omni-preview", []string{"generateContent"}, ""},
		{"Imagen needs an unimplemented endpoint", "imagen-4.0-generate-001", []string{"generateContent", "predictLongRunning"}, ""},
		{"unsupported", "legacy", []string{"generateText"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(remoteModelTaskTypes(tc.model, tc.methods), ","); got != tc.want {
				t.Errorf("remoteModelTaskTypes(%q, %v) = %q, want %q", tc.model, tc.methods, got, tc.want)
			}
		})
	}
}

// ListModelsWithTaskTypes surfaces the mixed catalog while refusing to label
// generateContent-only audio/music/image models as chat.
func TestListModelsWithTaskTypes_ClassifiesRealCatalogFamilies(t *testing.T) {
	fixture, err := os.ReadFile("testdata/list_models_mixed.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	models, taskTypes, err := NewWithBaseURL(srv.URL).ListModelsWithTaskTypes(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	got := make(map[string]provider.RemoteModel, len(models))
	for _, m := range models {
		got[m.ModelID] = m
	}
	if _, ok := got["gemini-embedding-001"]; !ok {
		t.Error("an embedding model must be listed — otherwise SupportsEmbeddings is true with no model to use")
	}
	if _, ok := got["gemini-2.5-flash"]; !ok {
		t.Error("chat models must still be listed")
	}
	if _, ok := got["gemini-2.5-flash-image"]; !ok {
		t.Error("Gemini image models must remain discoverable")
	}
	if _, ok := got["veo-3.1-generate-preview"]; !ok {
		t.Error("Veo predictLongRunning models must remain discoverable")
	}
	if _, ok := got["future-multimodal-001"]; !ok {
		t.Error("a future generateContent family should remain visible as unknown")
	}
	for _, id := range []string{
		"legacy-text",
		"gemini-2.5-flash-preview-tts",
		"gemini-live-2.5-flash-preview",
		"gemini-2.5-flash-native-audio-preview-12-2025",
		"lyria-realtime-exp",
		"gemini-2.5-flash-omni-preview",
		"imagen-4.0-generate-001",
	} {
		if _, ok := got[id]; ok {
			t.Errorf("unsupported catalog family %q should stay filtered out", id)
		}
	}
	if tasks := strings.Join(taskTypes["gemini-embedding-001"], ","); tasks != provider.RemoteModelTaskEmbedding {
		t.Errorf("embedding task types = %q, want %q", tasks, provider.RemoteModelTaskEmbedding)
	}
	if tasks := strings.Join(taskTypes["gemini-2.5-flash"], ","); tasks != provider.RemoteModelTaskChat {
		t.Errorf("chat task types = %q, want %q", tasks, provider.RemoteModelTaskChat)
	}
	wantImage := provider.RemoteModelTaskImageGenerate + "," + provider.RemoteModelTaskImageEdit
	if tasks := strings.Join(taskTypes["gemini-2.5-flash-image"], ","); tasks != wantImage {
		t.Errorf("image task types = %q, want %q", tasks, wantImage)
	}
	if tasks := strings.Join(taskTypes["veo-3.1-generate-preview"], ","); tasks != provider.RemoteModelTaskVideoGenerate {
		t.Errorf("video task types = %q, want %q", tasks, provider.RemoteModelTaskVideoGenerate)
	}
	if _, ok := taskTypes["future-multimodal-001"]; ok {
		t.Error("a future family with ambiguous output must have no task-map entry")
	}
	// The "models/" prefix is stripped so the ID can be handed straight back in.
	for _, m := range models {
		if strings.HasPrefix(m.ModelID, "models/") {
			t.Errorf("ModelID %q still carries the resource prefix", m.ModelID)
		}
	}
}

func TestListModels_PreservesLegacyMethodFilter(t *testing.T) {
	fixture, err := os.ReadFile("testdata/list_models_mixed.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	models, err := NewWithBaseURL(srv.URL).ListModels(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	got := make(map[string]bool, len(models))
	for _, model := range models {
		got[model.ModelID] = true
	}
	// Legacy ListModels only inspected methods. These media models therefore
	// remain present even though the rich method safely filters/classifies them.
	for _, id := range []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-image",
		"gemini-embedding-001",
		"gemini-2.5-flash-preview-tts",
		"gemini-live-2.5-flash-preview",
		"lyria-realtime-exp",
		"future-multimodal-001",
	} {
		if !got[id] {
			t.Errorf("legacy dispatchable model %q was filtered out", id)
		}
	}
	for _, id := range []string{"veo-3.1-generate-preview", "imagen-4.0-generate-001", "legacy-text"} {
		if got[id] {
			t.Errorf("legacy ListModels unexpectedly added %q", id)
		}
	}
}

func TestListModels_EmptyCatalogPreservesNilSlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"models":[]}`)
	}))
	defer srv.Close()

	models, err := NewWithBaseURL(srv.URL).ListModels(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if models != nil {
		t.Fatalf("legacy empty catalog = %#v, want nil slice", models)
	}
}

func TestListModelsWithTaskTypes_PaginatesOpaqueTokenWithoutDuplicateRequests(t *testing.T) {
	const opaqueToken = "page+2/="
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			if got := r.URL.Query().Get("pageToken"); got != "" {
				t.Errorf("first page token = %q, want empty", got)
			}
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]}],"nextPageToken":"`+opaqueToken+`"}`)
		case 2:
			if got := r.URL.Query().Get("pageToken"); got != opaqueToken {
				t.Errorf("second page token = %q, want %q", got, opaqueToken)
			}
			_, _ = io.WriteString(w, `{"models":[{"name":"models/veo-3.1-generate-preview","supportedGenerationMethods":["predictLongRunning"]}]}`)
		default:
			t.Errorf("unexpected duplicate catalog request %d", calls)
			_, _ = io.WriteString(w, `{"models":[]}`)
		}
	}))
	defer srv.Close()

	models, taskTypes, err := NewWithBaseURL(srv.URL).ListModelsWithTaskTypes(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModelsWithTaskTypes: %v", err)
	}
	if calls != 2 || len(models) != 2 {
		t.Fatalf("calls = %d, models = %+v; want two pages and two models", calls, models)
	}
	if got := strings.Join(taskTypes["gemini-2.5-flash"], ","); got != provider.RemoteModelTaskChat {
		t.Errorf("first-page task types = %q", got)
	}
	if got := strings.Join(taskTypes["veo-3.1-generate-preview"], ","); got != provider.RemoteModelTaskVideoGenerate {
		t.Errorf("second-page task types = %q", got)
	}
}

// The adapter must satisfy Embedder — this is what Client.SupportsEmbeddings
// type-asserts, and it is now expected to answer true for gemini.
func TestGemini_ImplementsEmbedder(t *testing.T) {
	var p any = New()
	if _, ok := p.(provider.Embedder); !ok {
		t.Error("gemini must implement provider.Embedder")
	}
}
