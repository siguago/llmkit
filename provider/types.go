package provider

import (
	"context"
	"encoding/json"
	"io"
)

// ChatCompletionRequest is the unified request format (OpenAI-compatible).
type ChatCompletionRequest struct {
	Model               string    `json:"model"`
	Messages            []Message `json:"messages"`
	Temperature         *float64  `json:"temperature,omitempty"`
	TopP                *float64  `json:"top_p,omitempty"`
	MaxTokens           *int      `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int      `json:"max_completion_tokens,omitempty"`
	Stream              bool      `json:"stream,omitempty"`
	Stop                any       `json:"stop,omitempty"`
	// P0: Tool Use
	Tools      []Tool `json:"tools,omitempty"`
	ToolChoice any    `json:"tool_choice,omitempty"`
	// ToolStream enables provider-side streaming of tool call arguments.
	// Currently used by Zhipu GLM-5.2+ with stream=true; other providers ignore.
	ToolStream *bool `json:"tool_stream,omitempty"`
	// P0: Structured Output
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	// P1: Thinking / Reasoning
	ReasoningEffort *string         `json:"reasoning_effort,omitempty"`
	Thinking        *ThinkingConfig `json:"thinking,omitempty"`
	// P1: Extra params
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	Seed             *int     `json:"seed,omitempty"`
	N                *int     `json:"n,omitempty"`
	LogProbs         *bool    `json:"logprobs,omitempty"`
	TopLogProbs      *int     `json:"top_logprobs,omitempty"`
	// LogitBias is OpenAI's per-token sampling adjustment table — keys are
	// token IDs (as strings), values are biases in [-100, 100]. Decoded as
	// any so we don't lock the int-string-key map shape; opaque passthrough
	// to upstream. DeepSeek / reasoning models reject this; for them the
	// gateway either strips (reasoning) or relies on upstream 400 (DeepSeek).
	LogitBias any `json:"logit_bias,omitempty"`
	// Prediction is OpenAI's "Predicted Outputs" hint — when the response is
	// expected to closely match a known string (e.g. minor edits to existing
	// code), supplying it here can roughly halve latency. Shape:
	//   {"type": "content", "content": "<expected output>"}
	// Other providers ignore.
	Prediction any `json:"prediction,omitempty"`
	// Labels (Gemini only) — string→string map for request tagging in
	// Google's billing/monitoring dashboards. Other providers ignore.
	Labels        map[string]string `json:"labels,omitempty"`
	TopK          *int              `json:"top_k,omitempty"`
	StreamOptions *StreamOptions    `json:"stream_options,omitempty"`
	// P1: OpenAI extensions widely supported by compat providers.
	// ParallelToolCalls (default true) lets clients opt out of concurrent tool
	// dispatch. Some reasoning models require it set to false. Pointer so we
	// can distinguish "unset" from "explicit false".
	ParallelToolCalls *bool `json:"parallel_tool_calls,omitempty"`
	// ServiceTier toggles OpenAI's "auto"/"default"/"flex" routing. Other
	// providers ignore it.
	ServiceTier *string `json:"service_tier,omitempty"`
	// User is an opaque end-user identifier for abuse/usage tracking.
	User string `json:"user,omitempty"`
	// Provider routing extension (OpenRouter-specific). Other providers ignore
	// it. Common shape: {"order":["openai","anthropic"], "allow_fallbacks":true,
	// "require_parameters":true, "data_collection":"deny", "ignore":[...]}.
	ProviderRouting any `json:"provider,omitempty"`
	// SafetySettings is a Gemini-specific harm-category threshold list.
	// Other providers ignore. Shape:
	//   [{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}, ...]
	// Common categories: HATE_SPEECH, DANGEROUS_CONTENT, HARASSMENT,
	// SEXUALLY_EXPLICIT. Thresholds: BLOCK_NONE / BLOCK_ONLY_HIGH /
	// BLOCK_MEDIUM_AND_ABOVE / BLOCK_LOW_AND_ABOVE / OFF.
	SafetySettings any `json:"safety_settings,omitempty"`
	// Verbosity controls response length/style on OpenAI GPT-5 family.
	// Values: "low" / "medium" / "high". Other providers ignore.
	Verbosity *string `json:"verbosity,omitempty"`
	// PromptCacheKey is an OpenAI prompt-caching hint (groups requests for
	// shared cache buckets). Other providers ignore.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
	// SafetyIdentifier is OpenAI's replacement for User in newer SDKs (same
	// purpose: stable end-user identifier for abuse detection).
	SafetyIdentifier string `json:"safety_identifier,omitempty"`
	// Store is OpenAI's flag to opt the request into Stored Completions /
	// dashboard tracing. Other providers ignore.
	Store *bool `json:"store,omitempty"`
	// Metadata is the OpenAI key-value tag map for Stored Completions; up to
	// 16 entries with string keys/values. Carried as opaque JSON for
	// passthrough; other providers ignore.
	Metadata any `json:"metadata,omitempty"`
	// CacheID is Kimi/Moonshot's explicit prompt-cache reference. Other
	// providers ignore. See https://platform.moonshot.cn/docs (context caching).
	CacheID string `json:"cache_id,omitempty"`
	// DoSample is Zhipu/GLM's greedy-decoding switch. false → 强制贪心
	// (temperature/top_p ignored). Other providers ignore.
	DoSample *bool `json:"do_sample,omitempty"`
	// EnableThinking is Zhipu/GLM's legacy thinking-mode toggle. Current GLM
	// models use the unified Thinking field directly; if both are supplied, the
	// Zhipu provider drops this legacy boolean to avoid conflicting switches.
	// Clients can still set it explicitly for older model/API compatibility.
	EnableThinking *bool `json:"enable_thinking,omitempty"`
	// ClearThinkingContent (Zhipu) — when true, removes prior turns'
	// reasoning_content from the prompt to save tokens. Other providers ignore.
	ClearThinkingContent *bool `json:"clear_thinking_content,omitempty"`
	// RequestID is Zhipu/GLM's idempotency / tracing key. Echoed back in the
	// response so clients can correlate. Other providers ignore.
	RequestID string `json:"request_id,omitempty"`
	// BotSetting is MiniMax's character-roleplay configuration (Pro models).
	// Opaque JSON for passthrough; other providers ignore.
	BotSetting any `json:"bot_setting,omitempty"`
	// ChatTemplateKwargs forwards extra kwargs to the upstream chat template
	// engine (vLLM / SGLang / Kimi K2.6). Kimi K2.6 specifically requires
	// {"thinking": true} here to enable reasoning. Opaque JSON; other
	// providers ignore.
	ChatTemplateKwargs any `json:"chat_template_kwargs,omitempty"`
	// Modalities lists output modalities the client expects. Currently honored
	// by OpenRouter chat image generation (e.g. ["image","text"]). Plain text
	// providers ignore.
	Modalities []string `json:"modalities,omitempty"`
	// ImageConfig is OpenRouter's image generation options block: opaque JSON
	// shape with fields like {image_size, aspect_ratio, style, strength,
	// rgb_colors, ...}. Plain text providers ignore.
	ImageConfig any `json:"image_config,omitempty"`
	// Audio is OpenAI's audio output config (placeholder for second phase).
	Audio any `json:"audio,omitempty"`
	// CachedContent is a Gemini-specific reference to a previously created
	// cached content resource (full name like "cachedContents/abc123"). When
	// set, Gemini reuses the cached prompt prefix and bills cached tokens at a
	// reduced rate. Cache creation itself is a separate REST endpoint not
	// covered by chat completions; this field only references existing caches.
	// Other providers ignore.
	CachedContent string `json:"cached_content,omitempty"`
	// ExtraTools is an opaque list of additional tool definitions to forward
	// AS-IS in the upstream's tools array. Currently consumed only by the
	// Anthropic provider so callers can invoke server-side built-ins
	// (web_search_20250305 / web_search_20260209, code_execution_20250522,
	// computer_20250124, bash_20250124, text_editor_*) whose JSON shapes
	// don't match OpenAI's `{type:"function",function:{...}}` envelope.
	// Each entry must be a complete object the upstream will accept verbatim.
	// Other providers ignore.
	ExtraTools any `json:"extra_tools,omitempty"`
	// Container (Anthropic only) — session-level reference for the
	// code_execution sandbox. When provided, Anthropic resumes the same
	// container instead of spinning up a fresh one, keeping files/state from
	// the previous turn. The value is the opaque object Anthropic returned in
	// the previous response's top-level `container` field. Other providers
	// ignore.
	Container any `json:"container,omitempty"`
	// ProviderOptions is Vercel AI Gateway's per-request routing/options block
	// (gateway.order, byok keys, etc.). Shape is opaque JSON per Vercel docs;
	// the compat layer forwards verbatim. Other providers ignore.
	ProviderOptions any `json:"providerOptions,omitempty"`
}

type Message struct {
	Role             string     `json:"role"`
	Content          any        `json:"content,omitempty"`           // string | []ContentPart | nil
	ReasoningContent *string    `json:"reasoning_content,omitempty"` // thinking/reasoning trace
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
	Prefix           *bool      `json:"prefix,omitempty"` // DeepSeek beta chat prefix completion
	// ReasoningContentSignature is Anthropic's opaque integrity signature for
	// the preceding thinking block. Anthropic requires it to be echoed back on
	// any multi-turn conversation that combines extended thinking with tool
	// use; without it the API returns 400 ("signature missing"). Other
	// providers ignore the field via omitempty.
	ReasoningContentSignature *string `json:"reasoning_content_signature,omitempty"`
	// Refusal is OpenAI's explicit refusal explanation, distinct from content.
	// When the model declines (e.g. policy violation), `content` is null and
	// `refusal` carries the explanation string. Without binding this field
	// the gateway would silently drop the refusal reason, leaving clients
	// with an empty assistant message.
	Refusal *string `json:"refusal,omitempty"`
	// RedactedThinking carries Anthropic's `redacted_thinking` blocks, which
	// appear when the model's thinking is auto-redacted (PII, policy). They
	// are opaque encrypted blobs (the `data` field on the block) that MUST be
	// echoed back verbatim on multi-turn so the model can maintain reasoning
	// context. The unified surface preserves them as a list of opaque strings.
	RedactedThinking []string `json:"redacted_thinking,omitempty"`
	// Annotations carries OpenAI's content annotations (URL citations from
	// web search, file citations from file search, etc.). The shape varies
	// per annotation type so we keep it as opaque JSON for transparent
	// passthrough. Other providers ignore this field.
	Annotations any `json:"annotations,omitempty"`
	// ProviderTurnBlocks carries provider-native content blocks that don't
	// fit OpenAI's message shape but must round-trip verbatim across multi-
	// turn conversations to keep upstream features working. Currently used by
	// the Anthropic provider to preserve `server_tool_use`,
	// `web_search_tool_result`, and `code_execution_tool_result` blocks
	// (which carry encrypted_content / encrypted_index payloads Anthropic
	// requires echoed back). Each entry is the original block as a JSON
	// object. Clients that don't recognize the field simply ignore it; only
	// multi-turn citation continuity degrades.
	ProviderTurnBlocks []any `json:"provider_turn_blocks,omitempty"`
	// ThoughtSignature is Gemini 2.5+/3.x's encrypted reasoning state. The
	// model emits it on assistant turns when thinking is active, and it MUST
	// be echoed back on the next turn — Gemini 3 returns 400 without it
	// during multi-turn function calling. Gemini ignores the field on user
	// turns; other providers ignore it entirely.
	ThoughtSignature *string `json:"thought_signature,omitempty"`
	// Images carries normalized image media assets emitted by the assistant.
	// Populated from upstream `message.images[]` / `delta.images[]` (OpenRouter
	// chat image generation) and any future chat-shape image output.
	Images []MediaAsset `json:"images,omitempty"`
	// Videos carries normalized video media assets emitted by the assistant.
	// Reserved for future chat-shape video output; currently unused by any
	// provider.
	Videos []MediaAsset `json:"videos,omitempty"`
	// Media is a generic catch-all for media assets that don't fit Images /
	// Videos (e.g. audio). Currently unused.
	Media []MediaAsset `json:"media,omitempty"`
}

// MediaAsset is the unified shape used by both chat-shape image output and
// the dedicated media APIs (/v1/images, /v1/videos). One of URL / B64JSON /
// DataURL is populated depending on what the upstream returned.
type MediaAsset struct {
	ID              string         `json:"id,omitempty"`
	Object          string         `json:"object,omitempty"` // media.asset
	Type            string         `json:"type,omitempty"`   // image | video | audio
	MimeType        string         `json:"mime_type,omitempty"`
	URL             string         `json:"url,omitempty"`
	B64JSON         string         `json:"b64_json,omitempty"`
	DataURL         string         `json:"data_url,omitempty"`
	FileID          string         `json:"file_id,omitempty"`
	ProviderAssetID string         `json:"provider_asset_id,omitempty"`
	Width           int            `json:"width,omitempty"`
	Height          int            `json:"height,omitempty"`
	DurationMs      int            `json:"duration_ms,omitempty"`
	SizeBytes       int64          `json:"size_bytes,omitempty"`
	SHA256          string         `json:"sha256,omitempty"`
	ExpiresAt       int64          `json:"expires_at,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// ContentPart represents a part of a multimodal message content.
type ContentPart struct {
	Type     string    `json:"type"` // "text" / "image_url" / "input_audio" / "file"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	// InputAudio is OpenAI's gpt-4o-audio input shape. The gateway forwards
	// it as inline media to providers that support audio (currently Gemini);
	// providers without audio support drop it during their part conversion.
	InputAudio *InputAudio `json:"input_audio,omitempty"`
	// File is OpenAI's gpt-4o file content type — either a previously
	// uploaded file_id or an inline file_data + mime_type. Gemini accepts
	// the inline form via inlineData; providers without file support drop it.
	File *FileContent `json:"file,omitempty"`
	// CacheControl is an Anthropic prompt-caching hint. When set on a content
	// block, Anthropic will mark it as a cache breakpoint (up to 4 per request).
	// Other providers ignore the field thanks to omitempty. Standard shape:
	//   {"type": "ephemeral"}                  — 5 minute TTL (default)
	//   {"type": "ephemeral", "ttl": "1h"}     — 1 hour TTL when supported
	CacheControl any `json:"cache_control,omitempty"`
}

// ImageURL represents an image URL in a content part.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// InputAudio represents OpenAI's gpt-4o-audio input format —
// {data: "<base64>", format: "wav"|"mp3"|"flac"|"opus"}. The gateway maps
// this to provider-native audio inputs (Gemini inlineData with audio/<format>
// mime type).
type InputAudio struct {
	Data   string `json:"data"`             // base64-encoded audio bytes
	Format string `json:"format,omitempty"` // "wav" / "mp3" / "flac" / "opus" / etc
}

// FileContent represents OpenAI's `file` content part. Either FileID
// references a previously uploaded file (via Files API), or FileData carries
// the inline base64 payload along with MimeType. Most providers only consume
// the inline form; FileID requires the upstream provider to host the same
// file.
type FileContent struct {
	FileID   string `json:"file_id,omitempty"`
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

// Tool represents a tool definition (OpenAI format).
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
	// CacheControl is an Anthropic prompt-caching hint (for tools list).
	// Setting it on the LAST tool caches the tool definitions as a unit. Other
	// providers ignore the field.
	CacheControl any `json:"cache_control,omitempty"`
}

// ToolFunction describes a function tool.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"` // JSON Schema object
	Strict      *bool  `json:"strict,omitempty"`
}

// ToolCall represents a tool call in an assistant message.
type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"` // "function"
	Function ToolCallFunction `json:"function"`
	Index    *int             `json:"index,omitempty"`
}

// ToolCallFunction represents the function being called.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ResponseFormat specifies the desired output format.
type ResponseFormat struct {
	Type       string          `json:"type"` // "text", "json_object", "json_schema"
	JSONSchema *JSONSchemaSpec `json:"json_schema,omitempty"`
}

// JSONSchemaSpec describes a JSON schema for structured output.
type JSONSchemaSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Schema      any    `json:"schema"`
	Strict      *bool  `json:"strict,omitempty"`
}

// ThinkingConfig controls extended thinking / reasoning.
type ThinkingConfig struct {
	Type         string `json:"type,omitempty"`          // "enabled" / "disabled" (Anthropic) — also accepted by DeepSeek/Kimi/Zhipu compat
	BudgetTokens int    `json:"budget_tokens,omitempty"` // Anthropic budget
	// Display controls whether Anthropic returns the full reasoning trace or a
	// summary. Allowed values: "summarized" (default, compact) / "omitted"
	// (no reasoning_content in the response). Other providers ignore.
	Display string `json:"display,omitempty"`
	// Keep controls whether previous-turn reasoning blocks are kept in the
	// upstream prompt context. Currently used by Kimi K2.6 (mapped onto
	// chat_template_kwargs.thinking_keep by the moonshot wrapper). nil = let
	// upstream decide; true = preserve; false = drop. Other providers ignore.
	Keep *bool `json:"keep,omitempty"`
}

// StreamOptions controls streaming behavior.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatCompletionResponse is the unified response format.
type ChatCompletionResponse struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	SystemFingerprint string   `json:"system_fingerprint,omitempty"`
	Choices           []Choice `json:"choices"`
	Usage             *Usage   `json:"usage,omitempty"`
	// Container (Anthropic only) — code_execution sandbox descriptor that the
	// caller should pass back as request.container next turn to resume the
	// same Python sandbox (preserving uploaded files, installed packages,
	// etc.). Other providers leave this nil.
	Container any `json:"container,omitempty"`
}

type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	Delta        *Message `json:"delta,omitempty"`
	Logprobs     any      `json:"logprobs,omitempty"`
	FinishReason *string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	CachedTokens            int                      `json:"cached_tokens,omitempty"`
	ReasoningTokens         int                      `json:"reasoning_tokens,omitempty"`
	PromptCacheHitTokens    int                      `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens   int                      `json:"prompt_cache_miss_tokens,omitempty"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	// Cost is the upstream-reported request cost in USD. Currently emitted by
	// OpenRouter (which routes across vendors and aggregates their actual
	// charges); other providers leave it zero and omitempty hides it. The
	// gateway's own cost computation in usage_logs is independent — both can
	// coexist (gateway uses its price table for billing dashboards, the
	// passthrough field exposes upstream's number to clients that care).
	Cost float64 `json:"cost,omitempty"`
	// ImageCount / DurationMs / RequestCount 是非 token 计费维度的统一容量字段。
	// 由 provider 各自填充（图像类填 ImageCount、音视频填 DurationMs）；
	// NormalizeUsage 兜底把 RequestCount 升到至少 1，保证 per_request 策略
	// 在上游不报 usage 时仍能按 1 次结算。
	ImageCount   int `json:"image_count,omitempty"`
	DurationMs   int `json:"duration_ms,omitempty"`
	RequestCount int `json:"request_count,omitempty"`
	// MediaCount 是输出资产总数（跨 image/video/audio）；与 ImageCount 不应相加。
	// 由媒体 handler / chat 解析路径填充，主要用于日志统一聚合。
	MediaCount int `json:"media_count,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// ChatCompletionChunk is a streaming chunk.
type ChatCompletionChunk struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	SystemFingerprint string   `json:"system_fingerprint,omitempty"`
	Choices           []Choice `json:"choices"`
	Usage             *Usage   `json:"usage,omitempty"`
}

// StreamReader reads streaming chunks.
type StreamReader interface {
	Recv() (*ChatCompletionChunk, error) // io.EOF means done
	Close() error
	GetUsage() *Usage
}

// ModelLister is an optional interface for providers that support listing models.
type ModelLister interface {
	ListModels(ctx context.Context, apiKey string) ([]RemoteModel, error)
}

// Embedder is an optional interface for providers that support /v1/embeddings.
// Currently implemented by the OpenAI-compat layer (which means any compat-based
// provider — vercel, openai, deepseek, siliconflow, etc. — automatically
// supports embeddings if the upstream does). Whether a particular model can
// actually be called via embeddings is gated by the model binding's
// endpoint_kind=embeddings + task_types=["embedding"] in the resolver.
type Embedder interface {
	Embeddings(ctx context.Context, apiKey, model string, req *EmbeddingRequest) (*EmbeddingResponse, error)
}

// EmbeddingRequest is the unified embeddings request (OpenAI-compatible).
type EmbeddingRequest struct {
	Model          string  `json:"model"`
	Input          any     `json:"input"` // string | []string | []int | [][]int
	Dimensions     *int    `json:"dimensions,omitempty"`
	EncodingFormat *string `json:"encoding_format,omitempty"` // "float" | "base64"
	User           string  `json:"user,omitempty"`
	// ProviderOptions mirrors the chat-request field; lets clients control
	// Vercel AI Gateway routing for embeddings too. Other providers ignore.
	ProviderOptions any `json:"providerOptions,omitempty"`
}

// EmbeddingResponse is the unified embeddings response.
type EmbeddingResponse struct {
	Object string          `json:"object"` // "list"
	Data   []EmbeddingItem `json:"data"`
	Model  string          `json:"model"`
	Usage  *Usage          `json:"usage,omitempty"` // only PromptTokens / TotalTokens populated
}

// EmbeddingItem is a single embedding result.
type EmbeddingItem struct {
	Object    string `json:"object"` // "embedding"
	Index     int    `json:"index"`
	Embedding any    `json:"embedding"` // []float32 | string (base64) depending on EncodingFormat
}

// RemoteModel represents a model fetched from a provider's API.
type RemoteModel struct {
	ModelID       string `json:"model_id"`
	DisplayName   string `json:"display_name"`
	ContextWindow int    `json:"context_window"`
}

// ExtractCachedTokens extracts cached token count from raw JSON response data.
// Handles OpenAI format (prompt_tokens_details.cached_tokens) and
// DeepSeek format (prompt_cache_hit_tokens).
func ExtractCachedTokens(data []byte) int {
	var raw struct {
		Usage *struct {
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.Usage == nil {
		return 0
	}
	if raw.Usage.PromptTokensDetails != nil && raw.Usage.PromptTokensDetails.CachedTokens > 0 {
		return raw.Usage.PromptTokensDetails.CachedTokens
	}
	return raw.Usage.PromptCacheHitTokens
}

// ExtractReasoningTokens extracts thinking/reasoning token count from OpenAI-compatible
// responses. Source path: usage.completion_tokens_details.reasoning_tokens (DeepSeek thinking,
// OpenAI o-series, and most compatible providers).
func ExtractReasoningTokens(data []byte) int {
	var raw struct {
		Usage *struct {
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.Usage == nil || raw.Usage.CompletionTokensDetails == nil {
		return 0
	}
	return raw.Usage.CompletionTokensDetails.ReasoningTokens
}

// NormalizeUsage populates normalized usage aliases from provider-specific fields.
// Provider-native fields (prompt_cache_hit_tokens, prompt_tokens_details, etc.) are
// preserved so the original upstream information remains visible in the response.
func NormalizeUsage(usage *Usage) {
	if usage == nil {
		return
	}
	if usage.CachedTokens == 0 {
		switch {
		case usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0:
			usage.CachedTokens = usage.PromptTokensDetails.CachedTokens
		case usage.PromptCacheHitTokens > 0:
			usage.CachedTokens = usage.PromptCacheHitTokens
		}
	}
	if usage.ReasoningTokens == 0 && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.ReasoningTokens > 0 {
		usage.ReasoningTokens = usage.CompletionTokensDetails.ReasoningTokens
	}
	if usage.RequestCount == 0 {
		usage.RequestCount = 1
	}
}

// Ensure io.EOF is accessible
var _ = io.EOF
