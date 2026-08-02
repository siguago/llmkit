package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidUnion indicates that a protocol union has no selected variant or
// more than one selected variant.
var ErrInvalidUnion = errors.New("anthropic: invalid union value")

// Content is the string-or-content-block-array union used by input messages
// and tool results. Raw is used only for a future, currently unknown union
// shape. A non-nil Blocks slice represents the array variant, including [].
type Content struct {
	Text   *string
	Blocks []ContentBlock
	Raw    json.RawMessage
}

// StringContent constructs Anthropic's string shorthand content form.
func StringContent(text string) Content {
	return Content{Text: &text}
}

// BlockContent constructs the content-block array form.
func BlockContent(blocks ...ContentBlock) Content {
	return Content{Blocks: append([]ContentBlock{}, blocks...)}
}

func (content Content) MarshalJSON() ([]byte, error) {
	variants := 0
	if content.Text != nil {
		variants++
	}
	if content.Blocks != nil {
		variants++
	}
	if content.Raw != nil {
		variants++
	}
	if variants != 1 {
		return nil, fmt.Errorf("%w: Content has %d variants", ErrInvalidUnion, variants)
	}
	if content.Text != nil {
		return json.Marshal(*content.Text)
	}
	if content.Blocks != nil {
		return json.Marshal(content.Blocks)
	}
	if !json.Valid(content.Raw) {
		return nil, fmt.Errorf("anthropic: Content.Raw contains invalid JSON")
	}
	return cloneRaw(content.Raw), nil
}

func (content *Content) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("anthropic: empty content JSON")
	}
	*content = Content{}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		content.Text = &text
	case '[':
		var blocks []ContentBlock
		if err := json.Unmarshal(trimmed, &blocks); err != nil {
			return err
		}
		if blocks == nil {
			blocks = []ContentBlock{}
		}
		content.Blocks = blocks
	default:
		if !json.Valid(trimmed) {
			return fmt.Errorf("anthropic: invalid content JSON")
		}
		content.Raw = cloneRaw(trimmed)
	}
	return nil
}

// System is the string-or-text-block-array union used by the top-level system
// field.
type System struct {
	Text   *string
	Blocks []TextBlock
	Raw    json.RawMessage
}

func StringSystem(text string) System { return System{Text: &text} }

func BlockSystem(blocks ...TextBlock) System {
	return System{Blocks: append([]TextBlock{}, blocks...)}
}

func (system System) MarshalJSON() ([]byte, error) {
	variants := 0
	if system.Text != nil {
		variants++
	}
	if system.Blocks != nil {
		variants++
	}
	if system.Raw != nil {
		variants++
	}
	if variants != 1 {
		return nil, fmt.Errorf("%w: System has %d variants", ErrInvalidUnion, variants)
	}
	if system.Text != nil {
		return json.Marshal(*system.Text)
	}
	if system.Blocks != nil {
		return json.Marshal(system.Blocks)
	}
	if !json.Valid(system.Raw) {
		return nil, fmt.Errorf("anthropic: System.Raw contains invalid JSON")
	}
	return cloneRaw(system.Raw), nil
}

func (system *System) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("anthropic: empty system JSON")
	}
	*system = System{}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		system.Text = &text
	case '[':
		var blocks []TextBlock
		if err := json.Unmarshal(trimmed, &blocks); err != nil {
			return err
		}
		if blocks == nil {
			blocks = []TextBlock{}
		}
		system.Blocks = blocks
	default:
		if !json.Valid(trimmed) {
			return fmt.Errorf("anthropic: invalid system JSON")
		}
		system.Raw = cloneRaw(trimmed)
	}
	return nil
}

// ContentBlockType is an open discriminator for Message content blocks.
type ContentBlockType string

const (
	ContentBlockTypeText             ContentBlockType = "text"
	ContentBlockTypeImage            ContentBlockType = "image"
	ContentBlockTypeDocument         ContentBlockType = "document"
	ContentBlockTypeToolUse          ContentBlockType = "tool_use"
	ContentBlockTypeToolResult       ContentBlockType = "tool_result"
	ContentBlockTypeThinking         ContentBlockType = "thinking"
	ContentBlockTypeRedactedThinking ContentBlockType = "redacted_thinking"
)

// ContentBlock is a tagged union. Raw contains the complete JSON object for an
// unknown block type; known block types retain unknown members in the concrete
// block's ExtraFields. PartialJSON contains the exact concatenation of
// input_json_delta fragments observed by an Accumulator. It is transport
// metadata and is not marshaled; it can be invalid JSON when a generation is
// interrupted or reaches a token limit.
type ContentBlock struct {
	Type             ContentBlockType
	Text             *TextBlock
	Image            *ImageBlock
	Document         *DocumentBlock
	ToolUse          *ToolUseBlock
	ToolResult       *ToolResultBlock
	Thinking         *ThinkingBlock
	RedactedThinking *RedactedThinkingBlock
	Raw              json.RawMessage
	PartialJSON      string
}

func NewTextBlock(text string) ContentBlock {
	return ContentBlock{Type: ContentBlockTypeText, Text: &TextBlock{Type: ContentBlockTypeText, Text: text}}
}

func NewImageBlock(source Source) ContentBlock {
	return ContentBlock{Type: ContentBlockTypeImage, Image: &ImageBlock{Type: ContentBlockTypeImage, Source: source}}
}

func NewDocumentBlock(source Source) ContentBlock {
	return ContentBlock{Type: ContentBlockTypeDocument, Document: &DocumentBlock{Type: ContentBlockTypeDocument, Source: source}}
}

func NewToolUseBlock(id, name string, input json.RawMessage) ContentBlock {
	return ContentBlock{Type: ContentBlockTypeToolUse, ToolUse: &ToolUseBlock{
		Type: ContentBlockTypeToolUse, ID: id, Name: name, Input: cloneRaw(input),
	}}
}

func NewToolResultBlock(toolUseID string, content *Content, isError *bool) ContentBlock {
	return ContentBlock{Type: ContentBlockTypeToolResult, ToolResult: &ToolResultBlock{
		Type: ContentBlockTypeToolResult, ToolUseID: toolUseID, Content: content, IsError: isError,
	}}
}

func NewThinkingBlock(thinking, signature string) ContentBlock {
	return ContentBlock{Type: ContentBlockTypeThinking, Thinking: &ThinkingBlock{
		Type: ContentBlockTypeThinking, Thinking: thinking, Signature: signature,
	}}
}

func NewRedactedThinkingBlock(data string) ContentBlock {
	return ContentBlock{Type: ContentBlockTypeRedactedThinking, RedactedThinking: &RedactedThinkingBlock{
		Type: ContentBlockTypeRedactedThinking, Data: data,
	}}
}

func (block ContentBlock) MarshalJSON() ([]byte, error) {
	variants := 0
	for _, selected := range []bool{
		block.Text != nil, block.Image != nil, block.Document != nil,
		block.ToolUse != nil, block.ToolResult != nil, block.Thinking != nil,
		block.RedactedThinking != nil, block.Raw != nil,
	} {
		if selected {
			variants++
		}
	}
	if variants != 1 {
		return nil, fmt.Errorf("%w: ContentBlock has %d variants", ErrInvalidUnion, variants)
	}

	switch {
	case block.Text != nil:
		if block.Type != "" && block.Type != ContentBlockTypeText {
			return nil, blockTypeMismatch(block.Type, ContentBlockTypeText)
		}
		return json.Marshal(block.Text)
	case block.Image != nil:
		if block.Type != "" && block.Type != ContentBlockTypeImage {
			return nil, blockTypeMismatch(block.Type, ContentBlockTypeImage)
		}
		return json.Marshal(block.Image)
	case block.Document != nil:
		if block.Type != "" && block.Type != ContentBlockTypeDocument {
			return nil, blockTypeMismatch(block.Type, ContentBlockTypeDocument)
		}
		return json.Marshal(block.Document)
	case block.ToolUse != nil:
		if block.Type != "" && block.Type != ContentBlockTypeToolUse {
			return nil, blockTypeMismatch(block.Type, ContentBlockTypeToolUse)
		}
		return json.Marshal(block.ToolUse)
	case block.ToolResult != nil:
		if block.Type != "" && block.Type != ContentBlockTypeToolResult {
			return nil, blockTypeMismatch(block.Type, ContentBlockTypeToolResult)
		}
		return json.Marshal(block.ToolResult)
	case block.Thinking != nil:
		if block.Type != "" && block.Type != ContentBlockTypeThinking {
			return nil, blockTypeMismatch(block.Type, ContentBlockTypeThinking)
		}
		return json.Marshal(block.Thinking)
	case block.RedactedThinking != nil:
		if block.Type != "" && block.Type != ContentBlockTypeRedactedThinking {
			return nil, blockTypeMismatch(block.Type, ContentBlockTypeRedactedThinking)
		}
		return json.Marshal(block.RedactedThinking)
	default:
		if err := requireRawObject(block.Raw, "ContentBlock.Raw"); err != nil {
			return nil, err
		}
		return cloneRaw(block.Raw), nil
	}
}

func (block *ContentBlock) UnmarshalJSON(data []byte) error {
	typeName, err := rawObjectType(data)
	if err != nil {
		return fmt.Errorf("anthropic: decode content block discriminator: %w", err)
	}
	*block = ContentBlock{Type: ContentBlockType(typeName)}
	switch block.Type {
	case ContentBlockTypeText:
		block.Text = new(TextBlock)
		err = json.Unmarshal(data, block.Text)
	case ContentBlockTypeImage:
		block.Image = new(ImageBlock)
		err = json.Unmarshal(data, block.Image)
	case ContentBlockTypeDocument:
		block.Document = new(DocumentBlock)
		err = json.Unmarshal(data, block.Document)
	case ContentBlockTypeToolUse:
		block.ToolUse = new(ToolUseBlock)
		err = json.Unmarshal(data, block.ToolUse)
	case ContentBlockTypeToolResult:
		block.ToolResult = new(ToolResultBlock)
		err = json.Unmarshal(data, block.ToolResult)
	case ContentBlockTypeThinking:
		block.Thinking = new(ThinkingBlock)
		err = json.Unmarshal(data, block.Thinking)
	case ContentBlockTypeRedactedThinking:
		block.RedactedThinking = new(RedactedThinkingBlock)
		err = json.Unmarshal(data, block.RedactedThinking)
	default:
		if !json.Valid(data) {
			return fmt.Errorf("anthropic: invalid unknown content block JSON")
		}
		block.Raw = cloneRaw(data)
	}
	return err
}

func blockTypeMismatch(got, want ContentBlockType) error {
	return fmt.Errorf("%w: content block type %q does not match %q", ErrInvalidUnion, got, want)
}

// TextBlock is a text input or output block. Citations is kept as raw JSON
// because the citation union evolves independently and is not required to read
// text safely.
type TextBlock struct {
	Type         ContentBlockType `json:"type"`
	Text         string           `json:"text"`
	Citations    json.RawMessage  `json:"citations,omitempty"`
	CacheControl *CacheControl    `json:"cache_control,omitempty"`
	ExtraFields  ExtraFields      `json:"-"`
}

func (block TextBlock) MarshalJSON() ([]byte, error) {
	if block.Type == "" {
		block.Type = ContentBlockTypeText
	}
	if block.Type != ContentBlockTypeText {
		return nil, blockTypeMismatch(block.Type, ContentBlockTypeText)
	}
	type wire TextBlock
	return marshalWithExtra(wire(block), block.ExtraFields, "type", "text", "citations", "cache_control")
}

func (block *TextBlock) UnmarshalJSON(data []byte) error {
	type wire TextBlock
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "text", "citations", "cache_control")
	if err != nil {
		return err
	}
	*block = TextBlock(decoded)
	block.Citations = cloneRaw(block.Citations)
	block.ExtraFields = extra
	return nil
}

// Source models image and document source unions. Content is the raw value of
// a document source's content member when type is "content".
type Source struct {
	Type        string          `json:"type"`
	MediaType   *string         `json:"media_type,omitempty"`
	Data        *string         `json:"data,omitempty"`
	URL         *string         `json:"url,omitempty"`
	FileID      *string         `json:"file_id,omitempty"`
	Content     json.RawMessage `json:"content,omitempty"`
	ExtraFields ExtraFields     `json:"-"`
}

func (source Source) MarshalJSON() ([]byte, error) {
	type wire Source
	return marshalWithExtra(wire(source), source.ExtraFields, "type", "media_type", "data", "url", "file_id", "content")
}

func (source *Source) UnmarshalJSON(data []byte) error {
	type wire Source
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "media_type", "data", "url", "file_id", "content")
	if err != nil {
		return err
	}
	*source = Source(decoded)
	source.Content = cloneRaw(source.Content)
	source.ExtraFields = extra
	return nil
}

type ImageBlock struct {
	Type         ContentBlockType `json:"type"`
	Source       Source           `json:"source"`
	CacheControl *CacheControl    `json:"cache_control,omitempty"`
	ExtraFields  ExtraFields      `json:"-"`
}

func (block ImageBlock) MarshalJSON() ([]byte, error) {
	if block.Type == "" {
		block.Type = ContentBlockTypeImage
	}
	if block.Type != ContentBlockTypeImage {
		return nil, blockTypeMismatch(block.Type, ContentBlockTypeImage)
	}
	type wire ImageBlock
	return marshalWithExtra(wire(block), block.ExtraFields, "type", "source", "cache_control")
}

func (block *ImageBlock) UnmarshalJSON(data []byte) error {
	type wire ImageBlock
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "source", "cache_control")
	if err != nil {
		return err
	}
	*block = ImageBlock(decoded)
	block.ExtraFields = extra
	return nil
}

type DocumentBlock struct {
	Type         ContentBlockType `json:"type"`
	Source       Source           `json:"source"`
	CacheControl *CacheControl    `json:"cache_control,omitempty"`
	Citations    json.RawMessage  `json:"citations,omitempty"`
	Context      *string          `json:"context,omitempty"`
	Title        *string          `json:"title,omitempty"`
	ExtraFields  ExtraFields      `json:"-"`
}

func (block DocumentBlock) MarshalJSON() ([]byte, error) {
	if block.Type == "" {
		block.Type = ContentBlockTypeDocument
	}
	if block.Type != ContentBlockTypeDocument {
		return nil, blockTypeMismatch(block.Type, ContentBlockTypeDocument)
	}
	type wire DocumentBlock
	return marshalWithExtra(wire(block), block.ExtraFields, "type", "source", "cache_control", "citations", "context", "title")
}

func (block *DocumentBlock) UnmarshalJSON(data []byte) error {
	type wire DocumentBlock
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "source", "cache_control", "citations", "context", "title")
	if err != nil {
		return err
	}
	*block = DocumentBlock(decoded)
	block.Citations = cloneRaw(block.Citations)
	block.ExtraFields = extra
	return nil
}

type ToolUseBlock struct {
	Type         ContentBlockType `json:"type"`
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Input        json.RawMessage  `json:"input"`
	Caller       json.RawMessage  `json:"caller,omitempty"`
	CacheControl *CacheControl    `json:"cache_control,omitempty"`
	ExtraFields  ExtraFields      `json:"-"`
}

func (block ToolUseBlock) MarshalJSON() ([]byte, error) {
	if block.Type == "" {
		block.Type = ContentBlockTypeToolUse
	}
	if block.Type != ContentBlockTypeToolUse {
		return nil, blockTypeMismatch(block.Type, ContentBlockTypeToolUse)
	}
	type wire ToolUseBlock
	return marshalWithExtra(wire(block), block.ExtraFields, "type", "id", "name", "input", "caller", "cache_control")
}

func (block *ToolUseBlock) UnmarshalJSON(data []byte) error {
	type wire ToolUseBlock
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "id", "name", "input", "caller", "cache_control")
	if err != nil {
		return err
	}
	*block = ToolUseBlock(decoded)
	block.Input = cloneRaw(block.Input)
	block.Caller = cloneRaw(block.Caller)
	block.ExtraFields = extra
	return nil
}

type ToolResultBlock struct {
	Type         ContentBlockType `json:"type"`
	ToolUseID    string           `json:"tool_use_id"`
	Content      *Content         `json:"content,omitempty"`
	IsError      *bool            `json:"is_error,omitempty"`
	CacheControl *CacheControl    `json:"cache_control,omitempty"`
	ExtraFields  ExtraFields      `json:"-"`
}

func (block ToolResultBlock) MarshalJSON() ([]byte, error) {
	if block.Type == "" {
		block.Type = ContentBlockTypeToolResult
	}
	if block.Type != ContentBlockTypeToolResult {
		return nil, blockTypeMismatch(block.Type, ContentBlockTypeToolResult)
	}
	type wire ToolResultBlock
	return marshalWithExtra(wire(block), block.ExtraFields, "type", "tool_use_id", "content", "is_error", "cache_control")
}

func (block *ToolResultBlock) UnmarshalJSON(data []byte) error {
	type wire ToolResultBlock
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "tool_use_id", "content", "is_error", "cache_control")
	if err != nil {
		return err
	}
	*block = ToolResultBlock(decoded)
	block.ExtraFields = extra
	return nil
}

type ThinkingBlock struct {
	Type        ContentBlockType `json:"type"`
	Thinking    string           `json:"thinking"`
	Signature   string           `json:"signature"`
	ExtraFields ExtraFields      `json:"-"`
}

func (block ThinkingBlock) MarshalJSON() ([]byte, error) {
	if block.Type == "" {
		block.Type = ContentBlockTypeThinking
	}
	if block.Type != ContentBlockTypeThinking {
		return nil, blockTypeMismatch(block.Type, ContentBlockTypeThinking)
	}
	type wire ThinkingBlock
	return marshalWithExtra(wire(block), block.ExtraFields, "type", "thinking", "signature")
}

func (block *ThinkingBlock) UnmarshalJSON(data []byte) error {
	type wire ThinkingBlock
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "thinking", "signature")
	if err != nil {
		return err
	}
	*block = ThinkingBlock(decoded)
	block.ExtraFields = extra
	return nil
}

type RedactedThinkingBlock struct {
	Type        ContentBlockType `json:"type"`
	Data        string           `json:"data"`
	ExtraFields ExtraFields      `json:"-"`
}

func (block RedactedThinkingBlock) MarshalJSON() ([]byte, error) {
	if block.Type == "" {
		block.Type = ContentBlockTypeRedactedThinking
	}
	if block.Type != ContentBlockTypeRedactedThinking {
		return nil, blockTypeMismatch(block.Type, ContentBlockTypeRedactedThinking)
	}
	type wire RedactedThinkingBlock
	return marshalWithExtra(wire(block), block.ExtraFields, "type", "data")
}

func (block *RedactedThinkingBlock) UnmarshalJSON(data []byte) error {
	type wire RedactedThinkingBlock
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "data")
	if err != nil {
		return err
	}
	*block = RedactedThinkingBlock(decoded)
	block.ExtraFields = extra
	return nil
}

func cloneContentBlock(block ContentBlock) (ContentBlock, error) {
	data, err := json.Marshal(block)
	if err != nil {
		return ContentBlock{}, err
	}
	var cloned ContentBlock
	if err := json.Unmarshal(data, &cloned); err != nil {
		return ContentBlock{}, err
	}
	if block.Raw != nil {
		cloned.Raw = cloneRaw(block.Raw)
	}
	cloned.PartialJSON = block.PartialJSON
	return cloned, nil
}

// copyContentBlock makes an infallible defensive copy of an already validated
// block. Accumulators validate blocks on ingress, so snapshots must not need to
// marshal mutable state merely to clone it.
func copyContentBlock(block ContentBlock) ContentBlock {
	copy := block
	copy.Raw = cloneRaw(block.Raw)
	if block.Text != nil {
		value := *block.Text
		value.Citations = cloneRaw(value.Citations)
		value.CacheControl = cloneCacheControl(value.CacheControl)
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.Text = &value
	}
	if block.Image != nil {
		value := *block.Image
		value.Source = cloneSource(value.Source)
		value.CacheControl = cloneCacheControl(value.CacheControl)
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.Image = &value
	}
	if block.Document != nil {
		value := *block.Document
		value.Source = cloneSource(value.Source)
		value.CacheControl = cloneCacheControl(value.CacheControl)
		value.Citations = cloneRaw(value.Citations)
		value.Context = cloneString(value.Context)
		value.Title = cloneString(value.Title)
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.Document = &value
	}
	if block.ToolUse != nil {
		value := *block.ToolUse
		value.Input = cloneRaw(value.Input)
		value.Caller = cloneRaw(value.Caller)
		value.CacheControl = cloneCacheControl(value.CacheControl)
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.ToolUse = &value
	}
	if block.ToolResult != nil {
		value := *block.ToolResult
		value.Content = cloneContent(value.Content)
		value.IsError = cloneBool(value.IsError)
		value.CacheControl = cloneCacheControl(value.CacheControl)
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.ToolResult = &value
	}
	if block.Thinking != nil {
		value := *block.Thinking
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.Thinking = &value
	}
	if block.RedactedThinking != nil {
		value := *block.RedactedThinking
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.RedactedThinking = &value
	}
	return copy
}

func cloneCacheControl(control *CacheControl) *CacheControl {
	if control == nil {
		return nil
	}
	copy := *control
	copy.TTL = cloneString(control.TTL)
	copy.ExtraFields = cloneExtras(control.ExtraFields)
	return &copy
}

func cloneSource(source Source) Source {
	copy := source
	copy.MediaType = cloneString(source.MediaType)
	copy.Data = cloneString(source.Data)
	copy.URL = cloneString(source.URL)
	copy.FileID = cloneString(source.FileID)
	copy.Content = cloneRaw(source.Content)
	copy.ExtraFields = cloneExtras(source.ExtraFields)
	return copy
}

func cloneContent(content *Content) *Content {
	if content == nil {
		return nil
	}
	copy := *content
	copy.Text = cloneString(content.Text)
	if content.Blocks != nil {
		copy.Blocks = make([]ContentBlock, len(content.Blocks))
		for index, block := range content.Blocks {
			copy.Blocks[index] = copyContentBlock(block)
		}
	}
	copy.Raw = cloneRaw(content.Raw)
	return &copy
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
