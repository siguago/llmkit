package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	// ErrStreamNotStarted means no message_start event has been observed.
	ErrStreamNotStarted = errors.New("anthropic: stream has not started")
	// ErrStreamIncomplete means the stream ended before message_stop or error.
	ErrStreamIncomplete = errors.New("anthropic: stream ended before a terminal event")
	// ErrStreamState means events violated the Messages stream state machine.
	ErrStreamState = errors.New("anthropic: invalid stream state")
)

// Accumulator reconstructs a MessageResponse from native Messages SSE events.
// It supports interleaved content block indexes, delays tool JSON parsing until
// content_block_stop, and preserves thinking signatures and redacted data.
type Accumulator struct {
	mu sync.Mutex

	message          *MessageResponse
	requestID        string
	blocks           map[int]ContentBlock
	startedBlocks    map[int]bool
	stoppedBlocks    map[int]bool
	partialJSON      map[int][]byte
	unknownEvents    []json.RawMessage
	terminal         bool
	seenMessageDelta bool
	messageStop      bool
	streamError      *APIError
}

func NewAccumulator() *Accumulator {
	return &Accumulator{
		blocks:        make(map[int]ContentBlock),
		startedBlocks: make(map[int]bool),
		stoppedBlocks: make(map[int]bool),
		partialJSON:   make(map[int][]byte),
	}
}

// SetRequestID attaches transport response metadata to accumulated snapshots.
func (accumulator *Accumulator) SetRequestID(requestID string) {
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	accumulator.requestID = requestID
	if accumulator.message != nil {
		accumulator.message.RequestID = requestID
	}
}

// Add applies one event to the stream state machine.
func (accumulator *Accumulator) Add(event *Event) error {
	if event == nil {
		return fmt.Errorf("%w: nil event", ErrStreamState)
	}

	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.terminal {
		return fmt.Errorf("%w: event %q arrived after terminal event", ErrStreamState, event.Type)
	}

	switch event.Type {
	case EventTypeMessageStart:
		return accumulator.addMessageStart(event.MessageStart)
	case EventTypeContentBlockStart:
		return accumulator.addContentBlockStart(event.ContentBlockStart)
	case EventTypeContentBlockDelta:
		return accumulator.addContentBlockDelta(event.ContentBlockDelta)
	case EventTypeContentBlockStop:
		return accumulator.addContentBlockStop(event.ContentBlockStop)
	case EventTypeMessageDelta:
		return accumulator.addMessageDelta(event.MessageDelta)
	case EventTypeMessageStop:
		return accumulator.addMessageStop(event.MessageStop)
	case EventTypePing:
		if event.Ping == nil {
			return missingEventPayload(event.Type)
		}
		return nil
	case EventTypeError:
		if event.Error == nil {
			return missingEventPayload(event.Type)
		}
		apiError := event.Error.Error
		apiError.ExtraFields = cloneExtras(apiError.ExtraFields)
		accumulator.streamError = &apiError
		accumulator.terminal = true
		return nil
	default:
		if event.Raw != nil {
			accumulator.unknownEvents = append(accumulator.unknownEvents, cloneRaw(event.Raw))
		} else if event.Unknown != nil {
			accumulator.unknownEvents = append(accumulator.unknownEvents, cloneRaw(event.Unknown.Raw))
		}
		return nil
	}
}

// Apply is an alias for Add.
func (accumulator *Accumulator) Apply(event *Event) error { return accumulator.Add(event) }

func (accumulator *Accumulator) addMessageStart(event *MessageStartEvent) error {
	if event == nil {
		return missingEventPayload(EventTypeMessageStart)
	}
	if accumulator.message != nil {
		return fmt.Errorf("%w: duplicate message_start", ErrStreamState)
	}
	if len(event.Message.Content) != 0 {
		return fmt.Errorf("%w: message_start content must be empty", ErrStreamState)
	}
	message, err := cloneMessageResponse(&event.Message)
	if err != nil {
		return fmt.Errorf("anthropic: clone message_start: %w", err)
	}
	message.RequestID = accumulator.requestID
	message.Content = nil
	accumulator.message = message
	return nil
}

func (accumulator *Accumulator) addContentBlockStart(event *ContentBlockStartEvent) error {
	if event == nil {
		return missingEventPayload(EventTypeContentBlockStart)
	}
	if accumulator.message == nil {
		return fmt.Errorf("%w: content_block_start before message_start", ErrStreamState)
	}
	if accumulator.seenMessageDelta {
		return fmt.Errorf("%w: content_block_start after message_delta", ErrStreamState)
	}
	if event.Index < 0 {
		return fmt.Errorf("%w: negative content block index %d", ErrStreamState, event.Index)
	}
	if accumulator.startedBlocks[event.Index] {
		return fmt.Errorf("%w: duplicate content block index %d", ErrStreamState, event.Index)
	}
	block, err := cloneContentBlock(event.ContentBlock)
	if err != nil {
		return fmt.Errorf("anthropic: clone content block %d: %w", event.Index, err)
	}
	// A streamed server-side fallback keeps the originally requested model in
	// message_start. The fallback boundary identifies the model that actually
	// produces the following output, and the final hop must win.
	if block.Type == "fallback" && block.Raw != nil {
		var fallback struct {
			To struct {
				Model string `json:"model"`
			} `json:"to"`
		}
		if json.Unmarshal(block.Raw, &fallback) == nil && fallback.To.Model != "" {
			accumulator.message.Model = fallback.To.Model
		}
	}
	accumulator.blocks[event.Index] = block
	accumulator.startedBlocks[event.Index] = true
	return nil
}

func (accumulator *Accumulator) addContentBlockDelta(event *ContentBlockDeltaEvent) error {
	if event == nil {
		return missingEventPayload(EventTypeContentBlockDelta)
	}
	if accumulator.seenMessageDelta {
		return fmt.Errorf("%w: content_block_delta after message_delta", ErrStreamState)
	}
	block, ok := accumulator.blocks[event.Index]
	if !ok || !accumulator.startedBlocks[event.Index] {
		return fmt.Errorf("%w: delta for unstarted content block %d", ErrStreamState, event.Index)
	}
	if accumulator.stoppedBlocks[event.Index] {
		return fmt.Errorf("%w: delta for stopped content block %d", ErrStreamState, event.Index)
	}

	switch event.Delta.Type {
	case DeltaTypeText:
		if event.Delta.Text == nil {
			return missingDeltaPayload(event.Delta.Type)
		}
		if block.Text == nil {
			return deltaBlockMismatch(event.Index, event.Delta.Type, block.Type)
		}
		block.Text.Text += event.Delta.Text.Text
	case DeltaTypeInputJSON:
		if event.Delta.InputJSON == nil {
			return missingDeltaPayload(event.Delta.Type)
		}
		// Future and server-side tool blocks are intentionally represented by
		// Raw, but Anthropic streams their input with the same delta type as a
		// client tool_use block.
		if block.ToolUse == nil && block.Raw == nil {
			return deltaBlockMismatch(event.Index, event.Delta.Type, block.Type)
		}
		accumulator.partialJSON[event.Index] = append(
			accumulator.partialJSON[event.Index], event.Delta.InputJSON.PartialJSON...,
		)
	case DeltaTypeCitations:
		if event.Delta.Citations == nil {
			return missingDeltaPayload(event.Delta.Type)
		}
		if block.Text == nil {
			return deltaBlockMismatch(event.Index, event.Delta.Type, block.Type)
		}
		if err := appendCitation(&block.Text.Citations, event.Delta.Citations.Citation); err != nil {
			return fmt.Errorf("anthropic: append citation for block %d: %w", event.Index, err)
		}
	case DeltaTypeThinking:
		if event.Delta.Thinking == nil {
			return missingDeltaPayload(event.Delta.Type)
		}
		if block.Thinking == nil {
			return deltaBlockMismatch(event.Index, event.Delta.Type, block.Type)
		}
		block.Thinking.Thinking += event.Delta.Thinking.Thinking
	case DeltaTypeSignature:
		if event.Delta.Signature == nil {
			return missingDeltaPayload(event.Delta.Type)
		}
		if block.Thinking == nil {
			return deltaBlockMismatch(event.Index, event.Delta.Type, block.Type)
		}
		block.Thinking.Signature += event.Delta.Signature.Signature
	default:
		if event.Delta.Unknown != nil {
			if block.Raw != nil && block.Type == "compaction" && event.Delta.Type == "compaction_delta" {
				var delta map[string]json.RawMessage
				if err := json.Unmarshal(event.Delta.Unknown, &delta); err != nil {
					return fmt.Errorf("anthropic: decode compaction delta for block %d: %w", event.Index, err)
				}
				content, hasContent := delta["content"]
				if !hasContent || !json.Valid(content) {
					return fmt.Errorf("anthropic: compaction delta for block %d has invalid content", event.Index)
				}
				updates := map[string]json.RawMessage{"content": content}
				if encryptedContent, exists := delta["encrypted_content"]; exists {
					if !json.Valid(encryptedContent) {
						return fmt.Errorf("anthropic: compaction delta for block %d has invalid encrypted_content", event.Index)
					}
					updates["encrypted_content"] = encryptedContent
				}
				updated, err := setRawObjectFields(block.Raw, updates)
				if err != nil {
					return fmt.Errorf("anthropic: update compaction block %d: %w", event.Index, err)
				}
				block.Raw = updated
			}
			accumulator.unknownEvents = append(accumulator.unknownEvents, cloneRaw(event.Delta.Unknown))
		}
	}
	accumulator.blocks[event.Index] = block
	return nil
}

func (accumulator *Accumulator) addContentBlockStop(event *ContentBlockStopEvent) error {
	if event == nil {
		return missingEventPayload(EventTypeContentBlockStop)
	}
	if accumulator.seenMessageDelta {
		return fmt.Errorf("%w: content_block_stop after message_delta", ErrStreamState)
	}
	block, ok := accumulator.blocks[event.Index]
	if !ok || !accumulator.startedBlocks[event.Index] {
		return fmt.Errorf("%w: stop for unstarted content block %d", ErrStreamState, event.Index)
	}
	if accumulator.stoppedBlocks[event.Index] {
		return fmt.Errorf("%w: duplicate stop for content block %d", ErrStreamState, event.Index)
	}
	if partial, exists := accumulator.partialJSON[event.Index]; exists {
		block.PartialJSON = string(partial)
		trimmed := bytes.TrimSpace(partial)
		switch {
		case len(trimmed) == 0 || !json.Valid(trimmed):
			// Fine-grained tool streaming can end with a partial JSON value when
			// generation is interrupted or reaches max_tokens. Preserve the exact
			// fragments without turning a valid terminal stream into an error.
		case block.ToolUse != nil:
			block.ToolUse.Input = cloneRaw(trimmed)
		case block.Raw != nil:
			updated, err := setRawObjectField(block.Raw, "input", trimmed)
			if err != nil {
				return fmt.Errorf("anthropic: update streamed input for block %d: %w", event.Index, err)
			}
			block.Raw = updated
		default:
			return deltaBlockMismatch(event.Index, DeltaTypeInputJSON, block.Type)
		}
		accumulator.blocks[event.Index] = block
		delete(accumulator.partialJSON, event.Index)
	}
	accumulator.stoppedBlocks[event.Index] = true
	return nil
}

func (accumulator *Accumulator) addMessageDelta(event *MessageDeltaEvent) error {
	if event == nil {
		return missingEventPayload(EventTypeMessageDelta)
	}
	if accumulator.message == nil {
		return fmt.Errorf("%w: message_delta before message_start", ErrStreamState)
	}
	for index := range accumulator.startedBlocks {
		if !accumulator.stoppedBlocks[index] {
			return fmt.Errorf("%w: message_delta before content block %d stopped", ErrStreamState, index)
		}
	}
	accumulator.message.StopReason = cloneStopReason(event.Delta.StopReason)
	accumulator.message.StopSequence = cloneString(event.Delta.StopSequence)
	accumulator.message.StopDetails = cloneStopDetails(event.Delta.StopDetails)
	if event.Delta.Container != nil {
		accumulator.message.Container = cloneRaw(event.Delta.Container)
	}
	if contextManagement, exists := event.ExtraFields["context_management"]; exists {
		if accumulator.message.ExtraFields == nil {
			accumulator.message.ExtraFields = make(ExtraFields)
		}
		accumulator.message.ExtraFields["context_management"] = cloneRaw(contextManagement)
	}

	usage := &accumulator.message.Usage
	usage.OutputTokens = event.Usage.OutputTokens
	if event.Usage.InputTokens != nil {
		usage.InputTokens = *event.Usage.InputTokens
	}
	if event.Usage.CacheCreationInputTokens != nil {
		value := *event.Usage.CacheCreationInputTokens
		usage.CacheCreationInputTokens = &value
	}
	if event.Usage.CacheReadInputTokens != nil {
		value := *event.Usage.CacheReadInputTokens
		usage.CacheReadInputTokens = &value
	}
	if event.Usage.OutputTokensDetails != nil {
		copy := *event.Usage.OutputTokensDetails
		copy.ExtraFields = cloneExtras(copy.ExtraFields)
		usage.OutputTokensDetails = &copy
	}
	if event.Usage.ServerToolUse != nil {
		copy := *event.Usage.ServerToolUse
		copy.ExtraFields = cloneExtras(copy.ExtraFields)
		usage.ServerToolUse = &copy
	}
	if len(event.Usage.ExtraFields) != 0 {
		for key, value := range event.Usage.ExtraFields {
			switch key {
			case "cache_creation":
				var decoded *CacheCreation
				if err := json.Unmarshal(value, &decoded); err != nil {
					return fmt.Errorf("anthropic: decode message_delta usage cache_creation: %w", err)
				}
				usage.CacheCreation = decoded
			case "inference_geo":
				var decoded *string
				if err := json.Unmarshal(value, &decoded); err != nil {
					return fmt.Errorf("anthropic: decode message_delta usage inference_geo: %w", err)
				}
				usage.InferenceGeo = decoded
			case "service_tier":
				var decoded *string
				if err := json.Unmarshal(value, &decoded); err != nil {
					return fmt.Errorf("anthropic: decode message_delta usage service_tier: %w", err)
				}
				usage.ServiceTier = decoded
			default:
				if isUsageField(key) {
					return &ExtraFieldConflictError{Field: key}
				}
				if usage.ExtraFields == nil {
					usage.ExtraFields = make(ExtraFields)
				}
				usage.ExtraFields[key] = cloneRaw(value)
			}
		}
	}
	accumulator.seenMessageDelta = true
	return nil
}

func (accumulator *Accumulator) addMessageStop(event *MessageStopEvent) error {
	if event == nil {
		return missingEventPayload(EventTypeMessageStop)
	}
	if accumulator.message == nil {
		return fmt.Errorf("%w: message_stop before message_start", ErrStreamState)
	}
	for index := range accumulator.startedBlocks {
		if !accumulator.stoppedBlocks[index] {
			return fmt.Errorf("%w: message_stop before content block %d stopped", ErrStreamState, index)
		}
	}
	for index := 0; index < len(accumulator.startedBlocks); index++ {
		if !accumulator.startedBlocks[index] {
			return fmt.Errorf("%w: missing content block index %d", ErrStreamState, index)
		}
	}
	if !accumulator.seenMessageDelta {
		return fmt.Errorf("%w: message_stop before message_delta", ErrStreamState)
	}
	accumulator.messageStop = true
	accumulator.terminal = true
	return nil
}

// Message returns a deep copy of the current partial Message, or nil before
// message_start. It does not require a terminal event.
func (accumulator *Accumulator) Message() *MessageResponse {
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	return accumulator.snapshotLocked()
}

// FinalMessage returns a final successful Message or nil when the stream is
// incomplete or ended with an error.
func (accumulator *Accumulator) FinalMessage() *MessageResponse {
	message, err := accumulator.Result()
	if err != nil {
		return nil
	}
	return message
}

// Result returns the completed Message. A stream error is returned as
// *APIError; EOF before message_stop is ErrStreamIncomplete.
func (accumulator *Accumulator) Result() (*MessageResponse, error) {
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.message == nil {
		if accumulator.streamError != nil {
			return nil, cloneAPIError(accumulator.streamError)
		}
		return nil, ErrStreamNotStarted
	}
	message := accumulator.snapshotLocked()
	if accumulator.streamError != nil {
		return message, cloneAPIError(accumulator.streamError)
	}
	if !accumulator.terminal || !accumulator.messageStop {
		return message, ErrStreamIncomplete
	}
	return message, nil
}

// Complete reports whether message_stop has been observed successfully.
func (accumulator *Accumulator) Complete() bool {
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	return accumulator.messageStop && accumulator.streamError == nil
}

// UnknownEvents returns copies of unknown event and delta JSON observed while
// accumulating. Order matches arrival order.
func (accumulator *Accumulator) UnknownEvents() []json.RawMessage {
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	result := make([]json.RawMessage, len(accumulator.unknownEvents))
	for index, raw := range accumulator.unknownEvents {
		result[index] = cloneRaw(raw)
	}
	return result
}

func (accumulator *Accumulator) snapshotLocked() *MessageResponse {
	if accumulator.message == nil {
		return nil
	}
	message := copyMessageResponse(accumulator.message)
	indices := make([]int, 0, len(accumulator.blocks))
	for index := range accumulator.blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	message.Content = make([]ContentBlock, 0, len(indices))
	for _, index := range indices {
		block := copyContentBlock(accumulator.blocks[index])
		if partial, exists := accumulator.partialJSON[index]; exists {
			block.PartialJSON = string(partial)
		}
		message.Content = append(message.Content, block)
	}
	message.RequestID = accumulator.requestID
	return message
}

func isUsageField(field string) bool {
	for _, known := range usageFields {
		if field == known {
			return true
		}
	}
	return false
}

func missingEventPayload(eventType EventType) error {
	return fmt.Errorf("%w: event %q has no typed payload", ErrStreamState, eventType)
}

func missingDeltaPayload(deltaType DeltaType) error {
	return fmt.Errorf("%w: delta %q has no typed payload", ErrStreamState, deltaType)
}

func deltaBlockMismatch(index int, deltaType DeltaType, blockType ContentBlockType) error {
	return fmt.Errorf("%w: delta %q cannot apply to block %d of type %q", ErrStreamState, deltaType, index, blockType)
}

func appendCitation(target *json.RawMessage, citation json.RawMessage) error {
	if citation == nil || !json.Valid(citation) {
		return fmt.Errorf("invalid citation JSON")
	}
	var citations []json.RawMessage
	if existing := bytes.TrimSpace(*target); len(existing) != 0 && !bytes.Equal(existing, []byte("null")) {
		if err := json.Unmarshal(existing, &citations); err != nil {
			return err
		}
	}
	citations = append(citations, cloneRaw(citation))
	encoded, err := json.Marshal(citations)
	if err != nil {
		return err
	}
	*target = encoded
	return nil
}

func cloneMessageResponse(message *MessageResponse) (*MessageResponse, error) {
	if message == nil {
		return nil, nil
	}
	data, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	var cloned MessageResponse
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	cloned.RequestID = message.RequestID
	return &cloned, nil
}

func copyMessageResponse(message *MessageResponse) *MessageResponse {
	if message == nil {
		return nil
	}
	copy := *message
	if message.Content != nil {
		copy.Content = make([]ContentBlock, len(message.Content))
		for index, block := range message.Content {
			copy.Content[index] = copyContentBlock(block)
		}
	}
	copy.Container = cloneRaw(message.Container)
	copy.StopReason = cloneStopReason(message.StopReason)
	copy.StopSequence = cloneString(message.StopSequence)
	copy.StopDetails = cloneStopDetails(message.StopDetails)
	copy.Usage = cloneUsage(message.Usage)
	copy.ExtraFields = cloneExtras(message.ExtraFields)
	return &copy
}

func cloneUsage(usage Usage) Usage {
	copy := usage
	copy.CacheCreationInputTokens = cloneInt64(usage.CacheCreationInputTokens)
	copy.CacheReadInputTokens = cloneInt64(usage.CacheReadInputTokens)
	if usage.CacheCreation != nil {
		value := *usage.CacheCreation
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.CacheCreation = &value
	}
	copy.InferenceGeo = cloneString(usage.InferenceGeo)
	if usage.OutputTokensDetails != nil {
		value := *usage.OutputTokensDetails
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.OutputTokensDetails = &value
	}
	if usage.ServerToolUse != nil {
		value := *usage.ServerToolUse
		value.ExtraFields = cloneExtras(value.ExtraFields)
		copy.ServerToolUse = &value
	}
	copy.ServiceTier = cloneString(usage.ServiceTier)
	copy.ExtraFields = cloneExtras(usage.ExtraFields)
	return copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStopReason(reason *StopReason) *StopReason {
	if reason == nil {
		return nil
	}
	copy := *reason
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStopDetails(details *StopDetails) *StopDetails {
	if details == nil {
		return nil
	}
	copy := *details
	copy.Category = cloneString(copy.Category)
	copy.Explanation = cloneString(copy.Explanation)
	copy.ExtraFields = cloneExtras(copy.ExtraFields)
	return &copy
}

func cloneAPIError(apiError *APIError) *APIError {
	if apiError == nil {
		return nil
	}
	copy := *apiError
	copy.ExtraFields = cloneExtras(copy.ExtraFields)
	return &copy
}
