package compat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestBuildRequest_ForwardsLogitBias(t *testing.T) {
	body := buildRequest("gpt-4o", &provider.ChatCompletionRequest{
		Messages:  []provider.Message{{Role: "user", Content: "hi"}},
		LogitBias: map[string]int{"50256": -100, "100": 50},
	}, "")
	v, ok := body["logit_bias"]
	if !ok {
		t.Fatal("logit_bias not forwarded to upstream body")
	}
	m, _ := v.(map[string]int)
	if m["50256"] != -100 || m["100"] != 50 {
		t.Errorf("logit_bias values lost: %+v", v)
	}
}

func TestBuildRequest_ForwardsPrediction(t *testing.T) {
	body := buildRequest("gpt-4o", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "edit this"}},
		Prediction: map[string]any{
			"type":    "content",
			"content": "expected output text",
		},
	}, "")
	v, ok := body["prediction"]
	if !ok {
		t.Fatal("prediction not forwarded")
	}
	p, _ := v.(map[string]any)
	if p["type"] != "content" || p["content"] != "expected output text" {
		t.Errorf("prediction shape corrupted: %+v", v)
	}
}

func TestBuildRequest_OmitsPassthroughFieldsWhenAbsent(t *testing.T) {
	// 默认未设置时，新加字段不应污染上游 body
	body := buildRequest("gpt-4o", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, "")
	bytes, _ := json.Marshal(body)
	for _, key := range []string{"logit_bias", "prediction"} {
		if strings.Contains(string(bytes), `"`+key+`"`) {
			t.Errorf("absent %s must not appear in body: %s", key, string(bytes))
		}
	}
}
