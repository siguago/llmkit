package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestBuildRequest_ContainerForwarded(t *testing.T) {
	// code_execution multi-turn requires passing back the previous container
	// descriptor so Anthropic resumes the same Python sandbox (files /
	// installed packages preserved). Without it every turn spins up a fresh
	// sandbox.
	container := map[string]any{
		"id":         "container_abc123",
		"expires_at": "2026-05-06T12:00:00Z",
	}
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages:  []provider.Message{{Role: "user", Content: "x"}},
		Container: container,
	})
	if r.Container == nil {
		t.Fatal("container descriptor lost in buildRequest")
	}
	got, ok := r.Container.(map[string]any)
	if !ok {
		t.Fatalf("container = %T, want map[string]any", r.Container)
	}
	if got["id"] != "container_abc123" {
		t.Errorf("container.id corrupted: %+v", got)
	}
	// Must serialize at request top level, not nested somewhere.
	bytes, _ := json.Marshal(r)
	if !strings.Contains(string(bytes), `"container":{`) {
		t.Errorf("container not at top level of request body: %s", string(bytes))
	}
}

func TestConvertResponse_ContainerSurfacedToOpenAIResponse(t *testing.T) {
	// The previous turn's response must expose container so the next turn
	// can echo it back via request.container — symmetric with
	// TestBuildRequest_ContainerForwarded.
	resp := &response{
		ID:         "msg_1",
		Model:      "claude-opus-4-7",
		Content:    []responseBlock{{Type: "text", Text: "executed."}},
		StopReason: "end_turn",
		Container: map[string]any{
			"id":         "container_abc123",
			"expires_at": "2026-05-06T12:00:00Z",
		},
	}
	out := convertResponse(resp, nil, "")
	if out.Container == nil {
		t.Fatal("ChatCompletionResponse.Container lost — multi-turn sandbox round-trip broken")
	}
	got, ok := out.Container.(map[string]any)
	if !ok {
		t.Fatalf("container = %T, want map[string]any", out.Container)
	}
	if got["id"] != "container_abc123" {
		t.Errorf("container.id corrupted: %+v", got)
	}
	// Wire form: container must serialize at top level of response body.
	bytes, _ := json.Marshal(out)
	if !strings.Contains(string(bytes), `"container":{`) {
		t.Errorf("container not at response top level: %s", string(bytes))
	}
}

func TestBuildRequest_NoContainerOmitsField(t *testing.T) {
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
	})
	bytes, _ := json.Marshal(r)
	if strings.Contains(string(bytes), `"container"`) {
		t.Errorf("absent container must not pollute wire bytes: %s", string(bytes))
	}
}
