package compat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
)

// Config configures an OpenAI-compatible provider.
type Config struct {
	ProviderName string // e.g. "deepseek"
	BaseURL      string // e.g. "https://api.deepseek.com/v1"
	// PrefillFieldName is the provider-specific JSON key for assistant
	// prefill. DeepSeek uses "prefix"; Kimi/Moonshot uses "partial". When
	// set, the compat layer attaches Message.Prefix as that field name on
	// outgoing assistant messages. Leave empty for providers without
	// prefill support so we don't pollute the request body.
	PrefillFieldName string
	// PassModalities forwards the OpenAI-compatible `modalities` field. Keep this
	// opt-in because some strict compat providers reject unknown multimodal keys.
	PassModalities bool
	// PassAudio forwards the OpenAI-compatible `audio` output configuration.
	PassAudio bool
}

// Provider implements provider.Provider for any OpenAI-compatible API.
type Provider struct {
	name             string
	baseURL          string
	chatURL          string // BaseURL + "/chat/completions"
	modelsURL        string // BaseURL + "/models"
	embeddingsURL    string // BaseURL + "/embeddings"
	prefillFieldName string // empty when the upstream has no prefill mechanism
	passModalities   bool
	passAudio        bool
	client           *http.Client
	streamClient     *http.Client
}

// New creates a new OpenAI-compatible provider.
func New(cfg Config) *Provider {
	outboundTransport := httpx.NewOutbound()
	return &Provider{
		name:             cfg.ProviderName,
		baseURL:          cfg.BaseURL,
		chatURL:          cfg.BaseURL + "/chat/completions",
		modelsURL:        cfg.BaseURL + "/models",
		embeddingsURL:    cfg.BaseURL + "/embeddings",
		prefillFieldName: cfg.PrefillFieldName,
		passModalities:   cfg.PassModalities,
		passAudio:        cfg.PassAudio,
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
	return p.name
}

func (p *Provider) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	body := buildRequestWithOptions(model, req, p.prefillFieldName, requestOptions{
		passModalities: p.passModalities,
		passAudio:      p.passAudio,
	})
	body["stream"] = false

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.chatURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, p.name, respBody)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result provider.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	// Lift `message.reasoning` (Kimi K2.6 / some vLLM-served models) into the
	// unified `reasoning_content` field. DeepSeek / OpenAI o-series already use
	// reasoning_content directly so this is a no-op for them.
	liftReasoningOnResponse(respBody, &result)
	provider.NormalizeUsage(result.Usage)

	return &result, nil
}

// liftReasoningOnResponse re-parses the raw upstream body to pull out
// message.reasoning → message.reasoning_content for providers that emit the
// shorter field name (Kimi K2.6 onward, some vLLM templates).
func liftReasoningOnResponse(raw []byte, resp *provider.ChatCompletionResponse) {
	if resp == nil {
		return
	}
	var aux struct {
		Choices []struct {
			Message *struct {
				Reasoning *string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return
	}
	for i, c := range aux.Choices {
		if i >= len(resp.Choices) || c.Message == nil || c.Message.Reasoning == nil {
			continue
		}
		// Skip empty reasoning so the response doesn't carry an empty
		// reasoning_content field (which clients may render as a blank "thinking"
		// block); upstream sometimes emits "reasoning":"" alongside content.
		if *c.Message.Reasoning == "" {
			continue
		}
		dst := resp.Choices[i].Message
		if dst != nil && dst.ReasoningContent == nil {
			dst.ReasoningContent = c.Message.Reasoning
		}
	}
}

func (p *Provider) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	body := buildRequestWithOptions(model, req, p.prefillFieldName, requestOptions{
		passModalities: p.passModalities,
		passAudio:      p.passAudio,
	})
	body["stream"] = true
	// Always request usage from upstream so the gateway can record token cost,
	// but only forward the terminal usage chunk to the client when they opted
	// in via stream_options.include_usage. Spamming clients with an unexpected
	// trailing chunk breaks lighter SDKs that assume the last delta is the
	// final event.
	body["stream_options"] = map[string]any{"include_usage": true}
	emitUsageChunk := req.StreamOptions != nil && req.StreamOptions.IncludeUsage

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.chatURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, p.name, respBody)
	}

	return NewStreamReader(resp.Body, p.name, emitUsageChunk), nil
}

// ListModels fetches available models from the provider's /models endpoint.
func (p *Provider) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.modelsURL, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, p.name, respBody)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []provider.RemoteModel
	for _, m := range result.Data {
		models = append(models, provider.RemoteModel{
			ModelID:     m.ID,
			DisplayName: m.ID,
		})
	}
	return models, nil
}

type requestOptions struct {
	passModalities bool
	passAudio      bool
}

func buildRequest(model string, req *provider.ChatCompletionRequest, prefillFieldName string) map[string]any {
	return buildRequestWithOptions(model, req, prefillFieldName, requestOptions{})
}

func buildRequestWithOptions(model string, req *provider.ChatCompletionRequest, prefillFieldName string, opts requestOptions) map[string]any {
	msgs := make([]map[string]any, len(req.Messages))
	for i, msg := range req.Messages {
		m := provider.MessageToMap(msg)
		// Attach assistant prefill flag under the upstream's preferred field
		// name (e.g. "prefix" for DeepSeek-compat, "partial" for Kimi).
		// Skipped entirely when the provider has no prefill mechanism so we
		// don't pollute the wire with an unknown field.
		if prefillFieldName != "" && msg.Role == "assistant" && msg.Prefix != nil {
			m[prefillFieldName] = *msg.Prefix
		}
		msgs[i] = m
	}

	body := map[string]any{
		"model":    model,
		"messages": msgs,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	// top_k is non-standard for OpenAI but accepted by Zhipu/SiliconFlow/some
	// Kimi models. Forwarding lets clients tune those models; OpenAI itself
	// will return 400 (which the gateway passes through), making the silent
	// drop the worse default.
	if req.TopK != nil {
		body["top_k"] = *req.TopK
	}
	if req.MaxCompletionTokens != nil {
		body["max_completion_tokens"] = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
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
	if req.ToolStream != nil {
		body["tool_stream"] = *req.ToolStream
	}
	if req.ResponseFormat != nil {
		body["response_format"] = req.ResponseFormat
	}
	if req.ReasoningEffort != nil {
		body["reasoning_effort"] = *req.ReasoningEffort
	}
	if req.Thinking != nil && req.Thinking.Type != "" {
		// 各家 OpenAI 兼容厂商（智谱 GLM-4.6+、Kimi、SiliconFlow Qwen3 等）
		// 都只接受 {"type":"enabled|disabled"} 形态。共享 ThinkingConfig 还
		// 带有 Anthropic 专属的 BudgetTokens 字段，原样透传会被严格 schema
		// 校验拒绝（智谱 400）。这里只透传 type，与 DeepSeek 原生处理一致。
		body["thinking"] = map[string]any{"type": req.Thinking.Type}
	}
	if req.FrequencyPenalty != nil {
		body["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		body["presence_penalty"] = *req.PresencePenalty
	}
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}
	if req.N != nil {
		body["n"] = *req.N
	}
	if req.LogProbs != nil {
		body["logprobs"] = *req.LogProbs
	}
	if req.TopLogProbs != nil {
		body["top_logprobs"] = *req.TopLogProbs
	}
	if req.LogitBias != nil {
		body["logit_bias"] = req.LogitBias
	}
	if req.Prediction != nil {
		body["prediction"] = req.Prediction
	}
	if req.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.ServiceTier != nil {
		body["service_tier"] = *req.ServiceTier
	}
	if req.User != "" {
		body["user"] = req.User
	}
	if req.Verbosity != nil {
		body["verbosity"] = *req.Verbosity
	}
	if req.PromptCacheKey != "" {
		body["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.SafetyIdentifier != "" {
		body["safety_identifier"] = req.SafetyIdentifier
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	if req.Metadata != nil {
		body["metadata"] = req.Metadata
	}
	// Provider-specific extensions, all forwarded as-is. Each has a single
	// upstream that consumes it; the others ignore unknown fields.
	if req.CacheID != "" {
		body["cache_id"] = req.CacheID // Kimi/Moonshot
	}
	if req.DoSample != nil {
		body["do_sample"] = *req.DoSample // Zhipu/GLM
	}
	if req.EnableThinking != nil {
		body["enable_thinking"] = *req.EnableThinking // Zhipu/GLM-4.5+
	}
	if req.ClearThinkingContent != nil {
		body["clear_thinking_content"] = *req.ClearThinkingContent // Zhipu/GLM
	}
	if req.RequestID != "" {
		body["request_id"] = req.RequestID // Zhipu/GLM
	}
	if req.BotSetting != nil {
		body["bot_setting"] = req.BotSetting // MiniMax Pro
	}
	if req.ChatTemplateKwargs != nil {
		body["chat_template_kwargs"] = req.ChatTemplateKwargs // Kimi K2.6 / vLLM / SGLang
	}
	if opts.passModalities && len(req.Modalities) > 0 {
		body["modalities"] = req.Modalities
	}
	if opts.passAudio && req.Audio != nil {
		body["audio"] = req.Audio
	}
	// Provider routing passthrough (OpenRouter `provider`, Vercel `providerOptions`).
	// Both are opaque JSON; non-recipient upstreams ignore the unknown field.
	// We forward them blindly so clients can target whichever aggregator's routing
	// shape they need without compat needing to know per-provider semantics.
	if req.ProviderRouting != nil {
		body["provider"] = req.ProviderRouting
	}
	if req.ProviderOptions != nil {
		body["providerOptions"] = req.ProviderOptions
	}
	return body
}

// Embeddings calls the upstream /embeddings endpoint with an OpenAI-compatible
// payload. Available to any compat-based provider (vercel, openai, deepseek,
// siliconflow, ...); whether a model can actually be invoked is enforced by
// the resolver's endpoint_kind check, not here.
func (p *Provider) Embeddings(ctx context.Context, apiKey, model string, req *provider.EmbeddingRequest) (*provider.EmbeddingResponse, error) {
	body := map[string]any{
		"model": model,
		"input": req.Input,
	}
	if req.Dimensions != nil {
		body["dimensions"] = *req.Dimensions
	}
	if req.EncodingFormat != nil {
		body["encoding_format"] = *req.EncodingFormat
	}
	if req.User != "" {
		body["user"] = req.User
	}
	if req.ProviderOptions != nil {
		body["providerOptions"] = req.ProviderOptions
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.embeddingsURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, p.name, respBody)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result provider.EmbeddingResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	provider.NormalizeUsage(result.Usage)
	return &result, nil
}
