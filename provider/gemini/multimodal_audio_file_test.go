package gemini

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestBuildGeminiParts_InputAudioBecomesInlineData(t *testing.T) {
	// OpenAI's gpt-4o-audio input_audio format → Gemini inlineData with
	// audio/<format> mime type. Without this, audio messages get silently
	// dropped at the gateway boundary.
	parts, err := buildGeminiParts(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "text", Text: "Transcribe this:"},
			{Type: "input_audio", InputAudio: &provider.InputAudio{
				Data:   "BASE64_AUDIO_DATA",
				Format: "wav",
			}},
		},
	})
	if err != nil {
		t.Fatalf("buildGeminiParts: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (text + audio), got %d: %+v", len(parts), parts)
	}
	if parts[0].Text != "Transcribe this:" {
		t.Errorf("text part lost: %+v", parts[0])
	}
	if parts[1].InlineData == nil {
		t.Fatalf("audio part not converted to inlineData: %+v", parts[1])
	}
	if parts[1].InlineData.MimeType != "audio/wav" {
		t.Errorf("mime type wrong: %q, want audio/wav", parts[1].InlineData.MimeType)
	}
	if parts[1].InlineData.Data != "BASE64_AUDIO_DATA" {
		t.Errorf("audio data lost: %q", parts[1].InlineData.Data)
	}
}

func TestBuildGeminiParts_InputAudio_DefaultsToMP3(t *testing.T) {
	// Format is optional in OpenAI; default to mp3 so we don't emit an
	// invalid "audio/" empty suffix that Gemini would reject.
	parts, err := buildGeminiParts(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "input_audio", InputAudio: &provider.InputAudio{Data: "X"}},
		},
	})
	if err != nil {
		t.Fatalf("buildGeminiParts: %v", err)
	}
	if parts[0].InlineData == nil || parts[0].InlineData.MimeType != "audio/mp3" {
		t.Errorf("default mime should be audio/mp3, got %+v", parts[0].InlineData)
	}
}

func TestBuildGeminiParts_FileInlineDataForwarded(t *testing.T) {
	// OpenAI file content (inline form) — file_data + mime_type → Gemini inlineData
	parts, err := buildGeminiParts(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "file", File: &provider.FileContent{
				FileData: "BASE64_PDF",
				MimeType: "application/pdf",
				Filename: "manual.pdf",
			}},
		},
	})
	if err != nil {
		t.Fatalf("buildGeminiParts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if parts[0].InlineData == nil {
		t.Fatalf("file not converted to inlineData: %+v", parts[0])
	}
	if parts[0].InlineData.MimeType != "application/pdf" || parts[0].InlineData.Data != "BASE64_PDF" {
		t.Errorf("inlineData fields wrong: %+v", parts[0].InlineData)
	}
}

func TestBuildGeminiParts_FileWithoutInlineData_Dropped(t *testing.T) {
	// file_id (Files API reference) is OpenAI-specific; Gemini's REST surface
	// uses fileUri pointing at Cloud Storage. Without coordinated upload, the
	// id is meaningless to Gemini — silently drop the part.
	parts, err := buildGeminiParts(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "text", Text: "describe"},
			{Type: "file", File: &provider.FileContent{FileID: "file-abc123"}},
		},
	})
	if err != nil {
		t.Fatalf("buildGeminiParts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected only text part to survive, got %d: %+v", len(parts), parts)
	}
	if parts[0].Text != "describe" {
		t.Errorf("text part lost: %+v", parts[0])
	}
}

func TestContentToParts_DecodesInputAudioFromJSONArray(t *testing.T) {
	// JSON-decoded content array (typical inbound HTTP path) — make sure
	// input_audio gets normalized to a structured ContentPart.
	raw := `[
        {"type":"text","text":"hello"},
        {"type":"input_audio","input_audio":{"data":"AAA","format":"opus"}}
    ]`
	var content any
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	parts := provider.ContentToParts(content)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if parts[1].Type != "input_audio" || parts[1].InputAudio == nil {
		t.Fatalf("input_audio not normalized: %+v", parts[1])
	}
	if parts[1].InputAudio.Format != "opus" || parts[1].InputAudio.Data != "AAA" {
		t.Errorf("input_audio fields lost: %+v", parts[1].InputAudio)
	}
}

func TestContentToParts_DecodesFileFromJSONArray(t *testing.T) {
	raw := `[{"type":"file","file":{"file_data":"BASE64","mime_type":"application/pdf","filename":"x.pdf"}}]`
	var content any
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	parts := provider.ContentToParts(content)
	if len(parts) != 1 || parts[0].File == nil {
		t.Fatalf("file not normalized: %+v", parts)
	}
	if parts[0].File.FileData != "BASE64" || parts[0].File.MimeType != "application/pdf" {
		t.Errorf("file fields lost: %+v", parts[0].File)
	}
}

func TestBuildRequest_AudioMessageEndToEnd_WiresAsInlineData(t *testing.T) {
	r, err := buildRequest(context.Background(), &provider.ChatCompletionRequest{
		Messages: []provider.Message{{
			Role: "user",
			Content: []provider.ContentPart{
				{Type: "input_audio", InputAudio: &provider.InputAudio{
					Data: "BASE64_BYTES", Format: "flac",
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	bytes, _ := json.Marshal(r)
	if !strings.Contains(string(bytes), `"mimeType":"audio/flac"`) {
		t.Errorf("audio inlineData not wired correctly: %s", string(bytes))
	}
}
