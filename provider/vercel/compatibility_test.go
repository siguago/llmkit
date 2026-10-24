package vercel_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
	"github.com/siguago/llmkit/provider/vercel"
)

// Provider has always been a direct exported field. External callers may use
// keyed literals or assign it after declaring a zero-value Vercel adapter.
func TestProvider_CompatFieldSourceCompatibility(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/custom/chat/completions":
			var req struct {
				Stream bool `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"replacement\"}}]}\n\ndata: [DONE]\n\n")
				return
			}
			_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"replacement"}}]}`)
		case "/custom/embeddings":
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
		case "/custom/rerank":
			_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.9}],"meta":{"billed_units":{"search_units":1}}}`)
		default:
			t.Errorf("request did not use the assigned Provider: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name string
		make func(*compat.Provider) *vercel.Provider
	}{
		{"keyed literal", func(base *compat.Provider) *vercel.Provider {
			return &vercel.Provider{Provider: base}
		}},
		{"zero-value field assignment", func(base *compat.Provider) *vercel.Provider {
			var p vercel.Provider
			p.Provider = base
			return &p
		}},
		{"constructor field replacement", func(base *compat.Provider) *vercel.Provider {
			p := vercel.New(srv.URL + "/original")
			p.Provider = base
			return p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := compat.New(compat.Config{ProviderName: "replacement", BaseURL: srv.URL + "/custom"})
			p := tc.make(base)
			if p.Provider != base || p.Name() != "replacement" {
				t.Fatal("the assigned compat Provider must remain directly accessible")
			}
			req := &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}}
			resp, err := p.ChatCompletion(context.Background(), "k", "m", req)
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			if len(resp.Choices) != 1 || resp.Choices[0].Message == nil || resp.Choices[0].Message.Content != "replacement" {
				t.Fatalf("chat response = %+v", resp)
			}

			stream, err := p.ChatCompletionStream(context.Background(), "k", "m", req)
			if err != nil {
				t.Fatalf("ChatCompletionStream: %v", err)
			}
			defer stream.Close()
			chunk, err := stream.Recv()
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if len(chunk.Choices) != 1 || chunk.Choices[0].Delta == nil || chunk.Choices[0].Delta.Content != "replacement" {
				t.Fatalf("chat chunk = %+v", chunk)
			}
			if _, err := stream.Recv(); err != io.EOF {
				t.Fatalf("terminal Recv = %v, want EOF", err)
			}

			embeddings, err := p.Embeddings(context.Background(), "k", "m", &provider.EmbeddingRequest{Input: "hi"})
			if err != nil {
				t.Fatalf("Embeddings: %v", err)
			}
			if len(embeddings.Data) != 1 {
				t.Fatalf("embeddings = %+v", embeddings)
			}
			if vector, ok := embeddings.Data[0].Embedding.([]any); !ok || len(vector) != 2 {
				t.Fatalf("embedding = %+v", embeddings.Data[0].Embedding)
			}

			rerank, err := p.Rerank(context.Background(), "k", "m", &provider.RerankRequest{
				Query: "q", Documents: []string{"a"},
			})
			if err != nil {
				t.Fatalf("Rerank: %v", err)
			}
			if rerank.Provider != "replacement" || len(rerank.Results) != 1 || rerank.Usage == nil ||
				rerank.Usage.RequestCount != 1 || rerank.Usage.BilledSearchUnits != 1 {
				t.Fatalf("rerank response = %+v", rerank)
			}
		})
	}
}
