package llmkit

// This file deliberately carries no build tag.
//
// The live tests behind the `integration` tag depend on two helpers that do the
// real assertions — liveResponseText validates a completed chat turn before its
// tool calls are replayed, and liveStreamText owns a stream's whole lifetime and
// keeps the termination/reasoning evidence that StreamChat drops on purpose.
// Those helpers, and the tests that exercise their failure paths, need no
// credentials and no network. Behind the tag they were only ever compiled, never
// executed: CI's integration step is `go test -tags=integration ./... -run '^$'`
// and `make live -run TestLive` never matches these names. Moving them here puts
// them on the default build, which `go test ./...`, the race job and the Go-floor
// job all run.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/siguago/llmkit/provider"
)

// liveResponseText also validates tool-call turns before their arguments and
// assistant message are replayed. It is separate from testing.T so the failure
// paths can be tested offline without making a live request.
func liveResponseText(resp *ChatResponse, needText bool) (string, error) {
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		return "", errors.New("response has no assistant message")
	}
	text := ResponseText(resp)
	choice := resp.Choices[0]
	if choice.FinishReason != nil && *choice.FinishReason == "length" {
		if strings.TrimSpace(ResponseReasoning(resp)) != "" || (resp.Usage != nil && resp.Usage.ReasoningTokens > 0) {
			return text, errors.New("verification incomplete: thinking exhausted the output budget (finish_reason=length)")
		}
		return text, errors.New("verification incomplete: output budget exhausted (finish_reason=length)")
	}
	if needText && strings.TrimSpace(text) == "" {
		return text, fmt.Errorf("empty response: %+v", resp)
	}
	return text, nil
}

// liveStreamText retains termination and reasoning evidence that StreamChat
// intentionally omits. WithTimeout does not bound streams, so this helper owns
// the full live-test lifetime, including Recv and cleanup.
func liveStreamText(ctx context.Context, c *Client, req *ChatRequest) (string, *Usage, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	stream, err := c.ChatStream(ctx, req)
	if err != nil {
		return "", nil, 0, err
	}
	defer stream.Close()

	var text, reasoning strings.Builder
	var finish string
	chunks := 0
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return text.String(), stream.GetUsage(), chunks, err
		}
		if delta := ChunkText(chunk); delta != "" {
			text.WriteString(delta)
			chunks++
		}
		reasoning.WriteString(ChunkReasoning(chunk))
		if chunk != nil && len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil {
			if reason := *chunk.Choices[0].FinishReason; reason != "" && finish != "length" {
				finish = reason
			}
		}
	}
	usage := stream.GetUsage()
	if finish == "" {
		return text.String(), usage, chunks, io.ErrUnexpectedEOF
	}
	reasoningText := reasoning.String()
	resp := &ChatResponse{
		Choices: []Choice{{Message: &Message{Role: "assistant", Content: text.String(), ReasoningContent: &reasoningText}, FinishReason: &finish}},
		Usage:   usage,
	}
	if _, err := liveResponseText(resp, true); err != nil {
		return text.String(), usage, chunks, err
	}
	if finish != "stop" {
		return text.String(), usage, chunks, fmt.Errorf("stream did not finish normally: finish_reason=%s", finish)
	}
	return text.String(), usage, chunks, nil
}

// These tests exercise the live-test guards entirely offline. Running only
// TestIntegrationValidation does not read credentials or contact a provider.
func TestIntegrationValidationResponse(t *testing.T) {
	for _, tt := range []struct {
		name     string
		text     string
		reason   string
		finish   string
		needText bool
		tools    bool
		wantErr  string
	}{
		{"complete text", "pong", "", "stop", true, false, ""},
		{"complete tool call", "", "thinking", "tool_calls", false, true, ""},
		{"truncated tool arguments", "", "thinking", "length", false, true, "thinking exhausted"},
		{"truncated tools without reasoning", "", "", "length", false, true, "output budget exhausted"},
		{"partial answer", "1 2", "thinking", "length", true, false, "thinking exhausted"},
		{"partial answer without reasoning", "1 2", "", "length", true, false, "output budget exhausted"},
		{"empty length", "", "", "length", true, false, "output budget exhausted"},
		{"empty stop", " \n", "", "stop", true, false, "empty response"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := &ChatResponse{Choices: []Choice{{
				Message:      &Message{Role: "assistant", Content: tt.text, ReasoningContent: &tt.reason},
				FinishReason: &tt.finish,
			}}}
			if tt.tools {
				arguments := `{"city":"Hangzhou"}`
				if tt.finish == "length" {
					arguments = `{"city":`
				}
				resp.Choices[0].Message.ToolCalls = []ToolCall{{
					ID: "call-1", Type: "function", Function: ToolCallFunction{Name: "get_temperature", Arguments: arguments},
				}}
			}
			text, err := liveResponseText(resp, tt.needText)
			if text != tt.text {
				t.Fatalf("text=%q, want %q", text, tt.text)
			}
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error=%v, want %q", err, tt.wantErr)
			}
		})
	}
	for _, resp := range []*ChatResponse{nil, {}, {Choices: []Choice{{}}}} {
		if _, err := liveResponseText(resp, false); err == nil {
			t.Fatalf("malformed tool turn accepted: %+v", resp)
		}
	}
}

type validationStream struct {
	ctx    context.Context
	frames []*Chunk
	usage  *Usage
	err    error
	block  bool
	closed bool
}

func (s *validationStream) Recv() (*Chunk, error) {
	if s.block {
		<-s.ctx.Done()
		return nil, s.ctx.Err()
	}
	if len(s.frames) == 0 {
		if s.err != nil {
			return nil, s.err
		}
		return nil, io.EOF
	}
	chunk := s.frames[0]
	s.frames = s.frames[1:]
	if chunk != nil && chunk.Usage != nil {
		s.usage = chunk.Usage
	}
	return chunk, nil
}
func (s *validationStream) Close() error     { s.closed = true; return nil }
func (s *validationStream) GetUsage() *Usage { return s.usage }

type validationStreamProvider struct {
	provider.Provider
	stream *validationStream
}

func (p *validationStreamProvider) Name() string { return DeepSeek }
func (p *validationStreamProvider) ChatCompletionStream(ctx context.Context, _, _ string, _ *ChatRequest) (Stream, error) {
	p.stream.ctx = ctx
	return p.stream, nil
}

func validationChunk(text, reasoning, finish string) *Chunk {
	return &Chunk{Choices: []Choice{{
		Delta:        &Message{Content: text, ReasoningContent: &reasoning},
		FinishReason: &finish,
	}}}
}

func TestIntegrationValidationStream(t *testing.T) {
	usage := &Usage{TotalTokens: 522, ReasoningTokens: 510}
	readErr := errors.New("stream read failed")
	for _, tt := range []struct {
		name    string
		frames  []*Chunk
		readErr error
		wantErr string
	}{
		{"complete", []*Chunk{validationChunk("1", "", ""), validationChunk(" 2 3", "", "stop"), {Usage: usage}}, nil, ""},
		{"single text chunk", []*Chunk{validationChunk("", "thinking", ""), validationChunk("1 2 3", "", "stop")}, nil, ""},
		{"truncated with reasoning", []*Chunk{validationChunk("1", "thinking", ""), validationChunk(" 2", "", "length")}, nil, "thinking exhausted"},
		{"truncated with reasoning usage", []*Chunk{validationChunk("1 2", "", "length"), {Usage: usage}}, nil, "thinking exhausted"},
		{"truncated without reasoning", []*Chunk{validationChunk("1 2", "", "length")}, nil, "output budget exhausted"},
		{"empty length", []*Chunk{validationChunk("", "", "length")}, nil, "output budget exhausted"},
		{"missing terminal frame", []*Chunk{validationChunk("1", "", ""), validationChunk(" 2", "", "")}, nil, "unexpected EOF"},
		{"filtered partial answer", []*Chunk{validationChunk("1", "", ""), validationChunk(" 2", "", "content_filter")}, nil, "finish_reason=content_filter"},
		{"empty response", []*Chunk{validationChunk(" \n", "", "stop")}, nil, "empty response"},
		{"late empty finish", []*Chunk{validationChunk("1 2", "thinking", "length"), validationChunk("", "", "")}, nil, "thinking exhausted"},
		{"read failure", []*Chunk{validationChunk("1", "", ""), {Usage: usage}}, readErr, "stream read failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stream := &validationStream{frames: tt.frames, err: tt.readErr}
			client, err := Wrap(&validationStreamProvider{stream: stream}, WithAPIKey("offline-key"), WithRetry(NoRetry()))
			if err != nil {
				t.Fatal(err)
			}
			text, gotUsage, chunks, err := liveStreamText(context.Background(), client, &ChatRequest{Model: "deepseek-flash", Messages: []Message{User("count")}})
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("text=%q chunks=%d error=%v, want %q", text, chunks, err, tt.wantErr)
			}
			if tt.readErr != nil && !errors.Is(err, tt.readErr) {
				t.Fatalf("read error lost: %v", err)
			}
			if gotUsage != stream.usage {
				t.Fatal("final or error usage was lost")
			}
			if !stream.closed || !errors.Is(stream.ctx.Err(), context.Canceled) {
				t.Fatalf("stream not released: closed=%v context error=%v", stream.closed, stream.ctx.Err())
			}
			deadline, ok := stream.ctx.Deadline()
			if !ok || time.Until(deadline) > 90*time.Second {
				t.Fatalf("stream lacks its 90-second lifetime: deadline=%v", deadline)
			}
		})
	}
}

func TestIntegrationValidationStreamDeadline(t *testing.T) {
	stream := &validationStream{block: true}
	client, err := Wrap(&validationStreamProvider{stream: stream}, WithAPIKey("offline-key"), WithRetry(NoRetry()), WithTimeout(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, _, _, err = liveStreamText(ctx, client, &ChatRequest{Model: "deepseek-flash", Messages: []Message{User("count")}})
	if !errors.Is(err, context.DeadlineExceeded) || !stream.closed {
		t.Fatalf("stream did not stop and close at context deadline: error=%v closed=%v", err, stream.closed)
	}
}
