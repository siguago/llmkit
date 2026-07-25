package compat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/siguago/llmkit/provider"
)

// StreamReader reads SSE chunks from an OpenAI-compatible streaming response.
type StreamReader struct {
	reader  io.ReadCloser
	scanner *bufio.Scanner
	usage   *provider.Usage
	// providerName is forwarded to mid-stream error envelopes so handler.errorBody
	// can surface the actual upstream provider name (rather than a generic
	// "upstream") in the OpenAI-shape error returned to the client.
	providerName string
	// emitUsageChunk gates whether the terminal {choices:[], usage:{...}} chunk
	// is forwarded to the gateway client. Defaults to false (legacy SDK-friendly
	// behavior); compat.Provider.ChatCompletionStream sets it to true when the
	// caller explicitly asked for stream_options.include_usage.
	emitUsageChunk bool
}

// NewStreamReader creates a StreamReader from an HTTP response body. providerName
// labels mid-stream error envelopes; pass emitUsageChunk=true to forward the
// terminal {choices:[], usage:{...}} chunk to the gateway client (the gateway
// always captures usage internally regardless).
func NewStreamReader(reader io.ReadCloser, providerName string, emitUsageChunk bool) *StreamReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &StreamReader{
		reader:         reader,
		scanner:        scanner,
		providerName:   providerName,
		emitUsageChunk: emitUsageChunk,
	}
}

func (s *StreamReader) Recv() (*provider.ChatCompletionChunk, error) {
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			continue
		}
		// SSE allows the field-value to be separated by an optional space; some
		// legacy proxies strip it. Accept both "data: foo" and "data:foo".
		var data string
		switch {
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		case strings.HasPrefix(line, "data:"):
			data = line[5:]
		default:
			continue
		}
		if data == "[DONE]" {
			return nil, io.EOF
		}
		if !strings.HasPrefix(data, "{") {
			// SSE keep-alive comments / blank pings — skip without warning.
			continue
		}

		// Mid-stream errors (e.g. OpenAI emits {"error":{...}} chunks when the
		// upstream connection failed after the 200 OK was sent). These were
		// previously filtered by isEmptyChunk because they have no choices,
		// leaving the client with a silent truncation.
		if errPayload, ok := detectStreamError([]byte(data), s.providerName); ok {
			return nil, errPayload
		}

		var chunk provider.ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Warn("compat stream: chunk parse error", "error", err, "data_preview", truncate(data, 200))
			continue
		}
		// Lift delta.reasoning → delta.reasoning_content for providers that
		// emit the shorter field name (Kimi K2.6, some vLLM templates).
		liftReasoningOnChunk([]byte(data), &chunk)

		if chunk.Usage != nil {
			provider.NormalizeUsage(chunk.Usage)
			s.usage = chunk.Usage
			// Terminal usage chunk has empty choices; only forward when client
			// opted in. The gateway already captured the usage above for billing.
			if len(chunk.Choices) == 0 && !s.emitUsageChunk {
				continue
			}
		}

		// 过滤空 chunk：跳过所有 choice 的 delta 都无实际内容且无 finish_reason 的 chunk
		// 这种 chunk 常见于 DeepSeek 等模型的"思考阶段"
		if isEmptyChunk(&chunk) {
			continue
		}

		return &chunk, nil
	}

	if err := s.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

// detectStreamError parses an SSE chunk payload and returns a ProviderError if
// the chunk is an upstream error envelope ({"error":{...}}) rather than a
// content delta. The unified ChatCompletionChunk doesn't bind an error field
// so without this hook the chunk would slip through as an empty delta.
//
// providerName labels the error so handler.errorBody's providerNameFromError
// can attach upstream_provider to the client-facing envelope. Wire format
// mirrors NewProviderError so the unwrap path picks the OpenAI-shape body out
// directly.
//
// Optional retryAfter extraction: if the upstream error body contains a
// retry_after / retryAfter field, surface it on the ProviderError so the
// handler can echo it as a Retry-After header even though we're past the
// HTTP-status header phase.
func detectStreamError(data []byte, providerName string) (*provider.ProviderError, bool) {
	var aux struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(data, &aux); err != nil || len(aux.Error) == 0 {
		return nil, false
	}
	// Heuristic: only treat as error envelope when the payload's top-level
	// shape is _just_ {"error": {...}} (no choices/usage/etc). Some providers
	// embed an error alongside a final delta; we conservatively avoid swallowing
	// those.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, false
	}
	for k := range probe {
		if k != "error" {
			return nil, false
		}
	}
	status := http.StatusBadGateway
	// OpenAI's `code` is usually a string ("rate_limit_exceeded"). Some other
	// providers stuff an HTTP code as a number; treat that as the status hint.
	if codeFloat, ok := aux.Error["code"].(float64); ok {
		if c := int(codeFloat); c >= 400 && c < 600 {
			status = c
		}
	}
	if providerName == "" {
		providerName = "upstream"
	}
	return &provider.ProviderError{
		StatusCode: status,
		Message:    fmt.Sprintf("%s api error (status %d): %s", providerName, status, string(data)),
		RetryAfter: extractRetryAfter(aux.Error),
	}, true
}

// extractRetryAfter pulls a backoff hint out of an upstream error body. Common
// shapes:
//
//	{"retry_after": 30}
//	{"retry_after": "30"}
//	{"retryAfter": 30}
//
// Returns "" when none of those are present so the handler skips emitting a
// Retry-After header.
func extractRetryAfter(errObj map[string]any) string {
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

func (s *StreamReader) Close() error {
	return s.reader.Close()
}

func (s *StreamReader) GetUsage() *provider.Usage {
	return s.usage
}

// isEmptyChunk 判断一个 streaming chunk 是否不含有效内容。
// 当所有 choice 的 delta 都没有 role、content、reasoning_content、tool_calls，
// 且没有 finish_reason 时返回 true。
// role 非空视为有效载荷：OpenAI 规范下首帧常为 {"role":"assistant","content":""}，
// 客户端 SDK 依赖该帧确定消息角色，不能过滤。
func isEmptyChunk(chunk *provider.ChatCompletionChunk) bool {
	if len(chunk.Choices) == 0 {
		// 无 choice 但可能携带 usage 信息（最后的统计 chunk），不过滤
		return false
	}
	for _, c := range chunk.Choices {
		if c.FinishReason != nil {
			return false
		}
		if c.Delta != nil {
			if c.Delta.Role != "" {
				return false
			}
			if c.Delta.Content != nil {
				if s, ok := c.Delta.Content.(string); ok && s == "" {
					// 空字符串 content，继续检查
				} else {
					return false
				}
			}
			if c.Delta.ReasoningContent != nil && *c.Delta.ReasoningContent != "" {
				return false
			}
			// reasoning_content_signature is opaque but load-bearing: clients
			// must capture it to round-trip extended-thinking turns through
			// Anthropic. A chunk that only carries the signature must NOT be
			// dropped as "empty".
			if c.Delta.ReasoningContentSignature != nil && *c.Delta.ReasoningContentSignature != "" {
				return false
			}
			// refusal carries OpenAI's safety-refusal explanation; preserving
			// it is critical so the client knows why the assistant returned
			// no content.
			if c.Delta.Refusal != nil && *c.Delta.Refusal != "" {
				return false
			}
			// redacted_thinking is opaque but must round-trip for Anthropic
			// multi-turn reasoning continuity.
			if len(c.Delta.RedactedThinking) > 0 {
				return false
			}
			// annotations carry citations (web search URLs, file references)
			// emitted by OpenAI's tool-augmented models. Silently dropping
			// these chunks would lose the source attribution the user needs.
			if c.Delta.Annotations != nil {
				return false
			}
			if len(c.Delta.ToolCalls) > 0 {
				return false
			}
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// liftReasoningOnChunk re-parses the raw chunk to pull
// choices[].delta.reasoning into choices[].delta.reasoning_content. Mirrors
// liftReasoningOnResponse for the streaming path.
func liftReasoningOnChunk(raw []byte, chunk *provider.ChatCompletionChunk) {
	if chunk == nil {
		return
	}
	var aux struct {
		Choices []struct {
			Delta *struct {
				Reasoning *string `json:"reasoning"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return
	}
	for i, c := range aux.Choices {
		if i >= len(chunk.Choices) || c.Delta == nil || c.Delta.Reasoning == nil {
			continue
		}
		// Skip empty reasoning chunks so they don't get emitted as empty
		// reasoning_content deltas (which would survive isEmptyChunk if they
		// somehow had role/finish_reason set alongside).
		if *c.Delta.Reasoning == "" {
			continue
		}
		dst := chunk.Choices[i].Delta
		if dst != nil && dst.ReasoningContent == nil {
			dst.ReasoningContent = c.Delta.Reasoning
		}
	}
}
