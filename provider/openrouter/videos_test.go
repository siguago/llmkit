package openrouter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// TestCreateVideoJob_MapsInputReferenceImageToInputReferences 保护 fix 4：
// 网关侧 input_reference_image 必须被包装进 OpenRouter input_references 数组，
// 而不是发送非标字段名 "input_reference_image"。
func TestCreateVideoJob_MapsInputReferenceImageToInputReferences(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"job_xyz","status":"pending"}`))
	}))
	defer srv.Close()

	p := NewWithBaseURL(srv.URL)
	dur := 8.0
	_, err := p.CreateVideoJob(t.Context(), "test-key", "openrouter/google/veo-3.1",
		&provider.VideoCreateRequest{
			Model:           "openrouter/google/veo-3.1",
			Prompt:          "a cat",
			DurationSeconds: &dur,
			InputReferenceImage: &provider.ImageReference{
				ImageURL: "https://example.com/ref.png",
			},
			WebhookURL: "https://example.com/wh",
		})
	if err != nil {
		t.Fatalf("CreateVideoJob: %v", err)
	}

	if _, exists := captured["input_reference_image"]; exists {
		t.Errorf("OpenRouter does not accept 'input_reference_image' — must map to input_references")
	}
	refs, ok := captured["input_references"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("input_references should be a 1-element array, got %+v", captured["input_references"])
	}
	first, ok := refs[0].(map[string]any)
	if !ok || first["image_url"] != "https://example.com/ref.png" {
		t.Errorf("input_references[0].image_url not preserved: %+v", refs[0])
	}

	// duration → duration（不是 duration_seconds）
	if captured["duration"] == nil {
		t.Errorf("duration field should be present")
	}
	if captured["duration_seconds"] != nil {
		t.Errorf("OpenRouter uses 'duration' not 'duration_seconds'")
	}

	// webhook_url → callback_url
	if captured["callback_url"] != "https://example.com/wh" {
		t.Errorf("webhook_url should map to callback_url, got %+v", captured["callback_url"])
	}
	if captured["webhook_url"] != nil {
		t.Errorf("'webhook_url' must not be forwarded raw to OpenRouter")
	}
}

func TestParseOpenRouterVideoJob_Completed(t *testing.T) {
	raw := []byte(`{
		"id": "job_xyz",
		"status": "completed",
		"progress": 100,
		"polling_url": "https://or.example/poll/xyz",
		"generation_id": "gen_abc",
		"unsigned_urls": ["https://cdn.example/v1.mp4", "https://cdn.example/v2.mp4"],
		"duration": 6.0,
		"usage": {"cost": 0.30, "duration_ms": 6000, "currency": "USD"}
	}`)
	job, err := parseOpenRouterVideoJob(raw, "openrouter/veo")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if job.Status != provider.VideoStatusCompleted {
		t.Errorf("status = %v", job.Status)
	}
	if len(job.Assets) != 2 {
		t.Errorf("expected 2 assets from unsigned_urls, got %d", len(job.Assets))
	}
	if job.Assets[0].DurationMs != 6000 {
		t.Errorf("asset duration_ms should = duration*1000, got %d", job.Assets[0].DurationMs)
	}
	if job.Metadata["polling_url"] != "https://or.example/poll/xyz" {
		t.Errorf("polling_url not preserved into metadata: %+v", job.Metadata)
	}
	if job.Usage == nil || job.Usage.Cost != 0.30 {
		t.Errorf("usage.cost not parsed: %+v", job.Usage)
	}
}

func TestNormalizeOpenRouterVideoStatus(t *testing.T) {
	cases := map[string]string{
		"pending":     provider.VideoStatusQueued,
		"queued":      provider.VideoStatusQueued,
		"in_progress": provider.VideoStatusInProgress,
		"processing":  provider.VideoStatusInProgress,
		"completed":   provider.VideoStatusCompleted,
		"succeeded":   provider.VideoStatusCompleted,
		"failed":      provider.VideoStatusFailed,
		"error":       provider.VideoStatusFailed,
		"cancelled":   provider.VideoStatusCancelled,
		"expired":     provider.VideoStatusExpired,
		"unknown":     provider.VideoStatusQueued,
	}
	for in, want := range cases {
		if got := normalizeOpenRouterVideoStatus(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
