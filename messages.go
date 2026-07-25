package llmkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/siguago/llmkit/provider"
)

// ---------------------------------------------------------------- messages

// System builds a system message.
func System(text string) Message {
	return Message{Role: RoleSystem, Content: text}
}

// User builds a plain-text user message.
func User(text string) Message {
	return Message{Role: RoleUser, Content: text}
}

// Assistant builds a plain-text assistant message, for seeding conversation
// history on a multi-turn call.
func Assistant(text string) Message {
	return Message{Role: RoleAssistant, Content: text}
}

// ToolResult builds the message that answers a tool call. callID must be the
// ID from the ToolCall the model issued.
func ToolResult(callID, content string) Message {
	return Message{Role: RoleTool, ToolCallID: callID, Content: content}
}

// ToolResultJSON is ToolResult with a value marshaled to JSON. A marshaling
// failure is reported as the tool result content so the model can react to it
// rather than the caller silently sending an empty result.
func ToolResultJSON(callID string, v any) Message {
	data, err := json.Marshal(v)
	if err != nil {
		return ToolResult(callID, fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	return ToolResult(callID, string(data))
}

// UserWith builds a multimodal user message from content parts:
//
//	llmkit.UserWith(
//		llmkit.Text("What's in this image?"),
//		llmkit.Image("https://example.com/photo.jpg"),
//	)
func UserWith(parts ...ContentPart) Message {
	return Message{Role: RoleUser, Content: parts}
}

// Text builds a text content part.
func Text(s string) ContentPart {
	return ContentPart{Type: "text", Text: s}
}

// Image builds an image content part from an https URL or a data: URI.
func Image(url string) ContentPart {
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: url}}
}

// ImageDetail is Image with OpenAI's detail hint ("low", "high", "auto"),
// which trades image tokens against fidelity. Providers without the concept
// ignore it.
func ImageDetail(url, detail string) ContentPart {
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: url, Detail: detail}}
}

// ImageBytes builds an image content part from raw bytes, encoded as a data
// URI. mimeType is e.g. "image/png" or "image/jpeg".
func ImageBytes(mimeType string, data []byte) ContentPart {
	return Image(DataURI(mimeType, data))
}

// Audio builds an audio input part. format is "wav", "mp3", "flac", or "opus".
// data must be base64-encoded audio.
func Audio(format, base64Data string) ContentPart {
	return ContentPart{Type: "input_audio", InputAudio: &InputAudio{Data: base64Data, Format: format}}
}

// File builds an inline file part, e.g. a PDF for a document-capable model.
func File(filename, mimeType, base64Data string) ContentPart {
	return ContentPart{Type: "file", File: &FileContent{
		Filename: filename,
		MimeType: mimeType,
		FileData: base64Data,
	}}
}

// DataURI encodes bytes as a data: URI suitable for Image or an ImageURL.
func DataURI(mimeType string, data []byte) string {
	var sb strings.Builder
	sb.WriteString("data:")
	sb.WriteString(mimeType)
	sb.WriteString(";base64,")
	sb.WriteString(base64Encode(data))
	return sb.String()
}

// ---------------------------------------------------------------- tools

// NewTool builds a function tool definition. parameters is a JSON Schema
// object describing the arguments; pass nil for a no-argument tool.
//
//	llmkit.NewTool("get_weather", "Look up current weather", map[string]any{
//		"type": "object",
//		"properties": map[string]any{
//			"city": map[string]any{"type": "string"},
//		},
//		"required": []string{"city"},
//	})
func NewTool(name, description string, parameters any) Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}
}

// JSONSchemaFormat builds a ResponseFormat requesting output that conforms to
// the given JSON Schema. Providers that support strict schema adherence enforce
// it; others fall back to best-effort JSON.
func JSONSchemaFormat(name string, schema any) *ResponseFormat {
	strict := true
	return &ResponseFormat{
		Type: "json_schema",
		JSONSchema: &JSONSchemaSpec{
			Name:   name,
			Schema: schema,
			Strict: &strict,
		},
	}
}

// JSONFormat requests any valid JSON object, without a schema.
func JSONFormat() *ResponseFormat {
	return &ResponseFormat{Type: "json_object"}
}

// EnableThinking asks the model to reason before answering. budgetTokens caps
// the reasoning budget for providers that accept one (Anthropic); pass 0 to let
// the vendor decide.
func EnableThinking(budgetTokens int) *ThinkingConfig {
	return &ThinkingConfig{Type: "enabled", BudgetTokens: budgetTokens}
}

// DisableThinking turns off reasoning on models that default to it.
func DisableThinking() *ThinkingConfig {
	return &ThinkingConfig{Type: "disabled"}
}

// ---------------------------------------------------------------- responses

// ResponseText returns the assistant's text from a non-streaming response, or
// "" when the response carries no text (a tool-call-only turn, or a refusal).
func ResponseText(resp *ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	msg := resp.Choices[0].Message
	if msg == nil {
		return ""
	}
	return provider.ContentToString(msg.Content)
}

// ResponseReasoning returns the reasoning/thinking trace from a response, or ""
// when the model produced none.
func ResponseReasoning(resp *ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	msg := resp.Choices[0].Message
	if msg == nil || msg.ReasoningContent == nil {
		return ""
	}
	return *msg.ReasoningContent
}

// ResponseToolCalls returns the tool calls the model issued, if any.
func ResponseToolCalls(resp *ChatResponse) []ToolCall {
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		return nil
	}
	return resp.Choices[0].Message.ToolCalls
}

// ChunkText returns the text delta carried by a streaming chunk, or "" for
// chunks that carry only role, tool-call, or usage information.
func ChunkText(chunk *Chunk) string {
	if chunk == nil || len(chunk.Choices) == 0 {
		return ""
	}
	delta := chunk.Choices[0].Delta
	if delta == nil {
		return ""
	}
	return provider.ContentToString(delta.Content)
}

// ChunkReasoning returns the reasoning delta carried by a chunk, or "".
func ChunkReasoning(chunk *Chunk) string {
	if chunk == nil || len(chunk.Choices) == 0 {
		return ""
	}
	delta := chunk.Choices[0].Delta
	if delta == nil || delta.ReasoningContent == nil {
		return ""
	}
	return *delta.ReasoningContent
}

// ---------------------------------------------------------------- shortcuts

// Say is the one-liner: send a single user message, get the reply text back.
//
//	answer, err := client.Say(ctx, "deepseek-chat", "What is a monad?")
func (c *Client) Say(ctx context.Context, model, prompt string) (string, error) {
	resp, err := c.Chat(ctx, &ChatRequest{
		Model:    model,
		Messages: []Message{User(prompt)},
	})
	if err != nil {
		return "", err
	}
	return ResponseText(resp), nil
}

// SayWithSystem is Say with a system prompt.
func (c *Client) SayWithSystem(ctx context.Context, model, systemPrompt, prompt string) (string, error) {
	resp, err := c.Chat(ctx, &ChatRequest{
		Model:    model,
		Messages: []Message{System(systemPrompt), User(prompt)},
	})
	if err != nil {
		return "", err
	}
	return ResponseText(resp), nil
}

// StreamText streams a single-prompt completion, invoking onText for each text
// delta as it arrives. It returns the full concatenated text and the final
// usage, if the provider reported one.
//
// onText may be nil when you only want the assembled result.
func (c *Client) StreamText(ctx context.Context, model, prompt string, onText func(string)) (string, *Usage, error) {
	return c.StreamChat(ctx, &ChatRequest{
		Model:    model,
		Messages: []Message{User(prompt)},
	}, onText)
}

// StreamChat streams a full request, invoking onText for each text delta. It
// returns the concatenated text and the final usage.
//
// The stream is drained and closed before returning, so no cleanup is left to
// the caller. Use ChatStream directly when you need the raw chunks (tool call
// deltas, reasoning, media assets).
func (c *Client) StreamChat(ctx context.Context, req *ChatRequest, onText func(string)) (string, *Usage, error) {
	stream, err := c.ChatStream(ctx, req)
	if err != nil {
		return "", nil, err
	}
	defer stream.Close()

	var sb strings.Builder
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sb.String(), stream.GetUsage(), err
		}
		if text := ChunkText(chunk); text != "" {
			sb.WriteString(text)
			if onText != nil {
				onText(text)
			}
		}
	}
	return sb.String(), stream.GetUsage(), nil
}
