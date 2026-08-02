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

	// A rejected event must leave the accumulator exactly as it was. apply
	// records how to undo each structural change it makes, so a failure part
	// way through cannot strand a half-applied event.
	var undo rollback
	if err := a.apply(event, &undo); err != nil {
		undo.run()
		return err
	}
	return nil
}

func (a *Accumulator) apply(event *Event, undo *rollback) error {
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
		// setOutputItem first: it can still reject the index, and dropping the
		// pending parts before that would discard state the caller keeps after
		// handling the error.
		if err := a.setOutputItem(event.OutputItem.OutputIndex, item, undo); err != nil {
			return err
		}
		if event.Type == EventTypeResponseOutputItemDone {
			a.clearPendingContentParts(event.OutputItem.OutputIndex)
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
					event.ContentPart.ItemID, part, undo,
				)
			}
			return nil
		}
		switch part.Type {
		case ContentTypeOutputText, ContentTypeRefusal:
			message, err := a.ensureMessage(event.ContentPart.OutputIndex, event.ContentPart.ItemID, undo)
			if err != nil {
				return err
			}
			if err := setContentPart(message, event.ContentPart.ContentIndex, part, undo); err != nil {
				return err
			}
			return a.flushPendingContentParts(event.ContentPart.OutputIndex, undo)
		case ContentTypeReasoningText:
			_, err := a.ensureReasoningPart(event.ContentPart.OutputIndex, event.ContentPart.ContentIndex, event.ContentPart.ItemID, false, undo)
			if err != nil {
				return err
			}
			reasoning := a.response.Output[event.ContentPart.OutputIndex].Reasoning
			replaceContentPartTracked(&reasoning.Content, event.ContentPart.ContentIndex, part, undo)
			return a.flushPendingContentParts(event.ContentPart.OutputIndex, undo)
		case ContentTypeSummaryText:
			_, err := a.ensureReasoningPart(event.ContentPart.OutputIndex, event.ContentPart.ContentIndex, event.ContentPart.ItemID, true, undo)
			if err != nil {
				return err
			}
			reasoning := a.response.Output[event.ContentPart.OutputIndex].Reasoning
			replaceContentPartTracked(&reasoning.Summary, event.ContentPart.ContentIndex, part, undo)
			return nil
		default:
			// A future part still occupies its content index. Keep it aside until
			// its Message or Reasoning owner is known, rather than guessing an
			// owner or creating a hole that makes the next known index fail.
			return a.stashContentPart(
				event.ContentPart.OutputIndex, event.ContentPart.ContentIndex,
				event.ContentPart.ItemID, part, undo,
			)
		}

	case EventTypeResponseOutputTextDelta:
		if event.OutputTextDelta == nil {
			return fmt.Errorf("responses: %s event has no text delta", event.Type)
		}
		if a.hasUnknownOutputItem(event.OutputTextDelta.OutputIndex) {
			return nil
		}
		part, err := a.ensureMessagePart(event.OutputTextDelta.OutputIndex, event.OutputTextDelta.ContentIndex, event.OutputTextDelta.ItemID, ContentTypeOutputText, undo)
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
		part, err := a.ensureMessagePart(event.OutputTextDone.OutputIndex, event.OutputTextDone.ContentIndex, event.OutputTextDone.ItemID, ContentTypeOutputText, undo)
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
		call, err := a.ensureFunctionCall(event.FunctionCallArgumentsDelta.OutputIndex, event.FunctionCallArgumentsDelta.ItemID, undo)
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
		call, err := a.ensureFunctionCall(event.FunctionCallArgumentsDone.OutputIndex, event.FunctionCallArgumentsDone.ItemID, undo)
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
		part, err := a.ensureMessagePart(event.RefusalDelta.OutputIndex, event.RefusalDelta.ContentIndex, event.RefusalDelta.ItemID, ContentTypeRefusal, undo)
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
		part, err := a.ensureMessagePart(event.RefusalDone.OutputIndex, event.RefusalDone.ContentIndex, event.RefusalDone.ItemID, ContentTypeRefusal, undo)
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
		part, err := a.ensureReasoningPart(event.ReasoningSummaryTextDelta.OutputIndex, event.ReasoningSummaryTextDelta.SummaryIndex, event.ReasoningSummaryTextDelta.ItemID, true, undo)
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
		part, err := a.ensureReasoningPart(event.ReasoningSummaryTextDone.OutputIndex, event.ReasoningSummaryTextDone.SummaryIndex, event.ReasoningSummaryTextDone.ItemID, true, undo)
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
		part, err := a.ensureReasoningPart(event.ReasoningTextDelta.OutputIndex, event.ReasoningTextDelta.ContentIndex, event.ReasoningTextDelta.ItemID, false, undo)
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
		part, err := a.ensureReasoningPart(event.ReasoningTextDone.OutputIndex, event.ReasoningTextDone.ContentIndex, event.ReasoningTextDone.ItemID, false, undo)
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

// rollback collects undo steps for mutations performed while handling one
// event.
//
// Every guarded mutation has an O(1) inverse: assignment, restoring a saved
// slice header after an append, or restoring a map entry. That makes "a rejected
// event changes nothing" affordable here. Snapshotting instead would deep-copy
// the response per event, and cloneItem goes through JSON, so the cost would
// scale with event count times response size.
//
// Leaving the growth in place is not a cosmetic problem. A placeholder appended
// for an event that is then rejected changes len(Output), so a later *valid*
// event for that same output_index takes the "already exists" branch instead of
// the "create" one.
type rollback []func()

func (r *rollback) add(undo func()) {
	if undo != nil {
		*r = append(*r, undo)
	}
}

func (r rollback) run() {
	for i := len(r) - 1; i >= 0; i-- {
		r[i]()
	}
}

func (a *Accumulator) ensureResponse() *Response {
	response, _ := a.ensureResponseTracked()
	return response
}

func (a *Accumulator) ensureResponseTracked() (*Response, func()) {
	if a.response != nil {
		return a.response, nil
	}
	a.response = &Response{
		Object:    "response",
		Status:    StatusInProgress,
		Output:    []Item{},
		RequestID: a.requestID,
	}
	return a.response, func() { a.response = nil }
}

func (a *Accumulator) setOutputItem(index int, item Item, undo *rollback) error {
	if index < 0 {
		return fmt.Errorf("responses: negative output_index %d", index)
	}
	response, undoResponse := a.ensureResponseTracked()
	undo.add(undoResponse)
	if index > len(response.Output) {
		return fmt.Errorf("responses: output_index %d skips current length %d", index, len(response.Output))
	}
	if index == len(response.Output) {
		previous := response.Output
		response.Output = append(response.Output, item)
		undo.add(func() { response.Output = previous })
	} else {
		previous := response.Output[index]
		response.Output[index] = item
		undo.add(func() { response.Output[index] = previous })
	}
	// flush can still reject this event, so the writes above must be undoable.
	return a.flushPendingContentParts(index, undo)
}

func (a *Accumulator) ensureMessage(index int, itemID string, undo *rollback) (*Message, error) {
	item, err := a.ensureOutputSlot(index, undo)
	if err != nil {
		return nil, err
	}
	if item.Message == nil {
		if !isPlaceholderItem(*item) {
			return nil, fmt.Errorf("responses: output[%d] is %q, not a message", index, item.Type)
		}
		previous := *item
		*item = NewMessageItem(Message{
			ID: itemID, Role: "assistant", Status: StatusInProgress,
			Content: NewPartContent(),
		})
		undo.add(func() { *item = previous })
	} else if err := matchItemID(item.Message.ID, itemID, index); err != nil {
		return nil, err
	}
	if item.Message.ID == "" {
		message := item.Message
		previousID := message.ID
		item.Message.ID = itemID
		undo.add(func() { message.ID = previousID })
	}
	if item.Message.Content.Parts == nil {
		if item.Message.Content.Text != nil || len(item.Message.Content.Raw) > 0 {
			return nil, fmt.Errorf("responses: output[%d] message content is not a part array", index)
		}
		message := item.Message
		message.Content.Parts = []ContentPart{}
		undo.add(func() { message.Content.Parts = nil })
	}
	if err := a.flushPendingContentParts(index, undo); err != nil {
		return nil, err
	}
	return item.Message, nil
}

func (a *Accumulator) ensureMessagePart(outputIndex, contentIndex int, itemID, contentType string, undo *rollback) (*ContentPart, error) {
	message, err := a.ensureMessage(outputIndex, itemID, undo)
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
		previous := message.Content.Parts
		message.Content.Parts = append(message.Content.Parts, ContentPart{})
		undo.add(func() { message.Content.Parts = previous })
	}
	parts := &message.Content.Parts
	part := &(*parts)[contentIndex]
	if isPlaceholderPart(*part) {
		var replacement ContentPart
		switch contentType {
		case ContentTypeOutputText:
			replacement = NewOutputTextPart("")
		case ContentTypeRefusal:
			replacement = NewRefusalPart("")
		default:
			return nil, fmt.Errorf("responses: unsupported message content type %q", contentType)
		}
		part = replaceContentPartTracked(parts, contentIndex, replacement, undo)
	}
	if part.Type != contentType {
		return nil, fmt.Errorf("responses: output[%d].content[%d] is %q, want %q", outputIndex, contentIndex, part.Type, contentType)
	}
	if err := a.flushPendingContentParts(outputIndex, undo); err != nil {
		return nil, err
	}
	return &message.Content.Parts[contentIndex], nil
}

func (a *Accumulator) ensureFunctionCall(index int, itemID string, undo *rollback) (*FunctionCall, error) {
	item, err := a.ensureOutputSlot(index, undo)
	if err != nil {
		return nil, err
	}
	if item.FunctionCall == nil {
		if !isPlaceholderItem(*item) {
			return nil, fmt.Errorf("responses: output[%d] is %q, not a function call", index, item.Type)
		}
		previous := *item
		*item = NewFunctionCallItem(FunctionCall{ID: itemID, Status: StatusInProgress})
		undo.add(func() { *item = previous })
	} else if err := matchItemID(item.FunctionCall.ID, itemID, index); err != nil {
		// Only a pre-existing call can disagree: the branch above seeds the ID
		// from itemID. Checking after the assignment would mean a rejected
		// event had already overwritten the output slot.
		return nil, err
	}
	if item.FunctionCall.ID == "" {
		call := item.FunctionCall
		previousID := call.ID
		item.FunctionCall.ID = itemID
		undo.add(func() { call.ID = previousID })
	}
	return item.FunctionCall, nil
}

func (a *Accumulator) ensureReasoningPart(outputIndex, partIndex int, itemID string, summary bool, undo *rollback) (*ContentPart, error) {
	item, err := a.ensureOutputSlot(outputIndex, undo)
	if err != nil {
		return nil, err
	}
	if item.Reasoning == nil {
		if !isPlaceholderItem(*item) {
			return nil, fmt.Errorf("responses: output[%d] is %q, not reasoning", outputIndex, item.Type)
		}
		previous := *item
		*item = NewReasoningItem(Reasoning{ID: itemID, Status: StatusInProgress})
		undo.add(func() { *item = previous })
	} else if err := matchItemID(item.Reasoning.ID, itemID, outputIndex); err != nil {
		return nil, err
	}
	if item.Reasoning.ID == "" {
		reasoning := item.Reasoning
		previousID := reasoning.ID
		item.Reasoning.ID = itemID
		undo.add(func() { reasoning.ID = previousID })
	}
	if !summary {
		if err := a.flushPendingContentParts(outputIndex, undo); err != nil {
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
		previous := *parts
		*parts = append(*parts, ContentPart{})
		undo.add(func() { *parts = previous })
	}
	part := &(*parts)[partIndex]
	if isPlaceholderPart(*part) {
		var replacement ContentPart
		if summary {
			replacement = NewSummaryTextPart("")
		} else {
			replacement = NewReasoningTextPart("")
		}
		part = replaceContentPartTracked(parts, partIndex, replacement, undo)
	}
	if part.Type != contentType {
		return nil, fmt.Errorf("responses: reasoning part %d is %q, want %q", partIndex, part.Type, contentType)
	}
	if !summary {
		if err := a.flushPendingContentParts(outputIndex, undo); err != nil {
			return nil, err
		}
		return &item.Reasoning.Content[partIndex], nil
	}
	return part, nil
}

func (a *Accumulator) stashContentPart(outputIndex, contentIndex int, itemID string, part ContentPart, undo *rollback) error {
	if outputIndex < 0 {
		return fmt.Errorf("responses: negative output_index %d", outputIndex)
	}
	if contentIndex < 0 {
		return fmt.Errorf("responses: negative content_index %d", contentIndex)
	}
	if a.pendingContentParts == nil {
		a.pendingContentParts = make(map[pendingContentPartKey]pendingContentPart)
		undo.add(func() { a.pendingContentParts = nil })
	}
	key := pendingContentPartKey{outputIndex: outputIndex, contentIndex: contentIndex}
	previous, existed := a.pendingContentParts[key]
	a.pendingContentParts[key] = pendingContentPart{itemID: itemID, part: part}
	undo.add(func() {
		if existed {
			a.pendingContentParts[key] = previous
			return
		}
		delete(a.pendingContentParts, key)
	})
	return a.flushPendingContentParts(outputIndex, undo)
}

func (a *Accumulator) flushPendingContentParts(outputIndex int, undo *rollback) error {
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
			message := item.Message
			message.Content.Parts = []ContentPart{}
			undo.add(func() { message.Content.Parts = nil })
		}
		parts = &item.Message.Content.Parts
	case item.Reasoning != nil:
		itemID = item.Reasoning.ID
		parts = &item.Reasoning.Content
	default:
		return nil
	}

	// Validate every pending part these loops will touch before mutating
	// anything. Both of them delete map entries and append to parts as they go,
	// so a matchItemID failure partway through would leave a partially applied
	// flush behind — and because Go randomizes map iteration order, which parts
	// survived would differ from run to run. The first loop only replaces
	// existing indexes, so len(*parts) is stable between the two passes.
	for key, pending := range a.pendingContentParts {
		if key.outputIndex != outputIndex || key.contentIndex >= len(*parts) {
			continue
		}
		if err := matchItemID(itemID, pending.itemID, outputIndex); err != nil {
			return err
		}
	}
	for index := len(*parts); ; index++ {
		pending, exists := a.pendingContentParts[pendingContentPartKey{
			outputIndex: outputIndex, contentIndex: index,
		}]
		if !exists {
			break
		}
		if err := matchItemID(itemID, pending.itemID, outputIndex); err != nil {
			return err
		}
	}

	for key, pending := range a.pendingContentParts {
		if key.outputIndex != outputIndex || key.contentIndex >= len(*parts) {
			continue
		}
		existing := &(*parts)[key.contentIndex]
		if isPlaceholderPart(*existing) || existing.Type == pending.part.Type {
			previous := *existing
			*existing = pending.part
			index := key.contentIndex
			undo.add(func() { (*parts)[index] = previous })
			delete(a.pendingContentParts, key)
			deletedKey, deletedPart := key, pending
			undo.add(func() { a.pendingContentParts[deletedKey] = deletedPart })
		}
	}

	for {
		key := pendingContentPartKey{outputIndex: outputIndex, contentIndex: len(*parts)}
		pending, exists := a.pendingContentParts[key]
		if !exists {
			break
		}
		previous := *parts
		*parts = append(*parts, pending.part)
		undo.add(func() { *parts = previous })
		delete(a.pendingContentParts, key)
		deletedKey, deletedPart := key, pending
		undo.add(func() { a.pendingContentParts[deletedKey] = deletedPart })
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

// ensureOutputSlot returns the slot and records how to undo whatever it had to
// create. Callers must run the rollback on every error path below this point.
func (a *Accumulator) ensureOutputSlot(index int, undo *rollback) (*Item, error) {
	if index < 0 {
		return nil, fmt.Errorf("responses: negative output_index %d", index)
	}
	response, undoResponse := a.ensureResponseTracked()
	undo.add(undoResponse)
	if index > len(response.Output) {
		return nil, fmt.Errorf("responses: output_index %d skips current length %d", index, len(response.Output))
	}
	if index == len(response.Output) {
		previous := response.Output
		response.Output = append(response.Output, Item{})
		undo.add(func() { response.Output = previous })
	}
	return &response.Output[index], nil
}

func setContentPart(message *Message, index int, part ContentPart, undo *rollback) error {
	if index < 0 {
		return fmt.Errorf("responses: negative content_index %d", index)
	}
	if index > len(message.Content.Parts) {
		return fmt.Errorf("responses: content_index %d skips current length %d", index, len(message.Content.Parts))
	}
	if index == len(message.Content.Parts) {
		previous := message.Content.Parts
		message.Content.Parts = append(message.Content.Parts, part)
		undo.add(func() { message.Content.Parts = previous })
		return nil
	}
	previous := message.Content.Parts[index]
	message.Content.Parts[index] = part
	undo.add(func() { message.Content.Parts[index] = previous })
	return nil
}

// replaceContentPartTracked locates the part through its owning slice both
// when applying and undoing the replacement. Capturing &slice[index] in the
// undo closure would become stale if a later append reallocates the slice.
func replaceContentPartTracked(parts *[]ContentPart, index int, part ContentPart, undo *rollback) *ContentPart {
	previous := (*parts)[index]
	(*parts)[index] = part
	undo.add(func() { (*parts)[index] = previous })
	return &(*parts)[index]
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
