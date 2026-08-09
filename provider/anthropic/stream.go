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
	reader              io.ReadCloser
	scanner             *bufio.Scanner
	diag                provider.StreamDiagnostics
	usage               *provider.Usage
	inputTokens         int // latest cumulative prompt-side usage fields
	cacheReadTokens     int
	cacheCreationTokens int
	cacheCreation5m     int
	cacheCreation1h     int
	cacheDetailsKnown   bool
	inferAutomatic5m    bool // official Anthropic server-tool cache writes only
	msgID               string
	model               string
	activeBlocks        map[int]*activeBlock // Anthropic block index → block info
	toolCallIdx         int                  // next tool_call index to assign
	jsonSchemaToolName  string               // if set, tool_use with this name is json_schema content
	hasRealToolCalls    bool                 // whether any non-json-schema tool_use blocks exist
}

func NewStreamReader(ctx context.Context, reader io.ReadCloser, jsonSchemaToolName string) *StreamReader {
	// Directly constructed readers may consume arbitrary relay output, so they
	// must not infer a TTL from an aggregate-only update.
	return newStreamReader(ctx, reader, jsonSchemaToolName, false)
}

func newStreamReader(ctx context.Context, reader io.ReadCloser, jsonSchemaToolName string, inferAutomatic5m bool) *StreamReader {
	diag := provider.NewStreamDiagnostics(ctx, "anthropic")
	scanner := diag.NewScanner(reader)
	return &StreamReader{
		reader:             reader,
		scanner:            scanner,
		diag:               diag,
		usage:              &provider.Usage{},
		activeBlocks:       make(map[int]*activeBlock),
		jsonSchemaToolName: jsonSchemaToolName,
		inferAutomatic5m:   inferAutomatic5m,
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
			s.inputTokens = e.Message.Usage.InputTokens
			s.cacheReadTokens = e.Message.Usage.CacheReadInputTokens
			s.cacheCreationTokens = e.Message.Usage.CacheCreationInputTokens
			s.cacheDetailsKnown = false
			s.usage.CompletionTokens = e.Message.Usage.OutputTokens
			if creation := e.Message.Usage.CacheCreation; creation != nil {
				detailTotal := creation.Ephemeral5mInputTokens + creation.Ephemeral1hInputTokens
				if s.cacheCreationTokens == 0 {
					s.cacheCreationTokens = detailTotal
				}
				if detailTotal == s.cacheCreationTokens {
					s.cacheCreation5m = creation.Ephemeral5mInputTokens
					s.cacheCreation1h = creation.Ephemeral1hInputTokens
					s.cacheDetailsKnown = true
				}
			}
			s.syncPromptUsage()
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
				if e.Usage.InputTokens != nil {
					s.inputTokens = *e.Usage.InputTokens
				}
				if e.Usage.CacheReadInputTokens != nil {
					s.cacheReadTokens = *e.Usage.CacheReadInputTokens
				}

				previousCacheCreation := s.cacheCreationTokens
				aggregateReported := e.Usage.CacheCreationInputTokens != nil
				if aggregateReported {
					s.cacheCreationTokens = *e.Usage.CacheCreationInputTokens
				}
				if creation := e.Usage.CacheCreation; creation != nil {
					s.mergeCacheCreationDelta(creation, aggregateReported)
				} else if s.cacheCreationTokens > previousCacheCreation {
					if s.inferAutomatic5m {
						// Official Anthropic server-tool results add automatic cache
						// writes after message_start and document them as always 5m.
						growth := s.cacheCreationTokens - previousCacheCreation
						if s.cacheDetailsKnown {
							s.cacheCreation5m += growth
						} else if previousCacheCreation == 0 {
							s.cacheCreation5m = growth
							s.cacheCreation1h = 0
							s.cacheDetailsKnown = true
						}
					} else {
						// A relay's late aggregate may describe either TTL. Preserve
						// the total but do not fabricate a billing breakdown.
						s.cacheDetailsKnown = false
					}
				} else if s.cacheCreationTokens < previousCacheCreation {
					// Without a fresh breakdown, an older split no longer describes
					// the explicitly reported aggregate.
					s.cacheDetailsKnown = false
				}
				s.syncPromptUsage()
			}
			if e.Delta.StopReason == nil || *e.Delta.StopReason == "" {
				// Some relays split the usage update and terminal stop reason
				// across separate message_delta events. Do not manufacture an
				// early "stop" for a usage-only delta.
				continue
			}
			finishReason := mapStopReason(*e.Delta.StopReason)
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

// syncPromptUsage projects Anthropic's three disjoint prompt-side counters into
// the common usage shape. Keeping each counter independently is important:
// message_delta can contain only one of them, and summing that partial event
// would otherwise discard values captured at message_start.
func (s *StreamReader) syncPromptUsage() {
	s.usage.PromptTokens = s.inputTokens + s.cacheReadTokens + s.cacheCreationTokens
	s.usage.CachedTokens = s.cacheReadTokens
	s.usage.CacheCreationTokens = s.cacheCreationTokens
	if s.cacheDetailsKnown {
		s.usage.CacheCreationTokensDetails = &provider.CacheCreationTokensDetails{
			Ephemeral5mTokens: s.cacheCreation5m,
			Ephemeral1hTokens: s.cacheCreation1h,
		}
	} else {
		s.usage.CacheCreationTokensDetails = nil
	}
	if s.cacheReadTokens > 0 {
		s.usage.PromptTokensDetails = &provider.PromptTokensDetails{
			CachedTokens: s.cacheReadTokens,
		}
	} else {
		s.usage.PromptTokensDetails = nil
	}
	s.usage.TotalTokens = s.usage.PromptTokens + s.usage.CompletionTokens
}

// mergeCacheCreationDelta applies a possibly partial TTL breakdown. When a
// prior complete split exists, omitted fields retain their earlier cumulative
// values. Without prior details, one reported bucket can only be completed when
// the aggregate is also present; otherwise exposing a split would invent the
// missing bucket and make exact billing unsafe.
func (s *StreamReader) mergeCacheCreationDelta(delta *messageDeltaCacheCreationUsage, aggregateReported bool) {
	fiveReported := delta.Ephemeral5mInputTokens != nil
	oneReported := delta.Ephemeral1hInputTokens != nil

	if s.cacheDetailsKnown {
		if fiveReported {
			s.cacheCreation5m = *delta.Ephemeral5mInputTokens
		}
		if oneReported {
			s.cacheCreation1h = *delta.Ephemeral1hInputTokens
		}
		detailTotal := s.cacheCreation5m + s.cacheCreation1h
		if !aggregateReported {
			s.cacheCreationTokens = detailTotal
		}
		s.cacheDetailsKnown = detailTotal == s.cacheCreationTokens
		return
	}

	switch {
	case fiveReported && oneReported:
		s.cacheCreation5m = *delta.Ephemeral5mInputTokens
		s.cacheCreation1h = *delta.Ephemeral1hInputTokens
		detailTotal := s.cacheCreation5m + s.cacheCreation1h
		if !aggregateReported {
			s.cacheCreationTokens = detailTotal
		}
		s.cacheDetailsKnown = detailTotal == s.cacheCreationTokens
	case aggregateReported && fiveReported:
		s.cacheCreation5m = *delta.Ephemeral5mInputTokens
		s.cacheCreation1h = s.cacheCreationTokens - s.cacheCreation5m
		s.cacheDetailsKnown = s.cacheCreation1h >= 0
	case aggregateReported && oneReported:
		s.cacheCreation1h = *delta.Ephemeral1hInputTokens
		s.cacheCreation5m = s.cacheCreationTokens - s.cacheCreation1h
		s.cacheDetailsKnown = s.cacheCreation5m >= 0
	}
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
