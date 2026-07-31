package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
)

// Stream is the transport-independent native Messages stream contract.
// Recv returns io.EOF only after a complete stream. Implementations should
// expose the partially or fully accumulated message through FinalMessage.
type Stream interface {
	Recv() (*Event, error)
	Close() error
	RequestID() string
	FinalMessage() *MessageResponse
}

var _ error = (*APIError)(nil)

// EventType is an open Anthropic SSE event discriminator.
type EventType string

const (
	EventTypeMessageStart      EventType = "message_start"
	EventTypeContentBlockStart EventType = "content_block_start"
	EventTypeContentBlockDelta EventType = "content_block_delta"
	EventTypeContentBlockStop  EventType = "content_block_stop"
	EventTypeMessageDelta      EventType = "message_delta"
	EventTypeMessageStop       EventType = "message_stop"
	EventTypePing              EventType = "ping"
	EventTypeError             EventType = "error"
)

// Event is a tagged union for all core Messages stream events. Raw contains a
// copy of the complete wire JSON. For known variants MarshalJSON uses the typed
// member plus its ExtraFields; for unknown variants it returns Raw unchanged.
type Event struct {
	Type              EventType
	MessageStart      *MessageStartEvent
	ContentBlockStart *ContentBlockStartEvent
	ContentBlockDelta *ContentBlockDeltaEvent
	ContentBlockStop  *ContentBlockStopEvent
	MessageDelta      *MessageDeltaEvent
	MessageStop       *MessageStopEvent
	Ping              *PingEvent
	Error             *ErrorEvent
	Unknown           *UnknownEvent
	Raw               json.RawMessage
}

// ParseEvent decodes one SSE data payload.
func ParseEvent(data []byte) (*Event, error) {
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

func (event Event) MarshalJSON() ([]byte, error) {
	variants := 0
	for _, selected := range []bool{
		event.MessageStart != nil, event.ContentBlockStart != nil,
		event.ContentBlockDelta != nil, event.ContentBlockStop != nil,
		event.MessageDelta != nil, event.MessageStop != nil, event.Ping != nil,
		event.Error != nil, event.Unknown != nil,
	} {
		if selected {
			variants++
		}
	}
	if variants != 1 {
		return nil, fmt.Errorf("%w: Event has %d variants", ErrInvalidUnion, variants)
	}

	switch {
	case event.MessageStart != nil:
		return marshalEventVariant(event.Type, EventTypeMessageStart, event.MessageStart)
	case event.ContentBlockStart != nil:
		return marshalEventVariant(event.Type, EventTypeContentBlockStart, event.ContentBlockStart)
	case event.ContentBlockDelta != nil:
		return marshalEventVariant(event.Type, EventTypeContentBlockDelta, event.ContentBlockDelta)
	case event.ContentBlockStop != nil:
		return marshalEventVariant(event.Type, EventTypeContentBlockStop, event.ContentBlockStop)
	case event.MessageDelta != nil:
		return marshalEventVariant(event.Type, EventTypeMessageDelta, event.MessageDelta)
	case event.MessageStop != nil:
		return marshalEventVariant(event.Type, EventTypeMessageStop, event.MessageStop)
	case event.Ping != nil:
		return marshalEventVariant(event.Type, EventTypePing, event.Ping)
	case event.Error != nil:
		return marshalEventVariant(event.Type, EventTypeError, event.Error)
	default:
		if event.Unknown.Raw == nil || !json.Valid(event.Unknown.Raw) {
			return nil, fmt.Errorf("anthropic: unknown event contains invalid JSON")
		}
		return cloneRaw(event.Unknown.Raw), nil
	}
}

func marshalEventVariant(got, want EventType, variant any) ([]byte, error) {
	if got != "" && got != want {
		return nil, fmt.Errorf("%w: event type %q does not match %q", ErrInvalidUnion, got, want)
	}
	return json.Marshal(variant)
}

func (event *Event) UnmarshalJSON(data []byte) error {
	typeName, err := rawObjectType(data)
	if err != nil {
		return fmt.Errorf("anthropic: decode event discriminator: %w", err)
	}
	*event = Event{Type: EventType(typeName), Raw: cloneRaw(data)}
	switch event.Type {
	case EventTypeMessageStart:
		event.MessageStart = new(MessageStartEvent)
		err = json.Unmarshal(data, event.MessageStart)
	case EventTypeContentBlockStart:
		event.ContentBlockStart = new(ContentBlockStartEvent)
		err = json.Unmarshal(data, event.ContentBlockStart)
	case EventTypeContentBlockDelta:
		event.ContentBlockDelta = new(ContentBlockDeltaEvent)
		err = json.Unmarshal(data, event.ContentBlockDelta)
	case EventTypeContentBlockStop:
		event.ContentBlockStop = new(ContentBlockStopEvent)
		err = json.Unmarshal(data, event.ContentBlockStop)
	case EventTypeMessageDelta:
		event.MessageDelta = new(MessageDeltaEvent)
		err = json.Unmarshal(data, event.MessageDelta)
	case EventTypeMessageStop:
		event.MessageStop = new(MessageStopEvent)
		err = json.Unmarshal(data, event.MessageStop)
	case EventTypePing:
		event.Ping = new(PingEvent)
		err = json.Unmarshal(data, event.Ping)
	case EventTypeError:
		event.Error = new(ErrorEvent)
		err = json.Unmarshal(data, event.Error)
	default:
		event.Unknown = &UnknownEvent{Type: event.Type, Raw: cloneRaw(data)}
	}
	return err
}

// Terminal reports whether this event ends a Messages stream.
func (event *Event) Terminal() bool {
	return event != nil && (event.Type == EventTypeMessageStop || event.Type == EventTypeError)
}

type MessageStartEvent struct {
	Type        EventType       `json:"type"`
	Message     MessageResponse `json:"message"`
	ExtraFields ExtraFields     `json:"-"`
}

func (event MessageStartEvent) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		event.Type = EventTypeMessageStart
	}
	type wire MessageStartEvent
	return marshalWithExtra(wire(event), event.ExtraFields, "type", "message")
}

func (event *MessageStartEvent) UnmarshalJSON(data []byte) error {
	type wire MessageStartEvent
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "message")
	if err != nil {
		return err
	}
	*event = MessageStartEvent(decoded)
	event.ExtraFields = extra
	return nil
}

type ContentBlockStartEvent struct {
	Type         EventType    `json:"type"`
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
	ExtraFields  ExtraFields  `json:"-"`
}

func (event ContentBlockStartEvent) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		event.Type = EventTypeContentBlockStart
	}
	type wire ContentBlockStartEvent
	return marshalWithExtra(wire(event), event.ExtraFields, "type", "index", "content_block")
}

func (event *ContentBlockStartEvent) UnmarshalJSON(data []byte) error {
	type wire ContentBlockStartEvent
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "index", "content_block")
	if err != nil {
		return err
	}
	if _, err := json.Marshal(decoded.ContentBlock); err != nil {
		return fmt.Errorf("anthropic: invalid content_block_start content_block: %w", err)
	}
	*event = ContentBlockStartEvent(decoded)
	event.ExtraFields = extra
	return nil
}

type ContentBlockDeltaEvent struct {
	Type        EventType    `json:"type"`
	Index       int          `json:"index"`
	Delta       ContentDelta `json:"delta"`
	ExtraFields ExtraFields  `json:"-"`
}

func (event ContentBlockDeltaEvent) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		event.Type = EventTypeContentBlockDelta
	}
	type wire ContentBlockDeltaEvent
	return marshalWithExtra(wire(event), event.ExtraFields, "type", "index", "delta")
}

func (event *ContentBlockDeltaEvent) UnmarshalJSON(data []byte) error {
	type wire ContentBlockDeltaEvent
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "index", "delta")
	if err != nil {
		return err
	}
	if _, err := json.Marshal(decoded.Delta); err != nil {
		return fmt.Errorf("anthropic: invalid content_block_delta delta: %w", err)
	}
	*event = ContentBlockDeltaEvent(decoded)
	event.ExtraFields = extra
	return nil
}

type ContentBlockStopEvent struct {
	Type        EventType   `json:"type"`
	Index       int         `json:"index"`
	ExtraFields ExtraFields `json:"-"`
}

func (event ContentBlockStopEvent) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		event.Type = EventTypeContentBlockStop
	}
	type wire ContentBlockStopEvent
	return marshalWithExtra(wire(event), event.ExtraFields, "type", "index")
}

func (event *ContentBlockStopEvent) UnmarshalJSON(data []byte) error {
	type wire ContentBlockStopEvent
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "index")
	if err != nil {
		return err
	}
	*event = ContentBlockStopEvent(decoded)
	event.ExtraFields = extra
	return nil
}

type MessageDeltaEvent struct {
	Type        EventType    `json:"type"`
	Delta       MessageDelta `json:"delta"`
	Usage       DeltaUsage   `json:"usage"`
	ExtraFields ExtraFields  `json:"-"`
}

func (event MessageDeltaEvent) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		event.Type = EventTypeMessageDelta
	}
	type wire MessageDeltaEvent
	return marshalWithExtra(wire(event), event.ExtraFields, "type", "delta", "usage")
}

func (event *MessageDeltaEvent) UnmarshalJSON(data []byte) error {
	type wire MessageDeltaEvent
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "delta", "usage")
	if err != nil {
		return err
	}
	*event = MessageDeltaEvent(decoded)
	event.ExtraFields = extra
	return nil
}

type MessageStopEvent struct {
	Type        EventType   `json:"type"`
	ExtraFields ExtraFields `json:"-"`
}

func (event MessageStopEvent) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		event.Type = EventTypeMessageStop
	}
	type wire MessageStopEvent
	return marshalWithExtra(wire(event), event.ExtraFields, "type")
}

func (event *MessageStopEvent) UnmarshalJSON(data []byte) error {
	type wire MessageStopEvent
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type")
	if err != nil {
		return err
	}
	*event = MessageStopEvent(decoded)
	event.ExtraFields = extra
	return nil
}

type PingEvent struct {
	Type        EventType   `json:"type"`
	ExtraFields ExtraFields `json:"-"`
}

func (event PingEvent) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		event.Type = EventTypePing
	}
	type wire PingEvent
	return marshalWithExtra(wire(event), event.ExtraFields, "type")
}

func (event *PingEvent) UnmarshalJSON(data []byte) error {
	type wire PingEvent
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type")
	if err != nil {
		return err
	}
	*event = PingEvent(decoded)
	event.ExtraFields = extra
	return nil
}

type ErrorEvent struct {
	Type        EventType   `json:"type"`
	Error       APIError    `json:"error"`
	ExtraFields ExtraFields `json:"-"`
}

func (event ErrorEvent) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		event.Type = EventTypeError
	}
	type wire ErrorEvent
	return marshalWithExtra(wire(event), event.ExtraFields, "type", "error")
}

func (event *ErrorEvent) UnmarshalJSON(data []byte) error {
	type wire ErrorEvent
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "error")
	if err != nil {
		return err
	}
	*event = ErrorEvent(decoded)
	event.ExtraFields = extra
	return nil
}

// APIError is Anthropic's stream error payload. Error types remain open.
type APIError struct {
	Type        string      `json:"type"`
	Message     string      `json:"message"`
	ExtraFields ExtraFields `json:"-"`
}

func (apiError APIError) Error() string {
	if apiError.Type == "" {
		return apiError.Message
	}
	if apiError.Message == "" {
		return apiError.Type
	}
	return apiError.Type + ": " + apiError.Message
}

func (apiError APIError) MarshalJSON() ([]byte, error) {
	type wire APIError
	return marshalWithExtra(wire(apiError), apiError.ExtraFields, "type", "message")
}

func (apiError *APIError) UnmarshalJSON(data []byte) error {
	type wire APIError
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "message")
	if err != nil {
		return err
	}
	*apiError = APIError(decoded)
	apiError.ExtraFields = extra
	return nil
}

type UnknownEvent struct {
	Type EventType
	Raw  json.RawMessage
}

// MessageDelta contains the response-wide fields sent near the end of a
// stream. Null pointers preserve Anthropic's explicit null values.
type MessageDelta struct {
	Container    json.RawMessage `json:"container"`
	StopReason   *StopReason     `json:"stop_reason"`
	StopSequence *string         `json:"stop_sequence"`
	StopDetails  *StopDetails    `json:"stop_details"`
	ExtraFields  ExtraFields     `json:"-"`
}

func (delta MessageDelta) MarshalJSON() ([]byte, error) {
	type wire MessageDelta
	return marshalWithExtra(wire(delta), delta.ExtraFields, "container", "stop_reason", "stop_sequence", "stop_details")
}

func (delta *MessageDelta) UnmarshalJSON(data []byte) error {
	type wire MessageDelta
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "container", "stop_reason", "stop_sequence", "stop_details")
	if err != nil {
		return err
	}
	*delta = MessageDelta(decoded)
	delta.Container = cloneRaw(delta.Container)
	delta.ExtraFields = extra
	return nil
}

// DeltaUsage is the cumulative usage shape sent by message_delta.
type DeltaUsage struct {
	InputTokens              *int64               `json:"input_tokens"`
	OutputTokens             int64                `json:"output_tokens"`
	CacheCreationInputTokens *int64               `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64               `json:"cache_read_input_tokens"`
	OutputTokensDetails      *OutputTokensDetails `json:"output_tokens_details"`
	ServerToolUse            *ServerToolUsage     `json:"server_tool_use"`
	ExtraFields              ExtraFields          `json:"-"`
}

func (usage DeltaUsage) MarshalJSON() ([]byte, error) {
	type wire DeltaUsage
	return marshalWithExtra(wire(usage), usage.ExtraFields,
		"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens",
		"output_tokens_details", "server_tool_use")
}

func (usage *DeltaUsage) UnmarshalJSON(data []byte) error {
	type wire DeltaUsage
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded,
		"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens",
		"output_tokens_details", "server_tool_use")
	if err != nil {
		return err
	}
	*usage = DeltaUsage(decoded)
	usage.ExtraFields = extra
	return nil
}

// DeltaType is an open content-block delta discriminator.
type DeltaType string

const (
	DeltaTypeText      DeltaType = "text_delta"
	DeltaTypeInputJSON DeltaType = "input_json_delta"
	DeltaTypeCitations DeltaType = "citations_delta"
	DeltaTypeThinking  DeltaType = "thinking_delta"
	DeltaTypeSignature DeltaType = "signature_delta"
)

type ContentDelta struct {
	Type      DeltaType
	Text      *TextDelta
	InputJSON *InputJSONDelta
	Citations *CitationsDelta
	Thinking  *ThinkingDelta
	Signature *SignatureDelta
	Unknown   json.RawMessage
}

func (delta ContentDelta) MarshalJSON() ([]byte, error) {
	variants := 0
	for _, selected := range []bool{
		delta.Text != nil, delta.InputJSON != nil, delta.Citations != nil,
		delta.Thinking != nil, delta.Signature != nil, delta.Unknown != nil,
	} {
		if selected {
			variants++
		}
	}
	if variants != 1 {
		return nil, fmt.Errorf("%w: ContentDelta has %d variants", ErrInvalidUnion, variants)
	}
	switch {
	case delta.Text != nil:
		return json.Marshal(delta.Text)
	case delta.InputJSON != nil:
		return json.Marshal(delta.InputJSON)
	case delta.Citations != nil:
		return json.Marshal(delta.Citations)
	case delta.Thinking != nil:
		return json.Marshal(delta.Thinking)
	case delta.Signature != nil:
		return json.Marshal(delta.Signature)
	default:
		if !json.Valid(delta.Unknown) {
			return nil, fmt.Errorf("anthropic: unknown content delta contains invalid JSON")
		}
		return cloneRaw(delta.Unknown), nil
	}
}

func (delta *ContentDelta) UnmarshalJSON(data []byte) error {
	typeName, err := rawObjectType(data)
	if err != nil {
		return fmt.Errorf("anthropic: decode content delta discriminator: %w", err)
	}
	*delta = ContentDelta{Type: DeltaType(typeName)}
	switch delta.Type {
	case DeltaTypeText:
		delta.Text = new(TextDelta)
		err = json.Unmarshal(data, delta.Text)
	case DeltaTypeInputJSON:
		delta.InputJSON = new(InputJSONDelta)
		err = json.Unmarshal(data, delta.InputJSON)
	case DeltaTypeCitations:
		delta.Citations = new(CitationsDelta)
		err = json.Unmarshal(data, delta.Citations)
	case DeltaTypeThinking:
		delta.Thinking = new(ThinkingDelta)
		err = json.Unmarshal(data, delta.Thinking)
	case DeltaTypeSignature:
		delta.Signature = new(SignatureDelta)
		err = json.Unmarshal(data, delta.Signature)
	default:
		delta.Unknown = cloneRaw(data)
	}
	return err
}

type TextDelta struct {
	Type        DeltaType   `json:"type"`
	Text        string      `json:"text"`
	ExtraFields ExtraFields `json:"-"`
}

func (delta TextDelta) MarshalJSON() ([]byte, error) {
	if delta.Type == "" {
		delta.Type = DeltaTypeText
	}
	type wire TextDelta
	return marshalWithExtra(wire(delta), delta.ExtraFields, "type", "text")
}

func (delta *TextDelta) UnmarshalJSON(data []byte) error {
	type wire TextDelta
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "text")
	if err != nil {
		return err
	}
	*delta = TextDelta(decoded)
	delta.ExtraFields = extra
	return nil
}

type InputJSONDelta struct {
	Type        DeltaType   `json:"type"`
	PartialJSON string      `json:"partial_json"`
	ExtraFields ExtraFields `json:"-"`
}

func (delta InputJSONDelta) MarshalJSON() ([]byte, error) {
	if delta.Type == "" {
		delta.Type = DeltaTypeInputJSON
	}
	type wire InputJSONDelta
	return marshalWithExtra(wire(delta), delta.ExtraFields, "type", "partial_json")
}

func (delta *InputJSONDelta) UnmarshalJSON(data []byte) error {
	type wire InputJSONDelta
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "partial_json")
	if err != nil {
		return err
	}
	*delta = InputJSONDelta(decoded)
	delta.ExtraFields = extra
	return nil
}

type CitationsDelta struct {
	Type        DeltaType       `json:"type"`
	Citation    json.RawMessage `json:"citation"`
	ExtraFields ExtraFields     `json:"-"`
}

func (delta CitationsDelta) MarshalJSON() ([]byte, error) {
	if delta.Type == "" {
		delta.Type = DeltaTypeCitations
	}
	type wire CitationsDelta
	return marshalWithExtra(wire(delta), delta.ExtraFields, "type", "citation")
}

func (delta *CitationsDelta) UnmarshalJSON(data []byte) error {
	type wire CitationsDelta
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "citation")
	if err != nil {
		return err
	}
	*delta = CitationsDelta(decoded)
	delta.Citation = cloneRaw(delta.Citation)
	delta.ExtraFields = extra
	return nil
}

type ThinkingDelta struct {
	Type        DeltaType   `json:"type"`
	Thinking    string      `json:"thinking"`
	ExtraFields ExtraFields `json:"-"`
}

func (delta ThinkingDelta) MarshalJSON() ([]byte, error) {
	if delta.Type == "" {
		delta.Type = DeltaTypeThinking
	}
	type wire ThinkingDelta
	return marshalWithExtra(wire(delta), delta.ExtraFields, "type", "thinking")
}

func (delta *ThinkingDelta) UnmarshalJSON(data []byte) error {
	type wire ThinkingDelta
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "thinking")
	if err != nil {
		return err
	}
	*delta = ThinkingDelta(decoded)
	delta.ExtraFields = extra
	return nil
}

type SignatureDelta struct {
	Type        DeltaType   `json:"type"`
	Signature   string      `json:"signature"`
	ExtraFields ExtraFields `json:"-"`
}

func (delta SignatureDelta) MarshalJSON() ([]byte, error) {
	if delta.Type == "" {
		delta.Type = DeltaTypeSignature
	}
	type wire SignatureDelta
	return marshalWithExtra(wire(delta), delta.ExtraFields, "type", "signature")
}

func (delta *SignatureDelta) UnmarshalJSON(data []byte) error {
	type wire SignatureDelta
	var decoded wire
	extra, err := unmarshalWithExtra(data, &decoded, "type", "signature")
	if err != nil {
		return err
	}
	*delta = SignatureDelta(decoded)
	delta.ExtraFields = extra
	return nil
}

// DrainStream is a small protocol helper that applies all events from stream
// to accumulator. It is transport-independent and preserves the final event
// semantics; a premature io.EOF is reported by Accumulator.Result.
func DrainStream(stream Stream, accumulator *Accumulator) (*MessageResponse, error) {
	if accumulator == nil {
		accumulator = NewAccumulator()
	}
	for {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return accumulator.Result()
			}
			return accumulator.Message(), err
		}
		if err := accumulator.Add(event); err != nil {
			return accumulator.Message(), err
		}
	}
}
