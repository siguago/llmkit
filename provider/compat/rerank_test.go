package compat

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

// rerankServer stands up a /rerank endpoint and returns a provider wired to it
// plus the decoded request body the handler saw.
func rerankServer(t *testing.T, status int, body string) (*WithRerank, *map[string]any, *string, *string) {
	t.Helper()
	got := map[string]any{}
	var path, auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	p := NewWithRerank(Config{ProviderName: "testvendor", BaseURL: srv.URL})
	return p, &got, &path, &auth
}

// The canonical response: sorted by score, and NOT in input order.
const rerankOK = `{
	"results": [
		{"index": 2, "relevance_score": 0.93},
		{"index": 0, "relevance_score": 0.41}
	],
	"tokens": {"input_tokens": 88, "output_tokens": 0}
}`

func TestRerank_RequestShape(t *testing.T) {
	p, got, path, auth := rerankServer(t, http.StatusOK, rerankOK)
	topN := 2
	ret := true

	_, err := p.Rerank(context.Background(), "sk-test", "BAAI/bge-reranker-v2-m3", &provider.RerankRequest{
		Query:           "什么是熊猫？",
		Documents:       []string{"文档一", "文档二", "文档三"},
		TopN:            &topN,
		ReturnDocuments: &ret,
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}

	if !strings.HasSuffix(*path, "/rerank") {
		t.Errorf("path = %q, want /rerank", *path)
	}
	if *auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", *auth)
	}
	if (*got)["model"] != "BAAI/bge-reranker-v2-m3" || (*got)["query"] != "什么是熊猫？" {
		t.Errorf("model/query not forwarded: %v", *got)
	}
	docs, ok := (*got)["documents"].([]any)
	if !ok || len(docs) != 3 || docs[0] != "文档一" {
		t.Errorf("documents not forwarded in order: %v", (*got)["documents"])
	}
	if (*got)["top_n"] != float64(2) || (*got)["return_documents"] != true {
		t.Errorf("top_n/return_documents not forwarded: %v", *got)
	}
}

// The defining property of this endpoint: results are score-sorted, and Index
// is the only link back to the caller's slice.
func TestRerank_ResultsAreScoreSortedNotPositional(t *testing.T) {
	p, _, _, _ := rerankServer(t, http.StatusOK, rerankOK)

	resp, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
		Query:     "q",
		Documents: []string{"a", "b", "c"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	// Most relevant first — and that is document index 2, not 0.
	if resp.Results[0].Index != 2 || resp.Results[0].RelevanceScore != 0.93 {
		t.Errorf("first result should be the highest-scoring one: %+v", resp.Results[0])
	}
	if resp.Results[1].Index != 0 {
		t.Errorf("second result index = %d, want 0", resp.Results[1].Index)
	}
	if resp.Provider != "testvendor" || resp.Model != "m" {
		t.Errorf("response not stamped: %+v", resp)
	}
}

// An index pointing outside the documents sent would become an out-of-range
// panic at the caller's use site. Fail here instead.
func TestRerank_RejectsOutOfRangeIndex(t *testing.T) {
	for _, body := range []string{
		`{"results":[{"index":5,"relevance_score":0.9}]}`,
		`{"results":[{"index":-1,"relevance_score":0.9}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			p, _, _, _ := rerankServer(t, http.StatusOK, body)
			_, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
				Query: "q", Documents: []string{"a", "b"},
			})
			if err == nil || !strings.Contains(err.Error(), "outside the 2 documents") {
				t.Fatalf("expected an out-of-range error, got %v", err)
			}
		})
	}
}

// Vendors disagree on the document field: an object for Cohere/SiliconFlow, a
// bare string from some relays. Both must decode.
func TestRerank_DocumentShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"object form", `{"results":[{"index":0,"relevance_score":1,"document":{"text":"命中"}}]}`, "命中"},
		{"bare string form", `{"results":[{"index":0,"relevance_score":1,"document":"命中"}]}`, "命中"},
		{"omitted", `{"results":[{"index":0,"relevance_score":1}]}`, ""},
		{"null", `{"results":[{"index":0,"relevance_score":1,"document":null}]}`, ""},
		{"empty object", `{"results":[{"index":0,"relevance_score":1,"document":{}}]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _, _ := rerankServer(t, http.StatusOK, tc.body)
			resp, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
				Query: "q", Documents: []string{"a"},
			})
			if err != nil {
				t.Fatalf("Rerank: %v", err)
			}
			if resp.Results[0].Document != tc.want {
				t.Errorf("Document = %q, want %q", resp.Results[0].Document, tc.want)
			}
		})
	}
}

// Unset optionals must be omitted: top_n=0 would read as "return nothing".
func TestRerank_OmitsUnsetOptionals(t *testing.T) {
	p, got, _, _ := rerankServer(t, http.StatusOK, rerankOK)

	if _, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
		Query: "q", Documents: []string{"a", "b", "c"},
	}); err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	for _, k := range []string{"top_n", "return_documents"} {
		if _, present := (*got)[k]; present {
			t.Errorf("unset %q should be omitted, got %v", k, (*got)[k])
		}
	}
}

func TestRerank_ProviderOptionsMerged(t *testing.T) {
	p, got, _, _ := rerankServer(t, http.StatusOK, rerankOK)

	// Three documents, because rerankOK references index 2 — the out-of-range
	// guard is strict enough that a mismatch here fails the call.
	if _, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
		Query:     "q",
		Documents: []string{"a", "b", "c"},
		ProviderOptions: map[string]any{
			"testvendor": map[string]any{"max_chunks_per_doc": 512},
			"otherco":    map[string]any{"leaked": true},
		},
	}); err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if (*got)["max_chunks_per_doc"] != float64(512) {
		t.Errorf("vendor knob not merged: %v", *got)
	}
	if _, leaked := (*got)["leaked"]; leaked {
		t.Errorf("another vendor's block leaked into the body: %v", *got)
	}
}

// Token counts live in different places depending on vendor.
func TestRerank_UsageFromTokens(t *testing.T) {
	p, _, _, _ := rerankServer(t, http.StatusOK, rerankOK)

	resp, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
		Query: "q", Documents: []string{"a", "b", "c"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 88 || resp.Usage.TotalTokens != 88 {
		t.Errorf("usage not mapped: %+v", resp.Usage)
	}
	if resp.Usage.RequestCount != 1 {
		t.Errorf("RequestCount = %d, want 1", resp.Usage.RequestCount)
	}
}

// A vendor that reports no tokens still needs a usable Usage for per-call
// pricing.
func TestRerank_UsageWithoutTokens(t *testing.T) {
	p, _, _, _ := rerankServer(t, http.StatusOK, `{"results":[{"index":0,"relevance_score":1}]}`)

	resp, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
		Query: "q", Documents: []string{"a"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if resp.Usage == nil || resp.Usage.RequestCount != 1 {
		t.Fatalf("Usage must exist with RequestCount floored at 1: %+v", resp.Usage)
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("no token block should mean zero, not an invented number: %+v", resp.Usage)
	}
}

func TestRerank_ValidatesRequest(t *testing.T) {
	cases := []struct {
		name string
		req  *provider.RerankRequest
	}{
		{"nil request", nil},
		{"empty query", &provider.RerankRequest{Documents: []string{"a"}}},
		{"no documents", &provider.RerankRequest{Query: "q"}},
		{"empty documents", &provider.RerankRequest{Query: "q", Documents: []string{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, path, _ := rerankServer(t, http.StatusOK, rerankOK)
			if _, err := p.Rerank(context.Background(), "k", "m", tc.req); err == nil {
				t.Fatal("expected a validation error")
			}
			if *path != "" {
				t.Errorf("an invalid request must not reach the upstream, but hit %q", *path)
			}
		})
	}
}

func TestRerank_UpstreamError(t *testing.T) {
	p, _, _, _ := rerankServer(t, http.StatusUnauthorized, `{"error":"bad key"}`)

	_, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
		Query: "q", Documents: []string{"a"},
	})
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *provider.ProviderError, got %T (%v)", err, err)
	}
	if pe.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", pe.StatusCode)
	}
}

func TestRerank_MalformedResponse(t *testing.T) {
	p, _, _, _ := rerankServer(t, http.StatusOK, `{"results": [`)

	if _, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
		Query: "q", Documents: []string{"a"},
	}); err == nil {
		t.Fatal("a truncated body should error")
	}
}

// An empty result set is legitimate — every candidate scored below the
// vendor's threshold — and must not be confused with a failure.
func TestRerank_EmptyResults(t *testing.T) {
	p, _, _, _ := rerankServer(t, http.StatusOK, `{"results":[]}`)

	resp, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
		Query: "q", Documents: []string{"a"},
	})
	if err != nil {
		t.Fatalf("an empty result set is valid: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("want no results, got %+v", resp.Results)
	}
}

// The capability contract: plain compat must NOT satisfy Reranker, or every
// OpenAI-compatible provider would claim a route most of them don't have.
func TestRerank_CapabilitySurface(t *testing.T) {
	var plain any = New(Config{ProviderName: "plain", BaseURL: "https://example.com/v1"})
	if _, ok := plain.(provider.Reranker); ok {
		t.Error("compat.Provider must not implement Reranker — rerank is not part of " +
			"the OpenAI API, and claiming it would make SupportsRerank lie for every " +
			"compat-based vendor")
	}

	var withRerank any = NewWithRerank(Config{ProviderName: "r", BaseURL: "https://example.com/v1"})
	if _, ok := withRerank.(provider.Reranker); !ok {
		t.Error("compat.WithRerank must implement Reranker")
	}
	// It must keep everything Provider had, not trade one capability for another.
	if _, ok := withRerank.(provider.Embedder); !ok {
		t.Error("WithRerank must still promote Embeddings")
	}
	if _, ok := withRerank.(provider.ModelLister); !ok {
		t.Error("WithRerank must still promote ListModels")
	}
}

func TestRerankExtras(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]any
		want int
	}{
		{"nil", nil, 0},
		{"no block for us", map[string]any{"other": map[string]any{"a": 1}}, 0},
		{"block not a map", map[string]any{"me": "oops"}, 0},
		{"our block", map[string]any{"me": map[string]any{"a": 1, "b": 2}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rerankExtras(tc.opts, "me"); len(got) != tc.want {
				t.Errorf("got %v, want %d entries", got, tc.want)
			}
		})
	}
}
