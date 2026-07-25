// Command embeddings computes vectors and ranks texts by cosine similarity.
//
//	SILICONFLOW_API_KEY=sk-... go run ./examples/embeddings
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"

	"github.com/siguago/llmkit"
)

func main() {
	client, err := llmkit.New(llmkit.SiliconFlow)
	if err != nil {
		log.Fatal(err)
	}

	query := "怎么给 Go 程序做性能分析？"
	docs := []string{
		"pprof 可以采集 CPU 和内存 profile。",
		"红烧肉要先焯水再上色。",
		"go test -bench 用来跑基准测试。",
		"杭州的梅雨季在六月。",
	}

	// One call embeds the query and every document — batching keeps the
	// vectors in the same space and costs one round trip.
	inputs := append([]string{query}, docs...)
	resp, err := client.Embed(context.Background(), &llmkit.EmbeddingRequest{
		Model: "BAAI/bge-m3",
		Input: inputs,
	})
	if err != nil {
		log.Fatal(err)
	}
	if len(resp.Data) != len(inputs) {
		log.Fatalf("got %d vectors for %d inputs", len(resp.Data), len(inputs))
	}

	// Results are not guaranteed to arrive in input order; index them.
	vectors := make([][]float64, len(inputs))
	for _, item := range resp.Data {
		vectors[item.Index] = toFloats(item.Embedding)
	}

	type scored struct {
		doc   string
		score float64
	}
	ranked := make([]scored, 0, len(docs))
	for i, doc := range docs {
		ranked = append(ranked, scored{doc, cosine(vectors[0], vectors[i+1])})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	fmt.Printf("query: %s\n\n", query)
	for _, r := range ranked {
		fmt.Printf("%.4f  %s\n", r.score, r.doc)
	}
}

// toFloats normalizes the embedding payload, which decodes as []any of float64
// unless base64 encoding was requested.
func toFloats(v any) []float64 {
	raw, ok := v.([]any)
	if !ok {
		log.Fatalf("unexpected embedding payload %T (base64 encoding is not handled here)", v)
	}
	out := make([]float64, len(raw))
	for i, x := range raw {
		f, ok := x.(float64)
		if !ok {
			log.Fatalf("embedding element %d is %T", i, x)
		}
		out[i] = f
	}
	return out
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
