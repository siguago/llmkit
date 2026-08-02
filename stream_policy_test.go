package llmkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// sseHandler serves a fixed SSE body.
func sseHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
}

// drain reads a stream to completion, returning the concatenated text and the
// error that ended it (io.EOF on a clean finish).
func drain(t *testing.T, s Stream) (string, error) {
	t.Helper()
	var sb strings.Builder
	for {
		chunk, err := s.Recv()
		if err != nil {
			return sb.String(), err
		}
		sb.WriteString(ChunkText(chunk))
	}
}

const (
	goodFrame1 = `data: {"id":"1","choices":[{"index":0,"delta":{"content":"he"}}]}`
	goodFrame2 = `data: {"id":"1","choices":[{"index":0,"delta":{"content":"llo"}}]}`
	badFrame   = `data: {"id":"1","choices":[{"index":0,"delta":{"content":`
)

func sseBody(frames ...string) string {
	return strings.Join(frames, "\n\n") + "\n\n"
}

// The default must surface a malformed frame rather than skip it. Skipping is
// silent data loss: the caller gets "he" and has no way to know "llo" existed.
func TestStream_StrictByDefault(t *testing.T) {
	c := newTestClient(t, sseHandler(sseBody(goodFrame1, badFrame, goodFrame2, "data: [DONE]")))

	stream, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()

	text, err := drain(t, stream)
	if errors.Is(err, io.EOF) {
		t.Fatalf("stream ended cleanly despite a malformed frame; text = %q", text)
	}
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("err = %v, want it to name the malformed frame", err)
	}
	// Content delivered before the bad frame is still valid and still returned.
	if text != "he" {
		t.Errorf("text before the error = %q, want %q", text, "he")
	}
}

// Strictness must cover the whole data protocol, not only payloads that happen
// to start with "{". Otherwise a corrupted frame that lost its opening byte —
// or plain garbage emitted by a relay — is silently classified as a keep-alive,
// recreating the incomplete-but-apparently-successful response strict mode was
// introduced to prevent.
func TestStream_StrictRejectsNonJSONDataPayload(t *testing.T) {
	const garbage = "data: garbage-from-upstream"

	t.Run("strict", func(t *testing.T) {
		c := newTestClient(t, sseHandler(sseBody(goodFrame1, garbage, goodFrame2, "data: [DONE]")))
		stream, err := c.ChatStream(context.Background(), &ChatRequest{
			Model: "test-model", Messages: []Message{User("hi")},
		})
		if err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		defer stream.Close()

		text, err := drain(t, stream)
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("stream ended cleanly despite non-JSON data; text = %q", text)
		}
		if text != "he" {
			t.Errorf("text before malformed data = %q, want he", text)
		}
	})

	t.Run("tolerant", func(t *testing.T) {
		c := newTestClient(t, sseHandler(sseBody(goodFrame1, garbage, goodFrame2, "data: [DONE]")),
			WithStreamTolerance(TolerateMalformedChunks))
		stream, err := c.ChatStream(context.Background(), &ChatRequest{
			Model: "test-model", Messages: []Message{User("hi")},
		})
		if err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		defer stream.Close()

		text, err := drain(t, stream)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("tolerant stream ended with %v, want EOF", err)
		}
		if text != "hello" {
			t.Errorf("text = %q, want hello", text)
		}
	})
}

// The error must not leak the raw frame: callers forward SDK errors into their
// own logs and user-facing messages, and a vendor frame can carry prompt or
// completion content.
func TestStream_StrictErrorOmitsPayload(t *testing.T) {
	secret := `data: {"choices":[{"delta":{"content":"SENSITIVE-PROMPT-TEXT"` // truncated JSON
	c := newTestClient(t, sseHandler(sseBody(secret, "data: [DONE]")))

	stream, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()

	if _, err = drain(t, stream); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("want a parse error, got %v", err)
	}
	if strings.Contains(err.Error(), "SENSITIVE-PROMPT-TEXT") {
		t.Errorf("error leaked the frame payload: %v", err)
	}
}

// Opting into tolerance restores the old skip-and-continue behavior.
func TestStream_TolerantSkipsBadFrames(t *testing.T) {
	c := newTestClient(t, sseHandler(sseBody(goodFrame1, badFrame, goodFrame2, "data: [DONE]")),
		WithStreamTolerance(TolerateMalformedChunks))

	stream, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()

	text, err := drain(t, stream)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream err = %v, want clean EOF", err)
	}
	if text != "hello" {
		t.Errorf("text = %q, want %q — good frames on both sides of the bad one", text, "hello")
	}
}

// A well-formed stream must behave identically under both policies; strictness
// only changes what happens to frames that fail to parse.
func TestStream_CleanStreamUnaffectedByPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"strict", nil},
		{"tolerant", []Option{WithStreamTolerance(TolerateMalformedChunks)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, sseHandler(sseBody(goodFrame1, goodFrame2, "data: [DONE]")), tc.opts...)
			stream, err := c.ChatStream(context.Background(), &ChatRequest{
				Model:    "test-model",
				Messages: []Message{User("hi")},
			})
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			defer stream.Close()

			text, err := drain(t, stream)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("err = %v, want EOF", err)
			}
			if text != "hello" {
				t.Errorf("text = %q", text)
			}
		})
	}
}

// A frame past the ceiling has to fail loudly, and the error has to say which
// knob raises it — bufio's bare "token too long" sends people hunting.
func TestStream_FrameCeilingIsEnforcedAndNamed(t *testing.T) {
	huge := `data: {"id":"1","choices":[{"index":0,"delta":{"content":"` +
		strings.Repeat("x", 5000) + `"}}]}`
	c := newTestClient(t, sseHandler(sseBody(huge, "data: [DONE]")),
		WithMaxStreamFrameBytes(1024))

	stream, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()

	_, err = drain(t, stream)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("oversized frame ended the stream with %v, want an error", err)
	}
	// Both, not either: the message has to state the limit that was hit AND name
	// a real option to raise it. An && here let an earlier version ship a
	// message pointing at llmkit.WithStreamPolicy, which does not exist on this
	// package — the caller would go looking for it and come up empty.
	msg := err.Error()
	if !strings.Contains(msg, "1024") {
		t.Errorf("err = %v, want it to state the ceiling that was hit", err)
	}
	if !strings.Contains(msg, "WithMaxStreamFrameBytes") {
		t.Errorf("err = %v, want it to name the option that raises the ceiling", err)
	}
}

// The default ceiling has to clear a realistically large tool call, which is
// the case that motivated a megabyte rather than a kilobyte.
func TestStream_DefaultCeilingFitsLargeToolCall(t *testing.T) {
	args := fmt.Sprintf(`{\"payload\":\"%s\"}`, strings.Repeat("a", 200*1024))
	frame := fmt.Sprintf(
		`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"%s"}}]}}]}`,
		args)

	c := newTestClient(t, sseHandler(sseBody(frame, "data: [DONE]")))
	stream, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()

	var sawToolCall bool
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("a 200 KiB tool call must fit the default ceiling: %v", err)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta != nil && len(ch.Delta.ToolCalls) > 0 {
				sawToolCall = true
			}
		}
	}
	if !sawToolCall {
		t.Error("large tool call never arrived")
	}
}

// Non-positive values mean "use the default", so clearing or deriving an
// unset value does not create a zero-byte ceiling that rejects everything.
func TestStream_NonPositiveFrameCeilingMeansDefault(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			c := newTestClient(t, sseHandler(sseBody(goodFrame1, "data: [DONE]")),
				WithMaxStreamFrameBytes(limit))

			stream, err := c.ChatStream(context.Background(), &ChatRequest{
				Model:    "test-model",
				Messages: []Message{User("hi")},
			})
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			defer stream.Close()

			text, err := drain(t, stream)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("err = %v, want EOF", err)
			}
			if text != "he" {
				t.Errorf("text = %q", text)
			}
		})
	}
}

// Every streaming adapter must honor the policy, not just the compat one — the
// vendor-specific readers are separate implementations and drift independently.
func TestStream_PolicyReachesEveryAdapter(t *testing.T) {
	// Each adapter's own malformed-frame shape: a truncated JSON body in the
	// envelope that adapter's reader parses.
	bodies := map[string]string{
		DeepSeek:   sseBody(badFrame, "data: [DONE]"),
		OpenRouter: sseBody(badFrame, "data: [DONE]"),
		Anthropic: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		Gemini: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\n\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			c := newTestClientFor(t, name, sseHandler(body))
			stream, err := c.ChatStream(context.Background(), &ChatRequest{
				Model:    "m",
				Messages: []Message{User("hi")},
			})
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			defer stream.Close()

			if _, err := drain(t, stream); err == nil || errors.Is(err, io.EOF) {
				t.Errorf("%s ended with %v, want a malformed-frame error under the strict default", name, err)
			}
		})
	}
}

// Relays configured via WithBaseURL routinely reshape a vendor's SSE to look
// OpenAI-like: appending the [DONE] sentinel, emitting empty data lines. None of
// that is vendor corruption, and the strict policy must not treat it as such.
//
// This is a regression guard: the [DONE] and non-JSON checks were open-coded in
// four readers and two of them lacked it, so making frames strict turned benign
// relay output into a failed stream on gemini and anthropic.
func TestStream_BenignRelayFramesAcrossAdapters(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		body     string
	}{
		{"gemini/done sentinel", Gemini,
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\ndata: [DONE]\n\n"},
		{"gemini/empty data line", Gemini,
			"data: \n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"},
		{"gemini/non-json scalar", Gemini,
			"data: null\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"},
		{"anthropic/done sentinel", Anthropic,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"c\"}}\n\ndata: [DONE]\n\n"},
		{"anthropic/empty data line", Anthropic,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"c\"}}\n\ndata: \n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"deepseek/empty data line", DeepSeek,
			"data: \n\n" + goodFrame1 + "\n\ndata: [DONE]\n\n"},
		{"openrouter/empty data line", OpenRouter,
			"data: \n\n" + goodFrame1 + "\n\ndata: [DONE]\n\n"},
		{"openrouter/spec comment", OpenRouter,
			": OPENROUTER PROCESSING\n\n" + goodFrame1 + "\n\ndata: [DONE]\n\n"},
		{"openrouter/relayed data ping", OpenRouter,
			"data: : ping\n\n" + goodFrame1 + "\n\ndata: [DONE]\n\n"},
		{"openrouter/quoted whitespace ping", OpenRouter,
			"data: \" \"\n\n" + goodFrame1 + "\n\ndata: [DONE]\n\n"},
		// The space after an SSE comment's colon is optional, so a relay
		// repackaging one emits either form. Matching only ": ping" failed the
		// stream on the other half of the grammar.
		{"openrouter/relayed ping without space", OpenRouter,
			"data: :ping\n\n" + goodFrame1 + "\n\ndata: [DONE]\n\n"},
		{"openrouter/relayed comment without space", OpenRouter,
			"data: :OPENROUTER PROCESSING\n\n" + goodFrame1 + "\n\ndata: [DONE]\n\n"},
		// And the wording must not matter: an allowlist of known ping texts is
		// what made benign relay output depend on being on a list.
		{"openrouter/unlisted relayed comment", OpenRouter,
			"data: :heartbeat from some relay nobody enumerated\n\n" + goodFrame1 + "\n\ndata: [DONE]\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClientFor(t, tc.provider, sseHandler(tc.body))
			stream, err := c.ChatStream(context.Background(), &ChatRequest{
				Model: "m", Messages: []Message{User("hi")},
			})
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			defer stream.Close()

			if _, err := drain(t, stream); !errors.Is(err, io.EOF) {
				t.Errorf("benign relay frame failed the stream: %v", err)
			}
		})
	}
}

// Every provider that streams must honor the policy. The per-vendor readers are
// separate implementations wired through separate ChatCompletionStream methods,
// so covering only a few of them leaves the rest free to drift — which is
// exactly how gemini and anthropic ended up missing the [DONE] guard.
func TestStream_PolicyHonoredByAllProviders(t *testing.T) {
	// A truncated JSON object: malformed for every vendor's reader, whatever
	// envelope shape it expects.
	const truncated = `data: {"x":`

	for _, name := range Providers() {
		t.Run(name, func(t *testing.T) {
			// Strict (default): the stream must fail.
			c := newTestClientFor(t, name, sseHandler(sseBody(truncated, "data: [DONE]")))
			if !c.SupportsChat() {
				t.Skip("video-only provider, no chat stream")
			}
			stream, err := c.ChatStream(context.Background(), &ChatRequest{
				Model: "m", Messages: []Message{User("hi")},
			})
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			_, err = drain(t, stream)
			stream.Close()
			if err == nil || errors.Is(err, io.EOF) {
				t.Errorf("strict: stream ended with %v, want a malformed-frame error", err)
			}

			// Tolerant: the same stream must finish cleanly.
			c2 := newTestClientFor(t, name, sseHandler(sseBody(truncated, "data: [DONE]")),
				WithStreamTolerance(TolerateMalformedChunks))
			stream2, err := c2.ChatStream(context.Background(), &ChatRequest{
				Model: "m", Messages: []Message{User("hi")},
			})
			if err != nil {
				t.Fatalf("ChatStream (tolerant): %v", err)
			}
			defer stream2.Close()
			if _, err := drain(t, stream2); !errors.Is(err, io.EOF) {
				t.Errorf("tolerant: stream ended with %v, want clean EOF", err)
			}
		})
	}
}
