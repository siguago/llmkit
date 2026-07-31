package responses

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrAccumulatorTerminal is returned when an event is added after a terminal
// completed, failed, incomplete, or error event.
var ErrAccumulatorTerminal = errors.New("responses: accumulator is already terminal")

// Accumulator reconstructs a Response from ordered stream events. A terminal
// lifecycle event's full Response snapshot is authoritative and replaces any
// partial state assembled from deltas.
type Accumulator struct {
	response  *Response
	requestID string
	terminal  bool
}

// SetRequestID attaches response-header metadata to the current and future
// accumulated response. It is never serialized as JSON.
func (a *Accumulator) SetRequestID(requestID string) {
	if a == nil {
		return
	}
	a.requestID = requestID
	if a.response != nil {
		a.response.RequestID = requestID
	}
}

// Add incorporates one event. Events must be supplied in stream order.
func (a *Accumulator) Add(event *Event) error {
	if a == nil {
		return errors.New("responses: nil Accumulator")
	}
	if event == nil {
		return errors.New("responses: cannot accumulate a nil Event")
	}
	if a.terminal {
		return ErrAccumulatorTerminal
	}

	switch event.Type {
	case EventTypeResponseCreated, EventTypeResponseQueued, EventTypeResponseInProgress,
		EventTypeResponseCompleted, EventTypeResponseFailed, EventTypeResponseIncomplete:
		if event.Response == nil {
			return fmt.Errorf("responses: %s event has no response", event.Type)
		}
		if err := a.replaceResponse(event.Response); err != nil {
			return err
		}
		if event.IsTerminal() {
			a.terminal = true
		}
		return nil

	case EventTypeResponseOutputItemAdded, EventTypeResponseOutputItemDone:
		if event.OutputItem == nil {
			return fmt.Errorf("responses: %s event has no output item", event.Type)
		}
		item, err := cloneItem(event.OutputItem.Item)
		if err != nil {
			return fmt.Errorf("responses: clone output item: %w", err)
		}
		if err := a.setOutputItem(event.OutputItem.OutputIndex, item); err != nil {
			return err
		}
		return nil

	case EventTypeResponseContentPartAdded, EventTypeResponseContentPartDone:
		if event.ContentPart == nil {
			return fmt.Errorf("responses: %s event has no content part", event.Type)
		}
		part, err := cloneContentPart(event.ContentPart.Part)
		if err != nil {
			return fmt.Errorf("responses: clone content part: %w", err)
		}
		switch part.Type {
		case ContentTypeOutputText, ContentTypeRefusal:
			message, err := a.ensureMessage(event.ContentPart.OutputIndex, event.ContentPart.ItemID)
			if err != nil {
				return err
			}
			return setContentPart(message, event.ContentPart.ContentIndex, part)
		case ContentTypeReasoningText:
			target, err := a.ensureReasoningPart(event.ContentPart.OutputIndex, event.ContentPart.ContentIndex, event.ContentPart.ItemID, false)
			if err != nil {
				return err
			}
			*target = part
			return nil
		case ContentTypeSummaryText:
			target, err := a.ensureReasoningPart(event.ContentPart.OutputIndex, event.ContentPart.ContentIndex, event.ContentPart.ItemID, true)
			if err != nil {
				return err
			}
			*target = part
			return nil
		default:
			// A known boundary event can carry a future part type. Without
			// knowing its owning item shape, forcing it into Message would turn
			// a forward-compatible stream into a fatal type mismatch. The event
			// remains available to the caller in typed/Raw form.
			return nil
		}

	case EventTypeResponseOutputTextDelta:
		if event.OutputTextDelta == nil {
			return fmt.Errorf("responses: %s event has no text delta", event.Type)
		}
		part, err := a.ensureMessagePart(event.OutputTextDelta.OutputIndex, event.OutputTextDelta.ContentIndex, event.OutputTextDelta.ItemID, ContentTypeOutputText)
		if err != nil {
			return err
		}
		part.OutputText.Text += event.OutputTextDelta.Delta
		part.OutputText.Logprobs = append(part.OutputText.Logprobs, cloneRawSlice(event.OutputTextDelta.Logprobs)...)
		return nil

	case EventTypeResponseOutputTextDone:
		if event.OutputTextDone == nil {
			return fmt.Errorf("responses: %s event has no completed text", event.Type)
		}
		part, err := a.ensureMessagePart(event.OutputTextDone.OutputIndex, event.OutputTextDone.ContentIndex, event.OutputTextDone.ItemID, ContentTypeOutputText)
		if err != nil {
			return err
		}
		part.OutputText.Text = event.OutputTextDone.Text
		part.OutputText.Logprobs = cloneRawSlice(event.OutputTextDone.Logprobs)
		return nil

	case EventTypeResponseFunctionArgumentsDelta:
		if event.FunctionCallArgumentsDelta == nil {
			return fmt.Errorf("responses: %s event has no arguments delta", event.Type)
		}
		call, err := a.ensureFunctionCall(event.FunctionCallArgumentsDelta.OutputIndex, event.FunctionCallArgumentsDelta.ItemID)
		if err != nil {
			return err
		}
		call.Arguments += event.FunctionCallArgumentsDelta.Delta
		return nil

	case EventTypeResponseFunctionArgumentsDone:
		if event.FunctionCallArgumentsDone == nil {
			return fmt.Errorf("responses: %s event has no completed arguments", event.Type)
		}
		call, err := a.ensureFunctionCall(event.FunctionCallArgumentsDone.OutputIndex, event.FunctionCallArgumentsDone.ItemID)
		if err != nil {
			return err
		}
		call.Name = event.FunctionCallArgumentsDone.Name
		call.Arguments = event.FunctionCallArgumentsDone.Arguments
		return nil

	case EventTypeResponseRefusalDelta:
		if event.RefusalDelta == nil {
			return fmt.Errorf("responses: %s event has no refusal delta", event.Type)
		}
		part, err := a.ensureMessagePart(event.RefusalDelta.OutputIndex, event.RefusalDelta.ContentIndex, event.RefusalDelta.ItemID, ContentTypeRefusal)
		if err != nil {
			return err
		}
		part.Refusal.Refusal += event.RefusalDelta.Delta
		return nil

	case EventTypeResponseRefusalDone:
		if event.RefusalDone == nil {
			return fmt.Errorf("responses: %s event has no completed refusal", event.Type)
		}
		part, err := a.ensureMessagePart(event.RefusalDone.OutputIndex, event.RefusalDone.ContentIndex, event.RefusalDone.ItemID, ContentTypeRefusal)
		if err != nil {
			return err
		}
		part.Refusal.Refusal = event.RefusalDone.Refusal
		return nil

	case EventTypeResponseReasoningSummaryTextDelta:
		if event.ReasoningSummaryTextDelta == nil {
			return fmt.Errorf("responses: %s event has no reasoning summary delta", event.Type)
		}
		part, err := a.ensureReasoningPart(event.ReasoningSummaryTextDelta.OutputIndex, event.ReasoningSummaryTextDelta.SummaryIndex, event.ReasoningSummaryTextDelta.ItemID, true)
		if err != nil {
			return err
		}
		part.SummaryText.Text += event.ReasoningSummaryTextDelta.Delta
		return nil

	case EventTypeResponseReasoningSummaryTextDone:
		if event.ReasoningSummaryTextDone == nil {
			return fmt.Errorf("responses: %s event has no completed reasoning summary", event.Type)
		}
		part, err := a.ensureReasoningPart(event.ReasoningSummaryTextDone.OutputIndex, event.ReasoningSummaryTextDone.SummaryIndex, event.ReasoningSummaryTextDone.ItemID, true)
		if err != nil {
			return err
		}
		part.SummaryText.Text = event.ReasoningSummaryTextDone.Text
		return nil

	case EventTypeResponseReasoningTextDelta:
		if event.ReasoningTextDelta == nil {
			return fmt.Errorf("responses: %s event has no reasoning text delta", event.Type)
		}
		part, err := a.ensureReasoningPart(event.ReasoningTextDelta.OutputIndex, event.ReasoningTextDelta.ContentIndex, event.ReasoningTextDelta.ItemID, false)
		if err != nil {
			return err
		}
		part.ReasoningText.Text += event.ReasoningTextDelta.Delta
		return nil

	case EventTypeResponseReasoningTextDone:
		if event.ReasoningTextDone == nil {
			return fmt.Errorf("responses: %s event has no completed reasoning text", event.Type)
		}
		part, err := a.ensureReasoningPart(event.ReasoningTextDone.OutputIndex, event.ReasoningTextDone.ContentIndex, event.ReasoningTextDone.ItemID, false)
		if err != nil {
			return err
		}
		part.ReasoningText.Text = event.ReasoningTextDone.Text
		return nil

	case EventTypeError:
		if event.Error == nil {
			return errors.New("responses: error event has no error payload")
		}
		response := a.ensureResponse()
		response.Status = StatusFailed
		response.Error = &Error{Code: event.Error.Code, Message: event.Error.Message, Param: event.Error.Param}
		a.terminal = true
		return nil

	default:
		// Unknown events remain visible to the caller but cannot be assembled
		// safely without knowing their semantics.
		return nil
	}
}

// Response returns the current partial or terminal response. The returned
// pointer is owned by the Accumulator and is updated by later Add calls.
func (a *Accumulator) Response() *Response {
	if a == nil {
		return nil
	}
	return a.response
}

// FinalResponse is an alias for Response so an Accumulator can back a Stream's
// FinalResponse method while still exposing useful partial state after an
// interrupted stream.
func (a *Accumulator) FinalResponse() *Response { return a.Response() }

func (a *Accumulator) IsTerminal() bool { return a != nil && a.terminal }

func (a *Accumulator) replaceResponse(source *Response) error {
	data, err := json.Marshal(source)
	if err != nil {
		return fmt.Errorf("responses: clone response: %w", err)
	}
	var cloned Response
	if err := json.Unmarshal(data, &cloned); err != nil {
		return fmt.Errorf("responses: clone response: %w", err)
	}
	if source.RequestID != "" {
		cloned.RequestID = source.RequestID
	} else {
		cloned.RequestID = a.requestID
	}
	if a.requestID == "" {
		a.requestID = cloned.RequestID
	}
	a.response = &cloned
	return nil
}

func (a *Accumulator) ensureResponse() *Response {
	if a.response == nil {
		a.response = &Response{
			Object:    "response",
			Status:    StatusInProgress,
			Output:    []Item{},
			RequestID: a.requestID,
		}
	}
	return a.response
}

func (a *Accumulator) setOutputItem(index int, item Item) error {
	if index < 0 {
		return fmt.Errorf("responses: negative output_index %d", index)
	}
	response := a.ensureResponse()
	if index > len(response.Output) {
		return fmt.Errorf("responses: output_index %d skips current length %d", index, len(response.Output))
	}
	if index == len(response.Output) {
		response.Output = append(response.Output, item)
		return nil
	}
	response.Output[index] = item
	return nil
}

func (a *Accumulator) ensureMessage(index int, itemID string) (*Message, error) {
	item, err := a.ensureOutputSlot(index)
	if err != nil {
		return nil, err
	}
	if item.Message == nil {
		if !isPlaceholderItem(*item) {
			return nil, fmt.Errorf("responses: output[%d] is %q, not a message", index, item.Type)
		}
		*item = NewMessageItem(Message{
			ID: itemID, Role: "assistant", Status: StatusInProgress,
			Content: NewPartContent(),
		})
	}
	if err := matchItemID(item.Message.ID, itemID, index); err != nil {
		return nil, err
	}
	if item.Message.ID == "" {
		item.Message.ID = itemID
	}
	if item.Message.Content.Parts == nil {
		if item.Message.Content.Text != nil || len(item.Message.Content.Raw) > 0 {
			return nil, fmt.Errorf("responses: output[%d] message content is not a part array", index)
		}
		item.Message.Content.Parts = []ContentPart{}
	}
	return item.Message, nil
}

func (a *Accumulator) ensureMessagePart(outputIndex, contentIndex int, itemID, contentType string) (*ContentPart, error) {
	message, err := a.ensureMessage(outputIndex, itemID)
	if err != nil {
		return nil, err
	}
	if contentIndex < 0 {
		return nil, fmt.Errorf("responses: negative content_index %d", contentIndex)
	}
	if contentIndex > len(message.Content.Parts) {
		return nil, fmt.Errorf("responses: content_index %d skips current length %d", contentIndex, len(message.Content.Parts))
	}
	if contentIndex == len(message.Content.Parts) {
		message.Content.Parts = append(message.Content.Parts, ContentPart{})
	}
	part := &message.Content.Parts[contentIndex]
	if isPlaceholderPart(*part) {
		switch contentType {
		case ContentTypeOutputText:
			*part = NewOutputTextPart("")
		case ContentTypeRefusal:
			*part = NewRefusalPart("")
		default:
			return nil, fmt.Errorf("responses: unsupported message content type %q", contentType)
		}
	}
	if part.Type != contentType {
		return nil, fmt.Errorf("responses: output[%d].content[%d] is %q, want %q", outputIndex, contentIndex, part.Type, contentType)
	}
	return part, nil
}

func (a *Accumulator) ensureFunctionCall(index int, itemID string) (*FunctionCall, error) {
	item, err := a.ensureOutputSlot(index)
	if err != nil {
		return nil, err
	}
	if item.FunctionCall == nil {
		if !isPlaceholderItem(*item) {
			return nil, fmt.Errorf("responses: output[%d] is %q, not a function call", index, item.Type)
		}
		*item = NewFunctionCallItem(FunctionCall{ID: itemID, Status: StatusInProgress})
	}
	if err := matchItemID(item.FunctionCall.ID, itemID, index); err != nil {
		return nil, err
	}
	if item.FunctionCall.ID == "" {
		item.FunctionCall.ID = itemID
	}
	return item.FunctionCall, nil
}

func (a *Accumulator) ensureReasoningPart(outputIndex, partIndex int, itemID string, summary bool) (*ContentPart, error) {
	item, err := a.ensureOutputSlot(outputIndex)
	if err != nil {
		return nil, err
	}
	if item.Reasoning == nil {
		if !isPlaceholderItem(*item) {
			return nil, fmt.Errorf("responses: output[%d] is %q, not reasoning", outputIndex, item.Type)
		}
		*item = NewReasoningItem(Reasoning{ID: itemID, Status: StatusInProgress})
	}
	if err := matchItemID(item.Reasoning.ID, itemID, outputIndex); err != nil {
		return nil, err
	}
	if item.Reasoning.ID == "" {
		item.Reasoning.ID = itemID
	}
	if partIndex < 0 {
		return nil, fmt.Errorf("responses: negative reasoning part index %d", partIndex)
	}
	parts := &item.Reasoning.Content
	contentType := ContentTypeReasoningText
	if summary {
		parts = &item.Reasoning.Summary
		contentType = ContentTypeSummaryText
	}
	if partIndex > len(*parts) {
		return nil, fmt.Errorf("responses: reasoning part index %d skips current length %d", partIndex, len(*parts))
	}
	if partIndex == len(*parts) {
		*parts = append(*parts, ContentPart{})
	}
	part := &(*parts)[partIndex]
	if isPlaceholderPart(*part) {
		if summary {
			*part = NewSummaryTextPart("")
		} else {
			*part = NewReasoningTextPart("")
		}
	}
	if part.Type != contentType {
		return nil, fmt.Errorf("responses: reasoning part %d is %q, want %q", partIndex, part.Type, contentType)
	}
	return part, nil
}

func (a *Accumulator) ensureOutputSlot(index int) (*Item, error) {
	if index < 0 {
		return nil, fmt.Errorf("responses: negative output_index %d", index)
	}
	response := a.ensureResponse()
	if index > len(response.Output) {
		return nil, fmt.Errorf("responses: output_index %d skips current length %d", index, len(response.Output))
	}
	if index == len(response.Output) {
		response.Output = append(response.Output, Item{})
	}
	return &response.Output[index], nil
}

func setContentPart(message *Message, index int, part ContentPart) error {
	if index < 0 {
		return fmt.Errorf("responses: negative content_index %d", index)
	}
	if index > len(message.Content.Parts) {
		return fmt.Errorf("responses: content_index %d skips current length %d", index, len(message.Content.Parts))
	}
	if index == len(message.Content.Parts) {
		message.Content.Parts = append(message.Content.Parts, part)
		return nil
	}
	message.Content.Parts[index] = part
	return nil
}

func matchItemID(existing, incoming string, index int) error {
	if existing != "" && incoming != "" && existing != incoming {
		return fmt.Errorf("responses: output[%d] item ID %q does not match event item ID %q", index, existing, incoming)
	}
	return nil
}

func isPlaceholderItem(item Item) bool {
	return item.Type == "" && item.EasyMessage == nil && item.Message == nil && item.Reasoning == nil &&
		item.FunctionCall == nil && item.FunctionCallOutput == nil && len(item.Raw) == 0
}

func isPlaceholderPart(part ContentPart) bool {
	return part.Type == "" && part.InputText == nil && part.OutputText == nil &&
		part.Refusal == nil && part.InputImage == nil && part.InputFile == nil &&
		part.SummaryText == nil && part.ReasoningText == nil && len(part.Raw) == 0
}

func cloneItem(item Item) (Item, error) {
	data, err := json.Marshal(item)
	if err != nil {
		return Item{}, err
	}
	var cloned Item
	if err := json.Unmarshal(data, &cloned); err != nil {
		return Item{}, err
	}
	return cloned, nil
}

func cloneContentPart(part ContentPart) (ContentPart, error) {
	data, err := json.Marshal(part)
	if err != nil {
		return ContentPart{}, err
	}
	var cloned ContentPart
	if err := json.Unmarshal(data, &cloned); err != nil {
		return ContentPart{}, err
	}
	return cloned, nil
}

func cloneRawSlice(values []json.RawMessage) []json.RawMessage {
	if values == nil {
		return nil
	}
	cloned := make([]json.RawMessage, len(values))
	for index, value := range values {
		cloned[index] = cloneRaw(value)
	}
	return cloned
}
