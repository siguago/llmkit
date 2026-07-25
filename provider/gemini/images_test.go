package gemini

import (
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestParseGeminiImageResponse_InlineData(t *testing.T) {
	raw := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "Here is the image:"},
					{"inlineData": {"mimeType": "image/png", "data": "aGVsbG8="}},
					{"inlineData": {"mimeType": "image/jpeg", "data": "Zm9v"}}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 5,
			"candidatesTokenCount": 0,
			"totalTokenCount": 5
		}
	}`)
	resp, err := parseGeminiImageResponse(raw, "gemini-image")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 inline images, got %d", len(resp.Data))
	}
	if resp.Data[0].MimeType != "image/png" || resp.Data[0].B64JSON != "aGVsbG8=" {
		t.Errorf("first image not parsed: %+v", resp.Data[0])
	}
	if resp.Data[1].MimeType != "image/jpeg" {
		t.Errorf("second image mime should be image/jpeg, got %q", resp.Data[1].MimeType)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 5 || resp.Usage.MediaCount != 2 {
		t.Errorf("usage not parsed: %+v", resp.Usage)
	}
}

func TestParseGeminiImageResponse_EmptyParts(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"no image returned"}]}}]}`)
	resp, err := parseGeminiImageResponse(raw, "gemini-image")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Errorf("text-only response should yield 0 assets, got %d", len(resp.Data))
	}
}

func TestBuildGeminiImageRequest_AspectRatioAndN(t *testing.T) {
	n := 3
	body := buildGeminiImageRequest(&provider.ImageGenerationRequest{
		Prompt:       "a cat on a wooden desk",
		N:            &n,
		AspectRatio:  "16:9",
		OutputFormat: "png",
	}, nil)
	cfg, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing: %+v", body)
	}
	mods, _ := cfg["responseModalities"].([]string)
	if len(mods) != 2 || mods[0] != "TEXT" || mods[1] != "IMAGE" {
		t.Errorf("responseModalities must be [TEXT,IMAGE], got %+v", mods)
	}
	if cfg["candidateCount"] != 3 {
		t.Errorf("candidateCount should be 3, got %+v", cfg["candidateCount"])
	}
	imgCfg, ok := cfg["imageConfig"].(map[string]any)
	if !ok {
		t.Fatalf("imageConfig missing: %+v", cfg)
	}
	if imgCfg["aspectRatio"] != "16:9" {
		t.Errorf("aspectRatio not forwarded: %+v", imgCfg)
	}
	if imgCfg["outputMimeType"] != "image/png" {
		t.Errorf("outputMimeType should be image/png, got %+v", imgCfg["outputMimeType"])
	}
}

func TestParseVeoOperation_InProgress(t *testing.T) {
	raw := []byte(`{"name":"operations/abc","done":false}`)
	prev := &provider.VideoJob{ID: "vidjob_a", ProviderJobID: "operations/abc", Model: "veo-3"}
	out, err := parseVeoOperation(raw, prev)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Status != provider.VideoStatusInProgress {
		t.Errorf("done=false should map to in_progress, got %v", out.Status)
	}
	if out.ID != "vidjob_a" {
		t.Errorf("ID should be preserved from prev, got %q", out.ID)
	}
}

func TestParseVeoOperation_Completed(t *testing.T) {
	raw := []byte(`{
		"name": "operations/abc",
		"done": true,
		"response": {
			"generateVideoResponse": {
				"generatedSamples": [
					{"video": {"uri": "https://generativelanguage.googleapis.com/v1beta/files/abc", "mimeType": "video/mp4"}}
				]
			}
		}
	}`)
	prev := &provider.VideoJob{ID: "vidjob_a", ProviderJobID: "operations/abc", Model: "veo-3"}
	out, err := parseVeoOperation(raw, prev)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Status != provider.VideoStatusCompleted {
		t.Errorf("done=true with response should map to completed, got %v", out.Status)
	}
	if len(out.Assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(out.Assets))
	}
	a := out.Assets[0]
	if a.URL == "" {
		t.Errorf("URL missing: %+v", a)
	}
	if a.Metadata == nil || a.Metadata["requires_provider_auth"] != true {
		t.Errorf("Veo asset should be flagged requires_provider_auth=true: %+v", a.Metadata)
	}
}

func TestParseVeoOperation_Failed(t *testing.T) {
	raw := []byte(`{
		"name": "operations/abc",
		"done": true,
		"error": {"code": 13, "message": "internal", "status": "INTERNAL"}
	}`)
	prev := &provider.VideoJob{ID: "vidjob_a", ProviderJobID: "operations/abc", Model: "veo-3"}
	out, err := parseVeoOperation(raw, prev)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Status != provider.VideoStatusFailed {
		t.Errorf("done=true with error should map to failed, got %v", out.Status)
	}
	if out.Error == nil || out.Error.Message != "internal" {
		t.Errorf("error not propagated: %+v", out.Error)
	}
}
