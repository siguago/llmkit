package easyrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestNormalizeBaseURLAddsV1(t *testing.T) {
	cases := map[string]string{
		"":                         defaultBaseURL,
		"https://easyrouter.io":    "https://easyrouter.io/v1",
		"https://easyrouter.io/":   "https://easyrouter.io/v1",
		"https://easyrouter.io/v1": "https://easyrouter.io/v1",
		"http://proxy.local/api":   "http://proxy.local/api/v1",
	}
	for in, want := range cases {
		if got := normalizeBaseURL(in); got != want {
			t.Fatalf("normalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChatCompletionForwardsEasyRouterChatFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("auth header = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if _, ok := body["modalities"].([]any); !ok {
			t.Fatalf("modalities not forwarded: %+v", body)
		}
		if _, ok := body["audio"].(map[string]any); !ok {
			t.Fatalf("audio not forwarded: %+v", body)
		}
		if body["stream"] != false {
			t.Fatalf("stream=false missing: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl_1","object":"chat.completion","created":1,"model":"gpt-5.4-nano","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer ts.Close()

	p := New(ts.URL)
	resp, err := p.ChatCompletion(context.Background(), "sk-test", "gpt-5.4-nano", &provider.ChatCompletionRequest{
		Messages:   []provider.Message{{Role: "user", Content: "hi"}},
		Modalities: []string{"text", "audio"},
		Audio:      map[string]any{"voice": "alloy", "format": "mp3"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Usage == nil || resp.Usage.RequestCount != 1 {
		t.Fatalf("usage not normalized: %+v", resp.Usage)
	}
}

func TestGenerateImage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != "gpt-image-2" || body["prompt"] != "cat" || body["size"] != "1024x1024" {
			t.Fatalf("unexpected body: %+v", body)
		}
		if body["style"] != "natural" || body["response_format"] != "b64_json" || body["stream"] != false {
			t.Fatalf("easyrouter image params not forwarded: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"abc","revised_prompt":"polished"}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}`))
	}))
	defer ts.Close()

	stream := false
	p := New(ts.URL)
	resp, err := p.GenerateImage(context.Background(), "sk-test", "gpt-image-2", &provider.ImageGenerationRequest{
		Prompt:         "cat",
		Size:           "1024x1024",
		Style:          "natural",
		ResponseFormat: "b64_json",
		Stream:         &stream,
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if resp.Provider != "easyrouter" || len(resp.Data) != 1 || resp.Data[0].B64JSON != "abc" {
		t.Fatalf("image response not normalized: %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 3 || resp.Usage.CompletionTokens != 4 || resp.Usage.ImageCount != 1 {
		t.Fatalf("usage not normalized: %+v", resp.Usage)
	}
}

func TestGenerateImageRejectsStreaming(t *testing.T) {
	p := New("https://easyrouter.io")
	stream := true
	_, err := p.GenerateImage(context.Background(), "sk-test", "gpt-image-2", &provider.ImageGenerationRequest{
		Prompt: "cat",
		Stream: &stream,
	})
	if err == nil {
		t.Fatal("expected unsupported stream error")
	}
}

func TestEditImageMultipart(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if r.FormValue("model") != "gpt-image-2" || r.FormValue("prompt") != "edit" {
			t.Fatalf("form fields lost: model=%q prompt=%q", r.FormValue("model"), r.FormValue("prompt"))
		}
		if len(r.MultipartForm.File["image"]) != 1 {
			t.Fatalf("image file missing: %+v", r.MultipartForm.File)
		}
		if got := r.MultipartForm.File["image"][0].Header.Get("Content-Type"); got != "image/png" {
			t.Fatalf("image content-type = %q, want image/png", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"url":"https://cdn.example/out.png"}]}`))
	}))
	defer ts.Close()

	p := New(ts.URL)
	resp, err := p.EditImage(context.Background(), "sk-test", "gpt-image-2", &provider.ImageEditRequest{
		Prompt: "edit",
		Images: []provider.UploadPart{{
			Filename:    "in.png",
			ContentType: "image/png",
			Reader:      bytes.NewReader([]byte("png")),
		}},
	})
	if err != nil {
		t.Fatalf("EditImage: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].URL == "" {
		t.Fatalf("edit response not normalized: %+v", resp)
	}
}

func TestVideoJobLifecycle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("auth header = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/video/generations":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			md, _ := body["metadata"].(map[string]any)
			if body["image"] != "https://example.com/cat.png" || md["resolution"] != "720p" || md["ratio"] != "16:9" {
				t.Fatalf("video create body not mapped: %+v", body)
			}
			content, _ := md["content"].([]any)
			if len(content) != 1 {
				t.Fatalf("video input content not mapped: %+v", md)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"task_123","task_id":"task_123","model":"dreamina-seedance-2-0","status":"queued","progress":0,"created_at":1778236598}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/video/generations/task_123":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","message":"","data":{"task_id":"task_123","status":"SUCCESS","result_url":"https://signed.example/out.mp4","submit_time":1778236598,"finish_time":1778236698,"progress":"100%","data":{"duration":5,"resolution":"720p","usage":{"completion_tokens":108900,"total_tokens":108900},"content":{"video_url":"https://signed.example/out.mp4"}}}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	p := New(ts.URL)
	dur := 5.0
	job, err := p.CreateVideoJob(context.Background(), "sk-test", "dreamina-seedance-2-0", &provider.VideoCreateRequest{
		Prompt:              "cat",
		DurationSeconds:     &dur,
		Resolution:          "720p",
		AspectRatio:         "16:9",
		InputReferenceImage: &provider.ImageReference{ImageURL: "https://example.com/cat.png"},
		InputReferences:     []provider.InputReference{{Type: "video_url", ImageURL: "https://example.com/source.mp4"}},
	})
	if err != nil {
		t.Fatalf("CreateVideoJob: %v", err)
	}
	if job.ProviderJobID != "task_123" || job.Status != provider.VideoStatusQueued {
		t.Fatalf("create job not normalized: %+v", job)
	}

	fresh, err := p.GetVideoJob(context.Background(), "sk-test", &provider.VideoJob{ProviderJobID: "task_123", Model: "dreamina-seedance-2-0"})
	if err != nil {
		t.Fatalf("GetVideoJob: %v", err)
	}
	if fresh.Status != provider.VideoStatusCompleted || fresh.Progress != 100 || len(fresh.Assets) != 1 {
		t.Fatalf("status job not normalized: %+v", fresh)
	}
	wantURL := ts.URL + "/v1/videos/task_123/content"
	if fresh.Assets[0].URL != wantURL {
		t.Fatalf("content URL = %q, want %q", fresh.Assets[0].URL, wantURL)
	}
	if fresh.Assets[0].Metadata["requires_provider_auth"] != true {
		t.Fatalf("asset auth metadata missing: %+v", fresh.Assets[0].Metadata)
	}
	if fresh.Usage == nil || fresh.Usage.DurationMs != 5000 || fresh.Usage.TotalTokens != 108900 {
		t.Fatalf("usage not normalized: %+v", fresh.Usage)
	}
}

func TestCreateVideoJobMapsVeoTopLevelFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/video/generations" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != "veo-3.1-generate-001" || body["prompt"] != "cat" {
			t.Fatalf("model/prompt not mapped: %+v", body)
		}
		if body["size"] != "1080p" || body["duration"] != float64(8) || body["generate_audio"] != true {
			t.Fatalf("veo size/duration/audio not top-level: %+v", body)
		}
		if body["input_reference"] != "https://example.com/cat.png" || body["aspect_ratio"] != "16:9" {
			t.Fatalf("veo reference/aspect not top-level: %+v", body)
		}
		if body["seed"] != float64(42) || body["negative_prompt"] != "blur" {
			t.Fatalf("veo seed/negative_prompt not top-level: %+v", body)
		}
		if _, ok := body["metadata"]; ok {
			t.Fatalf("veo request should not use seedance metadata mapping: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task_veo","task_id":"task_veo","model":"veo-3.1-generate-001","status":"queued","progress":0,"created_at":1778236598}`))
	}))
	defer ts.Close()

	p := New(ts.URL)
	dur := 8.0
	seed := 42
	audio := true
	job, err := p.CreateVideoJob(context.Background(), "sk-test", "veo-3.1-generate-001", &provider.VideoCreateRequest{
		Prompt:              "cat",
		Size:                "1080p",
		DurationSeconds:     &dur,
		AspectRatio:         "16:9",
		Seed:                &seed,
		GenerateAudio:       &audio,
		NegativePrompt:      "blur",
		InputReferenceImage: &provider.ImageReference{ImageURL: "https://example.com/cat.png"},
	})
	if err != nil {
		t.Fatalf("CreateVideoJob: %v", err)
	}
	if job.ProviderJobID != "task_veo" || job.Status != provider.VideoStatusQueued {
		t.Fatalf("create job not normalized: %+v", job)
	}
}

func TestVeoStatusUsesResultURLWithoutProviderAuth(t *testing.T) {
	p := New("https://easyrouter.io")
	job, err := p.parseVideoStatus([]byte(`{"code":"success","message":"","data":{"task_id":"task_veo","status":"SUCCESS","result_url":"https://storage.example/out.mp4","progress":"100%"}}`), "veo-3.1-generate-001")
	if err != nil {
		t.Fatalf("parseVideoStatus: %v", err)
	}
	if job.Status != provider.VideoStatusCompleted || len(job.Assets) != 1 {
		t.Fatalf("status job not normalized: %+v", job)
	}
	if job.Assets[0].URL != "https://storage.example/out.mp4" {
		t.Fatalf("veo asset URL = %q", job.Assets[0].URL)
	}
	if job.Assets[0].Metadata["requires_provider_auth"] == true {
		t.Fatalf("veo signed result_url should not require provider auth: %+v", job.Assets[0].Metadata)
	}
}

func TestVideoStatusWrapperError(t *testing.T) {
	p := New("https://easyrouter.io")
	_, err := p.parseVideoStatus([]byte(`{"code":"failed","message":"quota exhausted"}`), "dreamina-seedance-2-0")
	if err == nil {
		t.Fatal("expected wrapper error")
	}
}

func TestSniffImageMime(t *testing.T) {
	cases := map[string]string{
		"iVBORw0KGgoAAAANSU":     "image/png",
		"/9j/4AAQSkZJRgABAQ":     "image/jpeg",
		"UklGRiQAAABXRUJQVlA":    "image/webp",
		"R0lGODlhAQABAIAAAA":     "image/gif",
		"unknown-base64-content": "image/png", // 未知前缀回退 png
	}
	for b64, want := range cases {
		if got := sniffImageMime(b64); got != want {
			t.Fatalf("sniffImageMime(%q) = %q, want %q", b64, got, want)
		}
	}
	// dataURI 已带 prefix 时原样返回
	if got := dataURI("data:image/heic;base64,xxx"); got != "data:image/heic;base64,xxx" {
		t.Fatalf("dataURI passthrough failed: %s", got)
	}
	// 无 prefix 的 JPEG 应包成 image/jpeg
	if got := dataURI("/9j/abc"); got != "data:image/jpeg;base64,/9j/abc" {
		t.Fatalf("dataURI jpeg wrap failed: %s", got)
	}
}

func TestContentValuesMatchesExactType(t *testing.T) {
	refs := []provider.InputReference{
		{Type: "video_url", ImageURL: "https://e/v.mp4"},
		{Type: "image_url", ImageURL: "https://e/i.png"},
		{Type: "audio_video", ImageURL: "https://e/a.wav"}, // 不应被 video 分支误捕
		{Type: "VIDEO_URL", ImageURL: "https://e/u.mp4"},   // 大小写归一化
	}
	got := contentValues(refs)
	if len(got) != 3 {
		t.Fatalf("expected 3 mapped entries, got %d: %+v", len(got), got)
	}
	if got[0]["type"] != "video_url" || got[1]["type"] != "image_url" || got[2]["type"] != "video_url" {
		t.Fatalf("type mapping wrong: %+v", got)
	}
}

func TestNormalizeVideoStatusDefaults(t *testing.T) {
	cases := map[string]string{
		"":              provider.VideoStatusQueued,
		"unknown_state": provider.VideoStatusQueued,
		"SUCCESS":       provider.VideoStatusCompleted,
		"failure":       provider.VideoStatusFailed,
		"in_progress":   provider.VideoStatusInProgress,
	}
	for in, want := range cases {
		if got := normalizeVideoStatus(in); got != want {
			t.Fatalf("normalizeVideoStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
