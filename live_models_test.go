package llmkit

import "testing"

// This file deliberately carries no build tag.
//
// The live-model tables and their guards used to live in integration_test.go,
// where CI only ever *compiled* them: the integration step runs
// `go test -tags=integration ./... -run '^$'`, which builds the file and
// executes no test in it. So a provider could gain a capability, claim it in
// the capability matrix, and ship with no live model at all — TestLiveEmbeddings
// / TestLiveRerank would Skip, and a Skip reads like a pass.
//
// The guards themselves need nothing but New + Supports*, so they belong on the
// default build, where `go test ./...`, the race job and the Go-floor job all
// run them. The live tests that actually spend money stay behind the tag.

// liveEmbedModels and liveRerankModels are the same idea as liveModels for the
// non-chat routes, and need their own guard for the same reason: TestLiveEmbeddings
// and TestLiveRerank Skip when a provider has no entry, so a capability can be
// declared and never actually exercised against the vendor — the table stays
// quiet about what it is not covering.
//
// That is not hypothetical. Gemini gained embeddings without an entry here and
// silently skipped; writing this guard is what surfaced it, along with six
// other providers whose embeddings claim had never been verified live.
var (
	liveEmbedModels = map[string]string{
		OpenAI:      "text-embedding-3-small",
		Gemini:      "gemini-embedding-001",
		SiliconFlow: "BAAI/bge-m3",
		Zhipu:       "embedding-3",
		Mistral:     "mistral-embed",
		MiniMax:     "embo-01",
		DashScope:   "text-embedding-v4",
		Together:    "BAAI/bge-base-en-v1.5",
		Fireworks:   "nomic-ai/nomic-embed-text-v1.5",
		Ollama:      "nomic-embed-text",
		Vercel:      "openai/text-embedding-3-small",
		EasyRouter:  "text-embedding-3-small",
	}
	liveRerankModels = map[string]string{
		SiliconFlow: "BAAI/bge-reranker-v2-m3",
		// Vercel's gateway serves Cohere's rerank dialect, so the slug is
		// Cohere's. Probing both vendors is the point: they report usage in the
		// two different places the compat mapping reads (top-level "tokens" for
		// SiliconFlow, meta.billed_units for the Cohere dialect).
		Vercel: "cohere/rerank-v3.5",
	}
	// A vLLM process serves exactly one model, so no default is guessable —
	// set LLMKIT_TEST_EMBED_MODEL_VLLM to whatever you started it with.
	liveEmbedUnknown = map[string]bool{VLLM: true}
)

func TestLiveEmbedModelsCoverEmbedders(t *testing.T) {
	for _, name := range Providers() {
		c, err := New(name, WithAPIKey("list-only-placeholder"))
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if !c.SupportsEmbeddings() {
			if liveEmbedModels[name] != "" {
				t.Errorf("liveEmbedModels has an entry for %q, which does not implement Embedder", name)
			}
			continue
		}
		if liveEmbedModels[name] == "" && !liveEmbedUnknown[name] {
			t.Errorf("%q claims embeddings but has no live model — add one to liveEmbedModels, "+
				"or record why not in liveEmbedUnknown", name)
		}
	}
}

func TestLiveRerankModelsCoverRerankers(t *testing.T) {
	for _, name := range Providers() {
		c, err := New(name, WithAPIKey("list-only-placeholder"))
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if !c.SupportsRerank() {
			if liveRerankModels[name] != "" {
				t.Errorf("liveRerankModels has an entry for %q, which does not implement Reranker", name)
			}
			continue
		}
		if liveRerankModels[name] == "" {
			t.Errorf("%q claims rerank but has no live model — add one to liveRerankModels", name)
		}
	}
}
