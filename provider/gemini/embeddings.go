package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/siguago/llmkit/provider"
)

// Embeddings implements provider.Embedder against Gemini's batchEmbedContents.
//
// The batch endpoint is used even for a single input, because its semantics are
// the ones the unified API promises — N texts in, N vectors out, in order — so
// this is an envelope translation, not a change of operation:
//
//	unified                Gemini
//	Input[i]               requests[i].content.parts[0].text
//	Dimensions             requests[i].outputDimensionality
//	(none)                 requests[i].taskType   ProviderOptions["gemini"]["task_type"]
//	(none)                 requests[i].title      ProviderOptions["gemini"]["title"]
//	Data[i].Embedding      embeddings[i].values
//
// The sibling single-shot endpoint (:embedContent) is deliberately unused: it
// would turn one Embed call into N HTTP requests, which is the cost cliff that
// kept Volcengine's fused multimodal endpoint out of this interface entirely.
// Gemini does not have that problem — the batch route exists and is positional.
//
// Gemini reports no token counts on this endpoint, so Usage carries only
// RequestCount. Callers pricing per token get zeros here, not a fabricated
// estimate.
func (p *Provider) Embeddings(ctx context.Context, apiKey, model string, req *provider.EmbeddingRequest) (*provider.EmbeddingResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("gemini embeddings: nil request")
	}
	texts, err := embedInputTexts(req.Input)
	if err != nil {
		return nil, err
	}
	// Gemini returns float arrays and has no base64 wire format. Rejecting is
	// the same call MiniMax's adapter makes: silently handing back floats to a
	// caller who asked for base64 is a difference they cannot see until it
	// breaks something downstream.
	if req.EncodingFormat != nil && *req.EncodingFormat != "float" {
		return nil, &provider.ErrUnsupported{Provider: "gemini", Op: "embeddings encoding_format=" + *req.EncodingFormat}
	}

	opts, err := embedProviderOptions(req.ProviderOptions)
	if err != nil {
		return nil, err
	}
	taskType, _ := opts["task_type"].(string)
	title, _ := opts["title"].(string)
	// title is only meaningful for RETRIEVAL_DOCUMENT; Gemini rejects it
	// elsewhere. Forwarded as given so the caller sees that rejection rather
	// than a local rule that could drift from the vendor's.
	qualified := qualifyModel(model)

	reqs := make([]embedContentRequest, len(texts))
	for i, text := range texts {
		reqs[i] = embedContentRequest{
			Model:                qualified,
			Content:              embedContent{Parts: []embedPart{{Text: text}}},
			TaskType:             taskType,
			Title:                title,
			OutputDimensionality: req.Dimensions,
		}
	}
	jsonBody, err := json.Marshal(batchEmbedRequest{Requests: reqs})
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/%s:batchEmbedContents", p.modelsURL, bareModel(model))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	provider.SetKeyHeader(httpReq.Header, "x-goog-api-key", apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, "gemini", respBody)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseBatchEmbeddings(respBody, model, len(texts))
}

// batchEmbedRequest is Gemini's envelope: the model is named once in the URL and
// again inside every sub-request, and the API requires both.
type batchEmbedRequest struct {
	Requests []embedContentRequest `json:"requests"`
}

type embedContentRequest struct {
	Model                string       `json:"model"`
	Content              embedContent `json:"content"`
	TaskType             string       `json:"taskType,omitempty"`
	Title                string       `json:"title,omitempty"`
	OutputDimensionality *int         `json:"outputDimensionality,omitempty"`
}

type embedContent struct {
	Parts []embedPart `json:"parts"`
}

type embedPart struct {
	Text string `json:"text"`
}

// batchEmbedResponse mirrors Gemini's wire shape.
//
// values is decoded as []any rather than []float64 for the same reason MiniMax's
// adapter does it: every compat-based provider unmarshals straight into
// EmbeddingResponse, so its Embedding lands as []any of float64 and callers
// (llmkit-probe among them) type-assert exactly that. Producing []float64 here
// would make Gemini the one provider needing a different assertion.
type batchEmbedResponse struct {
	Embeddings []struct {
		Values []any `json:"values"`
	} `json:"embeddings"`
}

// parseBatchEmbeddings converts Gemini's response into the unified shape.
//
// wantN is the number of texts sent. A count mismatch means the vendor dropped
// or reordered inputs, and since the unified contract is positional —
// Data[i] describes Input[i] — returning a short list would silently
// misattribute vectors to the wrong text.
func parseBatchEmbeddings(body []byte, model string, wantN int) (*provider.EmbeddingResponse, error) {
	var out batchEmbedResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != wantN {
		return nil, fmt.Errorf("gemini embeddings: got %d vectors for %d inputs", len(out.Embeddings), wantN)
	}

	data := make([]provider.EmbeddingItem, len(out.Embeddings))
	for i, e := range out.Embeddings {
		data[i] = provider.EmbeddingItem{Object: "embedding", Index: i, Embedding: e.Values}
	}
	// Gemini sends no usageMetadata on this endpoint. NormalizeUsage floors
	// RequestCount at 1 so per-call pricing still has a quantity to multiply.
	usage := &provider.Usage{}
	provider.NormalizeUsage(usage)
	return &provider.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  model,
		Usage:  usage,
	}, nil
}

// bareModel strips the "models/" prefix Gemini uses in its own catalog output,
// so callers can pass either form. The URL path segment wants the bare name.
func bareModel(model string) string {
	return strings.TrimPrefix(model, "models/")
}

// qualifyModel is the inverse: the `model` field inside each sub-request wants
// the fully qualified resource name.
func qualifyModel(model string) string {
	return "models/" + bareModel(model)
}

// embedInputTexts narrows the unified Input to the []string Gemini accepts.
//
// The unified field is documented as string | []string | []int | [][]int. The
// token-array forms are an OpenAI feature — pre-tokenized input — that Gemini
// has no equivalent for, so they are refused rather than mangled into text.
func embedInputTexts(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		if v == "" {
			return nil, fmt.Errorf("gemini embeddings: empty input")
		}
		return []string{v}, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("gemini embeddings: empty input")
		}
		for i, s := range v {
			if s == "" {
				return nil, fmt.Errorf("gemini embeddings: input[%d] is empty", i)
			}
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("gemini embeddings: empty input")
		}
		out := make([]string, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, &provider.ErrUnsupported{Provider: "gemini", Op: fmt.Sprintf("embeddings input[%d] of type %T (text only)", i, item)}
			}
			if s == "" {
				return nil, fmt.Errorf("gemini embeddings: input[%d] is empty", i)
			}
			out[i] = s
		}
		return out, nil
	case nil:
		return nil, fmt.Errorf("gemini embeddings: input required")
	default:
		return nil, &provider.ErrUnsupported{Provider: "gemini", Op: fmt.Sprintf("embeddings input of type %T (text only)", input)}
	}
}

// embedProviderOptions pulls Gemini's sub-map out of the unified
// ProviderOptions blob, matching how MiniMax's adapter reads its own.
//
// task_type is forwarded, not validated locally — the opposite of the call
// MiniMax's adapter makes for its two-valued `type`. Gemini's set is open and
// still growing (CODE_RETRIEVAL_QUERY was a later addition), so a local
// whitelist would reject valid values the moment Google ships another one. That
// restores this SDK's default: let the caller see the vendor's own error.
func embedProviderOptions(opts any) (map[string]any, error) {
	if opts == nil {
		return nil, nil
	}
	outer, ok := opts.(map[string]any)
	if !ok {
		return nil, nil
	}
	if raw, present := outer["gemini"]; present {
		inner, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("gemini embeddings: ProviderOptions[%q] is %T, want map[string]any", "gemini", raw)
		}
		return inner, nil
	}
	// No sub-map, but our own option names at the top level: the nesting level
	// was forgotten. Say so rather than proceed as if nothing was asked for —
	// dropping a task_type silently degrades retrieval quality invisibly.
	for _, k := range []string{"task_type", "title"} {
		if _, present := outer[k]; present {
			return nil, fmt.Errorf(
				"gemini embeddings: ProviderOptions has %q at the top level; "+
					"it must be nested under %q, as in map[string]any{%q: map[string]any{%q: ...}}",
				k, "gemini", "gemini", k)
		}
	}
	return nil, nil
}
