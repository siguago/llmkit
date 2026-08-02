package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const (
	name           = "deepseek"
	defaultBaseURL = "https://api.deepseek.com"
)

type Provider struct {
	baseURL      string // host root, e.g. "https://api.deepseek.com" (no /v1 or /beta)
	client       *http.Client
	streamClient *http.Client // streaming requests (900s client-wide ceiling)
}

// New constructs a DeepSeek provider. Pass an empty baseURL to use the default
// global endpoint. The provider switches /v1 vs /beta path internally based on
// request features (prefix prefill, strict tools).
func New(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	outboundTransport := httpx.NewOutbound()
	return &Provider{
		baseURL: baseURL,
		client: &http.Client{
			Timeout:   300 * time.Second,
			Transport: outboundTransport,
		},
		streamClient: &http.Client{
			Timeout:   900 * time.Second,
			Transport: outboundTransport,
		},
	}
}

func (p *Provider) Name() string {
	return name
}

func (p *Provider) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	body, url, _, err := buildRequest(p.baseURL, model, req, false)
	if err != nil {
		return nil, err
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
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
		return nil, provider.NewProviderErrorFromResponse(resp, name, respBody)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result provider.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	provider.NormalizeUsage(result.Usage)

	return &result, nil
}

func (p *Provider) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	body, url, emitUsageChunk, err := buildRequest(p.baseURL, model, req, true)
	if err != nil {
		return nil, err
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, name, respBody)
	}

	// compat already gates the terminal usage chunk on emitUsageChunk; the
	// outer wrapper still applies its own gating for parity with the original
	// behavior, but with both pointing at the same flag we never accidentally
	// drop a chunk the client asked for.
	return &streamReader{
		inner:          compat.NewStreamReader(ctx, resp.Body, name, emitUsageChunk),
		emitUsageChunk: emitUsageChunk,
	}, nil
}

func (p *Provider) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	models, _, err := p.listModelsWithTaskTypes(ctx, apiKey)
	return models, err
}

// ListModelsWithTaskTypes returns the DeepSeek model catalog with its
// per-model chat classification kept separate from RemoteModel.
func (p *Provider) ListModelsWithTaskTypes(ctx context.Context, apiKey string) ([]provider.RemoteModel, map[string][]string, error) {
	return p.listModelsWithTaskTypes(ctx, apiKey)
}

func (p *Provider) listModelsWithTaskTypes(ctx context.Context, apiKey string) ([]provider.RemoteModel, map[string][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, nil, provider.NewProviderErrorFromResponse(resp, name, respBody)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, err
	}

	models := make([]provider.RemoteModel, 0, len(result.Data))
	taskTypes := make(map[string][]string, len(result.Data))
	for _, m := range result.Data {
		models = append(models, provider.RemoteModel{
			ModelID:     m.ID,
			DisplayName: m.ID,
		})
		if tasks := deepSeekModelTaskTypes(m.ID); len(tasks) > 0 {
			taskTypes[m.ID] = tasks
		}
	}
	return models, taskTypes, nil
}

// deepSeekModelTaskTypes is intentionally an allowlist because /v1/models
// exposes only IDs, not capability metadata. Only the current documented V4
// IDs are classified. Retired aliases and future IDs remain visible in the
// catalog but unknown until DeepSeek documents current endpoint semantics.
func deepSeekModelTaskTypes(modelID string) []string {
	switch modelID {
	case "deepseek-v4-flash", "deepseek-v4-pro":
		return []string{provider.RemoteModelTaskChat}
	default:
		return nil
	}
}

var _ provider.ModelTaskLister = (*Provider)(nil)

type streamReader struct {
	inner          *compat.StreamReader
	emitUsageChunk bool
}

func (s *streamReader) Recv() (*provider.ChatCompletionChunk, error) {
	for {
		chunk, err := s.inner.Recv()
		if err != nil {
			return nil, err
		}
		if chunk.Usage != nil && len(chunk.Choices) == 0 && !s.emitUsageChunk {
			continue
		}
		return chunk, nil
	}
}

func (s *streamReader) Close() error {
	return s.inner.Close()
}

func (s *streamReader) GetUsage() *provider.Usage {
	return s.inner.GetUsage()
}

func buildRequest(host, model string, req *provider.ChatCompletionRequest, streaming bool) (map[string]any, string, bool, error) {
	// DeepSeek classifies parameter-level rejections as HTTP 422; mirror that here so
	// locally-rejected requests carry the same status as the upstream would have returned.
	// See https://api-docs.deepseek.com/quick_start/error_codes
	if req.N != nil && *req.N != 1 {
		return nil, "", false, &provider.ProviderError{
			StatusCode: http.StatusUnprocessableEntity,
			Message:    "deepseek does not support n>1; only a single completion per request is allowed",
		}
	}
	if req.Seed != nil {
		return nil, "", false, &provider.ProviderError{
			StatusCode: http.StatusUnprocessableEntity,
			Message:    "deepseek does not support the seed parameter",
		}
	}

	msgs, needsBeta, err := buildMessages(req.Messages)
	if err != nil {
		return nil, "", false, err
	}

	body := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   streaming,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	} else if req.MaxCompletionTokens != nil {
		body["max_tokens"] = *req.MaxCompletionTokens
	}
	if req.Stop != nil {
		body["stop"] = req.Stop
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.ToolChoice != nil {
		body["tool_choice"] = req.ToolChoice
	}
	if req.ResponseFormat != nil {
		if req.ResponseFormat.Type == "json_schema" {
			return nil, "", false, &provider.ProviderError{
				StatusCode: http.StatusUnprocessableEntity,
				Message:    "deepseek only supports response_format.type=text|json_object on /chat/completions",
			}
		}
		body["response_format"] = map[string]any{"type": req.ResponseFormat.Type}
	}
	if req.ReasoningEffort != nil {
		body["reasoning_effort"] = *req.ReasoningEffort
	}
	if req.Thinking != nil && req.Thinking.Type != "" {
		// DeepSeek OpenAI 格式只定义 {"thinking":{"type":"enabled/disabled"}}。
		// 共享 ThinkingConfig 带有 Anthropic 专属的 BudgetTokens，不得原样透传。
		// thinking 的 reasoning_effort 由顶层 req.ReasoningEffort 独立承载。
		body["thinking"] = map[string]any{"type": req.Thinking.Type}
	}
	if req.FrequencyPenalty != nil {
		body["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		body["presence_penalty"] = *req.PresencePenalty
	}
	if req.LogProbs != nil {
		body["logprobs"] = *req.LogProbs
	}
	if req.TopLogProbs != nil {
		body["top_logprobs"] = *req.TopLogProbs
	}
	if req.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.User != "" {
		body["user"] = req.User
	}

	emitUsageChunk := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	if streaming {
		// Always ask upstream for usage so the gateway can log tokens, but only surface
		// the terminal usage chunk to the client when it was explicitly requested.
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	if usesStrictTools(req.Tools) {
		needsBeta = true
	}

	baseURL := host + "/v1"
	if needsBeta {
		baseURL = host + "/beta"
	}
	return body, baseURL + "/chat/completions", emitUsageChunk, nil
}

func buildMessages(messages []provider.Message) ([]map[string]any, bool, error) {
	msgs := make([]map[string]any, len(messages))
	needsBeta := false
	for i, msg := range messages {
		content, err := normalizeContent(msg.Content)
		if err != nil {
			return nil, false, err
		}
		msg.Content = content

		m := provider.MessageToMap(msg)
		if msg.Role == "assistant" && msg.Prefix != nil {
			m["prefix"] = *msg.Prefix
			if *msg.Prefix {
				needsBeta = true
			}
		}
		msgs[i] = m
	}
	return msgs, needsBeta, nil
}

func normalizeContent(content any) (any, error) {
	if content == nil {
		return nil, nil
	}
	// DeepSeek /chat/completions only accepts text. Reject any non-text part instead of
	// silently dropping it via ContentToString — silent downgrade hides bugs in clients
	// that were expecting multimodal dispatch.
	switch v := content.(type) {
	case []provider.ContentPart:
		for _, part := range v {
			if part.Type != "text" {
				return nil, unsupportedPartError(part.Type)
			}
		}
		return provider.ContentToString(content), nil
	case []any:
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, &provider.ProviderError{
					StatusCode: http.StatusUnprocessableEntity,
					Message:    "deepseek /chat/completions: content parts must be objects with a type field",
				}
			}
			t, _ := m["type"].(string)
			if t != "text" {
				return nil, unsupportedPartError(t)
			}
			// Require `text` to be present and a string. Missing / non-string text would
			// otherwise fall through ContentToString as silent empty output.
			raw, exists := m["text"]
			if !exists {
				return nil, &provider.ProviderError{
					StatusCode: http.StatusUnprocessableEntity,
					Message:    "deepseek /chat/completions: text content part missing 'text' field",
				}
			}
			if _, ok := raw.(string); !ok {
				return nil, &provider.ProviderError{
					StatusCode: http.StatusUnprocessableEntity,
					Message:    "deepseek /chat/completions: text content part 'text' must be a string",
				}
			}
		}
		return provider.ContentToString(content), nil
	}
	return content, nil
}

func unsupportedPartError(partType string) error {
	if partType == "" {
		partType = "<empty>"
	}
	return &provider.ProviderError{
		StatusCode: http.StatusUnprocessableEntity,
		Message:    "deepseek /chat/completions only accepts text content parts; got type=" + partType,
	}
}

func usesStrictTools(tools []provider.Tool) bool {
	for _, tool := range tools {
		if tool.Function.Strict != nil && *tool.Function.Strict {
			return true
		}
	}
	return false
}
