package minimax

import (
	"net/http"
	"strings"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const defaultBaseURL = "https://api.minimax.io/v1"

// Provider adapts MiniMax, whose two surfaces disagree about wire format.
//
// Chat is plain OpenAI-compatible and signals errors via HTTP status codes, so it
// is delegated. (The legacy /v1/text/chatcompletion_v2 endpoint used a base_resp
// in-body envelope; we don't target that surface.)
//
// Embeddings is not: /v1/embeddings takes `texts` rather than `input`, requires a
// `type`, may want a GroupId query parameter, and answers with a top-level
// `vectors` array instead of `data[].embedding` — plus an in-body base_resp status
// that can report failure under HTTP 200. Embedding compat.Provider would promote
// its OpenAI-shaped Embeddings and get every one of those wrong, so the embedded
// type is compat.NoEmbeddings: it withholds the wrong implementation, leaving room
// for the translating one in embeddings.go.
type Provider struct {
	*compat.NoEmbeddings
	embeddingsURL string
	client        *http.Client
}

// New constructs a MiniMax provider.
//
// Pass an empty baseURL to use the international default; mainland China users
// typically pass https://api.minimaxi.com/v1.
func New(baseURL string) *Provider {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		NoEmbeddings: compat.NewNoEmbeddings(compat.Config{
			ProviderName: "minimax",
			BaseURL:      baseURL,
		}),
		embeddingsURL: baseURL + "/embeddings",
		client:        httpx.NewClient(0),
	}
}

func (p *Provider) Name() string { return "minimax" }

var (
	_ provider.Provider    = (*Provider)(nil)
	_ provider.ModelLister = (*Provider)(nil)
	_ provider.Embedder    = (*Provider)(nil)
)
