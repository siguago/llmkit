package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

func TestChatCompletionStream_UsageOptInPreservesTerminalChoice(t *testing.T) {
	const usage = `{"prompt_tokens":46,"completion_tokens":5,"total_tokens":51,"prompt_cache_hit_tokens":12,"prompt_cache_miss_tokens":34,"completion_tokens_details":{"reasoning_tokens":2}}`
	for _, shape := range []string{"combined", "usage_only"} {
		for _, option := range []string{"nil", "false", "true"} {
			t.Run(shape+"/"+option, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode request: %v", err)
					}
					if options, ok := body["stream_options"].(map[string]any); !ok || options["include_usage"] != true {
						t.Errorf("upstream stream_options = %#v, want include_usage=true", body["stream_options"])
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"r\"},\"finish_reason\":null}]}\n\n")
					terminal := `"choices":[{"index":0,"delta":{"content":"ed"},"finish_reason":"stop"}]`
					if shape == "combined" {
						_, _ = fmt.Fprintf(w, "data: {%s,\"usage\":%s}\n\n", terminal, usage)
					} else {
						_, _ = fmt.Fprintf(w, "data: {%s}\n\ndata: {\"choices\":[],\"usage\":%s}\n\n", terminal, usage)
					}
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				}))
				defer srv.Close()
				req := &provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hello"}}}
				if option != "nil" {
					req.StreamOptions = &provider.StreamOptions{IncludeUsage: option == "true"}
				}
				stream, err := New(srv.URL).ChatCompletionStream(context.Background(), "key", "deepseek-flash", req)
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				var text strings.Builder
				usageChunks, terminalChoices := 0, 0
				for {
					chunk, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						if provider.ErrorOriginOf(err) != provider.ErrorOriginUnknown {
							t.Errorf("EOF origin = %s, want unknown", provider.ErrorOriginOf(err))
						}
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if chunk.Usage != nil {
						usageChunks++
					}
					for _, choice := range chunk.Choices {
						if choice.Delta != nil {
							text.WriteString(provider.ContentToString(choice.Delta.Content))
						}
						if choice.FinishReason != nil && *choice.FinishReason == "stop" {
							terminalChoices++
						}
					}
				}
				wantUsageChunks := 0
				if option == "true" {
					wantUsageChunks = 1
				}
				if usageChunks != wantUsageChunks {
					t.Errorf("returned usage chunks = %d, want %d", usageChunks, wantUsageChunks)
				}
				if text.String() != "red" || terminalChoices != 1 {
					t.Errorf("text = %q, terminal choices = %d; want red and one stop", text.String(), terminalChoices)
				}
				got := stream.GetUsage()
				if got == nil || got.TotalTokens != 51 || got.CachedTokens != 12 || got.PromptCacheMissTokens != 34 || got.ReasoningTokens != 2 || got.CacheCreationTokens != 0 {
					t.Errorf("cached normalized usage = %+v, want preserved accounting", got)
				}
			})
		}
	}
}

func TestChatCompletionStream_ErrorEnvelopeHasUpstreamOrigin(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			payload := fmt.Sprintf(`{"error":{"code":%d,"message":"upstream stream failed","retry_after":7}}`, status)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			}))
			defer srv.Close()
			stream, err := New(srv.URL).ChatCompletionStream(context.Background(), "key", "deepseek-flash", &provider.ChatCompletionRequest{
				Messages: []provider.Message{{Role: "user", Content: "hello"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_, err = stream.Recv()
			var pe *provider.ProviderError
			if !errors.As(err, &pe) || pe.StatusCode != status || pe.RetryAfter != "7" || !strings.Contains(pe.Message, payload) {
				t.Fatalf("stream error = %v (%+v), want original error metadata", err, pe)
			}
			if provider.ErrorOriginOf(err) != provider.ErrorOriginUpstream {
				t.Errorf("error origin = %s, want upstream", provider.ErrorOriginOf(err))
			}
		})
	}
}

func TestStreamReader_NonEnvelopeErrorsKeepUnknownOrigin(t *testing.T) {
	direct := &provider.ProviderError{StatusCode: http.StatusBadGateway, Message: "direct transport cause"}
	wrapped := fmt.Errorf("transport failure: %w", &provider.ProviderError{StatusCode: http.StatusBadGateway, Message: "wrapped transport cause"})
	for _, tc := range []struct {
		name   string
		reader io.ReadCloser
		cause  error
	}{
		{"done", io.NopCloser(strings.NewReader("data: [DONE]\n\n")), io.EOF},
		{"eof", io.NopCloser(strings.NewReader("")), io.EOF},
		{"malformed", io.NopCloser(strings.NewReader("data: {broken}\n\n")), nil},
		{"canceled", errorReadCloser{context.Canceled}, context.Canceled},
		{"deadline", errorReadCloser{context.DeadlineExceeded}, context.DeadlineExceeded},
		{"network", errorReadCloser{io.ErrUnexpectedEOF}, io.ErrUnexpectedEOF},
		{"direct_provider_error", errorReadCloser{direct}, direct},
		{"wrapped_provider_error", errorReadCloser{wrapped}, wrapped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &streamReader{inner: compat.NewStreamReader(context.Background(), tc.reader, name, false)}
			defer stream.Close()
			_, err := stream.Recv()
			if err == nil || provider.ErrorOriginOf(err) != provider.ErrorOriginUnknown {
				t.Fatalf("error = %v, origin = %s; want non-envelope error with unknown origin", err, provider.ErrorOriginOf(err))
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Errorf("error = %v, want original cause %v", err, tc.cause)
			}
			if tc.name == "malformed" {
				var syntaxError *json.SyntaxError
				if !errors.As(err, &syntaxError) {
					t.Errorf("malformed frame error = %v, want original JSON syntax error", err)
				}
			}
		})
	}
}

type errorReadCloser struct{ err error }

func (r errorReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (r errorReadCloser) Close() error             { return nil }
