package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	ItemTypeMessage            = "message"
	ItemTypeReasoning          = "reasoning"
	ItemTypeFunctionCall       = "function_call"
	ItemTypeFunctionCallOutput = "function_call_output"
)

// Item is an input/output item union. The most common protocol variants are
// typed. Any other built-in tool call or future item type remains available in
// Raw and is round-tripped byte-for-byte.
type Item struct {
	Type               string              `json:"-"`
	EasyMessage        *EasyInputMessage   `json:"-"`
	Message            *Message            `json:"-"`
	Reasoning          *Reasoning          `json:"-"`
	FunctionCall       *FunctionCall       `json:"-"`
	FunctionCallOutput *FunctionCallOutput `json:"-"`
	Raw                json.RawMessage     `json:"-"`
}

// EasyInputMessage is the abbreviated input-message shape accepted inside a
// Responses input array. Unlike a regular Item, its type member is optional;
// this representation deliberately preserves the canonical omitted form.
// Decode it through Input rather than directly through Item so output-item
// unions keep their strict discriminator requirement.
type EasyInputMessage struct {
	Role        string         `json:"role"`
	Content     MessageContent `json:"content"`
	ExtraFields ExtraFields    `json:"-"`
}

type Message struct {
	Type        string         `json:"-"`
	ID          string         `json:"id,omitempty"`
	Role        string         `json:"role"`
	Status      string         `json:"status,omitempty"`
	Phase       string         `json:"phase,omitempty"`
	Content     MessageContent `json:"content"`
	ExtraFields ExtraFields    `json:"-"`
}

type Reasoning struct {
	Type             string        `json:"-"`
	ID               string        `json:"id,omitempty"`
	Status           string        `json:"status,omitempty"`
	Summary          []ContentPart `json:"summary,omitempty"`
	Content          []ContentPart `json:"content,omitempty"`
	EncryptedContent string        `json:"encrypted_content,omitempty"`
	ExtraFields      ExtraFields   `json:"-"`
}

type FunctionCall struct {
	Type        string          `json:"-"`
	ID          string          `json:"id,omitempty"`
	CallID      string          `json:"call_id"`
	Name        string          `json:"name"`
	Namespace   string          `json:"namespace,omitempty"`
	Arguments   string          `json:"arguments"`
	Status      string          `json:"status,omitempty"`
	Caller      json.RawMessage `json:"caller,omitempty"`
	ExtraFields ExtraFields     `json:"-"`
}

type FunctionCallOutput struct {
	Type        string          `json:"-"`
	ID          string          `json:"id,omitempty"`
	CallID      string          `json:"call_id"`
	Output      FunctionOutput  `json:"output"`
	Status      string          `json:"status,omitempty"`
	Caller      json.RawMessage `json:"caller,omitempty"`
	CreatedBy   string          `json:"created_by,omitempty"`
	ExtraFields ExtraFields     `json:"-"`
}

var (
	messageFields     = reservedFields("type", "id", "role", "status", "phase", "content")
	easyMessageFields = reservedFields("type", "role", "content")
	reasoningFields   = reservedFields(
		"type", "id", "status", "summary", "content", "encrypted_content",
	)
	functionCallFields = reservedFields(
		"type", "id", "call_id", "name", "namespace", "arguments", "status", "caller",
	)
	functionCallOutputFields = reservedFields(
		"type", "id", "call_id", "output", "status", "caller", "created_by",
	)
)

func NewMessageItem(message Message) Item {
	message.Type = ItemTypeMessage
	return Item{Type: ItemTypeMessage, Message: &message}
}

// NewEasyInputMessageItem constructs the type-omitted message shorthand valid
// only in a Responses request input array.
func NewEasyInputMessageItem(role string, content MessageContent) Item {
	return Item{EasyMessage: &EasyInputMessage{Role: role, Content: content}}
}

func NewReasoningItem(reasoning Reasoning) Item {
	reasoning.Type = ItemTypeReasoning
	return Item{Type: ItemTypeReasoning, Reasoning: &reasoning}
}

func NewFunctionCallItem(call FunctionCall) Item {
	call.Type = ItemTypeFunctionCall
	return Item{Type: ItemTypeFunctionCall, FunctionCall: &call}
}

func NewFunctionCallOutputItem(output FunctionCallOutput) Item {
	output.Type = ItemTypeFunctionCallOutput
	return Item{Type: ItemTypeFunctionCallOutput, FunctionCallOutput: &output}
}

func NewRawItem(raw json.RawMessage) (Item, error) {
	var item Item
	if err := json.Unmarshal(raw, &item); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (i Item) MarshalJSON() ([]byte, error) {
	count := variantCount(
		i.EasyMessage != nil, i.Message != nil, i.Reasoning != nil, i.FunctionCall != nil,
		i.FunctionCallOutput != nil, len(i.Raw) > 0,
	)
	if count != 1 {
		return nil, fmt.Errorf("%w: Item has %d variants", ErrInvalidUnion, count)
	}
	if len(i.Raw) > 0 {
		if err := checkUnknownDiscriminator(i.Raw, i.Type); err != nil {
			return nil, err
		}
		if isKnownItemType(i.Type) {
			return nil, fmt.Errorf("%w: known item type %q cannot use Raw", ErrInvalidUnion, i.Type)
		}
		return cloneRaw(i.Raw), nil
	}
	if i.EasyMessage != nil {
		if i.Type != "" {
			return nil, fmt.Errorf("%w: easy input message cannot declare item type %q", ErrInvalidUnion, i.Type)
		}
		return json.Marshal(i.EasyMessage)
	}

	var canonical string
	var value any
	switch {
	case i.Message != nil:
		canonical, value = ItemTypeMessage, i.Message
	case i.Reasoning != nil:
		canonical, value = ItemTypeReasoning, i.Reasoning
	case i.FunctionCall != nil:
		canonical, value = ItemTypeFunctionCall, i.FunctionCall
	default:
		canonical, value = ItemTypeFunctionCallOutput, i.FunctionCallOutput
	}
	if err := checkDiscriminator(i.Type, canonical, "item"); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (i *Item) UnmarshalJSON(data []byte) error {
	if i == nil {
		return fmt.Errorf("responses: cannot unmarshal Item into nil receiver")
	}
	if err := requireJSONObject(data, "item"); err != nil {
		return err
	}
	typ, err := discriminator(data)
	if err != nil {
		return err
	}
	*i = Item{Type: typ}
	switch typ {
	case ItemTypeMessage:
		i.Message = new(Message)
		err = json.Unmarshal(data, i.Message)
	case ItemTypeReasoning:
		i.Reasoning = new(Reasoning)
		err = json.Unmarshal(data, i.Reasoning)
	case ItemTypeFunctionCall:
		i.FunctionCall = new(FunctionCall)
		err = json.Unmarshal(data, i.FunctionCall)
	case ItemTypeFunctionCallOutput:
		i.FunctionCallOutput = new(FunctionCallOutput)
		err = json.Unmarshal(data, i.FunctionCallOutput)
	default:
		i.Raw = cloneRaw(data)
	}
	return err
}

// RawJSON returns an owned copy for an unknown item. Known variants return nil
// because their typed fields and ExtraFields are authoritative.
func (i Item) RawJSON() json.RawMessage { return cloneRaw(i.Raw) }

func (v EasyInputMessage) MarshalJSON() ([]byte, error) {
	return marshalObjectWithExtra(struct {
		Role    string         `json:"role"`
		Content MessageContent `json:"content"`
	}{v.Role, v.Content}, v.ExtraFields, easyMessageFields)
}

func (v *EasyInputMessage) UnmarshalJSON(data []byte) error {
	if v == nil {
		return fmt.Errorf("responses: cannot unmarshal EasyInputMessage into nil receiver")
	}
	if err := requireJSONObject(data, "easy input message"); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, exists := fields["type"]; exists {
		return fmt.Errorf("%w: easy input message must omit type", ErrInvalidUnion)
	}
	roleJSON, hasRole := fields["role"]
	contentJSON, hasContent := fields["content"]
	if !hasRole || !hasContent {
		return fmt.Errorf("%w: type-omitted input message requires role and content", ErrInvalidUnion)
	}
	var role string
	if err := json.Unmarshal(roleJSON, &role); err != nil || bytes.Equal(bytes.TrimSpace(roleJSON), []byte("null")) {
		return fmt.Errorf("responses: easy input message role must be a string")
	}
	var content MessageContent
	if err := json.Unmarshal(contentJSON, &content); err != nil {
		return fmt.Errorf("responses: decode easy input message content: %w", err)
	}
	if content.Text == nil && content.Parts == nil {
		return fmt.Errorf("responses: easy input message content must be a string or content array")
	}
	extra, err := decodeExtraFields(data, easyMessageFields)
	if err != nil {
		return err
	}
	*v = EasyInputMessage{Role: role, Content: content, ExtraFields: extra}
	return nil
}

func isKnownItemType(typ string) bool {
	switch typ {
	case ItemTypeMessage, ItemTypeReasoning, ItemTypeFunctionCall, ItemTypeFunctionCallOutput:
		return true
	default:
		return false
	}
}

func (v Message) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ItemTypeMessage, "Message"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		ID      string         `json:"id,omitempty"`
		Role    string         `json:"role"`
		Status  string         `json:"status,omitempty"`
		Phase   string         `json:"phase,omitempty"`
		Content MessageContent `json:"content"`
	}{v.ID, v.Role, v.Status, v.Phase, v.Content}, v.ExtraFields, ItemTypeMessage, messageFields)
}

func (v *Message) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type    string         `json:"type"`
		ID      string         `json:"id,omitempty"`
		Role    string         `json:"role"`
		Status  string         `json:"status,omitempty"`
		Phase   string         `json:"phase,omitempty"`
		Content MessageContent `json:"content"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ItemTypeMessage, "Message"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, messageFields)
	if err != nil {
		return err
	}
	*v = Message{Type: wire.Type, ID: wire.ID, Role: wire.Role, Status: wire.Status, Phase: wire.Phase, Content: wire.Content, ExtraFields: extra}
	return nil
}

func (v Reasoning) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ItemTypeReasoning, "Reasoning"); err != nil {
		return nil, err
	}
	summary := v.Summary
	if summary == nil {
		summary = []ContentPart{}
	}
	var content *[]ContentPart
	if v.Content != nil {
		content = &v.Content
	}
	return marshalDiscriminatedObject(struct {
		ID               string         `json:"id,omitempty"`
		Status           string         `json:"status,omitempty"`
		Summary          []ContentPart  `json:"summary"`
		Content          *[]ContentPart `json:"content,omitempty"`
		EncryptedContent string         `json:"encrypted_content,omitempty"`
	}{v.ID, v.Status, summary, content, v.EncryptedContent}, v.ExtraFields, ItemTypeReasoning, reasoningFields)
}

func (v *Reasoning) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type             string        `json:"type"`
		ID               string        `json:"id,omitempty"`
		Status           string        `json:"status,omitempty"`
		Summary          []ContentPart `json:"summary,omitempty"`
		Content          []ContentPart `json:"content,omitempty"`
		EncryptedContent string        `json:"encrypted_content,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ItemTypeReasoning, "Reasoning"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, reasoningFields)
	if err != nil {
		return err
	}
	*v = Reasoning{Type: wire.Type, ID: wire.ID, Status: wire.Status, Summary: wire.Summary, Content: wire.Content, EncryptedContent: wire.EncryptedContent, ExtraFields: extra}
	return nil
}

func (v FunctionCall) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ItemTypeFunctionCall, "FunctionCall"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		ID        string          `json:"id,omitempty"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Namespace string          `json:"namespace,omitempty"`
		Arguments string          `json:"arguments"`
		Status    string          `json:"status,omitempty"`
		Caller    json.RawMessage `json:"caller,omitempty"`
	}{v.ID, v.CallID, v.Name, v.Namespace, v.Arguments, v.Status, v.Caller}, v.ExtraFields, ItemTypeFunctionCall, functionCallFields)
}

func (v *FunctionCall) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type      string          `json:"type"`
		ID        string          `json:"id,omitempty"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Namespace string          `json:"namespace,omitempty"`
		Arguments string          `json:"arguments"`
		Status    string          `json:"status,omitempty"`
		Caller    json.RawMessage `json:"caller,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ItemTypeFunctionCall, "FunctionCall"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, functionCallFields)
	if err != nil {
		return err
	}
	*v = FunctionCall{Type: wire.Type, ID: wire.ID, CallID: wire.CallID, Name: wire.Name, Namespace: wire.Namespace, Arguments: wire.Arguments, Status: wire.Status, Caller: cloneRaw(wire.Caller), ExtraFields: extra}
	return nil
}

func (v FunctionCallOutput) MarshalJSON() ([]byte, error) {
	if err := checkDiscriminator(v.Type, ItemTypeFunctionCallOutput, "FunctionCallOutput"); err != nil {
		return nil, err
	}
	return marshalDiscriminatedObject(struct {
		ID        string          `json:"id,omitempty"`
		CallID    string          `json:"call_id"`
		Output    FunctionOutput  `json:"output"`
		Status    string          `json:"status,omitempty"`
		Caller    json.RawMessage `json:"caller,omitempty"`
		CreatedBy string          `json:"created_by,omitempty"`
	}{v.ID, v.CallID, v.Output, v.Status, v.Caller, v.CreatedBy}, v.ExtraFields, ItemTypeFunctionCallOutput, functionCallOutputFields)
}

func (v *FunctionCallOutput) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type      string          `json:"type"`
		ID        string          `json:"id,omitempty"`
		CallID    string          `json:"call_id"`
		Output    FunctionOutput  `json:"output"`
		Status    string          `json:"status,omitempty"`
		Caller    json.RawMessage `json:"caller,omitempty"`
		CreatedBy string          `json:"created_by,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := checkDiscriminator(wire.Type, ItemTypeFunctionCallOutput, "FunctionCallOutput"); err != nil {
		return err
	}
	extra, err := decodeExtraFields(data, functionCallOutputFields)
	if err != nil {
		return err
	}
	*v = FunctionCallOutput{Type: wire.Type, ID: wire.ID, CallID: wire.CallID, Output: wire.Output, Status: wire.Status, Caller: cloneRaw(wire.Caller), CreatedBy: wire.CreatedBy, ExtraFields: extra}
	return nil
}
