package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// ErrAccumulatorTerminal is returned when an event is added after a terminal
// completed, failed, incomplete, or error event.
var ErrAccumulatorTerminal = errors.New("responses: accumulator is already terminal")

// Accumulator reconstructs a Response from ordered stream events. A terminal
// lifecycle event's full Response snapshot is authoritative and replaces any
// partial state assembled from deltas.
type Accumulator struct {
	response            *Response
	requestID           string
	terminal            bool
	pendingContentParts map[pendingContentPartKey]pendingContentPart
}

type pendingContentPartKey struct {
	outputIndex  int
	contentIndex int
}

type pendingContentPart struct {
	itemID string
	part   ContentPart
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
		if event.Type == EventTypeResponseOutputItemDone {
			a.clearPendingContentParts(event.OutputItem.OutputIndex)
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
		if a.hasUnknownOutputItem(event.ContentPart.OutputIndex) {
			if !isKnownContentType(part.Type) {
				return a.stashContentPart(
					event.ContentPart.OutputIndex, event.ContentPart.ContentIndex,
					event.ContentPart.ItemID, part,
				)
			}
			return nil
		}
		switch part.Type {
		case ContentTypeOutputText, ContentTypeRefusal:
			message, err := a.ensureMessage(event.ContentPart.OutputIndex, event.ContentPart.ItemID)
			if err != nil {
				return err
			}
			if err := setContentPart(message, event.ContentPart.ContentIndex, part); err != nil {
				return err
			}
			return a.flushPendingContentParts(event.ContentPart.OutputIndex)
		case ContentTypeReasoningText:
			target, err := a.ensureReasoningPart(event.ContentPart.OutputIndex, event.ContentPart.ContentIndex, event.ContentPart.ItemID, false)
			if err != nil {
				return err
			}
			*target = part
			return a.flushPendingContentParts(event.ContentPart.OutputIndex)
		case ContentTypeSummaryText:
			target, err := a.ensureReasoningPart(event.ContentPart.OutputIndex, event.ContentPart.ContentIndex, event.ContentPart.ItemID, true)
			if err != nil {
				return err
			}
			*target = part
			return nil
		default:
			// A future part still occupies its content index. Keep it aside until
			// its Message or Reasoning owner is known, rather than guessing an
			// owner or creating a hole that makes the next known index fail.
			return a.stashContentPart(
				event.ContentPart.OutputIndex, event.ContentPart.ContentIndex,
				event.ContentPart.ItemID, part,
			)
		}

	case EventTypeResponseOutputTextDelta:
		if event.OutputTextDelta == nil {
			return fmt.Errorf("responses: %s event has no text delta", event.Type)
		}
		if a.hasUnknownOutputItem(event.OutputTextDelta.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.OutputTextDone.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.FunctionCallArgumentsDelta.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.FunctionCallArgumentsDone.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.RefusalDelta.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.RefusalDone.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.ReasoningSummaryTextDelta.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.ReasoningSummaryTextDone.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.ReasoningTextDelta.OutputIndex) {
			return nil
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
		if a.hasUnknownOutputItem(event.ReasoningTextDone.OutputIndex) {
			return nil
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
		response.Error = &Error{
			Code:        event.Error.Code,
			Message:     event.Error.Message,
			Param:       cloneStringPointer(event.Error.Param),
			ExtraFields: cloneExtraFields(event.ExtraFields),
		}
		a.pendingContentParts = nil
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
	restoreRawLeaves(reflect.ValueOf(source), reflect.ValueOf(&cloned))
	if source.RequestID != "" {
		cloned.RequestID = source.RequestID
	} else {
		cloned.RequestID = a.requestID
	}
	if a.requestID == "" {
		a.requestID = cloned.RequestID
	}
	a.response = &cloned
	a.pendingContentParts = nil
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
	} else {
		response.Output[index] = item
	}
	return a.flushPendingContentParts(index)
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
	if err := a.flushPendingContentParts(index); err != nil {
		return nil, err
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
	if err := a.flushPendingContentParts(outputIndex); err != nil {
		return nil, err
	}
	return &message.Content.Parts[contentIndex], nil
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
	if !summary {
		if err := a.flushPendingContentParts(outputIndex); err != nil {
			return nil, err
		}
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
	if !summary {
		if err := a.flushPendingContentParts(outputIndex); err != nil {
			return nil, err
		}
		return &item.Reasoning.Content[partIndex], nil
	}
	return part, nil
}

func (a *Accumulator) stashContentPart(outputIndex, contentIndex int, itemID string, part ContentPart) error {
	if outputIndex < 0 {
		return fmt.Errorf("responses: negative output_index %d", outputIndex)
	}
	if contentIndex < 0 {
		return fmt.Errorf("responses: negative content_index %d", contentIndex)
	}
	if a.pendingContentParts == nil {
		a.pendingContentParts = make(map[pendingContentPartKey]pendingContentPart)
	}
	key := pendingContentPartKey{outputIndex: outputIndex, contentIndex: contentIndex}
	a.pendingContentParts[key] = pendingContentPart{itemID: itemID, part: part}
	return a.flushPendingContentParts(outputIndex)
}

func (a *Accumulator) flushPendingContentParts(outputIndex int) error {
	if len(a.pendingContentParts) == 0 || a.response == nil ||
		outputIndex < 0 || outputIndex >= len(a.response.Output) {
		return nil
	}

	item := &a.response.Output[outputIndex]
	var (
		itemID string
		parts  *[]ContentPart
	)
	switch {
	case item.Message != nil:
		itemID = item.Message.ID
		if item.Message.Content.Parts == nil {
			if item.Message.Content.Text != nil || len(item.Message.Content.Raw) > 0 {
				return nil
			}
			item.Message.Content.Parts = []ContentPart{}
		}
		parts = &item.Message.Content.Parts
	case item.Reasoning != nil:
		itemID = item.Reasoning.ID
		parts = &item.Reasoning.Content
	default:
		return nil
	}

	for key, pending := range a.pendingContentParts {
		if key.outputIndex != outputIndex || key.contentIndex >= len(*parts) {
			continue
		}
		if err := matchItemID(itemID, pending.itemID, outputIndex); err != nil {
			return err
		}
		existing := &(*parts)[key.contentIndex]
		if isPlaceholderPart(*existing) || existing.Type == pending.part.Type {
			*existing = pending.part
			delete(a.pendingContentParts, key)
		}
	}

	for {
		key := pendingContentPartKey{outputIndex: outputIndex, contentIndex: len(*parts)}
		pending, exists := a.pendingContentParts[key]
		if !exists {
			break
		}
		if err := matchItemID(itemID, pending.itemID, outputIndex); err != nil {
			return err
		}
		*parts = append(*parts, pending.part)
		delete(a.pendingContentParts, key)
	}
	return nil
}

func (a *Accumulator) clearPendingContentParts(outputIndex int) {
	for key := range a.pendingContentParts {
		if key.outputIndex == outputIndex {
			delete(a.pendingContentParts, key)
		}
	}
}

func (a *Accumulator) hasUnknownOutputItem(outputIndex int) bool {
	return a.response != nil && outputIndex >= 0 && outputIndex < len(a.response.Output) &&
		len(a.response.Output[outputIndex].Raw) > 0
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
	if len(item.Raw) > 0 {
		if _, err := item.MarshalJSON(); err != nil {
			return Item{}, err
		}
		return Item{Type: item.Type, Raw: cloneRaw(item.Raw)}, nil
	}
	data, err := json.Marshal(item)
	if err != nil {
		return Item{}, err
	}
	var cloned Item
	if err := json.Unmarshal(data, &cloned); err != nil {
		return Item{}, err
	}
	restoreRawLeaves(reflect.ValueOf(item), reflect.ValueOf(&cloned))
	return cloned, nil
}

func cloneContentPart(part ContentPart) (ContentPart, error) {
	if len(part.Raw) > 0 {
		if _, err := part.MarshalJSON(); err != nil {
			return ContentPart{}, err
		}
		return ContentPart{Type: part.Type, Raw: cloneRaw(part.Raw)}, nil
	}
	data, err := json.Marshal(part)
	if err != nil {
		return ContentPart{}, err
	}
	var cloned ContentPart
	if err := json.Unmarshal(data, &cloned); err != nil {
		return ContentPart{}, err
	}
	restoreRawLeaves(reflect.ValueOf(part), reflect.ValueOf(&cloned))
	return cloned, nil
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// restoreRawLeaves overlays byte-exact JSON leaves from source onto a value
// that has already been deep-cloned through its public JSON representation.
// The JSON clone preserves nullable-field bookkeeping and union validation;
// this pass restores Raw and ExtraFields bytes that encoding/json compacts.
func restoreRawLeaves(source, target reflect.Value) {
	if !source.IsValid() || !target.IsValid() {
		return
	}
	if target.Kind() == reflect.Pointer {
		if source.Kind() == reflect.Pointer {
			if source.IsNil() || target.IsNil() {
				return
			}
			restoreRawLeaves(source.Elem(), target.Elem())
			return
		}
		if target.IsNil() {
			return
		}
		restoreRawLeaves(source, target.Elem())
		return
	}
	if source.Kind() == reflect.Pointer {
		if source.IsNil() {
			return
		}
		restoreRawLeaves(source.Elem(), target)
		return
	}
	if source.Type() != target.Type() {
		return
	}

	rawMessageType := reflect.TypeOf(json.RawMessage(nil))
	if source.Type() == rawMessageType {
		if target.CanSet() {
			target.Set(reflect.ValueOf(cloneRaw(source.Interface().(json.RawMessage))))
		}
		return
	}

	switch source.Kind() {
	case reflect.Struct:
		for index := 0; index < source.NumField(); index++ {
			if target.Field(index).CanSet() {
				restoreRawLeaves(source.Field(index), target.Field(index))
			}
		}
	case reflect.Slice, reflect.Array:
		length := source.Len()
		if target.Len() < length {
			length = target.Len()
		}
		for index := 0; index < length; index++ {
			restoreRawLeaves(source.Index(index), target.Index(index))
		}
	case reflect.Map:
		if source.Type().Key().Kind() != reflect.String || source.Type().Elem() != rawMessageType || !target.CanSet() {
			return
		}
		if source.IsNil() {
			target.SetZero()
			return
		}
		cloned := reflect.MakeMapWithSize(source.Type(), source.Len())
		iterator := source.MapRange()
		for iterator.Next() {
			raw := iterator.Value().Interface().(json.RawMessage)
			cloned.SetMapIndex(iterator.Key(), reflect.ValueOf(cloneRaw(raw)))
		}
		target.Set(cloned)
	}
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
