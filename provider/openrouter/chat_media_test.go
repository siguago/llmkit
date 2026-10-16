package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// TestNew_UsesOfficialEndpointAndSharesTransport pins the exported constructor
// that the smoke tests never exercised: every internal caller builds through
// NewWithBaseURL, so a typo in the defaulting path would ship unnoticed.
func TestNew_UsesOfficialEndpointAndSharesTransport(t *testing.T) {
	p := New()

	if p.Name() != "openrouter" {
		t.Errorf("Name() = %q", p.Name())
	}
	if p.chatURL != "https://openrouter.ai/api/v1/chat/completions" {
		t.Errorf("chatURL = %q", p.chatURL)
	}
	if p.modelsURL != "https://openrouter.ai/api/v1/models" {
		t.Errorf("modelsURL = %q", p.modelsURL)
	}
	if p.videosURL != "https://openrouter.ai/api/v1/videos" {
		t.Errorf("videosURL = %q", p.videosURL)
	}
	// One transport behind both clients: streaming must not open a second pool.
	if p.client == nil || p.streamClient == nil {
		t.Fatalf("clients = %v / %v", p.client, p.streamClient)
	}
	if p.client.Transport != p.streamClient.Transport {
		t.Error("chat and stream clients must share one transport")
	}

	// A trailing slash on a relay root must not produce "//chat/completions".
	if got := NewWithBaseURL("https://relay.example/v1/").chatURL; got != "https://relay.example/v1/chat/completions" {
		t.Errorf("trailing-slash baseURL -> %q", got)
	}
}

func TestChatCompletion_NonStreaming(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"gen-1","object":"chat.completion","created":1700000000,"model":"m",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant","content":"hi",
					"reasoning":"because",
					"reasoning_details":[
						{"type":"reasoning.text","text":"t","signature":"sig-1"},
						{"type":"reasoning.encrypted","data":"blob-1"}
					],
					"images":[{"type":"image_url","image_url":{"url":"https://cdn.example/a.png"}}]
				},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`)
	}))
	defer srv.Close()

	resp, err := NewWithBaseURL(srv.URL).ChatCompletion(context.Background(), "k", "m",
		&provider.ChatCompletionRequest{
			Messages: []provider.Message{{Role: "user", Content: "hi"}},
		})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if gotPath != "/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	// The non-streaming path must explicitly pin stream=false rather than
	// relying on the upstream default.
	if gotBody["stream"] != false {
		t.Errorf("stream = %#v, want false", gotBody["stream"])
	}
	if gotBody["model"] != "m" {
		t.Errorf("model = %#v", gotBody["model"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}

	if len(resp.Choices) != 1 || resp.Choices[0].Message == nil {
		t.Fatalf("choices = %+v", resp.Choices)
	}
	msg := resp.Choices[0].Message

	if msg.ReasoningContent == nil || *msg.ReasoningContent != "because" {
		t.Errorf("reasoning not normalized: %+v", msg.ReasoningContent)
	}
	if msg.ReasoningContentSignature == nil || *msg.ReasoningContentSignature != "sig-1" {
		t.Errorf("signature not lifted: %+v", msg.ReasoningContentSignature)
	}
	if len(msg.RedactedThinking) != 1 || msg.RedactedThinking[0] != "blob-1" {
		t.Errorf("redacted thinking not lifted: %+v", msg.RedactedThinking)
	}

	// attachAssetsToChoices must land the extracted asset on choice[0].Message.
	if len(msg.Images) != 1 {
		t.Fatalf("assets not attached to the choice: %+v", msg.Images)
	}
	if msg.Images[0].URL != "https://cdn.example/a.png" {
		t.Errorf("asset URL = %q", msg.Images[0].URL)
	}
	if resp.Usage == nil {
		t.Fatal("usage dropped")
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	// OpenRouter reports no image dimension, so both counters come from us.
	if resp.Usage.ImageCount != 1 || resp.Usage.MediaCount != 1 {
		t.Errorf("image/media counts = %d/%d, want 1/1",
			resp.Usage.ImageCount, resp.Usage.MediaCount)
	}
}

func TestChatCompletion_UpstreamErrorIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down","code":429}}`)
	}))
	defer srv.Close()

	_, err := NewWithBaseURL(srv.URL).ChatCompletion(context.Background(), "k", "m",
		&provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an error for a 429 response")
	}
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error is %T, want *provider.ProviderError", err)
	}
	if pe.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d", pe.StatusCode)
	}
	// The retry hint has to survive to the caller: it is what the retry loop
	// reads to decide when to come back.
	if pe.RetryAfter != "30" {
		t.Errorf("retry-after = %q", pe.RetryAfter)
	}
	if !strings.Contains(pe.Message, "slow down") {
		t.Errorf("message = %q", pe.Message)
	}
}

func TestChatCompletion_RejectsMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":`)
	}))
	defer srv.Close()

	if _, err := NewWithBaseURL(srv.URL).ChatCompletion(context.Background(), "k", "m",
		&provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("expected a decode error, not a zero-value success")
	}
}

// TestGenerateImage_ForwardsImageConfigAndModalities covers the whole Image
// path end to end: gateway fields map into OpenRouter's image_config, the
// opaque provider_options block merges in, and the returned message.images[]
// comes back as structured assets.
func TestGenerateImage_ForwardsImageConfigAndModalities(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"gen-2","created":1700000001,"model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","images":[
				{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}
			]}}]
		}`)
	}))
	defer srv.Close()

	n := 2
	resp, err := NewWithBaseURL(srv.URL).GenerateImage(context.Background(), "k", "m",
		&provider.ImageGenerationRequest{
			Model:       "m",
			Prompt:      "a cat",
			Size:        "1024x1024",
			AspectRatio: "1:1",
			N:           &n,
			User:        "u-1",
			ProviderOptions: map[string]any{
				"openrouter": map[string]any{
					"image_config": map[string]any{
						"style":     "vivid",
						"rgb":       []any{1, 2, 3},
						"super_res": "   ", // whitespace-only string: must be dropped
						"empty":     nil,   // nil: must be dropped
					},
					"provider": map[string]any{"order": []any{"a"}},
				},
				"other-vendor": map[string]any{"ignored": true},
			},
		})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	modalities, _ := gotBody["modalities"].([]any)
	if len(modalities) != 2 || modalities[0] != "image" || modalities[1] != "text" {
		t.Errorf("modalities = %#v", gotBody["modalities"])
	}
	cfg, _ := gotBody["image_config"].(map[string]any)
	if cfg["image_size"] != "1024x1024" {
		t.Errorf("size did not map to image_size: %#v", cfg)
	}
	if cfg["aspect_ratio"] != "1:1" {
		t.Errorf("aspect_ratio missing: %#v", cfg)
	}
	if cfg["style"] != "vivid" {
		t.Errorf("provider_options.image_config not merged: %#v", cfg)
	}
	if _, ok := cfg["rgb"]; !ok {
		t.Errorf("non-string provider_options values must survive: %#v", cfg)
	}
	if _, ok := cfg["super_res"]; ok {
		t.Errorf("whitespace-only string must be dropped: %#v", cfg)
	}
	if _, ok := cfg["empty"]; ok {
		t.Errorf("nil value must be dropped: %#v", cfg)
	}
	if gotBody["n"] != float64(2) {
		t.Errorf("n = %#v", gotBody["n"])
	}
	if gotBody["user"] != "u-1" {
		t.Errorf("user = %#v", gotBody["user"])
	}
	if gotBody["provider"] == nil {
		t.Error("provider_options.openrouter.provider must map to the provider routing field")
	}

	if len(resp.Data) != 1 {
		t.Fatalf("data = %+v", resp.Data)
	}
	if resp.Data[0].MimeType != "image/png" || resp.Data[0].B64JSON != "QUJD" {
		t.Errorf("data URL not split into mime + payload: %+v", resp.Data[0])
	}
	if resp.Provider != "openrouter" {
		t.Errorf("provider = %q", resp.Provider)
	}
	if resp.Usage == nil || resp.Usage.ImageCount != 1 || resp.Usage.MediaCount != 1 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGenerateImage_PropagatesChatError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer srv.Close()

	if _, err := NewWithBaseURL(srv.URL).GenerateImage(context.Background(), "k", "m",
		&provider.ImageGenerationRequest{Model: "m", Prompt: "p"}); err == nil {
		t.Fatal("expected the chat error to propagate")
	}
}

func TestOpenrouterImageConfigExtras_MissingShapes(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]any
		want int
	}{
		{"nil", nil, 0},
		{"no openrouter key", map[string]any{"other": map[string]any{}}, 0},
		{"openrouter not a map", map[string]any{"openrouter": "nope"}, 0},
		{"no image_config", map[string]any{"openrouter": map[string]any{"provider": map[string]any{}}}, 0},
		{"image_config not a map", map[string]any{"openrouter": map[string]any{"image_config": "nope"}}, 0},
		{"all values dropped", map[string]any{"openrouter": map[string]any{"image_config": map[string]any{"a": " ", "b": nil}}}, 0},
		{"one kept", map[string]any{"openrouter": map[string]any{"image_config": map[string]any{"a": "", "b": 7}}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(openrouterImageConfigExtras(tc.opts)); got != tc.want {
				t.Errorf("len = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOpenrouterProviderRouting_MissingShapes(t *testing.T) {
	if openrouterProviderRouting(nil) != nil {
		t.Error("nil options must yield nil routing")
	}
	if openrouterProviderRouting(map[string]any{"openrouter": "nope"}) != nil {
		t.Error("a non-map openrouter block must yield nil routing")
	}
	if openrouterProviderRouting(map[string]any{"openrouter": map[string]any{}}) != nil {
		t.Error("a missing provider key must yield nil routing")
	}
	if got := openrouterProviderRouting(map[string]any{
		"openrouter": map[string]any{"provider": "x"},
	}); got != "x" {
		t.Errorf("routing = %#v", got)
	}
}

// TestGetUsage_MergesAccumulatedMediaCounters pins the fallback that lets
// per-image / per-request billing work when OpenRouter sends no usage chunk.
func TestGetUsage_MergesAccumulatedMediaCounters(t *testing.T) {
	t.Run("synthesizes when upstream sent nothing", func(t *testing.T) {
		s := &StreamReader{imageCount: 2, mediaCount: 3}
		got := s.GetUsage()
		if got == nil {
			t.Fatal("expected a synthesized usage")
		}
		if got.ImageCount != 2 || got.MediaCount != 3 || got.RequestCount != 1 {
			t.Errorf("usage = %+v", got)
		}
	})

	t.Run("fills missing counters on an existing usage", func(t *testing.T) {
		s := &StreamReader{
			usage:      &provider.Usage{PromptTokens: 7},
			imageCount: 4,
			mediaCount: 5,
		}
		got := s.GetUsage()
		if got.ImageCount != 4 || got.MediaCount != 5 || got.PromptTokens != 7 {
			t.Errorf("usage = %+v", got)
		}
	})

	t.Run("never overwrites counters upstream did report", func(t *testing.T) {
		s := &StreamReader{
			usage:      &provider.Usage{ImageCount: 9, MediaCount: 8},
			imageCount: 4,
			mediaCount: 5,
		}
		got := s.GetUsage()
		if got.ImageCount != 9 || got.MediaCount != 8 {
			t.Errorf("usage = %+v", got)
		}
	})

	t.Run("stays nil when there is nothing to report", func(t *testing.T) {
		if got := (&StreamReader{}).GetUsage(); got != nil {
			t.Errorf("usage = %+v, want nil", got)
		}
	})
}

func TestAttachAssetsToChunk_EdgeCases(t *testing.T) {
	assets := []provider.MediaAsset{{URL: "https://cdn.example/a.png"}}

	// None of these may panic or attach.
	attachAssetsToChunk(nil, assets)
	attachAssetsToChunk(&provider.ChatCompletionChunk{}, assets)
	attachAssetsToChunk(&provider.ChatCompletionChunk{}, nil)
	attachAssetsToChunk(
		&provider.ChatCompletionChunk{Choices: []provider.Choice{{}}},
		assets,
	)

	// A final-form chunk carries message instead of delta.
	viaMessage := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{Message: &provider.Message{}}},
	}
	attachAssetsToChunk(viaMessage, assets)
	if got := viaMessage.Choices[0].Message.Images; len(got) != 1 {
		t.Errorf("message fallback attached %d assets", len(got))
	}

	// An incremental chunk carries delta, which takes precedence.
	viaDelta := &provider.ChatCompletionChunk{
		Choices: []provider.Choice{{
			Delta:   &provider.Message{},
			Message: &provider.Message{},
		}},
	}
	attachAssetsToChunk(viaDelta, assets)
	if got := viaDelta.Choices[0].Delta.Images; len(got) != 1 {
		t.Errorf("delta attached %d assets", len(got))
	}
	if len(viaDelta.Choices[0].Message.Images) != 0 {
		t.Error("message must stay untouched when delta is present")
	}
}

func TestGetVideoJob(t *testing.T) {
	t.Run("rejects a missing job id", func(t *testing.T) {
		p := NewWithBaseURL("https://example.invalid")
		if _, err := p.GetVideoJob(context.Background(), "k", nil); err == nil {
			t.Error("nil job must be rejected")
		}
		if _, err := p.GetVideoJob(context.Background(), "k", &provider.VideoJob{}); err == nil {
			t.Error("an empty provider_job_id must be rejected")
		}
	})

	t.Run("falls back to the videos endpoint", func(t *testing.T) {
		var gotPath, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			_, _ = io.WriteString(w, `{"id":"job_1","status":"in_progress","progress":42}`)
		}))
		defer srv.Close()

		job, err := NewWithBaseURL(srv.URL).GetVideoJob(context.Background(), "k",
			&provider.VideoJob{ID: "local-1", ProviderJobID: "job_1", Model: "m"})
		if err != nil {
			t.Fatalf("GetVideoJob: %v", err)
		}
		if gotPath != "/videos/job_1" {
			t.Errorf("path = %q", gotPath)
		}
		if gotAuth != "Bearer k" {
			t.Errorf("Authorization = %q", gotAuth)
		}
		if job.Status != provider.VideoStatusInProgress || job.Progress != 42 {
			t.Errorf("job = %+v", job)
		}
	})

	t.Run("prefers the polling url from metadata", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			// Deliberately no id: the caller's local id must be carried over.
			_, _ = io.WriteString(w, `{"status":"completed","unsigned_urls":["https://cdn.example/v.mp4"]}`)
		}))
		defer srv.Close()

		job, err := NewWithBaseURL(srv.URL).GetVideoJob(context.Background(), "k",
			&provider.VideoJob{
				ID:            "local-1",
				ProviderJobID: "job_1",
				Model:         "m",
				Metadata:      map[string]any{"polling_url": srv.URL + "/custom/poll"},
			})
		if err != nil {
			t.Fatalf("GetVideoJob: %v", err)
		}
		if gotPath != "/custom/poll" {
			t.Errorf("path = %q, want /custom/poll", gotPath)
		}
		if job.ID != "local-1" {
			t.Errorf("id = %q, want the caller's local id to survive", job.ID)
		}
		if len(job.Assets) != 1 {
			t.Errorf("assets = %+v", job.Assets)
		}
	})

	t.Run("propagates an upstream error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"gone"}}`)
		}))
		defer srv.Close()

		if _, err := NewWithBaseURL(srv.URL).GetVideoJob(context.Background(), "k",
			&provider.VideoJob{ProviderJobID: "job_1"}); err == nil {
			t.Fatal("expected a 404 to surface as an error")
		}
	})

	t.Run("ignores an unusable polling url", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_, _ = io.WriteString(w, `{"id":"job_1","status":"pending"}`)
		}))
		defer srv.Close()

		for _, metadata := range []map[string]any{
			{"polling_url": ""},    // empty string
			{"polling_url": 7},     // wrong type
			{"polling_url": nil},   // nil
			{"other_key": "value"}, // absent
		} {
			gotPath = ""
			if _, err := NewWithBaseURL(srv.URL).GetVideoJob(context.Background(), "k",
				&provider.VideoJob{ProviderJobID: "job_1", Metadata: metadata}); err != nil {
				t.Fatalf("GetVideoJob(%#v): %v", metadata, err)
			}
			if !strings.HasSuffix(gotPath, "/videos/job_1") {
				t.Errorf("metadata %#v -> path %q, want the videos endpoint", metadata, gotPath)
			}
		}
	})
}

// TestChatCompletion_ReplacesRawImageEntries guards the defect these tests were
// written to find. provider.Message binds `json:"images"`, so the raw OpenRouter
// entry lands in Message.Images during decoding — the same slice the normalized
// assets belong in. Appending yielded two entries per image: the raw one with no
// payload at all, plus its normalized twin.
func TestChatCompletion_ReplacesRawImageEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{
			name: "normalizable entry survives exactly once",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","images":[
				{"type":"image_url","image_url":{"url":"https://cdn.example/a.png"}}]}}]}`,
			want: 1,
		},
		{
			name: "two entries yield two assets, not four",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","images":[
				{"type":"image_url","image_url":{"url":"https://cdn.example/a.png"}},
				{"type":"image_url","image_url":{"url":"https://cdn.example/b.png"}}]}}]}`,
			want: 2,
		},
		{
			name: "an entry with no payload is dropped, not kept raw",
			body: `{"choices":[{"index":0,"message":{"role":"assistant","images":[{"type":"image_url"}]}}]}`,
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			resp, err := NewWithBaseURL(srv.URL).ChatCompletion(context.Background(), "k", "m",
				&provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}})
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			if len(resp.Choices) != 1 || resp.Choices[0].Message == nil {
				t.Fatalf("choices = %+v", resp.Choices)
			}
			got := resp.Choices[0].Message.Images
			if len(got) != tc.want {
				t.Fatalf("Images = %d entries %+v, want %d", len(got), got, tc.want)
			}
			for i, asset := range got {
				if asset.URL == "" && asset.B64JSON == "" && asset.DataURL == "" && asset.FileID == "" {
					t.Errorf("Images[%d] carries no payload: %+v", i, asset)
				}
				if asset.Type != "image" {
					t.Errorf("Images[%d].Type = %q, want the normalized image type", i, asset.Type)
				}
			}
		})
	}
}

func TestAttachAssetsToChoices_EdgeCases(t *testing.T) {
	assets := []provider.MediaAsset{{URL: "https://cdn.example/a.png"}}
	raw := []provider.MediaAsset{{Type: "image_url"}}

	// Neither of these may panic or attach.
	attachAssetsToChoices(nil, assets)
	attachAssetsToChoices([]provider.Choice{{}}, assets)

	// Message wins, and the raw entry decoding left behind is cleared first.
	viaMessage := []provider.Choice{{Message: &provider.Message{Images: raw}}}
	attachAssetsToChoices(viaMessage, assets)
	if got := viaMessage[0].Message.Images; len(got) != 1 || got[0].URL != assets[0].URL {
		t.Errorf("Images = %+v", got)
	}

	// Delta is the fallback when a response carries no message.
	viaDelta := []provider.Choice{{Delta: &provider.Message{Images: raw}}}
	attachAssetsToChoices(viaDelta, assets)
	if got := viaDelta[0].Delta.Images; len(got) != 1 || got[0].URL != assets[0].URL {
		t.Errorf("Delta.Images = %+v", got)
	}

	// Nothing to install still clears a stale raw entry.
	stale := []provider.Choice{{Message: &provider.Message{Images: raw}}}
	attachAssetsToChoices(stale, nil)
	if got := stale[0].Message.Images; len(got) != 0 {
		t.Errorf("stale raw entry survived: %+v", got)
	}
}

// TestStreamReader_ReplacesRawImageEntries is the streaming twin: the same
// decode-then-append collision would show up on every delta chunk.
func TestStreamReader_ReplacesRawImageEntries(t *testing.T) {
	body := `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","images":[` +
		`{"type":"image_url","image_url":{"url":"https://cdn.example/a.png"}}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"

	stream := NewStreamReader(context.Background(), io.NopCloser(strings.NewReader(body)), false)
	defer stream.Close()

	chunk, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Delta == nil {
		t.Fatalf("chunk = %+v", chunk)
	}
	got := chunk.Choices[0].Delta.Images
	if len(got) != 1 {
		t.Fatalf("Delta.Images = %d entries %+v, want exactly the normalized one", len(got), got)
	}
	if got[0].URL != "https://cdn.example/a.png" {
		t.Errorf("Images[0].URL = %q", got[0].URL)
	}
	if got[0].Type != "image" {
		t.Errorf("Images[0].Type = %q", got[0].Type)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Errorf("second Recv error = %v, want io.EOF", err)
	}
}
