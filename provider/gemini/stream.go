package gemini

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/siguago/llmkit/provider"
)

type StreamReader struct {
	reader  io.ReadCloser
	scanner *bufio.Scanner
	diag    provider.StreamDiagnostics
	usage   *provider.Usage
	model   string
	id      string
}

func NewStreamReader(ctx context.Context, reader io.ReadCloser, model string) *StreamReader {
	diag := provider.NewStreamDiagnostics(ctx, "gemini")
	scanner := diag.NewScanner(reader)
	return &StreamReader{
		reader:  reader,
		scanner: scanner,
		diag:    diag,
		model:   model,
		id:      fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano()),
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
			// Gemini itself doesn't send [DONE], but relays configured with
			// WithBaseURL routinely append it to look OpenAI-shaped.
			return nil, io.EOF
		case provider.FrameSkip:
			continue
		}

		var resp response
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			if err := s.diag.Malformed("chunk", err, data); err != nil {
				return nil, err
			}
			continue
		}

		// Update usage if present
		if resp.UsageMetadata != nil {
			s.usage = buildUsage(resp.UsageMetadata)
		}

		// Blocked prompt: Gemini returns no candidates but signals via
		// promptFeedback.blockReason. Synthesize a content_filter finish chunk
		// so the client sees a properly shaped end-of-stream rather than a
		// silent empty stream.
		if len(resp.Candidates) == 0 {
			if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
				reason := mapFinishReason(resp.PromptFeedback.BlockReason)
				if reason == "stop" {
					reason = "content_filter"
				}
				return &provider.ChatCompletionChunk{
					ID:      s.id,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   s.model,
					Choices: []provider.Choice{
						{
							Index:        0,
							Delta:        &provider.Message{Role: "assistant"},
							FinishReason: &reason,
						},
					},
				}, nil
			}
			continue
		}

		// Iterate ALL candidates so n>1 streaming preserves every choice.
		// Mirror the non-streaming convertResponse: each Gemini candidate
		// becomes one Choice in the chunk, OpenAI clients accumulate per
		// Choice.Index. Default n=1 keeps the previous shape (one Choice per
		// chunk with Index=0).
		var choices []provider.Choice
		for ci, candidate := range resp.Candidates {
			choiceIndex := candidate.Index
			if choiceIndex == 0 && ci > 0 {
				choiceIndex = ci
			}

			// Finish-only chunk for this candidate (no content yet).
			if candidate.Content == nil {
				if candidate.FinishReason == "" {
					continue
				}
				reason := mapFinishReason(candidate.FinishReason)
				choices = append(choices, provider.Choice{
					Index:        choiceIndex,
					Delta:        &provider.Message{},
					FinishReason: &reason,
				})
				continue
			}

			text := ""
			reasoningText := ""
			var toolCalls []provider.ToolCall
			toolCallIndex := 0

			for _, p := range candidate.Content.Parts {
				if p.Thought != nil && *p.Thought {
					reasoningText += p.Text
					continue
				}
				if p.FunctionCall != nil {
					args := p.FunctionCall.Args
					if args == nil {
						args = map[string]any{}
					}
					argsBytes, _ := json.Marshal(args)
					idx := toolCallIndex
					toolCalls = append(toolCalls, provider.ToolCall{
						// Encode candidate index into the ID so multi-candidate
						// tool calls don't collide on the OpenAI-side accumulation.
						ID:   fmt.Sprintf("call_%d_%d_%d", time.Now().UnixNano(), ci, toolCallIndex),
						Type: "function",
						Function: provider.ToolCallFunction{
							Name:      p.FunctionCall.Name,
							Arguments: string(argsBytes),
						},
						Index: &idx,
					})
					toolCallIndex++
				} else if p.Text != "" {
					text += p.Text
				}
			}

			choice := provider.Choice{
				Index: choiceIndex,
				Delta: &provider.Message{
					Role:    "assistant",
					Content: text,
				},
			}
			if reasoningText != "" {
				choice.Delta.ReasoningContent = &reasoningText
			}
			if len(toolCalls) > 0 {
				choice.Delta.ToolCalls = toolCalls
			}
			if candidate.FinishReason != "" {
				reason := mapFinishReason(candidate.FinishReason)
				if len(toolCalls) > 0 && reason == "stop" {
					reason = "tool_calls"
				}
				choice.FinishReason = &reason
			}
			choices = append(choices, choice)
		}

		if len(choices) == 0 {
			continue
		}

		return &provider.ChatCompletionChunk{
			ID:      s.id,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   s.model,
			Choices: choices,
		}, nil
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
