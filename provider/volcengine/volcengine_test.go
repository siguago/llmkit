package volcengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestVideoLifecycle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("auth header = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/contents/generations/tasks":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["model"] != "doubao-seedance-1-0-pro-250528" {
				t.Fatalf("model not mapped: %+v", body)
			}
			// Ark v3 没有 input 包装和 parameters 对象；参数走 text 命令
			if _, has := body["input"]; has {
				t.Fatalf("unexpected input envelope: %+v", body)
			}
			if _, has := body["parameters"]; has {
				t.Fatalf("unexpected parameters object: %+v", body)
			}
			content, _ := body["content"].([]any)
			if len(content) != 2 {
				t.Fatalf("content items = %+v", content)
			}
			first, _ := content[0].(map[string]any)
			if first["type"] != "text" || first["text"] != "cat --resolution 1080p --ratio 16:9 --duration 5" {
				t.Fatalf("text command not mapped: %+v", first)
			}
			second, _ := content[1].(map[string]any)
			img, _ := second["image_url"].(map[string]any)
			if second["type"] != "image_url" || second["role"] != "first_frame" || img["url"] != "https://example.com/first.png" {
				t.Fatalf("frame image not mapped: %+v", second)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"task_seed","status":"queued","created_at":1778236598}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/contents/generations/tasks/task_seed":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":"task_seed","status":"succeeded","progress":100,"duration":5,"video_url":"https://cdn.example/seed.mp4"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	p := New(ts.URL + "/api/v3")
	dur := 5.0
	job, err := p.CreateVideoJob(context.Background(), "sk-test", "doubao-seedance-1-0-pro-250528", &provider.VideoCreateRequest{
		Prompt:          "cat",
		Resolution:      "1080p",
		AspectRatio:     "16:9",
		DurationSeconds: &dur,
		FrameImages:     []provider.FrameImage{{Role: "first", ImageURL: "https://example.com/first.png"}},
	})
	if err != nil {
		t.Fatalf("CreateVideoJob: %v", err)
	}
	if job.ProviderJobID != "task_seed" || job.Status != provider.VideoStatusQueued {
		t.Fatalf("create job not normalized: %+v", job)
	}

	fresh, err := p.GetVideoJob(context.Background(), "sk-test", &provider.VideoJob{ProviderJobID: "task_seed", Model: "doubao-seedance-1-0-pro-250528"})
	if err != nil {
		t.Fatalf("GetVideoJob: %v", err)
	}
	if fresh.Status != provider.VideoStatusCompleted || len(fresh.Assets) != 1 || fresh.Usage.DurationMs != 5000 {
		t.Fatalf("status not normalized: %+v", fresh)
	}
}

// TestParseStatusResponseFailedNestedError: Ark 失败任务的 error 是嵌套对象。
func TestParseStatusResponseFailedNestedError(t *testing.T) {
	raw := []byte(`{"id":"t1","status":"failed","error":{"code":"OutputVideoSensitiveContentDetected","message":"sensitive content"}}`)
	job, err := parseStatusResponse(raw, "m")
	if err != nil {
		t.Fatalf("parseStatusResponse: %v", err)
	}
	if job.Error == nil || job.Error.Code != "OutputVideoSensitiveContentDetected" || job.Error.Message != "sensitive content" {
		t.Fatalf("nested error not extracted: %+v", job.Error)
	}
}

// TestCollectURLsSkipsNonVideoEchoes: 输入回显 / 末帧截图不能被当成视频资产。
func TestCollectURLsSkipsNonVideoEchoes(t *testing.T) {
	m := map[string]any{
		"video_url":      "https://cdn.example/v.mp4",
		"last_frame_url": "https://cdn.example/last.png",
		"content": map[string]any{
			"image_url": map[string]any{"url": "https://cdn.example/echo.png"},
		},
	}
	urls := collectURLs(m)
	if len(urls) != 1 || urls[0] != "https://cdn.example/v.mp4" {
		t.Fatalf("collectURLs = %v, want only the video url", urls)
	}
}

// TestTextCommandStripsInjectedBillingFlags: 用户在 prompt 里注入受管 flag
// （--resolution / --duration / --ratio / --seed）必须被剥除，只有网关从结构化字段
// 生成的命令进入文本——否则用户能让 Ark 出更高分辨率/更长视频却按默认档少计费。
func TestTextCommandStripsInjectedBillingFlags(t *testing.T) {
	dur := 5.0
	got := textCommand(&provider.VideoCreateRequest{
		Prompt:          "a cat --resolution 1080p --duration 10 --seed 7 running",
		Resolution:      "720p",
		DurationSeconds: &dur,
	})
	want := "a cat running --resolution 720p --duration 5"
	if got != want {
		t.Fatalf("textCommand = %q, want %q (injected flags must be stripped, gateway fields win)", got, want)
	}
	// 没有结构化字段时，注入的 flag 也不能残留进文本。
	if got := textCommand(&provider.VideoCreateRequest{Prompt: "dog --resolution 4k"}); got != "dog" {
		t.Fatalf("textCommand = %q, want %q (injected flag must be stripped even without structured fields)", got, "dog")
	}
	// 非受管命令（如 --watermark）保留给用户。
	if got := textCommand(&provider.VideoCreateRequest{Prompt: "fox --watermark false"}); got != "fox --watermark false" {
		t.Fatalf("textCommand = %q, want unmanaged flag preserved", got)
	}
}
