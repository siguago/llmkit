package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/siguago/llmkit/provider"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSupportsResponseFormatURL(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"gpt-image-1", false},
		{"gpt-image-2", false},
		{"GPT-IMAGE-1", false}, // 大小写不敏感
		{"dall-e-3", true},
		{"dall-e-2", true},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := supportsResponseFormatURL(tc.model); got != tc.want {
				t.Errorf("supportsResponseFormatURL(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestParseOpenAIImagesResponse_B64(t *testing.T) {
	raw := []byte(`{
		"created": 1234567890,
		"data": [
			{"b64_json": "iVBORw0K"},
			{"b64_json": "AAAAAA"}
		],
		"usage": {
			"input_tokens": 12,
			"output_tokens": 0,
			"total_tokens": 12
		}
	}`)
	resp, err := parseOpenAIImagesResponse("gpt-image-1", raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data count = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].B64JSON != "iVBORw0K" {
		t.Errorf("b64_json not preserved: %+v", resp.Data[0])
	}
	if resp.Data[0].MimeType != "image/png" {
		t.Errorf("default mime should be image/png, got %q", resp.Data[0].MimeType)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 12 {
		t.Errorf("usage.input_tokens should map to PromptTokens: %+v", resp.Usage)
	}
	if resp.Usage.ImageCount != 2 || resp.Usage.MediaCount != 2 {
		t.Errorf("ImageCount/MediaCount should = data length: %+v", resp.Usage)
	}
}

func TestParseOpenAIImagesResponse_URLForDallE(t *testing.T) {
	raw := []byte(`{
		"created": 1234567890,
		"data": [{"url": "https://oaidalleapi.example/abc.png", "revised_prompt": "polished"}]
	}`)
	resp, err := parseOpenAIImagesResponse("dall-e-3", raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Data[0].URL != "https://oaidalleapi.example/abc.png" {
		t.Errorf("URL not preserved: %+v", resp.Data[0])
	}
	if md, ok := resp.Data[0].Metadata["revised_prompt"].(string); !ok || md != "polished" {
		t.Errorf("revised_prompt not preserved: %+v", resp.Data[0].Metadata)
	}
}

func TestEditImageMultipartContentType(t *testing.T) {
	var sawRequest bool
	p := NewWithBaseURL("")
	p.client = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			sawRequest = true
			if r.URL.Path != "/v1/images/edits" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
				t.Fatalf("auth header = %q", got)
			}
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			if r.FormValue("model") != "gpt-image-2" || r.FormValue("prompt") != "edit" {
				t.Fatalf("form fields lost: model=%q prompt=%q", r.FormValue("model"), r.FormValue("prompt"))
			}
			imageFiles := r.MultipartForm.File["image"]
			if len(imageFiles) != 1 {
				t.Fatalf("image file missing: %+v", r.MultipartForm.File)
			}
			if got := imageFiles[0].Header.Get("Content-Type"); got != "image/png" {
				t.Fatalf("image content-type = %q, want image/png", got)
			}
			maskFiles := r.MultipartForm.File["mask"]
			if len(maskFiles) != 1 {
				t.Fatalf("mask file missing: %+v", r.MultipartForm.File)
			}
			if got := maskFiles[0].Header.Get("Content-Type"); got != "image/png" {
				t.Fatalf("mask content-type = %q, want image/png", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{"created":123,"data":[{"b64_json":"iVBORw0KGgo="}]}`)),
			}, nil
		}),
	}

	resp, err := p.EditImage(context.Background(), "sk-test", "gpt-image-2", &provider.ImageEditRequest{
		Prompt: "edit",
		Images: []provider.UploadPart{{
			Filename:    "in.png",
			ContentType: "image/png",
			Reader:      bytes.NewReader([]byte("png")),
		}},
		Mask: &provider.UploadPart{
			Filename:    "mask.png",
			ContentType: "image/png",
			Reader:      bytes.NewReader([]byte("mask")),
		},
	})
	if err != nil {
		t.Fatalf("EditImage: %v", err)
	}
	if !sawRequest {
		t.Fatal("transport was not called")
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON == "" {
		t.Fatalf("edit response not normalized: %+v", resp)
	}
}
