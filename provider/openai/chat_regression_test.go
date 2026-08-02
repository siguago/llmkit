package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// This fixture guards the pre-native OpenAI Chat wire. Responses support is an
// opt-in surface and must not silently reroute or reshape existing Chat calls.
func TestChatWireRegressionGolden(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer chat-key" {
			t.Errorf("Authorization = %q", got)
		}
		captured, _ = io.ReadAll(request.Body)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl_golden","object":"chat.completion","created":1,"model":"gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	}))
	defer server.Close()

	client := NewWithBaseURL(server.URL + "/v1")
	response, err := client.ChatCompletion(context.Background(), "chat-key", "gpt-4o-mini", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message == nil || response.Choices[0].Message.Content != "pong" {
		t.Fatalf("unexpected response: %+v", response)
	}
	assertGoldenJSON(t, "testdata/chat_request.golden.json", captured)
}

// The native Responses surface must not alter the established Chat streaming
// request or the public chunks exposed through provider.StreamReader.
func TestChatStreamWireAndOutputRegressionGolden(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer chat-key" {
			t.Errorf("Authorization = %q", got)
		}
		captured, _ = io.ReadAll(request.Body)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_stream\",\"object\":\"chat.completion.chunk\",\"created\":123,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_stream\",\"object\":\"chat.completion.chunk\",\"created\":123,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_stream\",\"object\":\"chat.completion.chunk\",\"created\":123,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_stream\",\"object\":\"chat.completion.chunk\",\"created\":123,\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	stream, err := NewWithBaseURL(server.URL+"/v1").ChatCompletionStream(
		context.Background(), "chat-key", "gpt-4o-mini",
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
		chunks = append(chunks, chunk)
	}
	encoded, err := json.Marshal(chunks)
	if err != nil {
		t.Fatalf("marshal chunks: %v", err)
	}
	assertGoldenJSON(t, "testdata/chat_stream_output.golden.json", encoded)
	assertGoldenJSON(t, "testdata/chat_stream_request.golden.json", captured)
	usage := stream.GetUsage()
	if usage == nil || usage.PromptTokens != 1 || usage.CompletionTokens != 1 || usage.TotalTokens != 2 {
		t.Fatalf("stream usage = %+v", usage)
	}
}

func assertGoldenJSON(t *testing.T, path string, actual []byte) {
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
