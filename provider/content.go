package provider

import (
	"encoding/json"
	"strings"
)

// ContentToString extracts plain text from a Content field (string | []ContentPart | nil).
func ContentToString(content any) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []ContentPart:
		var sb strings.Builder
		for _, part := range v {
			if part.Type == "text" {
				sb.WriteString(part.Text)
			}
		}
		return sb.String()
	case []any:
		// JSON-decoded array — each element is map[string]any
		var sb strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, _ := m["type"].(string); t == "text" {
					if text, _ := m["text"].(string); text != "" {
						sb.WriteString(text)
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

// ContentToParts normalizes a Content field into a []ContentPart slice.
func ContentToParts(content any) []ContentPart {
	if content == nil {
		return nil
	}
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []ContentPart{{Type: "text", Text: v}}
	case []ContentPart:
		return v
	case []any:
		// JSON-decoded array
		var parts []ContentPart
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, _ := m["type"].(string)
			cc := m["cache_control"]
			switch t {
			case "text":
				text, _ := m["text"].(string)
				parts = append(parts, ContentPart{Type: "text", Text: text, CacheControl: cc})
			case "image_url":
				imgObj, _ := m["image_url"].(map[string]any)
				if imgObj != nil {
					url, _ := imgObj["url"].(string)
					detail, _ := imgObj["detail"].(string)
					parts = append(parts, ContentPart{
						Type:         "image_url",
						ImageURL:     &ImageURL{URL: url, Detail: detail},
						CacheControl: cc,
					})
				}
			case "input_audio":
				audObj, _ := m["input_audio"].(map[string]any)
				if audObj != nil {
					data, _ := audObj["data"].(string)
					format, _ := audObj["format"].(string)
					parts = append(parts, ContentPart{
						Type:         "input_audio",
						InputAudio:   &InputAudio{Data: data, Format: format},
						CacheControl: cc,
					})
				}
			case "file":
				fObj, _ := m["file"].(map[string]any)
				if fObj != nil {
					fileID, _ := fObj["file_id"].(string)
					fileData, _ := fObj["file_data"].(string)
					filename, _ := fObj["filename"].(string)
					mimeType, _ := fObj["mime_type"].(string)
					parts = append(parts, ContentPart{
						Type: "file",
						File: &FileContent{
							FileID:   fileID,
							FileData: fileData,
							Filename: filename,
							MimeType: mimeType,
						},
						CacheControl: cc,
					})
				}
			}
		}
		return parts
	}
	return nil
}

// HasImageParts checks if content contains any image_url parts.
func HasImageParts(content any) bool {
	parts := ContentToParts(content)
	for _, p := range parts {
		if p.Type == "image_url" {
			return true
		}
	}
	return false
}

// ParseDataURI parses a data URI (data:image/png;base64,iVBOR...) into media type and base64 data.
func ParseDataURI(uri string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(uri, "data:") {
		return "", "", false
	}
	// data:image/png;base64,iVBOR...
	rest := uri[5:] // after "data:"
	semicolonIdx := strings.Index(rest, ";")
	if semicolonIdx < 0 {
		return "", "", false
	}
	mediaType = rest[:semicolonIdx]
	rest = rest[semicolonIdx+1:]
	if !strings.HasPrefix(rest, "base64,") {
		return "", "", false
	}
	data = rest[7:] // after "base64,"
	return mediaType, data, true
}

// MarshalToolCallsForJSON converts tool calls to a JSON-friendly format for outbound
// message serialization. The `index` field is intentionally omitted: it is a streaming-
// delta concept and has no meaning when echoing a completed tool_call back to the API.
func MarshalToolCallsForJSON(calls []ToolCall) []map[string]any {
	if len(calls) == 0 {
		return nil
	}
	result := make([]map[string]any, len(calls))
	for i, tc := range calls {
		result[i] = map[string]any{
			"id":   tc.ID,
			"type": tc.Type,
			"function": map[string]any{
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			},
		}
	}
	return result
}

// MessageToMap converts a Message to map[string]any for JSON serialization in OpenAI-compatible providers.
func MessageToMap(msg Message) map[string]any {
	m := map[string]any{
		"role": msg.Role,
	}
	// OpenAI spec: assistant's content is required-nullable. Emit explicit null when
	// there is no text (e.g. assistant turns that only carry tool_calls).
	if msg.Role == "assistant" {
		m["content"] = msg.Content
	} else if msg.Content != nil {
		m["content"] = msg.Content
	}
	if len(msg.ToolCalls) > 0 {
		m["tool_calls"] = MarshalToolCallsForJSON(msg.ToolCalls)
	}
	if msg.ToolCallID != "" {
		m["tool_call_id"] = msg.ToolCallID
	}
	// `name` is only defined for system/user/assistant; tool messages use tool_call_id instead.
	if msg.Name != "" && msg.Role != "tool" {
		m["name"] = msg.Name
	}
	// reasoning_content is an assistant-only field. DeepSeek V3.1+ thinking mode requires
	// it on assistant turns that produced a tool_call; missing it returns 400.
	if msg.Role == "assistant" && msg.ReasoningContent != nil {
		m["reasoning_content"] = *msg.ReasoningContent
	}
	// reasoning_content_signature is consumed only by Anthropic's native builder
	// (where it gets paired with reasoning_content into a thinking block).
	// Compat providers ignore unknown fields, but the signature can be sizable
	// so omit it for non-assistant or empty values to keep wire bytes lean.
	if msg.Role == "assistant" && msg.ReasoningContentSignature != nil && *msg.ReasoningContentSignature != "" {
		m["reasoning_content_signature"] = *msg.ReasoningContentSignature
	}
	// refusal mirrors OpenAI's safety-refusal explanation. Echoing it back on
	// multi-turn keeps the conversation context coherent for OpenAI-aware
	// clients; other providers silently ignore the field.
	if msg.Role == "assistant" && msg.Refusal != nil && *msg.Refusal != "" {
		m["refusal"] = *msg.Refusal
	}
	// redacted_thinking carries Anthropic's auto-redacted thinking payloads.
	// Anthropic native uses Message.RedactedThinking directly via its own
	// builder, but for the OpenRouter → Anthropic path the message goes
	// through MessageToMap; without echoing this field, Anthropic returns
	// 400 ("missing thinking block") on the next turn.
	if msg.Role == "assistant" && len(msg.RedactedThinking) > 0 {
		m["redacted_thinking"] = msg.RedactedThinking
	}
	// Prefix is intentionally NOT emitted here. Each provider has its own
	// field name (DeepSeek: prefix, Kimi/Moonshot: partial) and emitting
	// either to the wrong upstream pollutes the wire with an unknown field —
	// some strict-schema providers may 4xx. The compat layer attaches the
	// right field name per provider via Config.PrefillFieldName; the
	// DeepSeek native builder injects "prefix" itself.
	return m
}

// DecodeToolCalls decodes tool_calls from a JSON-deserialized any value.
func DecodeToolCalls(v any) []ToolCall {
	if v == nil {
		return nil
	}
	// Try direct type first
	if calls, ok := v.([]ToolCall); ok {
		return calls
	}
	// Re-marshal and unmarshal for JSON-decoded arrays
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var calls []ToolCall
	if err := json.Unmarshal(data, &calls); err != nil {
		return nil
	}
	return calls
}
