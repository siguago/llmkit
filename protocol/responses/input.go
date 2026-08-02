package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Input is the create request's string-or-items union.
//
// Known string, item-array, and null values are decoded into Text, Items, or
// Null. Raw is set only for a future union shape that this version does not
// understand.
type Input struct {
	Text  *string         `json:"-"`
	Items []Item          `json:"-"`
	Null  bool            `json:"-"`
	Raw   json.RawMessage `json:"-"`
}

// Instructions names the string-or-items-or-null union returned on a Response.
// It aliases Input because the two wire unions have the same variants.
type Instructions = Input

// NewTextInput constructs a string input. Empty strings are preserved.
func NewTextInput(text string) Input { return Input{Text: &text} }

// NewItemInput constructs an item-list input. A zero-length argument list is
// encoded as an empty JSON array, not null.
func NewItemInput(items ...Item) Input {
	owned := make([]Item, len(items))
	copy(owned, items)
	return Input{Items: owned}
}

// NewNullInput constructs an explicit JSON null. A zero Input is omitted from
// create and token-count requests, so this constructor is useful where null and
// absence have distinct wire semantics (notably Response.Instructions).
func NewNullInput() Input { return Input{Null: true} }

// NewRawInput validates and retains an input variant not modeled by this
// package yet.
func NewRawInput(raw json.RawMessage) (Input, error) {
	if err := validateRaw(raw, "raw input"); err != nil {
		return Input{}, err
	}
	var input Input
	if err := input.UnmarshalJSON(raw); err != nil {
		return Input{}, err
	}
	return input, nil
}

func (i Input) validate() error {
	count := variantCount(i.Text != nil, i.Items != nil, i.Null, len(i.Raw) > 0)
	if count > 1 {
		return fmt.Errorf("%w: Input has %d variants", ErrInvalidUnion, count)
	}
	if len(i.Raw) > 0 {
		return validateRaw(i.Raw, "input Raw")
	}
	return nil
}

func (i Input) isZero() bool {
	return i.Text == nil && i.Items == nil && !i.Null && len(i.Raw) == 0
}

func (i Input) MarshalJSON() ([]byte, error) {
	if err := i.validate(); err != nil {
		return nil, err
	}
	if len(i.Raw) > 0 {
		return cloneRaw(i.Raw), nil
	}
	switch {
	case i.Text != nil:
		return json.Marshal(*i.Text)
	case i.Items != nil:
		return json.Marshal(i.Items)
	case i.Null:
		return []byte("null"), nil
	default:
		return []byte("null"), nil
	}
}

func (i *Input) UnmarshalJSON(data []byte) error {
	if i == nil {
		return fmt.Errorf("responses: cannot unmarshal input into nil receiver")
	}
	if !json.Valid(data) {
		return fmt.Errorf("responses: invalid input JSON")
	}
	*i = Input{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("responses: empty input JSON")
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		i.Text = &text
	case '[':
		var rawItems []json.RawMessage
		if err := json.Unmarshal(data, &rawItems); err != nil {
			return err
		}
		i.Items = make([]Item, len(rawItems))
		for index, raw := range rawItems {
			item, err := decodeInputItem(raw)
			if err != nil {
				return fmt.Errorf("responses: decode input item %d: %w", index, err)
			}
			i.Items[index] = item
		}
	default:
		if bytes.Equal(trimmed, []byte("null")) {
			i.Null = true
		} else {
			i.Raw = cloneRaw(data)
		}
	}
	return nil
}

// RawJSON returns an owned copy of an unknown input variant, or nil for known
// string and item-array variants.
func (i Input) RawJSON() json.RawMessage { return cloneRaw(i.Raw) }

func decodeInputItem(data json.RawMessage) (Item, error) {
	if err := requireJSONObject(data, "input item"); err != nil {
		return Item{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Item{}, err
	}
	if _, hasType := fields["type"]; hasType {
		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			return Item{}, err
		}
		return item, nil
	}
	if _, hasRole := fields["role"]; hasRole {
		if _, hasContent := fields["content"]; hasContent {
			var message EasyInputMessage
			if err := json.Unmarshal(data, &message); err != nil {
				return Item{}, err
			}
			return Item{EasyMessage: &message}, nil
		}
	}
	return Item{}, fmt.Errorf("%w: discriminator type is missing", ErrInvalidUnion)
}

// MessageContent is the string-or-content-parts union used by message content
// and function-call outputs.
type MessageContent struct {
	Text  *string         `json:"-"`
	Parts []ContentPart   `json:"-"`
	Raw   json.RawMessage `json:"-"`
}

// NewTextContent creates string message content.
func NewTextContent(text string) MessageContent { return MessageContent{Text: &text} }

// NewPartContent creates array message content.
func NewPartContent(parts ...ContentPart) MessageContent {
	owned := make([]ContentPart, len(parts))
	copy(owned, parts)
	return MessageContent{Parts: owned}
}

func (c MessageContent) MarshalJSON() ([]byte, error) {
	count := variantCount(c.Text != nil, c.Parts != nil, len(c.Raw) > 0)
	if count > 1 {
		return nil, fmt.Errorf("%w: MessageContent has %d variants", ErrInvalidUnion, count)
	}
	if len(c.Raw) > 0 {
		if err := validateRaw(c.Raw, "message content Raw"); err != nil {
			return nil, err
		}
		return cloneRaw(c.Raw), nil
	}
	switch {
	case c.Text != nil:
		return json.Marshal(*c.Text)
	case c.Parts != nil:
		return json.Marshal(c.Parts)
	default:
		return []byte("null"), nil
	}
}

func (c *MessageContent) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("responses: cannot unmarshal message content into nil receiver")
	}
	if !json.Valid(data) {
		return fmt.Errorf("responses: invalid message content JSON")
	}
	*c = MessageContent{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("responses: empty message content JSON")
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		c.Text = &text
	case '[':
		if err := json.Unmarshal(data, &c.Parts); err != nil {
			return err
		}
	default:
		c.Raw = cloneRaw(data)
	}
	return nil
}

func (c MessageContent) RawJSON() json.RawMessage { return cloneRaw(c.Raw) }

// FunctionOutput has the same wire union as MessageContent but is named for
// call sites that construct function_call_output items.
type FunctionOutput = MessageContent
