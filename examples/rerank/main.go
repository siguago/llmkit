// Command rerank reorders candidates with a cross-encoder reranker.
//
//	SILICONFLOW_API_KEY=sk-... go run ./examples/rerank
//	PROVIDER=vercel VERCEL_AI_GATEWAY_KEY=... go run ./examples/rerank
//
// Two providers implement this route, and they are not interchangeable by
// accident: siliconflow reports usage as token counts, while the Vercel AI
// Gateway serves Cohere's dialect and reports billed search units. The two
// dimensions stay separate in llmkit.Usage: token counters vs BilledSearchUnits
// (one search unit covers up to 100 documents).
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/siguago/llmkit"
)

// defaultModels maps each rerank-capable provider to a model worth probing.
// Override with MODEL=... when you have a different one deployed.
var defaultModels = map[string]string{
	llmkit.SiliconFlow: "BAAI/bge-reranker-v2-m3",
	llmkit.Vercel:      "cohere/rerank-v3.5",
}

func main() {
	providerName := os.Getenv("PROVIDER")
	if providerName == "" {
		providerName = llmkit.SiliconFlow
	}
	model := os.Getenv("MODEL")
	if model == "" {
		model = defaultModels[providerName]
	}
	if model == "" {
		log.Fatalf("no default model for %q — set MODEL", providerName)
	}

	client, err := llmkit.New(providerName)
	if err != nil {
		log.Fatal(err)
	}
	if !client.SupportsRerank() {
		log.Fatalf("%s does not implement the rerank route", providerName)
	}

	// Same query and candidates as the rerank example in README.md, so the two
	// cannot drift apart.
	query := "什么是熊猫？"
	docs := []string{
		"苹果是一种常见的水果。",
		"汽车通常有四个轮子。",
		"熊猫是中国特有的哺乳动物，以竹子为食。",
	}

	resp, err := client.Rerank(context.Background(), &llmkit.RerankRequest{
		Model:     model,
		Query:     query,
		Documents: docs,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("query: %s\n\n", query)
	for rank, r := range resp.Results {
		// r.Index is the position in docs — the only link back to the caller's
		// slice, because Results is score-sorted and may be truncated by TopN.
		fmt.Printf("%d. %.4f  %s\n", rank+1, r.RelevanceScore, docs[r.Index])
	}
	if resp.Usage != nil {
		fmt.Printf("\nusage: prompt=%d total=%d requests=%d", resp.Usage.PromptTokens,
			resp.Usage.TotalTokens, resp.Usage.RequestCount)
		if resp.Usage.BilledSearchUnits > 0 {
			// Cohere-dialect vendors bill per search unit, not per token; the
			// unit count is not a request count (100 docs still cost 1 request).
			fmt.Printf(" billed_search_units=%d", resp.Usage.BilledSearchUnits)
		}
		fmt.Printf(" (provider %s)\n", resp.Provider)
	}

	// The point of the call is the reordering, so check it rather than trusting
	// a 200: a reranker can echo the input order with flat scores and look
	// healthy. The relevant document is sent last on purpose.
	if len(resp.Results) == 0 || resp.Results[0].Index != 2 {
		log.Fatalf("top result = %+v, want index 2 (the panda sentence) — the reranker did not reorder",
			resp.Results)
	}
	fmt.Println("\nok: the relevant document was sent last and came back first")
}
