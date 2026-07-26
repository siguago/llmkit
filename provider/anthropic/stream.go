package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/siguago/llmkit/provider"
)

// activeBlock tracks the state of a content block being streamed.
type activeBlock struct {
	blockType     string // "text", "tool_use", "thinking", "json_schema_content", "provider_turn"
	id            string // tool_use ID
	name          string // tool_use function name
	toolCallIndex int    // assigned OpenAI-style tool_call index (only for real tool_use)
	// providerRaw is set when blockType == "provider_turn" — the gateway
	// accumulates the full block here so it can be emitted verbatim on
	// content_block_stop. server_tool_use blocks fill `input_json_buffer`
	// from input_json_delta events, then parse it into providerRaw["input"].
	providerRaw map[string]any
	// inputJSONBuffer accumulates input_json_delta partial_json strings for
	// server_tool_use blocks. Parsed on stop to populate providerRaw["input"].
	inputJSONBuffer strings.Builder
}

type StreamReader struct {
	reader             io.ReadCloser
	scanner            *bufio.Scanner
	diag               provider.StreamDiagnostics
	usage              *provider.Usage
	msgID              string
	model              string
	activeBlocks       map[int]*activeBlock // Anthropic block index → block info
	toolCallIdx        int                  // next tool_call index to assign
	jsonSchemaToolName string               // if set, tool_use with this name is json_schema content
	hasRealToolCalls   bool                 // whether any non-json-schema tool_use blocks exist
}

func NewStreamReader(ctx context.Context, reader io.ReadCloser, jsonSchemaToolName string) *StreamReader {
	diag := provider.NewStreamDiagnostics(ctx, "anthropic")
	scanner := diag.NewScanner(reader)
	return &StreamReader{
		reader:             reader,
		scanner:            scanner,
		diag:               diag,
		usage:              &provider.Usage{},
		activeBlocks:       make(map[int]*activeBlock),
		jsonSchemaToolName: jsonSchemaToolName,
	}
}

func (s *StreamReader) Recv() (*provider.ChatCompletionChunk, error) {
	for s.scanner.Scan() {
		line := s.scanner.Text()

		if line == "" {
			continue
		}
		// Accept both "data: foo" and "data:foo" — some proxies strip the
		// optional space between SSE field name and value.
		var data string
		switch {
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		case strings.HasPrefix(line, "data:"):
			data = line[5:]
		default:
			continue
		}

		switch provider.ClassifyFrame(data) {
		case provider.FrameDone:
			// Anthropic ends with message_stop, but relays configured with
			// WithBaseURL routinely append [DONE] to look OpenAI-shaped.
			return nil, io.EOF
		case provider.FrameSkip:
			continue
		}

		var evt streamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			if err := s.diag.Malformed("event", err, data); err != nil {
				return nil, err
			}
			continue
		}

		switch evt.Type {
		case "message_start":
			var e messageStartEvent
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				if err := s.diag.Malformed("message_start", err, data); err != nil {
					return nil, err
				}
				continue
			}
			s.msgID = e.Message.ID
			s.model = e.Message.Model
			// Anthropic's input_tokens excludes cached portions. PromptTokens must
			// include cache reads/creation so total_tokens reconciles in clients.
			s.usage.PromptTokens = e.Message.Usage.totalPromptTokens()
			s.usage.CachedTokens = e.Message.Usage.CacheReadInputTokens
			if e.Message.Usage.CacheReadInputTokens > 0 {
				s.usage.PromptTokensDetails = &provider.PromptTokensDetails{
					CachedTokens: e.Message.Usage.CacheReadInputTokens,
				}
			}
			continue

		case "content_block_start":
			var e contentBlockStartEvent
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				if err := s.diag.Malformed("content_block_start", err, data); err != nil {
					return nil, err
				}
				continue
			}

			if e.ContentBlock.Type == "tool_use" && s.jsonSchemaToolName != "" && e.ContentBlock.Name == s.jsonSchemaToolName {
				// json_schema structured output — treat input as content text
				s.activeBlocks[e.Index] = &activeBlock{
					blockType: "json_schema_content",
					id:        e.ContentBlock.ID,
					name:      e.ContentBlock.Name,
				}
				continue
			}

			if e.ContentBlock.Type == "tool_use" {
				idx := s.toolCallIdx
				s.toolCallIdx++
				s.hasRealToolCalls = true
				s.activeBlocks[e.Index] = &activeBlock{
					blockType:     "tool_use",
					id:            e.ContentBlock.ID,
					name:          e.ContentBlock.Name,
					toolCallIndex: idx,
				}
				return &provider.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index: 0,
							Delta: &provider.Message{
								Role: "assistant",
								ToolCalls: []provider.ToolCall{
									{
										ID:   e.ContentBlock.ID,
										Type: "function",
										Function: provider.ToolCallFunction{
											Name:      e.ContentBlock.Name,
											Arguments: "",
										},
										Index: &idx,
									},
								},
							},
						},
					},
				}, nil
			}

			// redacted_thinking arrives in a single block_start (no deltas).
			// Surface as a redacted_thinking delta so OpenAI-compat clients
			// can capture and round-trip the opaque payload.
			if e.ContentBlock.Type == "redacted_thinking" && e.ContentBlock.Data != "" {
				s.activeBlocks[e.Index] = &activeBlock{
					blockType: "redacted_thinking",
				}
				return &provider.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index: 0,
							Delta: &provider.Message{
								Role:             "assistant",
								RedactedThinking: []string{e.ContentBlock.Data},
							},
						},
					},
				}, nil
			}

			// Provider-native server-side blocks (server_tool_use,
			// web_search_tool_result, code_execution_tool_result, ...) need
			// to round-trip verbatim for multi-turn citation continuity.
			// Accumulate the raw block here and emit it at content_block_stop
			// as a single ProviderTurnBlocks delta — clients that retain that
			// field can replay the assistant turn back to Anthropic.
			if providerTurnBlockTypes[e.ContentBlock.Type] {
				raw := map[string]any{"type": e.ContentBlock.Type}
				if e.ContentBlock.ID != "" {
					raw["id"] = e.ContentBlock.ID
				}
				if e.ContentBlock.Name != "" {
					raw["name"] = e.ContentBlock.Name
				}
				if e.ContentBlock.ToolUseID != "" {
					raw["tool_use_id"] = e.ContentBlock.ToolUseID
				}
				if e.ContentBlock.Content != nil {
					raw["content"] = e.ContentBlock.Content
				}
				s.activeBlocks[e.Index] = &activeBlock{
					blockType:   "provider_turn",
					id:          e.ContentBlock.ID,
					name:        e.ContentBlock.Name,
					providerRaw: raw,
				}
				continue
			}

			// text / thinking blocks
			s.activeBlocks[e.Index] = &activeBlock{
				blockType: e.ContentBlock.Type,
				id:        e.ContentBlock.ID,
				name:      e.ContentBlock.Name,
			}
			continue

		case "content_block_stop":
			var stopEvt struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(data), &stopEvt); err != nil {
				// 解析失败时 Index 是零值，会错删 index=0 的活动块——跳过本事件，
				// 宁可留一个孤儿块（流结束随 reader 一起释放）也不污染别的块。
				if err := s.diag.Malformed("content_block_stop", err, data); err != nil {
					return nil, err
				}
				continue
			}
			block := s.activeBlocks[stopEvt.Index]
			delete(s.activeBlocks, stopEvt.Index)
			// Flush a provider-turn block when its stream ends. server_tool_use
			// blocks have their input arrive as input_json_delta chunks; parse
			// the accumulated buffer here so the emitted raw block matches the
			// non-streaming response shape exactly.
			if block != nil && block.blockType == "provider_turn" && block.providerRaw != nil {
				if buf := block.inputJSONBuffer.String(); buf != "" {
					var input any
					if err := json.Unmarshal([]byte(buf), &input); err == nil {
						block.providerRaw["input"] = input
					}
				}
				return &provider.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index: 0,
							Delta: &provider.Message{
								Role:               "assistant",
								ProviderTurnBlocks: []any{block.providerRaw},
							},
						},
					},
				}, nil
			}
			continue

		case "ping":
			continue

		case "content_block_delta":
			var e contentBlockDeltaEvent
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				if err := s.diag.Malformed("content_block_delta", err, data); err != nil {
					return nil, err
				}
				continue
			}

			block := s.activeBlocks[e.Index]

			switch e.Delta.Type {
			case "text_delta":
				return &provider.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index: 0,
							Delta: &provider.Message{
								Role:    "assistant",
								Content: e.Delta.Text,
							},
						},
					},
				}, nil

			case "input_json_delta":
				if block == nil {
					continue
				}

				if block.blockType == "json_schema_content" {
					// json_schema mode: stream tool input as text content
					return &provider.ChatCompletionChunk{
						ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   s.model,
						Choices: []provider.Choice{
							{
								Index: 0,
								Delta: &provider.Message{
									Role:    "assistant",
									Content: e.Delta.PartialJSON,
								},
							},
						},
					}, nil
				}

				if block.blockType == "provider_turn" {
					// Server-side tool input (e.g. web_search query). Accumulate
					// silently — the full input JSON is parsed and attached to
					// the raw block at content_block_stop.
					block.inputJSONBuffer.WriteString(e.Delta.PartialJSON)
					continue
				}

				// Regular tool_use: use stored toolCallIndex
				idx := block.toolCallIndex
				return &provider.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index: 0,
							Delta: &provider.Message{
								ToolCalls: []provider.ToolCall{
									{
										Index: &idx,
										Function: provider.ToolCallFunction{
											Arguments: e.Delta.PartialJSON,
										},
									},
								},
							},
						},
					},
				}, nil

			case "thinking_delta":
				rc := e.Delta.Thinking
				return &provider.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index: 0,
							Delta: &provider.Message{
								Role:             "assistant",
								ReasoningContent: &rc,
							},
						},
					},
				}, nil
			case "signature_delta":
				// signature_delta carries the integrity signature for the preceding
				// thinking block. Anthropic requires it to be echoed back on any
				// multi-turn conversation that combines extended thinking with
				// tool use; without it the next turn returns 400.
				// Emit it as reasoning_content_signature on the delta so OpenAI-
				// compat clients can capture and echo it back.
				if e.Delta.Signature == "" {
					continue
				}
				sig := e.Delta.Signature
				return &provider.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index: 0,
							Delta: &provider.Message{
								Role:                      "assistant",
								ReasoningContentSignature: &sig,
							},
						},
					},
				}, nil
			case "citations_delta":
				// Built-in server-tool citations (web_search etc.) are
				// surfaced incrementally on text blocks. Translate each
				// citation into an OpenAI-compat annotations entry so
				// streaming clients can render source attribution as it
				// arrives.
				if e.Delta.Citation == nil {
					continue
				}
				ann := citationToAnnotation(*e.Delta.Citation)
				return &provider.ChatCompletionChunk{
					ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index: 0,
							Delta: &provider.Message{
								Role:        "assistant",
								Annotations: []map[string]any{ann},
							},
						},
					},
				}, nil
			}

		case "message_delta":
			var e messageDeltaEvent
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				if err := s.diag.Malformed("message_delta", err, data); err != nil {
					return nil, err
				}
				continue
			}
			if e.Usage != nil {
				if e.Usage.OutputTokens > s.usage.CompletionTokens {
					s.usage.CompletionTokens = e.Usage.OutputTokens
				}
				// message_delta restates prompt-side counters with the final
				// post-cache tally. Take the MAX with message_start values:
				// some Anthropic implementations emit message_delta usage with
				// only output_tokens populated (other fields zero), and we
				// must not regress the more complete message_start data in
				// that case.
				if total := e.Usage.totalPromptTokens(); total > s.usage.PromptTokens {
					s.usage.PromptTokens = total
				}
				if e.Usage.CacheReadInputTokens > s.usage.CachedTokens {
					s.usage.CachedTokens = e.Usage.CacheReadInputTokens
					s.usage.PromptTokensDetails = &provider.PromptTokensDetails{
						CachedTokens: e.Usage.CacheReadInputTokens,
					}
				}
				s.usage.TotalTokens = s.usage.PromptTokens + s.usage.CompletionTokens
			}
			finishReason := mapStopReason(e.Delta.StopReason)
			// json_schema mode: if all tool_use blocks were json_schema, map "tool_calls" → "stop"
			if s.jsonSchemaToolName != "" && !s.hasRealToolCalls && finishReason == "tool_calls" {
				finishReason = "stop"
			}
			return &provider.ChatCompletionChunk{
				ID:      fmt.Sprintf("chatcmpl-%s", s.msgID),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   s.model,
				Choices: []provider.Choice{
					{
						Index:        0,
						Delta:        &provider.Message{},
						FinishReason: &finishReason,
					},
				},
			}, nil

		case "message_stop":
			return nil, io.EOF

		case "error":
			// Anthropic emits a mid-stream error event when the upstream hits
			// rate limits, gets overloaded, or violates a tool-use protocol
			// after 200 OK was sent. Without surfacing it the client would see
			// a silently truncated stream. Wire format follows NewProviderError
			// so handler.errorBody can unwrap the inner error envelope.
			status := http.StatusBadGateway
			var aux struct {
				Error map[string]any `json:"error"`
			}
			retryAfter := ""
			if err := json.Unmarshal([]byte(data), &aux); err == nil {
				if t, _ := aux.Error["type"].(string); t != "" {
					switch t {
					case "rate_limit_error":
						status = http.StatusTooManyRequests
					case "overloaded_error":
						status = http.StatusServiceUnavailable
					case "authentication_error":
						status = http.StatusUnauthorized
					case "permission_error":
						status = http.StatusForbidden
					case "not_found_error":
						status = http.StatusNotFound
					case "request_too_large":
						status = http.StatusRequestEntityTooLarge
					case "invalid_request_error":
						status = http.StatusBadRequest
					}
				}
				// Mid-stream rate_limit_error sometimes carries a hint in the
				// body (Anthropic varies by tier). Extract for client backoff.
				retryAfter = extractRetryAfterField(aux.Error)
			}
			return nil, &provider.ProviderError{
				StatusCode: status,
				Message:    fmt.Sprintf("anthropic api error (status %d): %s", status, data),
				RetryAfter: retryAfter,
			}
		}
	}

	if err := s.diag.ScanError(s.scanner.Err()); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (s *StreamReader) Close() error {
	return s.reader.Close()
}

func (s *StreamReader) GetUsage() *provider.Usage {
	return s.usage
}

// extractRetryAfterField pulls a backoff hint out of an upstream error body.
// Common shapes: {"retry_after": 30 | "30"}, {"retryAfter": 30}. Returns ""
// when none of those are present.
func extractRetryAfterField(errObj map[string]any) string {
	for _, key := range []string{"retry_after", "retryAfter"} {
		if v, ok := errObj[key]; ok {
			switch n := v.(type) {
			case string:
				if n != "" {
					return n
				}
			case float64:
				if n > 0 {
					return fmt.Sprintf("%d", int(n))
				}
			}
		}
	}
	return ""
}
