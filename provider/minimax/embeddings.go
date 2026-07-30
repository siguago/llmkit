package minimax

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/siguago/llmkit/provider"
)

// MiniMax asks what the text is *for*, because the two answers produce different
// vectors: the corpus side and the query side are encoded asymmetrically, and
// pairing a "db" document with a "query" query is what the model was trained for.
const (
	typeDB    = "db"    // text being embedded for storage / indexing
	typeQuery = "query" // text being embedded to search with
)

// defaultType is what we send when the caller says nothing.
//
// There is no neutral choice — the field is required and each value changes the
// output — so this picks the one that is a working baseline rather than a silent
// downgrade: embedding both sides as "db" is the ordinary symmetric setup most
// embedding models use, and it is what MiniMax's own examples and the other
// integrations default to. Asymmetric encoding is the optimization, and getting it
// means asking for it (see Embeddings).
const defaultType = typeDB

// Embeddings implements provider.Embedder against MiniMax's own shape.
//
// The batch semantics line up with the unified API — N texts in, N vectors out, in
// order — so this is a field-name and envelope translation, not a change of
// operation:
//
//	unified          MiniMax
//	Input            texts
//	(none)           type      "db" | "query", required
//	(none)           GroupId   query parameter, mainland endpoint
//	Data[i].Embedding  vectors[i]
//	Usage.PromptTokens total_tokens
//
// Both MiniMax-only inputs come from ProviderOptions, keyed by provider name the
// same way the video adapters read theirs:
//
//	&llmkit.EmbeddingRequest{
//	    Model: "embo-01",
//	    Input: []string{"天很蓝"},
//	    ProviderOptions: map[string]any{"minimax": map[string]any{
//	        "type":     "query",     // default "db"
//	        "group_id": "182...",    // only if your endpoint requires it
//	    }},
//	}
//
// Dimensions and EncodingFormat are rejected rather than dropped: embo-01 returns
// fixed-width float vectors, and silently ignoring a caller's explicit request for
// 256 dimensions or base64 would hand back something they did not ask for.
func (p *Provider) Embeddings(ctx context.Context, apiKey, model string, req *provider.EmbeddingRequest) (*provider.EmbeddingResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("minimax embeddings: nil request")
	}
	texts, err := inputTexts(req.Input)
	if err != nil {
		return nil, err
	}
	if req.Dimensions != nil {
		return nil, &provider.ErrUnsupported{Provider: "minimax", Op: "embeddings dimensions (embo-01 is fixed-width)"}
	}
	if req.EncodingFormat != nil && *req.EncodingFormat != "float" {
		return nil, &provider.ErrUnsupported{Provider: "minimax", Op: "embeddings encoding_format=" + *req.EncodingFormat}
	}

	opts, err := providerOptions(req.ProviderOptions, "minimax")
	if err != nil {
		return nil, err
	}
	embType := defaultType
	if v, ok := opts["type"].(string); ok && v != "" {
		// Validated locally rather than forwarded, which is the exception to this
		// SDK's usual "let the caller see the vendor's own error". The value set is
		// closed and two-valued, and if the upstream were to treat an unrecognized
		// type as its default, a typo would silently degrade retrieval quality
		// instead of failing. A loud local error beats a quiet remote shrug.
		if v != typeDB && v != typeQuery {
			return nil, fmt.Errorf("minimax embeddings: type must be %q or %q, got %q", typeDB, typeQuery, v)
		}
		embType = v
	}

	endpoint := p.embeddingsURL
	// GroupId is account configuration, not a request parameter, but MiniMax wants
	// it in the query string on the mainland endpoint. Sent only when supplied, so
	// the international endpoint — which does not ask for it — stays clean.
	if gid, ok := opts["group_id"].(string); ok && gid != "" {
		endpoint += "?GroupId=" + url.QueryEscape(gid)
	}

	jsonBody, err := json.Marshal(map[string]any{
		"model": model,
		"texts": texts,
		"type":  embType,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
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

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, "minimax", respBody)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseEmbeddings(resp, respBody, model, len(texts))
}

// minimaxEmbeddingResponse mirrors MiniMax's wire shape.
//
// vectors is decoded as [][]any rather than [][]float64 on purpose: every
// compat-based provider unmarshals straight into EmbeddingResponse, so its
// Embedding lands as []any of float64, and callers (llmkit-probe among them) type-
// assert exactly that. Producing []float64 here would make MiniMax the one
// provider whose vectors need a different assertion.
type minimaxEmbeddingResponse struct {
	Vectors     [][]any `json:"vectors"`
	TotalTokens int     `json:"total_tokens"`
	TraceID     string  `json:"trace_id"`
	BaseResp    *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// parseEmbeddings converts MiniMax's response into the unified shape.
//
// wantN is the number of texts sent. A vector count that doesn't match means the
// vendor dropped or reordered inputs, and since the unified contract is
// positional — Data[i] describes Input[i] — returning a short list would silently
// misattribute vectors to the wrong text.
func parseEmbeddings(resp *http.Response, body []byte, model string, wantN int) (*provider.EmbeddingResponse, error) {
	var out minimaxEmbeddingResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	// MiniMax reports failure in the body, so HTTP 200 is not success on its own.
	// Route it through ProviderError anyway, so callers classify and retry MiniMax
	// failures with the same code they use for every other provider.
	if out.BaseResp != nil && out.BaseResp.StatusCode != 0 {
		msg := out.BaseResp.StatusMsg
		if msg == "" {
			msg = "unknown error"
		}
		requestID := provider.RequestIDFromHeader(resp.Header)
		if requestID == "" {
			// Some MiniMax surfaces put trace_id in the JSON envelope rather
			// than (or in addition to) the response header.
			requestID = out.TraceID
		}
		apiErr := &provider.ProviderError{
			// Preserve the real HTTP status for observability while carrying the
			// body metadata separately. MiniMax commonly returns 200 for auth,
			// rate-limit and server failures; replacing 200 with a synthetic
			// status would make StatusCode lie.
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("minimax api error (base_resp %d): %s", out.BaseResp.StatusCode, msg),
			RetryAfter: resp.Header.Get("Retry-After"),
			RequestID:  requestID,
		}
		return nil, provider.WithErrorMetadata(
			apiErr,
			strconv.Itoa(out.BaseResp.StatusCode),
			minimaxErrorCategory(out.BaseResp.StatusCode),
		)
	}
	if len(out.Vectors) != wantN {
		return nil, fmt.Errorf("minimax embeddings: got %d vectors for %d inputs", len(out.Vectors), wantN)
	}

	data := make([]provider.EmbeddingItem, len(out.Vectors))
	for i, vec := range out.Vectors {
		data[i] = provider.EmbeddingItem{Object: "embedding", Index: i, Embedding: vec}
	}
	usage := &provider.Usage{PromptTokens: out.TotalTokens, TotalTokens: out.TotalTokens}
	provider.NormalizeUsage(usage)
	return &provider.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  model,
		Usage:  usage,
	}, nil
}

// minimaxErrorCategory maps stable, documented base_resp codes to the common
// error semantics exposed by llmkit. Unknown codes deliberately stay
// unclassified: guessing that a new vendor code is retryable can amplify an
// invalid request, while provider.ProviderCode and the message still reach
// callers.
func minimaxErrorCategory(code int) provider.ErrorCategory {
	switch code {
	case 1004, 2038, 2042, 2049:
		return provider.ErrorCategoryAuth
	case 1002, 1041, 2045:
		return provider.ErrorCategoryRateLimit
	case 1000, 1001, 1013, 1024, 1033:
		return provider.ErrorCategoryServer
	case 1026, 1027, 1039, 1042, 1043, 1044, 2013, 20132, 2037, 2039, 2048:
		return provider.ErrorCategoryInvalidRequest
	default:
		return ""
	}
}

// inputTexts narrows the unified Input to the []string MiniMax accepts.
//
// The unified field is documented as string | []string | []int | [][]int. The
// token-array forms are an OpenAI feature — pre-tokenized input — that MiniMax has
// no equivalent for, so they are refused rather than mangled into text.
func inputTexts(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		if v == "" {
			return nil, fmt.Errorf("minimax embeddings: empty input")
		}
		return []string{v}, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("minimax embeddings: empty input")
		}
		for i, s := range v {
			if s == "" {
				return nil, fmt.Errorf("minimax embeddings: input[%d] is empty", i)
			}
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("minimax embeddings: empty input")
		}
		out := make([]string, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, &provider.ErrUnsupported{Provider: "minimax", Op: fmt.Sprintf("embeddings input[%d] of type %T (text only)", i, item)}
			}
			if s == "" {
				return nil, fmt.Errorf("minimax embeddings: input[%d] is empty", i)
			}
			out[i] = s
		}
		return out, nil
	case nil:
		return nil, fmt.Errorf("minimax embeddings: input required")
	default:
		return nil, &provider.ErrUnsupported{Provider: "minimax", Op: fmt.Sprintf("embeddings input of type %T (text only)", input)}
	}
}

// providerOptions pulls this provider's sub-map out of the unified
// ProviderOptions blob, matching how the video adapters read theirs.
//
// It errors on a blob that was clearly meant for us but is shaped wrong, instead
// of returning nil and letting the defaults apply. Dropping a misplaced `type`
// silently is the exact harm the local type validation above exists to prevent —
// the caller asked for query-side encoding, got corpus-side, and nothing said so.
//
// Unrecognizable blobs are still ignored rather than rejected: the field is a
// passthrough for compat providers (Vercel reads its gateway routing from it), so
// generic code that always sets it must not break here. That leaves one gap this
// cannot close — a struct carrying a Type field reads as unrecognizable, since
// introspecting it would mean reflection. The documented shape is a map.
func providerOptions(opts any, key string) (map[string]any, error) {
	if opts == nil {
		return nil, nil
	}
	outer, ok := opts.(map[string]any)
	if !ok {
		return nil, nil
	}
	if raw, present := outer[key]; present {
		inner, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("minimax embeddings: ProviderOptions[%q] is %T, want map[string]any", key, raw)
		}
		return inner, nil
	}
	// No sub-map, but our own option names at the top level: the nesting level was
	// forgotten. Say so rather than proceed with defaults.
	for _, k := range []string{"type", "group_id"} {
		if _, present := outer[k]; present {
			return nil, fmt.Errorf(
				"minimax embeddings: ProviderOptions has %q at the top level; "+
					"it must be nested under %q, as in map[string]any{%q: map[string]any{%q: ...}}",
				k, key, key, k)
		}
	}
	return nil, nil
}
