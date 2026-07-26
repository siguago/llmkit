package anthropic

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// fakeReadCloser turns a string into io.ReadCloser for tests.
type fakeReadCloser struct{ *strings.Reader }

func (f fakeReadCloser) Close() error { return nil }

func newFakeBody(s string) io.ReadCloser {
	return fakeReadCloser{strings.NewReader(s)}
}

// drainStream consumes Recv() until io.EOF and returns the accumulated chunks.
func drainStream(t *testing.T, sr *StreamReader) {
	t.Helper()
	for {
		_, err := sr.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("Recv error: %v", err)
		}
	}
}

func TestStreamReader_MessageStartCaptureFullPromptTokens(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m_1","model":"claude-opus-4-7","usage":{"input_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"output_tokens":1}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage == nil {
		t.Fatalf("usage missing")
	}
	if usage.PromptTokens != 1250 {
		t.Fatalf("prompt_tokens should include cache reads/creation, got %d", usage.PromptTokens)
	}
	if usage.CompletionTokens != 42 {
		t.Fatalf("completion_tokens mismatch: got %d", usage.CompletionTokens)
	}
	if usage.TotalTokens != 1292 {
		t.Fatalf("total_tokens should be prompt + completion = 1292, got %d", usage.TotalTokens)
	}
	if usage.CachedTokens != 1000 {
		t.Fatalf("cached_tokens mismatch: got %d", usage.CachedTokens)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 1000 {
		t.Fatalf("prompt_tokens_details.cached_tokens missing: %+v", usage.PromptTokensDetails)
	}
}

func TestStreamReader_MessageDeltaUsageUpgradesStart(t *testing.T) {
	// message_delta restates the final usage — including any cache write that
	// only shows up after the model finishes. The stream reader should pick
	// up new info but not regress data that was richer at message_start.
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"cache_read_input_tokens":500,"cache_creation_input_tokens":0,"output_tokens":50}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage.PromptTokens != 510 {
		t.Fatalf("prompt_tokens should reflect final delta=510, got %d", usage.PromptTokens)
	}
	if usage.CachedTokens != 500 {
		t.Fatalf("cached_tokens should reflect final delta=500, got %d", usage.CachedTokens)
	}
	if usage.TotalTokens != 560 {
		t.Fatalf("total_tokens should be 560, got %d", usage.TotalTokens)
	}
}

func TestStreamReader_MessageDeltaDoesNotRegressUsage(t *testing.T) {
	// Defensive: if message_delta arrives with output_tokens only (other
	// fields zeroed), do NOT clobber the richer message_start values. Some
	// streaming implementations elide the prompt-side counters on message_delta.
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage.PromptTokens != 1250 {
		t.Fatalf("must NOT regress prompt_tokens when message_delta omits cache fields, got %d", usage.PromptTokens)
	}
	if usage.CachedTokens != 1000 {
		t.Fatalf("must NOT regress cached_tokens, got %d", usage.CachedTokens)
	}
	if usage.CompletionTokens != 42 {
		t.Fatalf("completion_tokens from delta should win: got %d", usage.CompletionTokens)
	}
}

func TestStreamReader_SignatureDeltaSurfacesAsReasoningSignature(t *testing.T) {
	// signature_delta carries the integrity signature for the preceding thinking
	// block. We surface it as delta.reasoning_content_signature so OpenAI-compat
	// clients can capture and echo it back on the next turn.
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":1,"output_tokens":1}}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thoughts"}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQB1234"}}`,
		``,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	gotSignature := false
	for {
		c, err := sr.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if len(c.Choices) == 0 || c.Choices[0].Delta == nil {
			continue
		}
		if c.Choices[0].Delta.ReasoningContentSignature != nil {
			if *c.Choices[0].Delta.ReasoningContentSignature == "EqQB1234" {
				gotSignature = true
			}
		}
	}
	if !gotSignature {
		t.Fatalf("signature_delta should have surfaced as reasoning_content_signature delta")
	}
}

func TestStreamReader_MidStreamErrorEvent(t *testing.T) {
	// Anthropic can emit `event: error` mid-stream (rate limit, overload).
	// The stream reader must surface the error rather than silently EOF.
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":1,"output_tokens":1}}}`,
		``,
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Anthropic is overloaded"}}`,
		``,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	for {
		_, err := sr.Recv()
		if err == io.EOF {
			t.Fatalf("must not EOF silently when error event was emitted")
		}
		if err == nil {
			continue // skip the message_start synthesis
		}
		pe, ok := err.(*provider.ProviderError)
		if !ok {
			t.Fatalf("want ProviderError, got %T: %v", err, err)
		}
		if pe.StatusCode != 503 {
			t.Fatalf("overloaded_error must map to 503, got %d", pe.StatusCode)
		}
		if !strings.Contains(pe.Message, "anthropic api error (status 503): ") {
			t.Fatalf("message format must follow NewProviderError convention for unwrap path: %s", pe.Message)
		}
		return
	}
}

func TestStreamReader_EmptySignatureDeltaDropped(t *testing.T) {
	// A signature_delta with no signature payload (rare but possible during
	// edge cases) must not produce a stray empty chunk.
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":1,"output_tokens":1}}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`,
		``,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	chunks := 0
	for {
		c, err := sr.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		chunks++
		// Empty signature_delta should not produce a stray chunk.
		if len(c.Choices) > 0 && c.Choices[0].Delta != nil {
			d := c.Choices[0].Delta
			if d.Content == nil && d.ReasoningContent == nil && d.ReasoningContentSignature == nil && len(d.ToolCalls) == 0 && d.Role == "" && c.Choices[0].FinishReason == nil {
				t.Fatalf("emitted an empty stray chunk: %+v", c)
			}
		}
	}
	if chunks == 0 {
		t.Fatalf("expected at least the thinking_delta + finish chunks")
	}
}
