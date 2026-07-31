package compat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/siguago/llmkit/provider"
)

// WithRerank is a Provider that also serves /rerank.
//
// It is the mirror image of NoEmbeddings. That type exists to *withhold* a
// promoted method; this one exists to *add* one, so embedding *Provider is
// exactly right here — chat, streaming, model listing and embeddings are
// promoted unchanged, and Rerank is added on top.
//
// Reranking is not part of the OpenAI API, so there is no "OpenAI-compatible"
// rerank shape to speak of. What this implements is the de-facto standard that
// Cohere established and Jina, SiliconFlow and Together all copied: POST
// /rerank with {model, query, documents}, answering with a score-sorted list
// carrying the original index. Adapters whose vendor deviates should write
// their own translation rather than bend this one.
type WithRerank struct {
	*Provider
	rerankURL string
}

// NewWithRerank creates an OpenAI-compatible provider that additionally claims
// rerank support. Use it only for vendors that actually publish the route —
// claiming it is what makes llmkit.Client.SupportsRerank answer true.
func NewWithRerank(cfg Config) *WithRerank {
	p := New(cfg)
	return &WithRerank{Provider: p, rerankURL: p.baseURL + "/rerank"}
}

// Rerank scores req.Documents against req.Query.
//
// The response is sorted by relevance rather than input order, which is the
// point of the call; each result carries the index it had in req.Documents.
func (p *WithRerank) Rerank(ctx context.Context, apiKey, model string, req *provider.RerankRequest) (*provider.RerankResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%s rerank: nil request", p.Name())
	}
	if req.Query == "" {
		return nil, fmt.Errorf("%s rerank: query is required", p.Name())
	}
	if len(req.Documents) == 0 {
		return nil, fmt.Errorf("%s rerank: at least one document is required", p.Name())
	}

	body := map[string]any{
		"model":     model,
		"query":     req.Query,
		"documents": req.Documents,
	}
	if req.TopN != nil {
		body["top_n"] = *req.TopN
	}
	if req.ReturnDocuments != nil {
		body["return_documents"] = *req.ReturnDocuments
	}
	for k, v := range rerankExtras(req.ProviderOptions, p.Name()) {
		body[k] = v
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.rerankURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewProviderErrorFromResponse(resp, p.Name(), respBody)
	}
	return parseRerank(respBody, model, p.Name(), len(req.Documents))
}

// rerankWireResponse mirrors the Cohere-derived shape the copying vendors use.
//
// Document is decoded as json.RawMessage because vendors disagree on it: Cohere
// and SiliconFlow send an object {"text": "..."} while some relays send a bare
// string. Deciding between them at parse time keeps that disagreement out of
// the unified type.
type rerankWireResponse struct {
	Results []struct {
		Index          int             `json:"index"`
		RelevanceScore float64         `json:"relevance_score"`
		Document       json.RawMessage `json:"document"`
	} `json:"results"`
	// Vendors split on where token counts live: SiliconFlow uses a top-level
	// "tokens" object, Cohere buries billed units under "meta". Both are read
	// so Usage is populated either way.
	Tokens *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"tokens"`
	Meta *struct {
		BilledUnits *struct {
			SearchUnits int `json:"search_units"`
		} `json:"billed_units"`
	} `json:"meta"`
}

// parseRerank converts the wire shape into the unified response.
//
// nDocs is how many documents were sent. An index outside that range means the
// vendor is pointing at data the caller never supplied, and since Index is the
// only link back to the caller's slice, forwarding it would hand them an
// out-of-range panic at the point of use rather than an error here.
func parseRerank(body []byte, model, name string, nDocs int) (*provider.RerankResponse, error) {
	var wire rerankWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, err
	}

	results := make([]provider.RerankResult, 0, len(wire.Results))
	for _, r := range wire.Results {
		if r.Index < 0 || r.Index >= nDocs {
			return nil, fmt.Errorf("%s rerank: result index %d is outside the %d documents sent", name, r.Index, nDocs)
		}
		results = append(results, provider.RerankResult{
			Index:          r.Index,
			RelevanceScore: r.RelevanceScore,
			Document:       decodeRerankDocument(r.Document),
		})
	}

	usage := &provider.Usage{}
	if wire.Tokens != nil {
		usage.PromptTokens = wire.Tokens.InputTokens
		usage.CompletionTokens = wire.Tokens.OutputTokens
		usage.TotalTokens = wire.Tokens.InputTokens + wire.Tokens.OutputTokens
	}
	provider.NormalizeUsage(usage)

	return &provider.RerankResponse{
		Object:   "list",
		Model:    model,
		Results:  results,
		Usage:    usage,
		Provider: name,
	}, nil
}

// decodeRerankDocument accepts both shapes vendors send, and returns "" for a
// response that omitted the field — which is the normal case when
// ReturnDocuments was not requested.
func decodeRerankDocument(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asObject struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &asObject); err == nil && asObject.Text != "" {
		return asObject.Text
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	return ""
}

// rerankExtras pulls this provider's sub-map out of ProviderOptions so callers
// can pass vendor-specific knobs (SiliconFlow's max_chunks_per_doc, for
// instance) without new fields on the unified request.
//
// Unlike the embeddings adapters' equivalent, a malformed block is ignored
// rather than rejected: there is no field here whose silent loss changes
// results the way a dropped task_type would. The keys are vendor tuning knobs,
// and the vendor will reject a bad value itself.
func rerankExtras(opts map[string]any, name string) map[string]any {
	if opts == nil {
		return nil
	}
	raw, ok := opts[name]
	if !ok {
		return nil
	}
	inner, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return inner
}

var (
	_ provider.Provider    = (*WithRerank)(nil)
	_ provider.ModelLister = (*WithRerank)(nil)
	_ provider.Embedder    = (*WithRerank)(nil)
	_ provider.Reranker    = (*WithRerank)(nil)
)
