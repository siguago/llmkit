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
		`data: {"type":"message_start","message":{"id":"m_1","model":"claude-opus-4-7","usage":{"input_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":80,"ephemeral_1h_input_tokens":120},"output_tokens":1}}}`,
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
	if usage.CacheCreationTokens != 200 {
		t.Fatalf("cache_creation_input_tokens should be carried, got %d", usage.CacheCreationTokens)
	}
	if details := usage.CacheCreationTokensDetails; details == nil || details.Ephemeral5mTokens != 80 || details.Ephemeral1hTokens != 120 {
		t.Fatalf("cache creation TTL details missing: %+v", details)
	}
	if usage.PromptCacheMissTokens != 0 {
		t.Fatalf("cache writes must not land on DeepSeek's miss counter, got %d", usage.PromptCacheMissTokens)
	}
	// The three prompt-side counters are disjoint and must reconcile, or
	// downstream billing double-counts or drops the uncached remainder.
	if uncached := usage.PromptTokens - usage.CachedTokens - usage.CacheCreationTokens; uncached != 50 {
		t.Fatalf("prompt_tokens - cached - cache_creation should leave input_tokens=50, got %d", uncached)
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

func TestStreamReader_MessageDeltaMergesPartialPromptUsage(t *testing.T) {
	// A server-tool loop can add cache writes after message_start. Anthropic's
	// automatic server-tool cache breakpoint is always 5m, but some streaming
	// responses only increase the aggregate cache_creation_input_tokens counter.
	// Preserve all omitted fields and attribute only that growth to the 5m tier.
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":80,"ephemeral_1h_input_tokens":120},"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"cache_creation_input_tokens":225,"output_tokens":42}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := newStreamReader(context.Background(), newFakeBody(body), "", true)
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage.PromptTokens != 1275 {
		t.Fatalf("partial delta should preserve input/read counters and update creation: got %d", usage.PromptTokens)
	}
	if usage.TotalTokens != 1317 {
		t.Fatalf("total_tokens should be recomputed after merging fields: got %d", usage.TotalTokens)
	}
	if usage.CachedTokens != 1000 || usage.CacheCreationTokens != 225 {
		t.Fatalf("merged cache counters mismatch: %+v", usage)
	}
	if details := usage.CacheCreationTokensDetails; details == nil || details.Ephemeral5mTokens != 105 || details.Ephemeral1hTokens != 120 {
		t.Fatalf("aggregate growth should be attributed to 5m writes: %+v", details)
	}
}

func TestStreamReader_MessageDeltaMergesPartialCacheCreationBreakdown(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":50,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":80,"ephemeral_1h_input_tokens":120},"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"cache_creation":{"ephemeral_5m_input_tokens":105},"output_tokens":42}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage.CacheCreationTokens != 225 || usage.PromptTokens != 275 || usage.TotalTokens != 317 {
		t.Fatalf("partial cache breakdown should preserve omitted buckets: %+v", usage)
	}
	if details := usage.CacheCreationTokensDetails; details == nil ||
		details.Ephemeral5mTokens != 105 || details.Ephemeral1hTokens != 120 {
		t.Fatalf("partial cache breakdown was not merged by field presence: %+v", details)
	}
}

func TestStreamReader_RelayDoesNotInferTTLFromAggregateGrowth(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":50,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":80,"ephemeral_1h_input_tokens":120},"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"cache_creation_input_tokens":225,"output_tokens":42}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	// Exported construction is conservative because the reader has no trusted
	// endpoint identity. Provider.ChatCompletionStream opts official API readers
	// into the documented automatic-5m inference separately.
	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage.CacheCreationTokens != 225 {
		t.Fatalf("relay aggregate update was lost: %+v", usage)
	}
	if usage.CacheCreationTokensDetails != nil {
		t.Fatalf("relay aggregate-only growth must not invent a TTL split: %+v", usage)
	}
}

func TestStreamReader_MessageDeltaDerivesAggregateFromBreakdown(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":70},"output_tokens":5}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage.CacheCreationTokens != 100 || usage.PromptTokens != 110 || usage.TotalTokens != 115 {
		t.Fatalf("breakdown-only delta should update aggregate accounting: %+v", usage)
	}
	if details := usage.CacheCreationTokensDetails; details == nil || details.Ephemeral5mTokens != 30 || details.Ephemeral1hTokens != 70 {
		t.Fatalf("cache creation TTL details mismatch: %+v", details)
	}
}

func TestStreamReader_MessageDeltaDropsInconsistentBreakdown(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":50},"output_tokens":5}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage.CacheCreationTokens != 100 || usage.PromptTokens != 110 || usage.TotalTokens != 115 {
		t.Fatalf("reported aggregate should remain authoritative: %+v", usage)
	}
	if usage.CacheCreationTokensDetails != nil {
		t.Fatalf("inconsistent breakdown must not be exposed as billing-safe: %+v", usage)
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
	if usage.CacheCreationTokens != 200 {
		t.Fatalf("must NOT regress cache creation tokens, got %d", usage.CacheCreationTokens)
	}
	if usage.CompletionTokens != 42 {
		t.Fatalf("completion_tokens from delta should win: got %d", usage.CompletionTokens)
	}
}

func TestStreamReader_MessageDeltaDistinguishesExplicitZeroFromMissing(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":50,"cache_read_input_tokens":100,"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"cache_read_input_tokens":0,"output_tokens":2}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	drainStream(t, sr)

	usage := sr.GetUsage()
	if usage.CachedTokens != 0 || usage.PromptTokens != 50 || usage.TotalTokens != 52 {
		t.Fatalf("explicit zero should replace the previous counter: %+v", usage)
	}
	if usage.PromptTokensDetails != nil {
		t.Fatalf("zero cached tokens should clear stale details: %+v", usage.PromptTokensDetails)
	}
}

func TestStreamReader_UsageOnlyDeltaDoesNotFinishEarly(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"x","usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":42}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		``,
		`data: {"type":"message_stop"}`,
	}, "\n")

	sr := NewStreamReader(context.Background(), newFakeBody(body), "")
	finishCount := 0
	for {
		chunk, err := sr.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv error: %v", err)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil {
			finishCount++
		}
	}
	if finishCount != 1 {
		t.Fatalf("usage-only delta produced an early finish; got %d terminal chunks", finishCount)
	}
	if usage := sr.GetUsage(); usage.CompletionTokens != 42 || usage.TotalTokens != 52 {
		t.Fatalf("usage-only delta was not retained: %+v", usage)
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
