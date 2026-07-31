// Package anthropic defines the native JSON protocol used by Anthropic's
// Messages API. It contains no HTTP implementation and depends only on the Go
// standard library.
package anthropic

import "encoding/json"

// Role is a Messages API conversational role.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// MessageRequest is the JSON body accepted by POST /v1/messages.
// Header-only configuration such as anthropic-version and anthropic-beta is
// represented by RequestOption instead.
type MessageRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int64           `json:"max_tokens"`
	Messages      []MessageParam  `json:"messages"`
	System        *System         `json:"system,omitempty"`
	CacheControl  *CacheControl   `json:"cache_control,omitempty"`
	Container     json.RawMessage `json:"container,omitempty"`
	InferenceGeo  *string         `json:"inference_geo,omitempty"`
	Metadata      *Metadata       `json:"metadata,omitempty"`
	OutputConfig  *OutputConfig   `json:"output_config,omitempty"`
	ServiceTier   *string         `json:"service_tier,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        *bool           `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	Thinking      *ThinkingConfig `json:"thinking,omitempty"`
	ToolChoice    *ToolChoice     `json:"tool_choice,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	TopK          *int64          `json:"top_k,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	ExtraFields   ExtraFields     `json:"-"`
}

var messageRequestFields = []string{
	"model", "max_tokens", "messages", "system", "cache_control", "container",
	"inference_geo", "metadata", "output_config", "service_tier", "stop_sequences",
	"stream", "temperature", "thinking", "tool_choice", "tools", "top_k", "top_p",
}

func (request MessageRequest) MarshalJSON() ([]byte, error) {
	type wire MessageRequest
	return marshalWithExtra(wire(request), request.ExtraFields, messageRequestFields...)
}

func (request *MessageRequest) UnmarshalJSON(data []byte) error {
	type wire MessageRequest
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, messageRequestFields...)
	if err != nil {
		return err
	}
	*request = MessageRequest(decoded)
	request.ExtraFields = extra
	return nil
}

// MessageParam is one conversational turn. Content retains whether the caller
// used Anthropic's string shorthand or the content-block array form.
type MessageParam struct {
	Role        Role        `json:"role"`
	Content     Content     `json:"content"`
	ExtraFields ExtraFields `json:"-"`
}

func (message MessageParam) MarshalJSON() ([]byte, error) {
	type wire MessageParam
	return marshalWithExtra(wire(message), message.ExtraFields, "role", "content")
}

func (message *MessageParam) UnmarshalJSON(data []byte) error {
	type wire MessageParam
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "role", "content")
	if err != nil {
		return err
	}
	*message = MessageParam(decoded)
	message.ExtraFields = extra
	return nil
}

// MessageResponse is Anthropic's native Message response. RequestID is filled
// from the response header by a transport and is deliberately not JSON data.
type MessageResponse struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Role         Role            `json:"role"`
	Model        string          `json:"model"`
	Content      []ContentBlock  `json:"content"`
	Container    json.RawMessage `json:"container"`
	StopReason   *StopReason     `json:"stop_reason"`
	StopSequence *string         `json:"stop_sequence"`
	StopDetails  *StopDetails    `json:"stop_details"`
	Usage        Usage           `json:"usage"`
	ExtraFields  ExtraFields     `json:"-"`
	RequestID    string          `json:"-"`
}

// Response is a concise alias for MessageResponse.
type Response = MessageResponse

var messageResponseFields = []string{
	"id", "type", "role", "model", "content", "container", "stop_reason",
	"stop_sequence", "stop_details", "usage",
}

func (response MessageResponse) MarshalJSON() ([]byte, error) {
	type wire MessageResponse
	return marshalWithExtra(wire(response), response.ExtraFields, messageResponseFields...)
}

func (response *MessageResponse) UnmarshalJSON(data []byte) error {
	type wire MessageResponse
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, messageResponseFields...)
	if err != nil {
		return err
	}
	requestID := response.RequestID
	*response = MessageResponse(decoded)
	response.ExtraFields = extra
	response.RequestID = requestID
	return nil
}

// StopReason explains why model generation ended. It is an open string type so
// new Anthropic reasons remain decodable before this package is updated.
type StopReason string

const (
	StopReasonEndTurn                    StopReason = "end_turn"
	StopReasonMaxTokens                  StopReason = "max_tokens"
	StopReasonStopSequence               StopReason = "stop_sequence"
	StopReasonToolUse                    StopReason = "tool_use"
	StopReasonPauseTurn                  StopReason = "pause_turn"
	StopReasonRefusal                    StopReason = "refusal"
	StopReasonModelContextWindowExceeded StopReason = "model_context_window_exceeded"
)

// StopDetails contains structured stop information, currently used for
// refusals. Type and category remain open strings for forward compatibility.
type StopDetails struct {
	Type        string      `json:"type"`
	Category    *string     `json:"category,omitempty"`
	Explanation *string     `json:"explanation,omitempty"`
	ExtraFields ExtraFields `json:"-"`
}

func (details StopDetails) MarshalJSON() ([]byte, error) {
	type wire StopDetails
	return marshalWithExtra(wire(details), details.ExtraFields, "type", "category", "explanation")
}

func (details *StopDetails) UnmarshalJSON(data []byte) error {
	type wire StopDetails
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "category", "explanation")
	if err != nil {
		return err
	}
	*details = StopDetails(decoded)
	details.ExtraFields = extra
	return nil
}

// CacheControl marks an Anthropic prompt-cache breakpoint.
type CacheControl struct {
	Type        string      `json:"type"`
	TTL         *string     `json:"ttl,omitempty"`
	ExtraFields ExtraFields `json:"-"`
}

func (control CacheControl) MarshalJSON() ([]byte, error) {
	type wire CacheControl
	return marshalWithExtra(wire(control), control.ExtraFields, "type", "ttl")
}

func (control *CacheControl) UnmarshalJSON(data []byte) error {
	type wire CacheControl
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "ttl")
	if err != nil {
		return err
	}
	*control = CacheControl(decoded)
	control.ExtraFields = extra
	return nil
}

// Metadata contains request attribution data.
type Metadata struct {
	UserID      *string     `json:"user_id,omitempty"`
	ExtraFields ExtraFields `json:"-"`
}

func (metadata Metadata) MarshalJSON() ([]byte, error) {
	type wire Metadata
	return marshalWithExtra(wire(metadata), metadata.ExtraFields, "user_id")
}

func (metadata *Metadata) UnmarshalJSON(data []byte) error {
	type wire Metadata
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "user_id")
	if err != nil {
		return err
	}
	*metadata = Metadata(decoded)
	metadata.ExtraFields = extra
	return nil
}

// ThinkingConfig configures disabled, manual (enabled), or adaptive thinking.
// BudgetTokens is used with type "enabled"; Display is supported by current
// enabled and adaptive modes.
type ThinkingConfig struct {
	Type         string      `json:"type"`
	BudgetTokens *int64      `json:"budget_tokens,omitempty"`
	Display      *string     `json:"display,omitempty"`
	ExtraFields  ExtraFields `json:"-"`
}

func (config ThinkingConfig) MarshalJSON() ([]byte, error) {
	type wire ThinkingConfig
	return marshalWithExtra(wire(config), config.ExtraFields, "type", "budget_tokens", "display")
}

func (config *ThinkingConfig) UnmarshalJSON(data []byte) error {
	type wire ThinkingConfig
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "budget_tokens", "display")
	if err != nil {
		return err
	}
	*config = ThinkingConfig(decoded)
	config.ExtraFields = extra
	return nil
}

// OutputConfig configures effort and structured JSON output.
type OutputConfig struct {
	Effort      *string           `json:"effort,omitempty"`
	Format      *JSONOutputFormat `json:"format,omitempty"`
	ExtraFields ExtraFields       `json:"-"`
}

func (config OutputConfig) MarshalJSON() ([]byte, error) {
	type wire OutputConfig
	return marshalWithExtra(wire(config), config.ExtraFields, "effort", "format")
}

func (config *OutputConfig) UnmarshalJSON(data []byte) error {
	type wire OutputConfig
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "effort", "format")
	if err != nil {
		return err
	}
	*config = OutputConfig(decoded)
	config.ExtraFields = extra
	return nil
}

// JSONOutputFormat requests an output matching Schema.
type JSONOutputFormat struct {
	Type        string          `json:"type"`
	Schema      json.RawMessage `json:"schema"`
	ExtraFields ExtraFields     `json:"-"`
}

func (format JSONOutputFormat) MarshalJSON() ([]byte, error) {
	type wire JSONOutputFormat
	return marshalWithExtra(wire(format), format.ExtraFields, "type", "schema")
}

func (format *JSONOutputFormat) UnmarshalJSON(data []byte) error {
	type wire JSONOutputFormat
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "schema")
	if err != nil {
		return err
	}
	*format = JSONOutputFormat(decoded)
	format.Schema = cloneRaw(format.Schema)
	format.ExtraFields = extra
	return nil
}

// Tool is a custom or server-side tool definition. InputSchema is raw JSON to
// retain the full JSON Schema vocabulary and exact number representation.
// ExtraFields also makes new built-in tool versions usable without a package
// release.
type Tool struct {
	Type           string            `json:"type,omitempty"`
	Name           string            `json:"name"`
	Description    *string           `json:"description,omitempty"`
	InputSchema    json.RawMessage   `json:"input_schema,omitempty"`
	Strict         *bool             `json:"strict,omitempty"`
	CacheControl   *CacheControl     `json:"cache_control,omitempty"`
	DeferLoading   *bool             `json:"defer_loading,omitempty"`
	AllowedCallers []string          `json:"allowed_callers,omitempty"`
	InputExamples  []json.RawMessage `json:"input_examples,omitempty"`
	ExtraFields    ExtraFields       `json:"-"`
}

var toolFields = []string{
	"type", "name", "description", "input_schema", "strict", "cache_control",
	"defer_loading", "allowed_callers", "input_examples",
}

func (tool Tool) MarshalJSON() ([]byte, error) {
	type wire Tool
	return marshalWithExtra(wire(tool), tool.ExtraFields, toolFields...)
}

func (tool *Tool) UnmarshalJSON(data []byte) error {
	type wire Tool
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, toolFields...)
	if err != nil {
		return err
	}
	*tool = Tool(decoded)
	tool.InputSchema = cloneRaw(tool.InputSchema)
	tool.ExtraFields = extra
	return nil
}

// ToolChoice selects automatic, any, none, or a named tool.
type ToolChoice struct {
	Type                   string      `json:"type"`
	Name                   *string     `json:"name,omitempty"`
	DisableParallelToolUse *bool       `json:"disable_parallel_tool_use,omitempty"`
	ExtraFields            ExtraFields `json:"-"`
}

func (choice ToolChoice) MarshalJSON() ([]byte, error) {
	type wire ToolChoice
	return marshalWithExtra(wire(choice), choice.ExtraFields, "type", "name", "disable_parallel_tool_use")
}

func (choice *ToolChoice) UnmarshalJSON(data []byte) error {
	type wire ToolChoice
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "name", "disable_parallel_tool_use")
	if err != nil {
		return err
	}
	*choice = ToolChoice(decoded)
	choice.ExtraFields = extra
	return nil
}

// Usage is Anthropic's native token and server-tool accounting record.
type Usage struct {
	InputTokens              int64                `json:"input_tokens"`
	OutputTokens             int64                `json:"output_tokens"`
	CacheCreationInputTokens *int64               `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64               `json:"cache_read_input_tokens"`
	CacheCreation            *CacheCreation       `json:"cache_creation"`
	InferenceGeo             *string              `json:"inference_geo"`
	OutputTokensDetails      *OutputTokensDetails `json:"output_tokens_details"`
	ServerToolUse            *ServerToolUsage     `json:"server_tool_use"`
	ServiceTier              *string              `json:"service_tier"`
	ExtraFields              ExtraFields          `json:"-"`
}

var usageFields = []string{
	"input_tokens", "output_tokens", "cache_creation_input_tokens",
	"cache_read_input_tokens", "cache_creation", "inference_geo",
	"output_tokens_details", "server_tool_use", "service_tier",
}

func (usage Usage) MarshalJSON() ([]byte, error) {
	type wire Usage
	return marshalWithExtra(wire(usage), usage.ExtraFields, usageFields...)
}

func (usage *Usage) UnmarshalJSON(data []byte) error {
	type wire Usage
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, usageFields...)
	if err != nil {
		return err
	}
	*usage = Usage(decoded)
	usage.ExtraFields = extra
	return nil
}

type CacheCreation struct {
	Ephemeral1hInputTokens int64       `json:"ephemeral_1h_input_tokens"`
	Ephemeral5mInputTokens int64       `json:"ephemeral_5m_input_tokens"`
	ExtraFields            ExtraFields `json:"-"`
}

func (creation CacheCreation) MarshalJSON() ([]byte, error) {
	type wire CacheCreation
	return marshalWithExtra(wire(creation), creation.ExtraFields, "ephemeral_1h_input_tokens", "ephemeral_5m_input_tokens")
}

func (creation *CacheCreation) UnmarshalJSON(data []byte) error {
	type wire CacheCreation
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "ephemeral_1h_input_tokens", "ephemeral_5m_input_tokens")
	if err != nil {
		return err
	}
	*creation = CacheCreation(decoded)
	creation.ExtraFields = extra
	return nil
}

type OutputTokensDetails struct {
	ThinkingTokens int64       `json:"thinking_tokens"`
	ExtraFields    ExtraFields `json:"-"`
}

func (details OutputTokensDetails) MarshalJSON() ([]byte, error) {
	type wire OutputTokensDetails
	return marshalWithExtra(wire(details), details.ExtraFields, "thinking_tokens")
}

func (details *OutputTokensDetails) UnmarshalJSON(data []byte) error {
	type wire OutputTokensDetails
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "thinking_tokens")
	if err != nil {
		return err
	}
	*details = OutputTokensDetails(decoded)
	details.ExtraFields = extra
	return nil
}

type ServerToolUsage struct {
	WebFetchRequests  int64       `json:"web_fetch_requests"`
	WebSearchRequests int64       `json:"web_search_requests"`
	ExtraFields       ExtraFields `json:"-"`
}

func (usage ServerToolUsage) MarshalJSON() ([]byte, error) {
	type wire ServerToolUsage
	return marshalWithExtra(wire(usage), usage.ExtraFields, "web_fetch_requests", "web_search_requests")
}

func (usage *ServerToolUsage) UnmarshalJSON(data []byte) error {
	type wire ServerToolUsage
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "web_fetch_requests", "web_search_requests")
	if err != nil {
		return err
	}
	*usage = ServerToolUsage(decoded)
	usage.ExtraFields = extra
	return nil
}

// TokenCountRequest is the body accepted by POST /v1/messages/count_tokens.
// It intentionally has no max_tokens field because counting does not generate
// a Message.
type TokenCountRequest struct {
	Model        string          `json:"model"`
	Messages     []MessageParam  `json:"messages"`
	System       *System         `json:"system,omitempty"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
	OutputConfig *OutputConfig   `json:"output_config,omitempty"`
	Thinking     *ThinkingConfig `json:"thinking,omitempty"`
	ToolChoice   *ToolChoice     `json:"tool_choice,omitempty"`
	Tools        []Tool          `json:"tools,omitempty"`
	ExtraFields  ExtraFields     `json:"-"`
}

var tokenCountRequestFields = []string{
	"model", "messages", "system", "cache_control", "output_config", "thinking", "tool_choice", "tools",
}

func (request TokenCountRequest) MarshalJSON() ([]byte, error) {
	type wire TokenCountRequest
	return marshalWithExtra(wire(request), request.ExtraFields, tokenCountRequestFields...)
}

func (request *TokenCountRequest) UnmarshalJSON(data []byte) error {
	type wire TokenCountRequest
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, tokenCountRequestFields...)
	if err != nil {
		return err
	}
	*request = TokenCountRequest(decoded)
	request.ExtraFields = extra
	return nil
}

// TokenCountResponse is returned by /v1/messages/count_tokens. RequestID is
// transport metadata and is not part of the JSON object.
type TokenCountResponse struct {
	InputTokens int64       `json:"input_tokens"`
	ExtraFields ExtraFields `json:"-"`
	RequestID   string      `json:"-"`
}

func (response TokenCountResponse) MarshalJSON() ([]byte, error) {
	type wire TokenCountResponse
	return marshalWithExtra(wire(response), response.ExtraFields, "input_tokens")
}

func (response *TokenCountResponse) UnmarshalJSON(data []byte) error {
	type wire TokenCountResponse
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "input_tokens")
	if err != nil {
		return err
	}
	requestID := response.RequestID
	*response = TokenCountResponse(decoded)
	response.ExtraFields = extra
	response.RequestID = requestID
	return nil
}
