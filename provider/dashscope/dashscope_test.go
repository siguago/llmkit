package dashscope

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
		if got := r.Header.Get("X-DashScope-Async"); got != "enable" {
			t.Fatalf("async header = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/services/aigc/video-generation/video-synthesis":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["model"] != "wan2.7-t2v" {
				t.Fatalf("model not mapped: %+v", body)
			}
			params, _ := body["parameters"].(map[string]any)
			// 网关内部小写 "720p" 发上游前归一为大写档位 "720P"；duration 取整数秒。
			if params["resolution"] != "720P" || params["duration"] != float64(6) {
				t.Fatalf("parameters not mapped: %+v", params)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":{"task_id":"task_horse","task_status":"PENDING"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tasks/task_horse":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":{"task_id":"task_horse","task_status":"SUCCEEDED","video_url":"https://cdn.example/horse.mp4","duration":6}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	p := New(ts.URL)
	dur := 6.0
	job, err := p.CreateVideoJob(context.Background(), "sk-test", "wan2.7-t2v", &provider.VideoCreateRequest{
		Prompt:          "horse",
		Resolution:      "720p",
		DurationSeconds: &dur,
	})
	if err != nil {
		t.Fatalf("CreateVideoJob: %v", err)
	}
	if job.ProviderJobID != "task_horse" || job.Status != provider.VideoStatusQueued {
		t.Fatalf("create job not normalized: %+v", job)
	}
	fresh, err := p.GetVideoJob(context.Background(), "sk-test", &provider.VideoJob{ProviderJobID: "task_horse", Model: "wan2.7-t2v"})
	if err != nil {
		t.Fatalf("GetVideoJob: %v", err)
	}
	if fresh.Status != provider.VideoStatusCompleted || len(fresh.Assets) != 1 || fresh.Usage.DurationMs != 6000 {
		t.Fatalf("status not normalized: %+v", fresh)
	}
}

// TestBuildCreateBodyFieldMapping: size 像素尺寸转 * 分隔、参考图单 key、
// 首尾帧映射到文档字段、aspect_ratio 不透传（更不能塞进 size）。
func TestBuildCreateBodyFieldMapping(t *testing.T) {
	dur := 5.0
	body := buildCreateBody("m", &provider.VideoCreateRequest{
		Prompt:              "p",
		Size:                "1280x720",
		AspectRatio:         "16:9",
		DurationSeconds:     &dur,
		InputReferenceImage: &provider.ImageReference{ImageURL: "https://e/i.png"},
		FrameImages: []provider.FrameImage{
			{Role: "first", ImageURL: "https://e/f.png"},
			{Role: "last", ImageURL: "https://e/l.png"},
		},
	})
	input, _ := body["input"].(map[string]any)
	if input["img_url"] != "https://e/i.png" {
		t.Fatalf("img_url not mapped: %+v", input)
	}
	if _, has := input["image_url"]; has {
		t.Fatalf("duplicate image_url key: %+v", input)
	}
	if input["first_frame_url"] != "https://e/f.png" || input["last_frame_url"] != "https://e/l.png" {
		t.Fatalf("frame urls not mapped: %+v", input)
	}
	params, _ := body["parameters"].(map[string]any)
	if params["size"] != "1280*720" {
		t.Fatalf("size not converted: %+v", params)
	}
	if _, has := params["aspect_ratio"]; has {
		t.Fatalf("aspect_ratio should not be sent: %+v", params)
	}
}

// TestNormalizeStatusTerminal: UNKNOWN（任务过期/不存在）必须落终态，
// 否则 reconcile 永远轮询、预占永不释放。
func TestNormalizeStatusTerminal(t *testing.T) {
	if got := normalizeStatus("UNKNOWN"); got != provider.VideoStatusExpired {
		t.Fatalf("UNKNOWN = %q, want expired", got)
	}
	if got := normalizeStatus("EXPIRED"); got != provider.VideoStatusExpired {
		t.Fatalf("EXPIRED = %q, want expired", got)
	}
}

// TestParseStatusResponseVideoDuration: 实际时长字段 usage.video_duration。
func TestParseStatusResponseVideoDuration(t *testing.T) {
	raw := []byte(`{"output":{"task_id":"t","task_status":"SUCCEEDED","video_url":"https://cdn/v.mp4","usage":{"video_duration":7}}}`)
	job, err := parseStatusResponse(raw, "m")
	if err != nil {
		t.Fatalf("parseStatusResponse: %v", err)
	}
	if job.Usage == nil || job.Usage.DurationMs != 7000 {
		t.Fatalf("video_duration not extracted: %+v", job.Usage)
	}
}
