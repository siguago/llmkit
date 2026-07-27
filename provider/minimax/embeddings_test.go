package minimax

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
)

// embedStub records what the adapter sent and replies with the canned body.
type embedStub struct {
	*httptest.Server
	path    string
	query   string
	auth    string
	body    map[string]any
	status  int
	replies string
}

func newEmbedStub(t *testing.T, reply string) *embedStub {
	t.Helper()
	s := &embedStub{status: http.StatusOK, replies: reply}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path = r.URL.Path
		s.query = r.URL.RawQuery
		s.auth = r.Header.Get("Authorization")
		if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &s.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.replies)
	}))
	t.Cleanup(s.Close)
	return s
}

const twoVectors = `{"vectors":[[0.1,0.2,0.3],[0.4,0.5,0.6]],"model":"embo-01","total_tokens":7,` +
	`"base_resp":{"status_code":0,"status_msg":"success"}}`

// The whole point of the hand-written method: MiniMax's field names, not OpenAI's.
// A regression here sends `input` to an endpoint that wants `texts`, which fails as
// a vendor validation error far from its cause.
func TestEmbeddings_SendsMiniMaxShape(t *testing.T) {
	s := newEmbedStub(t, twoVectors)

	resp, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
		&provider.EmbeddingRequest{Model: "embo-01", Input: []string{"天很蓝", "海很深"}})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}

	if s.path != "/embeddings" {
		t.Errorf("path = %q", s.path)
	}
	if s.auth != "Bearer sk-test" {
		t.Errorf("auth = %q", s.auth)
	}
	if _, present := s.body["input"]; present {
		t.Errorf("sent OpenAI's `input`; MiniMax wants `texts`: %+v", s.body)
	}
	texts, ok := s.body["texts"].([]any)
	if !ok || len(texts) != 2 || texts[0] != "天很蓝" || texts[1] != "海很深" {
		t.Errorf("texts = %+v", s.body["texts"])
	}
	if s.body["type"] != typeDB {
		t.Errorf("type = %v, want the %q default", s.body["type"], typeDB)
	}
	if s.body["model"] != "embo-01" {
		t.Errorf("model = %v", s.body["model"])
	}

	// Positional contract: Data[i] describes Input[i].
	if len(resp.Data) != 2 {
		t.Fatalf("got %d items, want 2", len(resp.Data))
	}
	for i, item := range resp.Data {
		if item.Index != i {
			t.Errorf("Data[%d].Index = %d", i, item.Index)
		}
		if item.Object != "embedding" {
			t.Errorf("Data[%d].Object = %q", i, item.Object)
		}
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 7 || resp.Usage.PromptTokens != 7 {
		t.Errorf("usage = %+v, want total_tokens mapped to both fields", resp.Usage)
	}
	if resp.Usage.RequestCount != 1 {
		t.Errorf("RequestCount = %d, want NormalizeUsage's floor of 1", resp.Usage.RequestCount)
	}
}

// Every compat-based provider's Embedding lands as []any of float64, and callers —
// llmkit-probe included — type-assert exactly that. MiniMax decoding into
// []float64 would make it the one provider needing a different assertion, which is
// a cross-provider inconsistency no single-provider test would catch.
func TestEmbeddings_VectorTypeMatchesCompatProviders(t *testing.T) {
	s := newEmbedStub(t, `{"vectors":[[0.1,0.2,0.3]],"total_tokens":3,"base_resp":{"status_code":0}}`)

	resp, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
		&provider.EmbeddingRequest{Input: "天很蓝"})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	vec, ok := resp.Data[0].Embedding.([]any)
	if !ok {
		t.Fatalf("Embedding is %T, want []any like every compat provider", resp.Data[0].Embedding)
	}
	if len(vec) != 3 {
		t.Fatalf("vector length = %d", len(vec))
	}
	if _, ok := vec[0].(float64); !ok {
		t.Errorf("vec[0] is %T, want float64", vec[0])
	}
}

// A bare string is the single-input form of the unified API and must become a
// one-element batch, not a bare string MiniMax would reject.
func TestEmbeddings_SingleStringBecomesOneElementBatch(t *testing.T) {
	s := newEmbedStub(t, `{"vectors":[[0.1]],"total_tokens":1,"base_resp":{"status_code":0}}`)

	if _, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
		&provider.EmbeddingRequest{Input: "天很蓝"}); err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	texts, ok := s.body["texts"].([]any)
	if !ok || len(texts) != 1 || texts[0] != "天很蓝" {
		t.Errorf("texts = %+v, want a one-element array", s.body["texts"])
	}
}

// type is the field that decides whether the vector is for the corpus or the query
// side. It has no home in the unified request, so it rides ProviderOptions.
func TestEmbeddings_TypeFromProviderOptions(t *testing.T) {
	s := newEmbedStub(t, `{"vectors":[[0.1]],"total_tokens":1,"base_resp":{"status_code":0}}`)

	_, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
		&provider.EmbeddingRequest{
			Input:           "天很蓝",
			ProviderOptions: map[string]any{"minimax": map[string]any{"type": typeQuery}},
		})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if s.body["type"] != typeQuery {
		t.Errorf("type = %v, want %q", s.body["type"], typeQuery)
	}
}

// A typo must fail loudly here. If the upstream were to treat an unknown type as
// its default, forwarding it would silently degrade retrieval instead of erroring.
func TestEmbeddings_RejectsUnknownType(t *testing.T) {
	s := newEmbedStub(t, twoVectors)

	_, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
		&provider.EmbeddingRequest{
			Input:           "x",
			ProviderOptions: map[string]any{"minimax": map[string]any{"type": "queyr"}},
		})
	if err == nil {
		t.Fatal("expected an error for an unknown type")
	}
	if !strings.Contains(err.Error(), "queyr") {
		t.Errorf("err = %v, want it to name the bad value", err)
	}
}

// GroupId is account configuration the mainland endpoint wants in the query string.
// Sent only when supplied — the international endpoint does not ask for it, and a
// stray empty GroupId= is its own kind of malformed request.
func TestEmbeddings_GroupIDOnlyWhenSupplied(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		s := newEmbedStub(t, `{"vectors":[[0.1]],"total_tokens":1,"base_resp":{"status_code":0}}`)
		if _, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
			&provider.EmbeddingRequest{Input: "x"}); err != nil {
			t.Fatalf("Embeddings: %v", err)
		}
		if s.query != "" {
			t.Errorf("query = %q, want none", s.query)
		}
	})

	t.Run("supplied", func(t *testing.T) {
		s := newEmbedStub(t, `{"vectors":[[0.1]],"total_tokens":1,"base_resp":{"status_code":0}}`)
		_, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
			&provider.EmbeddingRequest{
				Input:           "x",
				ProviderOptions: map[string]any{"minimax": map[string]any{"group_id": "18 2/3"}},
			})
		if err != nil {
			t.Fatalf("Embeddings: %v", err)
		}
		if s.query != "GroupId=18+2%2F3" {
			t.Errorf("query = %q, want the group id escaped", s.query)
		}
	})
}

// MiniMax reports failure in the body under HTTP 200. Treating that as success
// would hand back an empty vector list as if the call had worked.
func TestEmbeddings_InBodyErrorUnderHTTP200(t *testing.T) {
	s := newEmbedStub(t, `{"base_resp":{"status_code":1004,"status_msg":"invalid api key"}}`)

	_, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
		&provider.EmbeddingRequest{Input: "x"})
	if err == nil {
		t.Fatal("expected an error for base_resp.status_code != 0")
	}
	var apiErr *provider.ProviderError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *provider.ProviderError so callers classify it like any other vendor failure", err, err)
	}
	if !strings.Contains(apiErr.Message, "1004") || !strings.Contains(apiErr.Message, "invalid api key") {
		t.Errorf("message = %q, want the upstream code and text", apiErr.Message)
	}
}

// HTTP-level failures must still come through as ProviderError, same as compat.
func TestEmbeddings_HTTPErrorCarriesStatus(t *testing.T) {
	s := newEmbedStub(t, `{"base_resp":{"status_code":1002,"status_msg":"rate limited"}}`)
	s.status = http.StatusTooManyRequests

	_, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
		&provider.EmbeddingRequest{Input: "x"})
	var apiErr *provider.ProviderError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *provider.ProviderError", err, err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
}

// The unified contract is positional. A short vector list would silently pair
// vectors with the wrong texts, which is worse than failing.
func TestEmbeddings_RejectsVectorCountMismatch(t *testing.T) {
	s := newEmbedStub(t, `{"vectors":[[0.1]],"total_tokens":1,"base_resp":{"status_code":0}}`)

	_, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01",
		&provider.EmbeddingRequest{Input: []string{"a", "b", "c"}})
	if err == nil {
		t.Fatal("expected an error when the vendor returns fewer vectors than inputs")
	}
	if !strings.Contains(err.Error(), "1 vectors for 3 inputs") {
		t.Errorf("err = %v, want both counts named", err)
	}
}

// Options with no MiniMax equivalent are refused, not dropped. A caller who asked
// for 256 dimensions and silently got 1536 has no way to notice.
func TestEmbeddings_RejectsUnsupportedOptions(t *testing.T) {
	dims := 256
	b64 := "base64"
	float := "float"

	for _, tc := range []struct {
		name string
		req  *provider.EmbeddingRequest
		want bool // want ErrUnsupported
	}{
		{"dimensions", &provider.EmbeddingRequest{Input: "x", Dimensions: &dims}, true},
		{"base64", &provider.EmbeddingRequest{Input: "x", EncodingFormat: &b64}, true},
		{"float is the default and fine", &provider.EmbeddingRequest{Input: "x", EncodingFormat: &float}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newEmbedStub(t, `{"vectors":[[0.1]],"total_tokens":1,"base_resp":{"status_code":0}}`)
			_, err := New(s.URL).Embeddings(context.Background(), "sk-test", "embo-01", tc.req)
			var unsup *provider.ErrUnsupported
			if got := errors.As(err, &unsup); got != tc.want {
				t.Errorf("ErrUnsupported = %v (err %v), want %v", got, err, tc.want)
			}
		})
	}
}

// Pre-tokenized input is an OpenAI feature with no MiniMax equivalent; empty input
// is a caller bug. Neither may be quietly turned into a request.
func TestEmbeddings_RejectsNonTextAndEmptyInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input any
	}{
		{"token ids", []int{1, 2, 3}},
		{"nested token ids", [][]int{{1, 2}}},
		{"mixed array", []any{"ok", 42}},
		{"nil", nil},
		{"empty string", ""},
		{"empty slice", []string{}},
		{"blank element", []string{"ok", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := inputTexts(tc.input); err == nil {
				t.Errorf("inputTexts(%#v) = nil error, want a refusal", tc.input)
			}
		})
	}
}

// Capability detection is the reason this method exists as a hand-written one: the
// adapter must satisfy Embedder while NOT inheriting compat's OpenAI-shaped
// implementation.
func TestProvider_SatisfiesEmbedderWithoutCompatImplementation(t *testing.T) {
	p := New("")
	if _, ok := any(p).(provider.Embedder); !ok {
		t.Error("minimax must satisfy provider.Embedder")
	}
	if _, ok := any(p).(provider.ModelLister); !ok {
		t.Error("minimax must still satisfy provider.ModelLister")
	}
	if p.Name() != "minimax" {
		t.Errorf("Name() = %q", p.Name())
	}
}

func TestEmbeddings_DefaultBaseURL(t *testing.T) {
	if got := New("").embeddingsURL; got != "https://api.minimax.io/v1/embeddings" {
		t.Errorf("default embeddings endpoint = %q", got)
	}
	// A trailing slash must not produce a doubled path.
	if got := New("https://api.minimaxi.com/v1/").embeddingsURL; got != "https://api.minimaxi.com/v1/embeddings" {
		t.Errorf("trailing-slash base = %q", got)
	}
}
