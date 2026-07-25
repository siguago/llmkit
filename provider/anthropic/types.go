package anthropic

import "encoding/json"

// jsonMarshal indirects through encoding/json — separated out so contentBlock's
// custom MarshalJSON doesn't need to import the package directly inside the
// function (keeps the type file's imports minimal/grouped).
var jsonMarshal = json.Marshal

// Anthropic API request/response types

type request struct {
	Model         string    `json:"model"`
	Messages      []message `json:"messages"`
	System        any       `json:"system,omitempty"` // string or []systemBlock
	MaxTokens     int       `json:"max_tokens"`
	Temperature   *float64  `json:"temperature,omitempty"`
	TopP          *float64  `json:"top_p,omitempty"`
	TopK          *int      `json:"top_k,omitempty"`
	StopSequences []string  `json:"stop_sequences,omitempty"`
	Stream        bool      `json:"stream,omitempty"`
	// Tools is `[]any` rather than `[]anthropicTool` so the array can hold a
	// mix of function tools (anthropicTool) and Anthropic built-in tool
	// definitions (raw map[string]any forwarded through ExtraTools, e.g.
	// {"type":"web_search_20250305","name":"web_search","max_uses":5}).
	Tools      []any             `json:"tools,omitempty"`
	ToolChoice *toolChoiceConfig `json:"tool_choice,omitempty"`
	Thinking   *thinkingConfig   `json:"thinking,omitempty"`
	// ServiceTier: "auto" or "standard_only" for Claude 3.5+. Optional.
	ServiceTier string `json:"service_tier,omitempty"`
	// Metadata: opaque object — typically {"user_id": "..."} for abuse signal.
	Metadata any `json:"metadata,omitempty"`
	// Container references a previous code_execution sandbox by descriptor.
	// Forwarded as-is from the gateway's unified ChatCompletionRequest.Container.
	Container any `json:"container,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []contentBlock
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Tool use fields
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
	// Tool result fields
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"` // string or nested blocks
	// Image fields
	Source *imageSource `json:"source,omitempty"`
	// Thinking fields
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"` // opaque integrity proof for thinking blocks
	// Data is the opaque payload on `redacted_thinking` blocks. Must be echoed
	// back verbatim on multi-turn so the model retains reasoning context.
	Data string `json:"data,omitempty"`
	// Prompt caching breakpoint. Up to 4 per request; the gateway forwards
	// whatever shape the client sent (typically {"type":"ephemeral"}).
	CacheControl any `json:"cache_control,omitempty"`
	// Raw, when non-nil, takes precedence over all other fields and is what
	// gets serialized. Used to round-trip provider-native block types
	// (server_tool_use, web_search_tool_result, etc.) verbatim from the
	// previous assistant turn — the gateway can't reconstruct their full
	// shape from the OpenAI envelope, so it preserves the original JSON
	// object as-is.
	Raw map[string]any `json:"-"`
}

// MarshalJSON implements custom marshaling so a contentBlock with a non-nil
// Raw payload serializes as that raw object (provider-native turn blocks),
// while normal blocks fall through to the standard struct encoding.
func (b contentBlock) MarshalJSON() ([]byte, error) {
	if b.Raw != nil {
		return jsonMarshal(b.Raw)
	}
	type alias contentBlock
	return jsonMarshal(alias(b))
}

type imageSource struct {
	Type      string `json:"type"`                 // "base64" or "url"
	MediaType string `json:"media_type,omitempty"` // e.g. "image/png"
	Data      string `json:"data,omitempty"`       // base64 data
	URL       string `json:"url,omitempty"`        // URL for url type
}

type anthropicTool struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	InputSchema  any    `json:"input_schema"`
	CacheControl any    `json:"cache_control,omitempty"`
}

type toolChoiceConfig struct {
	Type string `json:"type"`           // "auto", "any", "none", "tool"
	Name string `json:"name,omitempty"` // only for type="tool"
	// DisableParallelToolUse forces the model to emit at most one tool_use per
	// turn. Mirrors OpenAI's parallel_tool_calls=false. Anthropic accepts this
	// flag on auto/any/tool (not none).
	DisableParallelToolUse bool `json:"disable_parallel_tool_use,omitempty"`
}

type thinkingConfig struct {
	Type         string `json:"type"`              // "enabled" or "disabled"
	BudgetTokens int    `json:"budget_tokens"`     // required when enabled
	Display      string `json:"display,omitempty"` // "summarized" (default) or "omitted"
}

type response struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Content    []responseBlock `json:"content"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Usage      responseUsage   `json:"usage"`
	// Container is the code_execution sandbox descriptor returned when the
	// model used the code_execution tool. Pass it back as request.container
	// on the next turn to resume the same sandbox.
	Container any `json:"container,omitempty"`
}

type responseBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
	// Thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"` // present on completed thinking blocks
	// Data is set on `redacted_thinking` blocks (opaque encrypted payload).
	Data string `json:"data,omitempty"`
	// Citations is set on `text` blocks emitted by built-in server tools
	// (web_search, etc.). Each entry is a web_search_result_location or
	// document_block_location object; the gateway flattens them into
	// OpenAI-compat message.annotations.
	Citations []citation `json:"citations,omitempty"`
}

// citation captures Anthropic's citation object shape. Most fields are common
// across web_search_result_location, page_location, char_location, etc., though
// only the location-bearing fields differ per type.
type citation struct {
	Type           string `json:"type,omitempty"`            // "web_search_result_location" / "char_location" / etc
	URL            string `json:"url,omitempty"`             // web_search citations
	Title          string `json:"title,omitempty"`           // web_search citations
	EncryptedIndex string `json:"encrypted_index,omitempty"` // opaque; required to round-trip multi-turn
	CitedText      string `json:"cited_text,omitempty"`
	// Document-citation locators (kept opaque so we don't break when Anthropic
	// adds new citation types). These survive round-tripping intact.
	DocumentIndex  *int `json:"document_index,omitempty"`
	StartCharIndex *int `json:"start_char_index,omitempty"`
	EndCharIndex   *int `json:"end_char_index,omitempty"`
	StartPageNum   *int `json:"start_page_number,omitempty"`
	EndPageNum     *int `json:"end_page_number,omitempty"`
	StartBlockIdx  *int `json:"start_block_index,omitempty"`
	EndBlockIdx    *int `json:"end_block_index,omitempty"`
}

type responseUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// totalPromptTokens sums all prompt-side tokens that Anthropic bills as input.
// Anthropic returns input_tokens excluding cached portions, so we must add cache
// reads and cache creation to obtain the full prompt size that clients expect
// in OpenAI-compatible accounting (prompt_tokens + completion_tokens = total).
func (u responseUsage) totalPromptTokens() int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// Streaming event types

type streamEvent struct {
	Type string `json:"type"`
}

type messageStartEvent struct {
	Type    string          `json:"type"`
	Message messageStartMsg `json:"message"`
}

type messageStartMsg struct {
	ID    string        `json:"id"`
	Model string        `json:"model"`
	Usage responseUsage `json:"usage"`
}

type contentBlockStartEvent struct {
	Type         string            `json:"type"`
	Index        int               `json:"index"`
	ContentBlock contentBlockStart `json:"content_block"`
}

type contentBlockStart struct {
	Type string `json:"type"` // "text", "tool_use", "thinking", "redacted_thinking", "server_tool_use", "web_search_tool_result", ...
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// Data is set on `redacted_thinking` start events — the entire block ships
	// in one piece (no incremental deltas), so capturing it here is sufficient.
	Data string `json:"data,omitempty"`
	// ToolUseID is set on server-side tool result blocks
	// (web_search_tool_result, code_execution_tool_result) — references the
	// server_tool_use block that produced this result.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Content is set on result blocks (web_search_tool_result etc.) where the
	// whole nested content payload arrives in the start event itself (no
	// deltas). For server_tool_use blocks Content is empty — their input
	// arrives as input_json_delta chunks.
	Content any `json:"content,omitempty"`
}

type contentBlockDeltaEvent struct {
	Type  string     `json:"type"`
	Index int        `json:"index"`
	Delta deltaBlock `json:"delta"`
}

type deltaBlock struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"` // for input_json_delta
	Thinking    string `json:"thinking,omitempty"`     // for thinking_delta
	Signature   string `json:"signature,omitempty"`    // for signature_delta — opaque integrity proof
	// Citation is the inline citation object delivered by `citations_delta`.
	// Decoded directly into the same shape used in non-streaming responses so
	// the gateway can flatten it into OpenAI-compat annotations consistently.
	Citation *citation `json:"citation,omitempty"`
}

type messageDeltaEvent struct {
	Type  string             `json:"type"`
	Delta messageDelta       `json:"delta"`
	Usage *messageDeltaUsage `json:"usage,omitempty"`
}

type messageDelta struct {
	StopReason string `json:"stop_reason"`
}

// messageDeltaUsage carries the final usage tally emitted on the message_delta
// event. Anthropic re-states all prompt-side counters here (not just the output
// tokens) so the gateway can reconcile cache writes/reads that only become known
// at message completion. See message_delta example in:
// https://platform.claude.com/docs/en/api/messages-streaming
type messageDeltaUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u messageDeltaUsage) totalPromptTokens() int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}
