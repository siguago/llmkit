package compat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestStreamReader_ErrorOriginIsEstablishedAtEnvelope(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			payload := fmt.Sprintf(`{"error":{"code":%d,"message":"upstream failed","retry_after":7}}`, status)
			stream := NewStreamReader(context.Background(), io.NopCloser(strings.NewReader("data: "+payload+"\n\n")), "test-provider", false)
			defer stream.Close()
			_, err := stream.Recv()
			var pe *provider.ProviderError
			if !errors.As(err, &pe) || pe.StatusCode != status || pe.RetryAfter != "7" || !strings.Contains(pe.Message, payload) {
				t.Fatalf("error = %v (%+v), want unchanged provider metadata", err, pe)
			}
			if provider.ErrorOriginOf(err) != provider.ErrorOriginUpstream {
				t.Errorf("origin = %s, want upstream for an explicit SSE error envelope", provider.ErrorOriginOf(err))
			}
		})
	}
}

func TestStreamReader_NonEnvelopeFailuresAreNotMarkedAsUpstream(t *testing.T) {
	direct := &provider.ProviderError{StatusCode: http.StatusBadGateway, Message: "body read failure"}
	wrapped := fmt.Errorf("transport: %w", direct)
	for _, tc := range []struct {
		name   string
		reader io.ReadCloser
		cause  error
	}{
		{"direct_provider_error", errorReadCloser{direct}, direct},
		{"wrapped_provider_error", errorReadCloser{wrapped}, wrapped},
		{"canceled", errorReadCloser{context.Canceled}, context.Canceled},
		{"deadline", errorReadCloser{context.DeadlineExceeded}, context.DeadlineExceeded},
		{"network", errorReadCloser{io.ErrUnexpectedEOF}, io.ErrUnexpectedEOF},
		{"eof", io.NopCloser(strings.NewReader("")), io.EOF},
		{"done", io.NopCloser(strings.NewReader("data: [DONE]\n\n")), io.EOF},
		{"malformed", io.NopCloser(strings.NewReader("data: {broken}\n\n")), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := NewStreamReader(context.Background(), tc.reader, "test-provider", false)
			defer stream.Close()
			_, err := stream.Recv()
			if err == nil || provider.ErrorOriginOf(err) != provider.ErrorOriginUnknown {
				t.Fatalf("error = %v, origin = %s; want unknown non-envelope source", err, provider.ErrorOriginOf(err))
			}
			if tc.cause != nil && err != tc.cause {
				t.Errorf("error = %v, want unchanged body read error %v", err, tc.cause)
			}
			if tc.name == "malformed" {
				var syntaxError *json.SyntaxError
				if !errors.As(err, &syntaxError) {
					t.Errorf("error = %v, want original JSON syntax error", err)
				}
			}
		})
	}
}

type errorReadCloser struct{ err error }

func (r errorReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (r errorReadCloser) Close() error             { return nil }

func TestIsEmptyChunk_KeepsFirstRoleFrame(t *testing.T) {
	// OpenAI 规范首帧：{"role":"assistant","content":""}
	// 客户端 SDK 依赖该帧确定消息角色，必须保留。
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{
				Role:    "assistant",
				Content: "",
			},
		}},
	}
	if isEmptyChunk(chunk) {
		t.Fatalf("role-only first frame must not be filtered")
	}
}

func TestIsEmptyChunk_DropsPurelyEmptyDelta(t *testing.T) {
	// 无 role、无 content、无 tool_calls、无 finish_reason → 空 chunk，过滤。
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{Content: ""},
		}},
	}
	if !isEmptyChunk(chunk) {
		t.Fatalf("truly empty delta should be filtered")
	}
}

func TestIsEmptyChunk_KeepsRefusalFrame(t *testing.T) {
	// OpenAI sends `delta.refusal` (string) on safety refusals, often with no
	// content or tool_calls. Filtering as empty would silently drop the
	// refusal explanation.
	refusal := "I cannot help with that."
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{
				Refusal: &refusal,
			},
		}},
	}
	if isEmptyChunk(chunk) {
		t.Fatalf("refusal-only chunk must not be filtered")
	}
}

func TestIsEmptyChunk_KeepsReasoningSignatureFrame(t *testing.T) {
	// A chunk carrying only reasoning_content_signature is opaque but required
	// for round-tripping Anthropic extended thinking through compat layers.
	sig := "EqQB1234"
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{
				ReasoningContentSignature: &sig,
			},
		}},
	}
	if isEmptyChunk(chunk) {
		t.Fatalf("signature-only chunk must not be filtered")
	}
}

func TestIsEmptyChunk_KeepsContentFrame(t *testing.T) {
	chunk := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Index: 0,
			Delta: &provider.Message{Content: "hello"},
		}},
	}
	if isEmptyChunk(chunk) {
		t.Fatalf("content frame must not be filtered")
	}
}
