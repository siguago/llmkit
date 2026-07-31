package anthropic

import "encoding/json"

// MessageText concatenates text blocks in their original content order.
// Thinking and tool blocks are intentionally excluded.
func MessageText(message *MessageResponse) string {
	if message == nil {
		return ""
	}
	var size int
	for _, block := range message.Content {
		if block.Text != nil {
			size += len(block.Text.Text)
		}
	}
	buffer := make([]byte, 0, size)
	for _, block := range message.Content {
		if block.Text != nil {
			buffer = append(buffer, block.Text.Text...)
		}
	}
	return string(buffer)
}

// ToolUses returns copies of tool_use blocks in their original content order.
func ToolUses(message *MessageResponse) []ToolUseBlock {
	if message == nil {
		return nil
	}
	var uses []ToolUseBlock
	for _, block := range message.Content {
		if block.ToolUse == nil {
			continue
		}
		copy := *block.ToolUse
		copy.Input = cloneRaw(copy.Input)
		copy.Caller = cloneRaw(copy.Caller)
		copy.ExtraFields = cloneExtras(copy.ExtraFields)
		if copy.CacheControl != nil {
			cache := *copy.CacheControl
			cache.TTL = cloneString(cache.TTL)
			cache.ExtraFields = cloneExtras(cache.ExtraFields)
			copy.CacheControl = &cache
		}
		uses = append(uses, copy)
	}
	return uses
}

// MessageText is also available as a response method.
func (message *MessageResponse) MessageText() string { return MessageText(message) }

// Text is a shorter alias for MessageText.
func (message *MessageResponse) Text() string { return MessageText(message) }

func (message *MessageResponse) ToolUses() []ToolUseBlock { return ToolUses(message) }

// ToolInput decodes a tool_use input without converting JSON numbers through
// float64. Callers can pass a struct, map[string]json.RawMessage, or any other
// json.Unmarshal target.
func ToolInput(toolUse ToolUseBlock, target any) error {
	return decodeUseNumber(toolUse.Input, target)
}

// RawToolInput returns a defensive copy of a tool input payload.
func RawToolInput(toolUse ToolUseBlock) json.RawMessage {
	return cloneRaw(toolUse.Input)
}
