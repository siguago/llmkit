package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// Chat still uses its established compatibility DTO and wire conversion even
// though the same adapter now also exposes native Messages methods.
func TestChatWireRegressionGolden(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/messages" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("x-api-key"); got != "chat-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := request.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q", got)
		}
		if got := request.Header.Get("accept"); got != "" {
			t.Errorf("legacy stream Accept = %q, want empty", got)
		}
		captured, _ = io.ReadAll(request.Body)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"msg_golden","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer server.Close()

	client := NewWithBaseURL(server.URL + "/v1")
	response, err := client.ChatCompletion(context.Background(), "chat-key", "claude-test", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message == nil || response.Choices[0].Message.Content != "pong" {
		t.Fatalf("unexpected response: %+v", response)
	}
	assertChatGoldenJSON(t, "testdata/chat_request.golden.json", captured)
}

// Native Messages support shares transport setup with Chat, so guard both the
// legacy streaming wire and the normalized provider.StreamReader output.
func TestChatStreamWireAndOutputRegressionGolden(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/messages" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("x-api-key"); got != "chat-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := request.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q", got)
		}
		captured, _ = io.ReadAll(request.Body)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_golden\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"pong\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL+"/v1").ChatCompletionStream(
		context.Background(), "chat-key", "claude-test",
		&provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hello"}}},
	)
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	defer stream.Close()

	var chunks []*provider.ChatCompletionChunk
	for {
		chunk, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
		chunk.Created = 0 // The legacy adapter uses receipt time; normalize it for the golden.
		chunks = append(chunks, chunk)
	}
	encoded, err := json.Marshal(chunks)
	if err != nil {
		t.Fatalf("marshal chunks: %v", err)
	}
	assertChatGoldenJSON(t, "testdata/chat_stream_output.golden.json", encoded)
	assertChatGoldenJSON(t, "testdata/chat_stream_request.golden.json", captured)
	usage := stream.GetUsage()
	if usage == nil || usage.PromptTokens != 1 || usage.CompletionTokens != 1 || usage.TotalTokens != 2 {
		t.Fatalf("stream usage = %+v", usage)
	}
}

func TestLegacyChatErrorsRemainDirectProviderError(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(*Provider) error
	}{
		{
			name: "sync",
			invoke: func(client *Provider) error {
				_, err := client.ChatCompletion(context.Background(), "chat-key", "claude-test", &provider.ChatCompletionRequest{
					Messages: []provider.Message{{Role: "user", Content: "hello"}},
				})
				return err
			},
		},
		{
			name: "stream",
			invoke: func(client *Provider) error {
				stream, err := client.ChatCompletionStream(context.Background(), "chat-key", "claude-test", &provider.ChatCompletionRequest{
					Messages: []provider.Message{{Role: "user", Content: "hello"}},
				})
				if stream != nil {
					_ = stream.Close()
				}
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if got := request.Header.Get("accept"); got != "" {
					t.Errorf("legacy %s Accept = %q, want empty", test.name, got)
				}
				w.Header().Set("request-id", "req_legacy_error")
				w.Header().Set("retry-after", "9")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
			}))
			defer server.Close()

			err := test.invoke(NewWithBaseURL(server.URL))
			if err == nil {
				t.Fatal("expected API error")
			}
			providerErr, ok := err.(*provider.ProviderError)
			if !ok {
				t.Fatalf("error concrete type = %T, want *provider.ProviderError", err)
			}
			if providerErr.StatusCode != http.StatusUnauthorized ||
				providerErr.RequestID != "req_legacy_error" ||
				providerErr.RetryAfter != "9" {
				t.Fatalf("ProviderError = %+v", providerErr)
			}
		})
	}
}

func TestLegacyChatErrorBodyRemainsLimitedTo10KiB(t *testing.T) {
	limited := strings.Repeat("x", maxLegacyErrorBodyBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, limited+"TAIL_MUST_NOT_APPEAR")
	}))
	defer server.Close()

	_, err := NewWithBaseURL(server.URL).ChatCompletion(
		context.Background(), "chat-key", "claude-test",
		&provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hello"}}},
	)
	providerErr, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("error concrete type = %T, want *provider.ProviderError", err)
	}
	wantPrefix := "anthropic api error (status 500): "
	if len(providerErr.Message) != len(wantPrefix)+maxLegacyErrorBodyBytes {
		t.Fatalf("error message length = %d, want %d", len(providerErr.Message), len(wantPrefix)+maxLegacyErrorBodyBytes)
	}
	if !strings.HasPrefix(providerErr.Message, wantPrefix) ||
		!strings.HasSuffix(providerErr.Message, limited) ||
		strings.Contains(providerErr.Message, "TAIL_MUST_NOT_APPEAR") {
		t.Fatalf("legacy error body limit changed: prefix=%t suffix=%t tail=%t",
			strings.HasPrefix(providerErr.Message, wantPrefix),
			strings.HasSuffix(providerErr.Message, limited),
			strings.Contains(providerErr.Message, "TAIL_MUST_NOT_APPEAR"))
	}
}

func assertChatGoldenJSON(t *testing.T, path string, actual []byte) {
	t.Helper()
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	var expectedValue, actualValue any
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatalf("decode golden file: %v", err)
	}
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatalf("decode captured request: %v\n%s", err, actual)
	}
	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("request wire changed\nactual: %s\n  want: %s", actual, expected)
	}
}
