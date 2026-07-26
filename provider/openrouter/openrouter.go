package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

type Provider struct {
	baseURL      string
	chatURL      string
	modelsURL    string
	videosURL    string
	client       *http.Client // non-streaming requests (with timeout)
	streamClient *http.Client // streaming requests (no global timeout)
}

// New constructs an OpenRouter provider pointed at the official API.
func New() *Provider { return NewWithBaseURL("") }

// NewWithBaseURL constructs an OpenRouter provider against a custom API root
// (relay/proxy deployments). Pass an empty string for the official endpoint.
// The root must be the API base without a trailing slash, e.g.
// "https://openrouter.ai/api/v1".
func NewWithBaseURL(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	outboundTransport := httpx.NewOutbound()
	return &Provider{
		baseURL:   baseURL,
		chatURL:   baseURL + "/chat/completions",
		modelsURL: baseURL + "/models",
		videosURL: baseURL + "/videos",
		client: &http.Client{
			Timeout:   300 * time.Second,
			Transport: outboundTransport,
		},
		streamClient: &http.Client{
			Timeout:   900 * time.Second, // generous safety-net; WriteTimeout (600s) handles normal cutoff
			Transport: outboundTransport,
		},
	}
}

func (p *Provider) Name() string {
	return "openrouter"
}

func (p *Provider) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	body := buildRequest(model, req)
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
		return nil, provider.NewProviderErrorFromResponse(resp, "openrouter", respBody)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result provider.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	// OpenRouter ships the reasoning trace in `message.reasoning` (string) for
	// the back-compat path. Normalize it into reasoning_content so downstream
	// gateway clients see a single shape regardless of upstream.
	normalizeReasoningOnResponse(respBody, &result)

	provider.NormalizeUsage(result.Usage)

	// 同流式：图像类模型在 message.images[] 等路径上挂载图像，OpenRouter usage 不报这维度。
	// 非流式响应是一次性 body，直接复用 countImagesInChunk 即可。
	if n := countImagesInChunk(respBody); n > 0 {
		if result.Usage == nil {
			result.Usage = &provider.Usage{RequestCount: 1}
		}
		if result.Usage.ImageCount == 0 {
			result.Usage.ImageCount = n
		}
	}

	// 结构化资产提取：让客户端在 message.images 上拿到 MediaAsset 列表。
	// 与 countImagesInChunk 互补——后者只负责轻量计数（用于 usage），
	// 这里负责把 URL/data URL/base64 拆出来。
	if assets := extractMediaAssets(respBody); len(assets) > 0 {
		attachAssetsToChoices(result.Choices, assets)
		if result.Usage != nil && result.Usage.MediaCount == 0 {
			result.Usage.MediaCount = len(assets)
		}
	}

	return &result, nil
}

// attachAssetsToChoices 把提取到的 MediaAsset 列表挂到第一个 assistant choice 的
// Message.Images 字段上。流式/非流式都用同一种"放在 choice[0]"的简化——OpenRouter
// 实际只会输出一个 assistant choice；多 choice 情景由调用方按需扩展。
func attachAssetsToChoices(choices []provider.Choice, assets []provider.MediaAsset) {
	if len(assets) == 0 || len(choices) == 0 {
		return
	}
	target := choices[0].Message
	if target == nil {
		target = choices[0].Delta
	}
	if target == nil {
		return
	}
	target.Images = append(target.Images, assets...)
}

// normalizeReasoningOnResponse extracts choices[].message.reasoning (a string
// emitted by OpenRouter for non-OpenAI reasoning models) and assigns it to the
// unified ReasoningContent field. The unified Message struct doesn't bind that
// field at decode time, so we re-parse from the raw bytes.
//
// Also pulls reasoning_details into:
//   - ReasoningContentSignature ← reasoning.text[].signature
//   - RedactedThinking ← reasoning.encrypted[].data
//
// so the client can echo Anthropic-routed thinking turns back through the
// gateway on the next turn.
func normalizeReasoningOnResponse(raw []byte, resp *provider.ChatCompletionResponse) {
	var aux struct {
		Choices []struct {
			Message *struct {
				Reasoning        *string `json:"reasoning"`
				ReasoningDetails []struct {
					Type      string `json:"type"`
					Text      string `json:"text"`
					Signature string `json:"signature"`
					Data      string `json:"data"`
				} `json:"reasoning_details"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return
	}
	for i, c := range aux.Choices {
		if i >= len(resp.Choices) || c.Message == nil {
			continue
		}
		dst := resp.Choices[i].Message
		if dst == nil {
			continue
		}
		if dst.ReasoningContent == nil && c.Message.Reasoning != nil {
			dst.ReasoningContent = c.Message.Reasoning
		}
		// Lift the first non-empty signature from reasoning_details so the
		// gateway client can round-trip Anthropic-routed thinking turns. Walk
		// every entry to also collect reasoning.encrypted blobs into
		// RedactedThinking — these are opaque payloads that Anthropic uses to
		// retain reasoning state when sensitive content is auto-redacted.
		var redacted []string
		for _, rd := range c.Message.ReasoningDetails {
			if dst.ReasoningContentSignature == nil && rd.Type == "reasoning.text" && rd.Signature != "" {
				sig := rd.Signature
				dst.ReasoningContentSignature = &sig
			}
			if rd.Type == "reasoning.encrypted" && rd.Data != "" {
				redacted = append(redacted, rd.Data)
			}
		}
		if len(redacted) > 0 && len(dst.RedactedThinking) == 0 {
			dst.RedactedThinking = redacted
		}
	}
}

func (p *Provider) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	body := buildRequest(model, req)
	body["stream"] = true
	// Always ask upstream for usage so the gateway can record it; only forward
	// the terminal usage chunk to the client when they explicitly opted in.
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
		return nil, provider.NewProviderErrorFromResponse(resp, "openrouter", respBody)
	}

	return NewStreamReader(ctx, resp.Body, emitUsageChunk), nil
}

// ListModels fetches available models from the OpenRouter API.
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
		return nil, provider.NewProviderErrorFromResponse(resp, "openrouter", respBody)
	}

	var result struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []provider.RemoteModel
	for _, m := range result.Data {
		displayName := m.Name
		if displayName == "" {
			displayName = m.ID
		}
		models = append(models, provider.RemoteModel{
			ModelID:       m.ID,
			DisplayName:   displayName,
			ContextWindow: m.ContextLength,
		})
	}
	return models, nil
}

// buildReasoningDetails packs an assistant message's reasoning state into
// OpenRouter's reasoning_details array shape. OpenRouter routes these through
// to Anthropic with the original block types preserved, so multi-turn signed
// thinking and redacted_thinking both round-trip correctly.
func buildReasoningDetails(msg provider.Message) []map[string]any {
	var details []map[string]any
	idx := 0
	if msg.ReasoningContent != nil && msg.ReasoningContentSignature != nil && *msg.ReasoningContentSignature != "" {
		details = append(details, map[string]any{
			"type":      "reasoning.text",
			"text":      *msg.ReasoningContent,
			"signature": *msg.ReasoningContentSignature,
			"format":    "anthropic-claude-v1",
			"index":     idx,
		})
		idx++
	}
	for _, blob := range msg.RedactedThinking {
		details = append(details, map[string]any{
			"type":   "reasoning.encrypted",
			"data":   blob,
			"format": "anthropic-claude-v1",
			"index":  idx,
		})
		idx++
	}
	return details
}

func buildRequest(model string, req *provider.ChatCompletionRequest) map[string]any {
	// Convert messages to maps to preserve all fields (tool_calls, tool_call_id, multimodal content)
	msgs := make([]map[string]any, len(req.Messages))
	for i, msg := range req.Messages {
		m := provider.MessageToMap(msg)
		// OpenRouter expects assistant turns that previously emitted reasoning
		// to echo it back via the `reasoning` (string) field for back-compat
		// with reasoning models. MessageToMap emits reasoning_content; mirror
		// it as reasoning so OpenRouter routes it correctly without losing the
		// thought trace through the gateway.
		if msg.Role == "assistant" && msg.ReasoningContent != nil {
			m["reasoning"] = *msg.ReasoningContent
		}
		// Construct OpenRouter's reasoning_details array so signed thinking
		// blocks AND redacted_thinking blocks survive the OpenRouter →
		// Anthropic hop intact. Without this:
		//   - signed thinking → Anthropic 400 ("signature missing")
		//   - redacted_thinking → Anthropic 400 ("missing thinking block")
		// Append entries in the same order the Anthropic native builder uses:
		// signed thinking first (if any), then opaque redacted blobs.
		if msg.Role == "assistant" {
			details := buildReasoningDetails(msg)
			if len(details) > 0 {
				m["reasoning_details"] = details
			}
		}
		// assistant prefill flag: DeepSeek expects "prefix", Kimi/Moonshot
		// expects "partial". OpenRouter is a routing layer that forwards
		// unknown body fields to the chosen upstream, so we emit BOTH —
		// whichever underlying model the user routed to picks the field name
		// it knows; the other is ignored. Without this, Prefix is silently
		// lost on every OpenRouter-routed multi-turn prefill conversation.
		if msg.Role == "assistant" && msg.Prefix != nil {
			m["prefix"] = *msg.Prefix
			m["partial"] = *msg.Prefix
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
	if req.MaxCompletionTokens != nil {
		body["max_completion_tokens"] = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if req.Stop != nil {
		body["stop"] = req.Stop
	}
	// P0: Tool Use
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.ToolChoice != nil {
		body["tool_choice"] = req.ToolChoice
	}
	// P0: Structured Output
	if req.ResponseFormat != nil {
		body["response_format"] = req.ResponseFormat
	}
	// P1: Reasoning — OpenRouter uses reasoning object, not flat reasoning_effort
	hasReasoning := req.ReasoningEffort != nil || (req.Thinking != nil && req.Thinking.Type == "enabled" && req.Thinking.BudgetTokens > 0)
	if hasReasoning {
		reasoning := map[string]any{}
		if req.ReasoningEffort != nil {
			reasoning["effort"] = *req.ReasoningEffort
		}
		if req.Thinking != nil && req.Thinking.Type == "enabled" && req.Thinking.BudgetTokens > 0 {
			reasoning["max_tokens"] = req.Thinking.BudgetTokens
		}
		body["reasoning"] = reasoning
	}
	// P1: Extra params
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
	if req.TopK != nil {
		// OpenRouter forwards top_k to upstream models that accept it.
		body["top_k"] = *req.TopK
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
	if req.ProviderRouting != nil {
		body["provider"] = req.ProviderRouting
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
	// OpenRouter chat image generation: forward output modalities + image_config
	// when set. Without this the gateway silently drops the client's intent to
	// request image output, and upstream just returns plain text.
	if len(req.Modalities) > 0 {
		body["modalities"] = req.Modalities
	}
	if req.ImageConfig != nil {
		body["image_config"] = req.ImageConfig
	}
	return body
}
