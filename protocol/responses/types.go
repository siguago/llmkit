package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	StatusQueued     = "queued"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusIncomplete = "incomplete"
	StatusCancelled  = "cancelled"
)

// CreateRequest is the JSON body for POST /v1/responses.
//
// ExtraFields is intended for newly introduced API fields. A key that matches
// any first-class field is rejected instead of silently overriding it.
type CreateRequest struct {
	Model                string              `json:"model,omitempty"`
	Input                Input               `json:"input"`
	Instructions         *string             `json:"instructions,omitempty"`
	Include              []string            `json:"include,omitempty"`
	Metadata             map[string]string   `json:"metadata,omitempty"`
	Store                *bool               `json:"store,omitempty"`
	Background           *bool               `json:"background,omitempty"`
	ServiceTier          string              `json:"service_tier,omitempty"`
	PreviousResponseID   *string             `json:"previous_response_id,omitempty"`
	MaxOutputTokens      *int                `json:"max_output_tokens,omitempty"`
	MaxToolCalls         *int                `json:"max_tool_calls,omitempty"`
	Tools                []Tool              `json:"tools,omitempty"`
	ToolChoice           json.RawMessage     `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool               `json:"parallel_tool_calls,omitempty"`
	Reasoning            *ReasoningConfig    `json:"reasoning,omitempty"`
	Text                 *TextConfig         `json:"text,omitempty"`
	Temperature          *float64            `json:"temperature,omitempty"`
	TopLogprobs          *int                `json:"top_logprobs,omitempty"`
	TopP                 *float64            `json:"top_p,omitempty"`
	Truncation           string              `json:"truncation,omitempty"`
	Prompt               *Prompt             `json:"prompt,omitempty"`
	PromptCacheKey       string              `json:"prompt_cache_key,omitempty"`
	PromptCacheOptions   *PromptCacheOptions `json:"prompt_cache_options,omitempty"`
	PromptCacheRetention string              `json:"prompt_cache_retention,omitempty"`
	SafetyIdentifier     string              `json:"safety_identifier,omitempty"`
	User                 string              `json:"user,omitempty"`
	Stream               *bool               `json:"stream,omitempty"`
	StreamOptions        *StreamOptions      `json:"stream_options,omitempty"`
	Conversation         json.RawMessage     `json:"conversation,omitempty"`
	ContextManagement    json.RawMessage     `json:"context_management,omitempty"`
	Moderation           json.RawMessage     `json:"moderation,omitempty"`
	ExtraFields          ExtraFields         `json:"-"`
}

var createRequestFields = reservedFields(
	"model", "input", "instructions", "include", "metadata", "store", "background",
	"service_tier", "previous_response_id", "max_output_tokens", "max_tool_calls",
	"tools", "tool_choice", "parallel_tool_calls", "reasoning", "text", "temperature",
	"top_logprobs", "top_p", "truncation", "prompt", "prompt_cache_key", "prompt_cache_options",
	"prompt_cache_retention", "safety_identifier", "user", "stream", "stream_options",
	"conversation", "context_management", "moderation",
)

func reservedFields(names ...string) map[string]struct{} {
	fields := make(map[string]struct{}, len(names))
	for _, name := range names {
		fields[name] = struct{}{}
	}
	return fields
}

// Validate verifies local union and extension invariants without applying
// server-side policy such as model availability.
func (r CreateRequest) Validate() error {
	if err := r.Input.validate(); err != nil {
		return err
	}
	if err := validateExtraFields(r.ExtraFields, createRequestFields); err != nil {
		return err
	}
	for index, tool := range r.Tools {
		if _, err := json.Marshal(tool); err != nil {
			return fmt.Errorf("responses: tools[%d]: %w", index, err)
		}
	}
	for name, raw := range map[string]json.RawMessage{
		"tool_choice": r.ToolChoice, "conversation": r.Conversation,
		"context_management": r.ContextManagement, "moderation": r.Moderation,
	} {
		if len(raw) > 0 && !json.Valid(raw) {
			return fmt.Errorf("responses: %s is not valid JSON", name)
		}
	}
	return nil
}

func (r CreateRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type plain CreateRequest
	if r.Input.isZero() {
		return marshalObjectOmittingFieldsWithExtra(plain(r), r.ExtraFields, createRequestFields, "input")
	}
	return marshalObjectWithExtra(plain(r), r.ExtraFields, createRequestFields)
}

func (r *CreateRequest) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("responses: cannot unmarshal create request into nil receiver")
	}
	if err := requireJSONObject(data, "create request"); err != nil {
		return err
	}
	type plain CreateRequest
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, createRequestFields)
	if err != nil {
		return err
	}
	*r = CreateRequest(decoded)
	r.ExtraFields = extra
	return nil
}

type ReasoningConfig struct {
	Effort          string      `json:"effort,omitempty"`
	Summary         string      `json:"summary,omitempty"`
	GenerateSummary string      `json:"generate_summary,omitempty"`
	Context         string      `json:"context,omitempty"`
	Mode            string      `json:"mode,omitempty"`
	ExtraFields     ExtraFields `json:"-"`

	// fieldPresence and nullFields retain nullable response members without
	// changing the convenient string fields used to construct requests.
	fieldPresence map[string]struct{} `json:"-"`
	nullFields    map[string]struct{} `json:"-"`
}

type TextConfig struct {
	Format      *TextFormat `json:"format,omitempty"`
	Verbosity   string      `json:"verbosity,omitempty"`
	ExtraFields ExtraFields `json:"-"`

	fieldPresence map[string]struct{} `json:"-"`
	nullFields    map[string]struct{} `json:"-"`
}

type TextFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	ExtraFields ExtraFields     `json:"-"`

	fieldPresence map[string]struct{} `json:"-"`
	nullFields    map[string]struct{} `json:"-"`
}

type Prompt struct {
	ID          string                     `json:"id"`
	Version     string                     `json:"version,omitempty"`
	Variables   map[string]json.RawMessage `json:"variables,omitempty"`
	ExtraFields ExtraFields                `json:"-"`

	fieldPresence map[string]struct{} `json:"-"`
	nullFields    map[string]struct{} `json:"-"`
}

type PromptCacheOptions struct {
	Mode        string      `json:"mode,omitempty"`
	TTL         string      `json:"ttl,omitempty"`
	ExtraFields ExtraFields `json:"-"`
}

type StreamOptions struct {
	IncludeObfuscation *bool       `json:"include_obfuscation,omitempty"`
	ExtraFields        ExtraFields `json:"-"`
}

var (
	reasoningConfigFields    = reservedFields("effort", "summary", "generate_summary", "context", "mode")
	textConfigFields         = reservedFields("format", "verbosity")
	textFormatFields         = reservedFields("type", "name", "description", "schema", "strict")
	promptFields             = reservedFields("id", "version", "variables")
	promptNullableFields     = reservedFields("version", "variables")
	promptCacheOptionsFields = reservedFields("mode", "ttl")
	streamOptionsFields      = reservedFields("include_obfuscation")
)

func (v ReasoningConfig) MarshalJSON() ([]byte, error) {
	type plain ReasoningConfig
	return marshalObjectPreservingFieldPresence(
		plain(v), v.ExtraFields, reasoningConfigFields, v.fieldPresence, v.nullFields,
		func(name string) (any, bool) {
			switch name {
			case "effort":
				return v.Effort, true
			case "summary":
				return v.Summary, true
			case "generate_summary":
				return v.GenerateSummary, true
			case "context":
				return v.Context, true
			case "mode":
				return v.Mode, true
			}
			return nil, false
		},
	)
}

func (v *ReasoningConfig) UnmarshalJSON(data []byte) error {
	type plain ReasoningConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, reasoningConfigFields)
	if err != nil {
		return err
	}
	*v = ReasoningConfig(decoded)
	v.ExtraFields = extra
	if err := captureFieldPresence(data, reasoningConfigFields, &v.fieldPresence, &v.nullFields); err != nil {
		return err
	}
	return nil
}

func (v TextConfig) MarshalJSON() ([]byte, error) {
	type plain TextConfig
	return marshalObjectPreservingFieldPresence(
		plain(v), v.ExtraFields, textConfigFields, v.fieldPresence, v.nullFields,
		func(name string) (any, bool) {
			switch name {
			case "format":
				return v.Format, true
			case "verbosity":
				return v.Verbosity, true
			}
			return nil, false
		},
	)
}

func (v *TextConfig) UnmarshalJSON(data []byte) error {
	type plain TextConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, textConfigFields)
	if err != nil {
		return err
	}
	*v = TextConfig(decoded)
	v.ExtraFields = extra
	if err := captureFieldPresence(data, textConfigFields, &v.fieldPresence, &v.nullFields); err != nil {
		return err
	}
	return nil
}

func marshalObjectPreservingFieldPresence(
	base any,
	extra ExtraFields,
	known, presence, nulls map[string]struct{},
	fieldValue func(string) (any, bool),
) ([]byte, error) {
	encodedBase, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	var fields ExtraFields
	if err := json.Unmarshal(encodedBase, &fields); err != nil {
		return nil, err
	}
	for name := range presence {
		if _, exists := fields[name]; exists {
			continue
		}
		if _, wasNull := nulls[name]; wasNull {
			fields[name] = json.RawMessage("null")
			continue
		}
		value, exists := fieldValue(name)
		if !exists {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("responses: marshal field %q: %w", name, err)
		}
		fields[name] = encoded
	}
	return marshalObjectWithExtra(fields, extra, known)
}

func marshalDiscriminatedObjectPreservingFieldPresence(
	base any,
	extra ExtraFields,
	canonical string,
	known, presence, nulls map[string]struct{},
	fieldValue func(string) (any, bool),
) ([]byte, error) {
	encoded, err := marshalObjectPreservingFieldPresence(
		base, extra, known, presence, nulls, fieldValue,
	)
	if err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(json.RawMessage(encoded), nil, canonical, known)
}

func captureFieldPresence(data []byte, known map[string]struct{}, presence, nulls *map[string]struct{}) error {
	var fields ExtraFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, raw := range fields {
		if _, exists := known[name]; !exists {
			continue
		}
		if *presence == nil {
			*presence = make(map[string]struct{})
		}
		(*presence)[name] = struct{}{}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if *nulls == nil {
				*nulls = make(map[string]struct{})
			}
			(*nulls)[name] = struct{}{}
		}
	}
	return nil
}

func (v TextFormat) MarshalJSON() ([]byte, error) {
	type plain TextFormat
	return marshalObjectPreservingFieldPresence(
		plain(v), v.ExtraFields, textFormatFields, v.fieldPresence, v.nullFields,
		func(name string) (any, bool) {
			switch name {
			case "type":
				return v.Type, true
			case "name":
				return v.Name, true
			case "description":
				return v.Description, true
			case "schema":
				return v.Schema, true
			case "strict":
				return v.Strict, true
			}
			return nil, false
		},
	)
}

func (v *TextFormat) UnmarshalJSON(data []byte) error {
	type plain TextFormat
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, textFormatFields)
	if err != nil {
		return err
	}
	*v = TextFormat(decoded)
	v.Schema = cloneRaw(v.Schema)
	v.ExtraFields = extra
	if err := captureFieldPresence(data, textFormatFields, &v.fieldPresence, &v.nullFields); err != nil {
		return err
	}
	return nil
}

func (v Prompt) MarshalJSON() ([]byte, error) {
	type plain Prompt
	return marshalObjectPreservingFieldPresence(
		plain(v), v.ExtraFields, promptFields, v.fieldPresence, v.nullFields,
		func(name string) (any, bool) {
			switch name {
			case "version":
				return v.Version, true
			case "variables":
				return v.Variables, true
			}
			return nil, false
		},
	)
}

func (v *Prompt) UnmarshalJSON(data []byte) error {
	type plain Prompt
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, promptFields)
	if err != nil {
		return err
	}
	*v = Prompt(decoded)
	v.ExtraFields = extra
	if err := captureFieldPresence(data, promptNullableFields, &v.fieldPresence, &v.nullFields); err != nil {
		return err
	}
	return nil
}

func (v PromptCacheOptions) MarshalJSON() ([]byte, error) {
	type plain PromptCacheOptions
	return marshalObjectWithExtra(plain(v), v.ExtraFields, promptCacheOptionsFields)
}

func (v *PromptCacheOptions) UnmarshalJSON(data []byte) error {
	type plain PromptCacheOptions
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, promptCacheOptionsFields)
	if err != nil {
		return err
	}
	*v = PromptCacheOptions(decoded)
	v.ExtraFields = extra
	return nil
}

func (v StreamOptions) MarshalJSON() ([]byte, error) {
	type plain StreamOptions
	return marshalObjectWithExtra(plain(v), v.ExtraFields, streamOptionsFields)
}

func (v *StreamOptions) UnmarshalJSON(data []byte) error {
	type plain StreamOptions
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, streamOptionsFields)
	if err != nil {
		return err
	}
	*v = StreamOptions(decoded)
	v.ExtraFields = extra
	return nil
}

// Tool is a tool-definition union. Function is typed; all other current and
// future built-in tools remain available byte-for-byte through Raw.
type Tool struct {
	Type     string          `json:"-"`
	Function *FunctionTool   `json:"-"`
	Raw      json.RawMessage `json:"-"`
}

type FunctionTool struct {
	Type        string          `json:"-"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	ExtraFields ExtraFields     `json:"-"`

	fieldPresence map[string]struct{} `json:"-"`
	nullFields    map[string]struct{} `json:"-"`
}

var (
	functionToolFields         = reservedFields("type", "name", "description", "parameters", "strict")
	functionToolNullableFields = reservedFields("description")
)

func NewFunctionTool(tool FunctionTool) Tool {
	tool.Type = "function"
	return Tool{Type: "function", Function: &tool}
}

func NewRawTool(raw json.RawMessage) (Tool, error) {
	if err := requireJSONObject(raw, "raw tool"); err != nil {
		return Tool{}, err
	}
	var tool Tool
	if err := tool.UnmarshalJSON(raw); err != nil {
		return Tool{}, err
	}
	return tool, nil
}

func (t Tool) MarshalJSON() ([]byte, error) {
	count := variantCount(t.Function != nil, len(t.Raw) > 0)
	if count != 1 {
		return nil, fmt.Errorf("%w: Tool has %d variants", ErrInvalidUnion, count)
	}
	if len(t.Raw) > 0 {
		if err := checkUnknownDiscriminator(t.Raw, t.Type); err != nil {
			return nil, err
		}
		if t.Type == "function" {
			return nil, fmt.Errorf("%w: known tool type %q cannot use Raw", ErrInvalidUnion, t.Type)
		}
		return cloneRaw(t.Raw), nil
	}
	if err := checkDiscriminator(t.Type, "function", "tool"); err != nil {
		return nil, err
	}
	return json.Marshal(t.Function)
}

func (t *Tool) UnmarshalJSON(data []byte) error {
	if err := requireJSONObject(data, "tool"); err != nil {
		return err
	}
	typ, err := discriminator(data)
	if err != nil {
		return err
	}
	*t = Tool{Type: typ}
	if typ == "function" {
		t.Function = new(FunctionTool)
		return json.Unmarshal(data, t.Function)
	}
	t.Raw = cloneRaw(data)
	return nil
}

func (t Tool) RawJSON() json.RawMessage { return cloneRaw(t.Raw) }

func (t FunctionTool) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(t.Type, "function", "FunctionTool"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObjectPreservingFieldPresence(struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
		Strict      *bool           `json:"strict,omitempty"`
	}{t.Name, t.Description, t.Parameters, t.Strict}, t.ExtraFields, "function", functionToolFields,
		t.fieldPresence, t.nullFields, func(name string) (any, bool) {
			if name == "description" {
				return t.Description, true
			}
			return nil, false
		})
}

func (t *FunctionTool) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
		Strict      *bool           `json:"strict,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, "function", "FunctionTool"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, functionToolFields)
	if err != nil {
		return err
	}
	*t = FunctionTool{Type: wire.Type, Name: wire.Name, Description: wire.Description, Parameters: cloneRaw(wire.Parameters), Strict: wire.Strict, ExtraFields: extra}
	if err := captureFieldPresence(data, functionToolNullableFields, &t.fieldPresence, &t.nullFields); err != nil {
		return err
	}
	return nil
}

type Response struct {
	ID                   string              `json:"id"`
	Object               string              `json:"object"`
	CreatedAt            float64             `json:"created_at"`
	Status               string              `json:"status"`
	CompletedAt          *float64            `json:"completed_at,omitempty"`
	Background           *bool               `json:"background,omitempty"`
	Error                *Error              `json:"error"`
	IncompleteDetails    *IncompleteDetails  `json:"incomplete_details"`
	Instructions         Instructions        `json:"instructions"`
	MaxOutputTokens      *int                `json:"max_output_tokens,omitempty"`
	MaxToolCalls         *int                `json:"max_tool_calls,omitempty"`
	Model                string              `json:"model"`
	Output               []Item              `json:"output"`
	OutputTextValue      string              `json:"output_text,omitempty"`
	ParallelToolCalls    bool                `json:"parallel_tool_calls"`
	PreviousResponseID   *string             `json:"previous_response_id,omitempty"`
	Reasoning            *ReasoningConfig    `json:"reasoning,omitempty"`
	Store                bool                `json:"store"`
	Temperature          *float64            `json:"temperature,omitempty"`
	TopLogprobs          *int                `json:"top_logprobs,omitempty"`
	Text                 *TextConfig         `json:"text,omitempty"`
	ToolChoice           json.RawMessage     `json:"tool_choice,omitempty"`
	Tools                []Tool              `json:"tools,omitempty"`
	TopP                 *float64            `json:"top_p,omitempty"`
	Truncation           string              `json:"truncation,omitempty"`
	Usage                *Usage              `json:"usage,omitempty"`
	Metadata             map[string]string   `json:"metadata,omitempty"`
	ServiceTier          string              `json:"service_tier,omitempty"`
	Conversation         json.RawMessage     `json:"conversation,omitempty"`
	ContextManagement    json.RawMessage     `json:"context_management,omitempty"`
	Moderation           json.RawMessage     `json:"moderation,omitempty"`
	Prompt               *Prompt             `json:"prompt,omitempty"`
	PromptCacheKey       string              `json:"prompt_cache_key,omitempty"`
	PromptCacheOptions   *PromptCacheOptions `json:"prompt_cache_options,omitempty"`
	PromptCacheRetention string              `json:"prompt_cache_retention,omitempty"`
	SafetyIdentifier     string              `json:"safety_identifier,omitempty"`
	User                 string              `json:"user,omitempty"`

	// RequestID is response-header metadata populated by a transport. It is not
	// part of the Responses JSON object.
	RequestID   string      `json:"-"`
	ExtraFields ExtraFields `json:"-"`

	// fieldPresence and nullFields retain the wire distinction between an
	// absent optional response field and one explicitly returned as null or a
	// zero value. They are populated by UnmarshalJSON and rebuilt by stream
	// accumulator clones.
	fieldPresence map[string]struct{} `json:"-"`
	nullFields    map[string]struct{} `json:"-"`
}

var responseFields = reservedFields(
	"id", "object", "created_at", "status", "completed_at", "background", "error",
	"incomplete_details", "instructions", "max_output_tokens", "max_tool_calls", "model",
	"output", "output_text", "parallel_tool_calls", "previous_response_id", "reasoning",
	"store", "temperature", "top_logprobs", "text", "tool_choice", "tools", "top_p",
	"truncation", "usage", "metadata", "service_tier", "conversation", "context_management",
	"moderation", "prompt", "prompt_cache_key", "prompt_cache_options",
	"prompt_cache_retention", "safety_identifier", "user",
)

func (r Response) MarshalJSON() ([]byte, error) {
	for index, item := range r.Output {
		if item.EasyMessage != nil {
			return nil, fmt.Errorf("%w: response output[%d] cannot contain a type-omitted easy input message", ErrInvalidUnion, index)
		}
	}
	type plain Response
	base, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	var fields ExtraFields
	if err := json.Unmarshal(base, &fields); err != nil {
		return nil, err
	}

	// These collections and configuration members are required by the
	// response schema. Normalize nil Go values to their JSON wire defaults
	// rather than emitting null or silently omitting the key.
	output := r.Output
	if output == nil {
		output = []Item{}
	}
	tools := r.Tools
	if tools == nil {
		tools = []Tool{}
	}
	metadata := r.Metadata
	toolChoice := r.ToolChoice
	if len(toolChoice) == 0 {
		toolChoice = json.RawMessage(`"auto"`)
	}
	for name, value := range map[string]any{
		"output":      output,
		"tools":       tools,
		"metadata":    metadata,
		"temperature": r.Temperature,
		"tool_choice": toolChoice,
		"top_p":       r.TopP,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("responses: marshal response field %q: %w", name, err)
		}
		fields[name] = encoded
	}
	// encoding/json's omitempty intentionally erases zero values. Restore keys
	// that were present on the upstream response, including explicit nulls and
	// empty strings/arrays/objects.
	for name := range r.fieldPresence {
		if _, exists := fields[name]; exists {
			continue
		}
		if _, wasNull := r.nullFields[name]; wasNull {
			fields[name] = json.RawMessage("null")
			continue
		}
		encoded, err := r.marshalKnownField(name)
		if err != nil {
			return nil, err
		}
		if encoded != nil {
			fields[name] = encoded
		}
	}
	return marshalObjectWithExtra(fields, r.ExtraFields, responseFields)
}

func (r *Response) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("responses: cannot unmarshal response into nil receiver")
	}
	if err := requireJSONObject(data, "response"); err != nil {
		return err
	}
	type plain Response
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, responseFields)
	if err != nil {
		return err
	}
	*r = Response(decoded)
	r.ExtraFields = extra
	var rawFields ExtraFields
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return err
	}
	for name, raw := range rawFields {
		if _, known := responseFields[name]; !known {
			continue
		}
		if r.fieldPresence == nil {
			r.fieldPresence = make(map[string]struct{})
		}
		r.fieldPresence[name] = struct{}{}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if r.nullFields == nil {
				r.nullFields = make(map[string]struct{})
			}
			r.nullFields[name] = struct{}{}
		}
	}
	return nil
}

func (r Response) marshalKnownField(name string) (json.RawMessage, error) {
	var value any
	switch name {
	case "id":
		value = r.ID
	case "object":
		value = r.Object
	case "created_at":
		value = r.CreatedAt
	case "status":
		value = r.Status
	case "completed_at":
		value = r.CompletedAt
	case "background":
		value = r.Background
	case "error":
		value = r.Error
	case "incomplete_details":
		value = r.IncompleteDetails
	case "instructions":
		value = r.Instructions
	case "max_output_tokens":
		value = r.MaxOutputTokens
	case "max_tool_calls":
		value = r.MaxToolCalls
	case "model":
		value = r.Model
	case "output":
		value = r.Output
	case "output_text":
		value = r.OutputTextValue
	case "parallel_tool_calls":
		value = r.ParallelToolCalls
	case "previous_response_id":
		value = r.PreviousResponseID
	case "reasoning":
		value = r.Reasoning
	case "store":
		value = r.Store
	case "temperature":
		value = r.Temperature
	case "top_logprobs":
		value = r.TopLogprobs
	case "text":
		value = r.Text
	case "tool_choice":
		if len(r.ToolChoice) == 0 {
			return json.RawMessage("null"), nil
		}
		return cloneRaw(r.ToolChoice), nil
	case "tools":
		value = r.Tools
	case "top_p":
		value = r.TopP
	case "truncation":
		value = r.Truncation
	case "usage":
		value = r.Usage
	case "metadata":
		value = r.Metadata
	case "service_tier":
		value = r.ServiceTier
	case "conversation":
		value = r.Conversation
	case "context_management":
		value = r.ContextManagement
	case "moderation":
		value = r.Moderation
	case "prompt":
		value = r.Prompt
	case "prompt_cache_key":
		value = r.PromptCacheKey
	case "prompt_cache_options":
		value = r.PromptCacheOptions
	case "prompt_cache_retention":
		value = r.PromptCacheRetention
	case "safety_identifier":
		value = r.SafetyIdentifier
	case "user":
		value = r.User
	default:
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("responses: marshal response field %q: %w", name, err)
	}
	return encoded, nil
}

type Error struct {
	Type        string      `json:"type,omitempty"`
	Code        string      `json:"code,omitempty"`
	Message     string      `json:"message"`
	Param       *string     `json:"param,omitempty"`
	ExtraFields ExtraFields `json:"-"`
}

var errorFields = reservedFields("type", "code", "message", "param")

func (e Error) MarshalJSON() ([]byte, error) {
	type plain Error
	return marshalObjectWithExtra(plain(e), e.ExtraFields, errorFields)
}

func (e *Error) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("responses: cannot unmarshal error into nil receiver")
	}
	type plain Error
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, errorFields)
	if err != nil {
		return err
	}
	*e = Error(decoded)
	e.ExtraFields = extra
	return nil
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type IncompleteDetails struct {
	Reason      string      `json:"reason"`
	ExtraFields ExtraFields `json:"-"`
}

var incompleteDetailsFields = reservedFields("reason")

func (d IncompleteDetails) MarshalJSON() ([]byte, error) {
	type plain IncompleteDetails
	return marshalObjectWithExtra(plain(d), d.ExtraFields, incompleteDetailsFields)
}

func (d *IncompleteDetails) UnmarshalJSON(data []byte) error {
	type plain IncompleteDetails
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, incompleteDetailsFields)
	if err != nil {
		return err
	}
	*d = IncompleteDetails(decoded)
	d.ExtraFields = extra
	return nil
}

type Usage struct {
	InputTokens         int64                `json:"input_tokens"`
	InputTokensDetails  *InputTokensDetails  `json:"input_tokens_details"`
	OutputTokens        int64                `json:"output_tokens"`
	OutputTokensDetails *OutputTokensDetails `json:"output_tokens_details"`
	TotalTokens         int64                `json:"total_tokens"`
	ExtraFields         ExtraFields          `json:"-"`
}

type InputTokensDetails struct {
	CachedTokens     int64       `json:"cached_tokens"`
	CacheWriteTokens int64       `json:"cache_write_tokens"`
	ExtraFields      ExtraFields `json:"-"`
}

type OutputTokensDetails struct {
	ReasoningTokens int64       `json:"reasoning_tokens"`
	ExtraFields     ExtraFields `json:"-"`
}

var (
	usageFields               = reservedFields("input_tokens", "input_tokens_details", "output_tokens", "output_tokens_details", "total_tokens")
	inputTokensDetailsFields  = reservedFields("cached_tokens", "cache_write_tokens")
	outputTokensDetailsFields = reservedFields("reasoning_tokens")
)

func (u Usage) MarshalJSON() ([]byte, error) {
	inputDetails := u.InputTokensDetails
	if inputDetails == nil {
		inputDetails = &InputTokensDetails{}
	}
	outputDetails := u.OutputTokensDetails
	if outputDetails == nil {
		outputDetails = &OutputTokensDetails{}
	}
	return marshalObjectWithExtra(struct {
		InputTokens         int64                `json:"input_tokens"`
		InputTokensDetails  *InputTokensDetails  `json:"input_tokens_details"`
		OutputTokens        int64                `json:"output_tokens"`
		OutputTokensDetails *OutputTokensDetails `json:"output_tokens_details"`
		TotalTokens         int64                `json:"total_tokens"`
	}{u.InputTokens, inputDetails, u.OutputTokens, outputDetails, u.TotalTokens}, u.ExtraFields, usageFields)
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	type plain Usage
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, usageFields)
	if err != nil {
		return err
	}
	*u = Usage(decoded)
	u.ExtraFields = extra
	return nil
}

func (d InputTokensDetails) MarshalJSON() ([]byte, error) {
	type plain InputTokensDetails
	return marshalObjectWithExtra(plain(d), d.ExtraFields, inputTokensDetailsFields)
}

func (d *InputTokensDetails) UnmarshalJSON(data []byte) error {
	type plain InputTokensDetails
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, inputTokensDetailsFields)
	if err != nil {
		return err
	}
	*d = InputTokensDetails(decoded)
	d.ExtraFields = extra
	return nil
}

func (d OutputTokensDetails) MarshalJSON() ([]byte, error) {
	type plain OutputTokensDetails
	return marshalObjectWithExtra(plain(d), d.ExtraFields, outputTokensDetailsFields)
}

func (d *OutputTokensDetails) UnmarshalJSON(data []byte) error {
	type plain OutputTokensDetails
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, outputTokensDetailsFields)
	if err != nil {
		return err
	}
	*d = OutputTokensDetails(decoded)
	d.ExtraFields = extra
	return nil
}

// OutputText concatenates output_text parts in output order. The server's
// optional output_text convenience field is used only when no typed part is
// present.
func (r *Response) OutputText() string {
	if r == nil {
		return ""
	}
	var out strings.Builder
	sawTypedText := false
	for _, item := range r.Output {
		if item.Message == nil {
			continue
		}
		if item.Message.Content.Text != nil {
			sawTypedText = true
			out.WriteString(*item.Message.Content.Text)
		}
		for _, part := range item.Message.Content.Parts {
			if part.OutputText != nil {
				sawTypedText = true
				out.WriteString(part.OutputText.Text)
			}
		}
	}
	if sawTypedText {
		return out.String()
	}
	return r.OutputTextValue
}

// FunctionCalls returns an owned list of function_call output items.
func (r *Response) FunctionCalls() []FunctionCall {
	if r == nil {
		return nil
	}
	var calls []FunctionCall
	for _, item := range r.Output {
		if item.FunctionCall != nil {
			call := *item.FunctionCall
			call.Caller = cloneRaw(call.Caller)
			call.ExtraFields = cloneExtraFields(call.ExtraFields)
			calls = append(calls, call)
		}
	}
	return calls
}

// RetrieveOptions controls GET /v1/responses/{id} query parameters.
type RetrieveOptions struct {
	Include []string
}

// ListInputItemsOptions controls pagination for a response's input items.
type ListInputItemsOptions struct {
	After   string
	Include []string
	Limit   *int
	Order   string
}

type InputItemList struct {
	Object      string      `json:"object"`
	Data        []Item      `json:"data"`
	FirstID     string      `json:"first_id,omitempty"`
	LastID      string      `json:"last_id,omitempty"`
	HasMore     bool        `json:"has_more"`
	RequestID   string      `json:"-"`
	ExtraFields ExtraFields `json:"-"`
}

var inputItemListFields = reservedFields("object", "data", "first_id", "last_id", "has_more")

func (p InputItemList) MarshalJSON() ([]byte, error) {
	type plain InputItemList
	return marshalObjectWithExtra(plain(p), p.ExtraFields, inputItemListFields)
}

func (p *InputItemList) UnmarshalJSON(data []byte) error {
	if err := requireJSONObject(data, "input item list"); err != nil {
		return err
	}
	type plain InputItemList
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, inputItemListFields)
	if err != nil {
		return err
	}
	*p = InputItemList(decoded)
	p.ExtraFields = extra
	return nil
}

type DeletedResponse struct {
	ID          string      `json:"id"`
	Object      string      `json:"object"`
	Deleted     bool        `json:"deleted"`
	RequestID   string      `json:"-"`
	ExtraFields ExtraFields `json:"-"`
}

var deletedResponseFields = reservedFields("id", "object", "deleted")

func (r DeletedResponse) MarshalJSON() ([]byte, error) {
	type plain DeletedResponse
	return marshalObjectWithExtra(plain(r), r.ExtraFields, deletedResponseFields)
}

func (r *DeletedResponse) UnmarshalJSON(data []byte) error {
	if err := requireJSONObject(data, "deleted response"); err != nil {
		return err
	}
	type plain DeletedResponse
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, deletedResponseFields)
	if err != nil {
		return err
	}
	*r = DeletedResponse(decoded)
	r.ExtraFields = extra
	return nil
}

// TokenCountRequest is the request body for POST /v1/responses/input_tokens.
type TokenCountRequest struct {
	Model              string           `json:"model,omitempty"`
	Input              Input            `json:"input"`
	Instructions       *string          `json:"instructions,omitempty"`
	Conversation       json.RawMessage  `json:"conversation,omitempty"`
	ParallelToolCalls  *bool            `json:"parallel_tool_calls,omitempty"`
	Personality        string           `json:"personality,omitempty"`
	PreviousResponseID *string          `json:"previous_response_id,omitempty"`
	Reasoning          *ReasoningConfig `json:"reasoning,omitempty"`
	Text               *TextConfig      `json:"text,omitempty"`
	ToolChoice         json.RawMessage  `json:"tool_choice,omitempty"`
	Tools              []Tool           `json:"tools,omitempty"`
	Truncation         string           `json:"truncation,omitempty"`
	ExtraFields        ExtraFields      `json:"-"`
}

var tokenCountRequestFields = reservedFields(
	"model", "input", "instructions", "conversation", "parallel_tool_calls",
	"personality", "previous_response_id", "reasoning", "text", "tool_choice", "tools", "truncation",
)

func (r TokenCountRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type plain TokenCountRequest
	if r.Input.isZero() {
		return marshalObjectOmittingFieldsWithExtra(plain(r), r.ExtraFields, tokenCountRequestFields, "input")
	}
	return marshalObjectWithExtra(plain(r), r.ExtraFields, tokenCountRequestFields)
}

// Validate verifies local token-count request union and extension invariants.
func (r TokenCountRequest) Validate() error {
	if err := r.Input.validate(); err != nil {
		return err
	}
	if err := validateExtraFields(r.ExtraFields, tokenCountRequestFields); err != nil {
		return err
	}
	for index, tool := range r.Tools {
		if _, err := json.Marshal(tool); err != nil {
			return fmt.Errorf("responses: tools[%d]: %w", index, err)
		}
	}
	for name, raw := range map[string]json.RawMessage{
		"conversation": r.Conversation, "tool_choice": r.ToolChoice,
	} {
		if len(raw) > 0 && !json.Valid(raw) {
			return fmt.Errorf("responses: %s is not valid JSON", name)
		}
	}
	return nil
}

func (r *TokenCountRequest) UnmarshalJSON(data []byte) error {
	if err := requireJSONObject(data, "token count request"); err != nil {
		return err
	}
	type plain TokenCountRequest
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, tokenCountRequestFields)
	if err != nil {
		return err
	}
	*r = TokenCountRequest(decoded)
	r.ExtraFields = extra
	return nil
}

type TokenCountResponse struct {
	Object      string      `json:"object"`
	InputTokens int64       `json:"input_tokens"`
	RequestID   string      `json:"-"`
	ExtraFields ExtraFields `json:"-"`
}

var tokenCountResponseFields = reservedFields("object", "input_tokens")

func (r TokenCountResponse) MarshalJSON() ([]byte, error) {
	type plain TokenCountResponse
	return marshalObjectWithExtra(plain(r), r.ExtraFields, tokenCountResponseFields)
}

func (r *TokenCountResponse) UnmarshalJSON(data []byte) error {
	if err := requireJSONObject(data, "token count response"); err != nil {
		return err
	}
	type plain TokenCountResponse
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, tokenCountResponseFields)
	if err != nil {
		return err
	}
	*r = TokenCountResponse(decoded)
	r.ExtraFields = extra
	return nil
}
