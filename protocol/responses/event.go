package responses

import (
	"encoding/json"
	"fmt"
)

// Stream is the transport-independent Responses streaming contract.
type Stream interface {
	Recv() (*Event, error)
	Close() error
	RequestID() string
	FinalResponse() *Response
}

// EventType is an open Responses SSE event discriminator.
type EventType string

const (
	EventTypeResponseCreated                   EventType = "response.created"
	EventTypeResponseQueued                    EventType = "response.queued"
	EventTypeResponseInProgress                EventType = "response.in_progress"
	EventTypeResponseCompleted                 EventType = "response.completed"
	EventTypeResponseFailed                    EventType = "response.failed"
	EventTypeResponseIncomplete                EventType = "response.incomplete"
	EventTypeResponseOutputItemAdded           EventType = "response.output_item.added"
	EventTypeResponseOutputItemDone            EventType = "response.output_item.done"
	EventTypeResponseContentPartAdded          EventType = "response.content_part.added"
	EventTypeResponseContentPartDone           EventType = "response.content_part.done"
	EventTypeResponseOutputTextDelta           EventType = "response.output_text.delta"
	EventTypeResponseOutputTextDone            EventType = "response.output_text.done"
	EventTypeResponseFunctionArgumentsDelta    EventType = "response.function_call_arguments.delta"
	EventTypeResponseFunctionArgumentsDone     EventType = "response.function_call_arguments.done"
	EventTypeResponseRefusalDelta              EventType = "response.refusal.delta"
	EventTypeResponseRefusalDone               EventType = "response.refusal.done"
	EventTypeResponseReasoningSummaryTextDelta EventType = "response.reasoning_summary_text.delta"
	EventTypeResponseReasoningSummaryTextDone  EventType = "response.reasoning_summary_text.done"
	EventTypeResponseReasoningTextDelta        EventType = "response.reasoning_text.delta"
	EventTypeResponseReasoningTextDone         EventType = "response.reasoning_text.done"
	EventTypeError                             EventType = "error"
)

// Event is a compact typed view of core Responses stream events. Type and
// SequenceNumber are common to every known event. Exactly one typed payload is
// selected according to Type. Unknown event types retain their complete JSON in
// Raw; known event extensions are retained in ExtraFields.
type Event struct {
	Type           EventType `json:"-"`
	SequenceNumber int64     `json:"-"`

	Response                   *Response                        `json:"-"`
	OutputItem                 *OutputItemEvent                 `json:"-"`
	ContentPart                *ContentPartEvent                `json:"-"`
	OutputTextDelta            *TextDeltaEvent                  `json:"-"`
	OutputTextDone             *TextDoneEvent                   `json:"-"`
	FunctionCallArgumentsDelta *FunctionCallArgumentsDeltaEvent `json:"-"`
	FunctionCallArgumentsDone  *FunctionCallArgumentsDoneEvent  `json:"-"`
	RefusalDelta               *RefusalDeltaEvent               `json:"-"`
	RefusalDone                *RefusalDoneEvent                `json:"-"`
	ReasoningSummaryTextDelta  *ReasoningSummaryTextDeltaEvent  `json:"-"`
	ReasoningSummaryTextDone   *ReasoningSummaryTextDoneEvent   `json:"-"`
	ReasoningTextDelta         *ReasoningTextDeltaEvent         `json:"-"`
	ReasoningTextDone          *ReasoningTextDoneEvent          `json:"-"`
	Error                      *ErrorEvent                      `json:"-"`

	ExtraFields ExtraFields     `json:"-"`
	Raw         json.RawMessage `json:"-"`
}

type OutputItemEvent struct {
	OutputIndex int  `json:"output_index"`
	Item        Item `json:"item"`
}

type ContentPartEvent struct {
	ItemID       string      `json:"item_id"`
	OutputIndex  int         `json:"output_index"`
	ContentIndex int         `json:"content_index"`
	Part         ContentPart `json:"part"`
}

type TextDeltaEvent struct {
	ItemID       string            `json:"item_id"`
	OutputIndex  int               `json:"output_index"`
	ContentIndex int               `json:"content_index"`
	Delta        string            `json:"delta"`
	Logprobs     []json.RawMessage `json:"logprobs"`
}

type TextDoneEvent struct {
	ItemID       string            `json:"item_id"`
	OutputIndex  int               `json:"output_index"`
	ContentIndex int               `json:"content_index"`
	Text         string            `json:"text"`
	Logprobs     []json.RawMessage `json:"logprobs"`
}

type FunctionCallArgumentsDeltaEvent struct {
	ItemID      string `json:"item_id"`
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
}

type FunctionCallArgumentsDoneEvent struct {
	ItemID      string `json:"item_id"`
	OutputIndex int    `json:"output_index"`
	Name        string `json:"name"`
	Arguments   string `json:"arguments"`
}

type RefusalDeltaEvent struct {
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta"`
}

type RefusalDoneEvent struct {
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	ContentIndex int    `json:"content_index"`
	Refusal      string `json:"refusal"`
}

type ReasoningSummaryTextDeltaEvent struct {
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	SummaryIndex int    `json:"summary_index"`
	Delta        string `json:"delta"`
}

type ReasoningSummaryTextDoneEvent struct {
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	SummaryIndex int    `json:"summary_index"`
	Text         string `json:"text"`
}

type ReasoningTextDeltaEvent struct {
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta"`
}

type ReasoningTextDoneEvent struct {
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	ContentIndex int    `json:"content_index"`
	Text         string `json:"text"`
}

type ErrorEvent struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Param   *string `json:"param"`
}

func (e ErrorEvent) MarshalJSON() ([]byte, error) {
	var code *string
	if e.Code != "" {
		value := e.Code
		code = &value
	}
	return json.Marshal(struct {
		Code    *string `json:"code"`
		Message string  `json:"message"`
		Param   *string `json:"param"`
	}{code, e.Message, e.Param})
}

func (e *ErrorEvent) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

var (
	responseEventFields   = reservedFields("type", "sequence_number", "response")
	outputItemEventFields = reservedFields(
		"type", "sequence_number", "output_index", "item",
	)
	contentPartEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "content_index", "part",
	)
	textDeltaEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "content_index", "delta", "logprobs",
	)
	textDoneEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "content_index", "text", "logprobs",
	)
	functionArgumentsDeltaEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "delta",
	)
	functionArgumentsDoneEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "name", "arguments",
	)
	refusalDeltaEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "content_index", "delta",
	)
	refusalDoneEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "content_index", "refusal",
	)
	reasoningSummaryDeltaEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "summary_index", "delta",
	)
	reasoningSummaryDoneEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "summary_index", "text",
	)
	reasoningTextDeltaEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "content_index", "delta",
	)
	reasoningTextDoneEventFields = reservedFields(
		"type", "sequence_number", "item_id", "output_index", "content_index", "text",
	)
	errorEventFields = reservedFields("type", "sequence_number", "code", "message", "param")
)

// ParseEvent decodes one SSE data payload.
func ParseEvent(data []byte) (*Event, error) {
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

func (event *Event) UnmarshalJSON(data []byte) error {
	if event == nil {
		return fmt.Errorf("responses: cannot unmarshal Event into nil receiver")
	}
	if err := requireJSONObject(data, "event"); err != nil {
		return err
	}
	typ, err := discriminator(data)
	if err != nil {
		return err
	}
	var header struct {
		SequenceNumber int64 `json:"sequence_number"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	headerType := EventType(typ)
	*event = Event{Type: headerType, SequenceNumber: header.SequenceNumber}

	var reserved map[string]struct{}
	switch headerType {
	case EventTypeResponseCreated, EventTypeResponseQueued, EventTypeResponseInProgress,
		EventTypeResponseCompleted, EventTypeResponseFailed, EventTypeResponseIncomplete:
		var wire struct {
			Response *Response `json:"response"`
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			return err
		}
		event.Response = wire.Response
		reserved = responseEventFields

	case EventTypeResponseOutputItemAdded, EventTypeResponseOutputItemDone:
		event.OutputItem = new(OutputItemEvent)
		if err := json.Unmarshal(data, event.OutputItem); err != nil {
			return err
		}
		reserved = outputItemEventFields

	case EventTypeResponseContentPartAdded, EventTypeResponseContentPartDone:
		event.ContentPart = new(ContentPartEvent)
		if err := json.Unmarshal(data, event.ContentPart); err != nil {
			return err
		}
		reserved = contentPartEventFields

	case EventTypeResponseOutputTextDelta:
		event.OutputTextDelta = new(TextDeltaEvent)
		if err := json.Unmarshal(data, event.OutputTextDelta); err != nil {
			return err
		}
		reserved = textDeltaEventFields

	case EventTypeResponseOutputTextDone:
		event.OutputTextDone = new(TextDoneEvent)
		if err := json.Unmarshal(data, event.OutputTextDone); err != nil {
			return err
		}
		reserved = textDoneEventFields

	case EventTypeResponseFunctionArgumentsDelta:
		event.FunctionCallArgumentsDelta = new(FunctionCallArgumentsDeltaEvent)
		if err := json.Unmarshal(data, event.FunctionCallArgumentsDelta); err != nil {
			return err
		}
		reserved = functionArgumentsDeltaEventFields

	case EventTypeResponseFunctionArgumentsDone:
		event.FunctionCallArgumentsDone = new(FunctionCallArgumentsDoneEvent)
		if err := json.Unmarshal(data, event.FunctionCallArgumentsDone); err != nil {
			return err
		}
		reserved = functionArgumentsDoneEventFields

	case EventTypeResponseRefusalDelta:
		event.RefusalDelta = new(RefusalDeltaEvent)
		if err := json.Unmarshal(data, event.RefusalDelta); err != nil {
			return err
		}
		reserved = refusalDeltaEventFields

	case EventTypeResponseRefusalDone:
		event.RefusalDone = new(RefusalDoneEvent)
		if err := json.Unmarshal(data, event.RefusalDone); err != nil {
			return err
		}
		reserved = refusalDoneEventFields

	case EventTypeResponseReasoningSummaryTextDelta:
		event.ReasoningSummaryTextDelta = new(ReasoningSummaryTextDeltaEvent)
		if err := json.Unmarshal(data, event.ReasoningSummaryTextDelta); err != nil {
			return err
		}
		reserved = reasoningSummaryDeltaEventFields

	case EventTypeResponseReasoningSummaryTextDone:
		event.ReasoningSummaryTextDone = new(ReasoningSummaryTextDoneEvent)
		if err := json.Unmarshal(data, event.ReasoningSummaryTextDone); err != nil {
			return err
		}
		reserved = reasoningSummaryDoneEventFields

	case EventTypeResponseReasoningTextDelta:
		event.ReasoningTextDelta = new(ReasoningTextDeltaEvent)
		if err := json.Unmarshal(data, event.ReasoningTextDelta); err != nil {
			return err
		}
		reserved = reasoningTextDeltaEventFields

	case EventTypeResponseReasoningTextDone:
		event.ReasoningTextDone = new(ReasoningTextDoneEvent)
		if err := json.Unmarshal(data, event.ReasoningTextDone); err != nil {
			return err
		}
		reserved = reasoningTextDoneEventFields

	case EventTypeError:
		event.Error = new(ErrorEvent)
		if err := json.Unmarshal(data, event.Error); err != nil {
			return err
		}
		reserved = errorEventFields

	default:
		event.Raw = cloneRaw(data)
		return nil
	}

	extra, err := decodeExtraFields(data, reserved)
	if err != nil {
		return err
	}
	event.ExtraFields = extra
	return nil
}

func (event Event) MarshalJSON() ([]byte, error) {
	if event.Type == "" {
		return nil, fmt.Errorf("%w: event has an empty type", ErrInvalidUnion)
	}
	count := variantCount(
		event.Response != nil, event.OutputItem != nil, event.ContentPart != nil,
		event.OutputTextDelta != nil, event.OutputTextDone != nil,
		event.FunctionCallArgumentsDelta != nil, event.FunctionCallArgumentsDone != nil,
		event.RefusalDelta != nil, event.RefusalDone != nil,
		event.ReasoningSummaryTextDelta != nil, event.ReasoningSummaryTextDone != nil,
		event.ReasoningTextDelta != nil, event.ReasoningTextDone != nil,
		event.Error != nil, len(event.Raw) > 0,
	)
	if count != 1 {
		return nil, fmt.Errorf("%w: Event has %d variants", ErrInvalidUnion, count)
	}
	if len(event.Raw) > 0 {
		if err := checkUnknownDiscriminator(event.Raw, string(event.Type)); err != nil {
			return nil, err
		}
		if isKnownEventType(event.Type) {
			return nil, fmt.Errorf("%w: known event type %q cannot use Raw", ErrInvalidUnion, event.Type)
		}
		return cloneRaw(event.Raw), nil
	}

	header := struct {
		Type           EventType `json:"type"`
		SequenceNumber int64     `json:"sequence_number"`
	}{event.Type, event.SequenceNumber}

	switch event.Type {
	case EventTypeResponseCreated, EventTypeResponseQueued, EventTypeResponseInProgress,
		EventTypeResponseCompleted, EventTypeResponseFailed, EventTypeResponseIncomplete:
		if event.Response == nil {
			break
		}
		return marshalObjectWithExtra(struct {
			Type           EventType `json:"type"`
			SequenceNumber int64     `json:"sequence_number"`
			Response       *Response `json:"response"`
		}{header.Type, header.SequenceNumber, event.Response}, event.ExtraFields, responseEventFields)

	case EventTypeResponseOutputItemAdded, EventTypeResponseOutputItemDone:
		if event.OutputItem != nil {
			return marshalEventPayload(header, *event.OutputItem, event.ExtraFields, outputItemEventFields)
		}
	case EventTypeResponseContentPartAdded, EventTypeResponseContentPartDone:
		if event.ContentPart != nil {
			return marshalEventPayload(header, *event.ContentPart, event.ExtraFields, contentPartEventFields)
		}
	case EventTypeResponseOutputTextDelta:
		if event.OutputTextDelta != nil {
			payload := *event.OutputTextDelta
			if payload.Logprobs == nil {
				payload.Logprobs = []json.RawMessage{}
			}
			return marshalEventPayload(header, payload, event.ExtraFields, textDeltaEventFields)
		}
	case EventTypeResponseOutputTextDone:
		if event.OutputTextDone != nil {
			payload := *event.OutputTextDone
			if payload.Logprobs == nil {
				payload.Logprobs = []json.RawMessage{}
			}
			return marshalEventPayload(header, payload, event.ExtraFields, textDoneEventFields)
		}
	case EventTypeResponseFunctionArgumentsDelta:
		if event.FunctionCallArgumentsDelta != nil {
			return marshalEventPayload(header, *event.FunctionCallArgumentsDelta, event.ExtraFields, functionArgumentsDeltaEventFields)
		}
	case EventTypeResponseFunctionArgumentsDone:
		if event.FunctionCallArgumentsDone != nil {
			return marshalEventPayload(header, *event.FunctionCallArgumentsDone, event.ExtraFields, functionArgumentsDoneEventFields)
		}
	case EventTypeResponseRefusalDelta:
		if event.RefusalDelta != nil {
			return marshalEventPayload(header, *event.RefusalDelta, event.ExtraFields, refusalDeltaEventFields)
		}
	case EventTypeResponseRefusalDone:
		if event.RefusalDone != nil {
			return marshalEventPayload(header, *event.RefusalDone, event.ExtraFields, refusalDoneEventFields)
		}
	case EventTypeResponseReasoningSummaryTextDelta:
		if event.ReasoningSummaryTextDelta != nil {
			return marshalEventPayload(header, *event.ReasoningSummaryTextDelta, event.ExtraFields, reasoningSummaryDeltaEventFields)
		}
	case EventTypeResponseReasoningSummaryTextDone:
		if event.ReasoningSummaryTextDone != nil {
			return marshalEventPayload(header, *event.ReasoningSummaryTextDone, event.ExtraFields, reasoningSummaryDoneEventFields)
		}
	case EventTypeResponseReasoningTextDelta:
		if event.ReasoningTextDelta != nil {
			return marshalEventPayload(header, *event.ReasoningTextDelta, event.ExtraFields, reasoningTextDeltaEventFields)
		}
	case EventTypeResponseReasoningTextDone:
		if event.ReasoningTextDone != nil {
			return marshalEventPayload(header, *event.ReasoningTextDone, event.ExtraFields, reasoningTextDoneEventFields)
		}
	case EventTypeError:
		if event.Error != nil {
			return marshalEventPayload(header, *event.Error, event.ExtraFields, errorEventFields)
		}
	}
	return nil, fmt.Errorf("%w: event type %q does not match its typed payload", ErrInvalidUnion, event.Type)
}

// marshalEventPayload combines the common event header with a payload object
// without converting raw extension values through interface{}.
func marshalEventPayload(header struct {
	Type           EventType `json:"type"`
	SequenceNumber int64     `json:"sequence_number"`
}, payload any, extra ExtraFields, reserved map[string]struct{}) ([]byte, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var fields ExtraFields
	if err := json.Unmarshal(headerJSON, &fields); err != nil {
		return nil, err
	}
	var payloadFields ExtraFields
	if err := json.Unmarshal(payloadJSON, &payloadFields); err != nil {
		return nil, err
	}
	for key, value := range payloadFields {
		fields[key] = value
	}
	return marshalObjectWithExtra(fields, extra, reserved)
}

func isKnownEventType(eventType EventType) bool {
	switch eventType {
	case EventTypeResponseCreated, EventTypeResponseQueued, EventTypeResponseInProgress,
		EventTypeResponseCompleted, EventTypeResponseFailed, EventTypeResponseIncomplete,
		EventTypeResponseOutputItemAdded, EventTypeResponseOutputItemDone,
		EventTypeResponseContentPartAdded, EventTypeResponseContentPartDone,
		EventTypeResponseOutputTextDelta, EventTypeResponseOutputTextDone,
		EventTypeResponseFunctionArgumentsDelta, EventTypeResponseFunctionArgumentsDone,
		EventTypeResponseRefusalDelta, EventTypeResponseRefusalDone,
		EventTypeResponseReasoningSummaryTextDelta, EventTypeResponseReasoningSummaryTextDone,
		EventTypeResponseReasoningTextDelta, EventTypeResponseReasoningTextDone,
		EventTypeError:
		return true
	default:
		return false
	}
}

// RawJSON returns an owned copy for an unknown event. Known events return nil.
func (event Event) RawJSON() json.RawMessage { return cloneRaw(event.Raw) }

// IsTerminalStatus reports whether status cannot transition back to active.
func IsTerminalStatus(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusIncomplete, StatusCancelled:
		return true
	default:
		return false
	}
}

func (response *Response) IsTerminal() bool {
	return response != nil && IsTerminalStatus(response.Status)
}

func (event *Event) IsTerminal() bool {
	if event == nil {
		return false
	}
	switch event.Type {
	case EventTypeResponseCompleted, EventTypeResponseFailed, EventTypeResponseIncomplete, EventTypeError:
		return true
	default:
		return false
	}
}

// TerminalResponse returns the terminal response snapshot carried by completed,
// failed, or incomplete events. Error events have no response snapshot.
func (event *Event) TerminalResponse() *Response {
	if event == nil {
		return nil
	}
	switch event.Type {
	case EventTypeResponseCompleted, EventTypeResponseFailed, EventTypeResponseIncomplete:
		return event.Response
	default:
		return nil
	}
}
